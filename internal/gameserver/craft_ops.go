package gameserver

// 制作域：9/1 制作、9/3 分解、9/4 询价、9/6 配方模板。
//
// 9/1 校验配方和材料，在事务内扣料、产出入包并写流水；9/3 对铲子挖饵，其它物品按分解处理；
// 9/4 返回费用行；9/6 返回配方详情。响应类型由命令合同约束。
import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

func init() {
	RegisterExtra(func(d *Dispatch) {
		d.Register(9, 1, hCraft)
		d.Register(9, 3, hCraftMine2)
		d.Register(9, 4, hCraftCost)
		d.Register(9, 6, hCraftAddTemplate)
	})
}

// ---- 常量 ----

// dhkphmjinmj 的 wire type_id / 类名（obj8，9/1 与 9/4 的同一载荷）。
const (
	obj8TypeID = 8
	obj8Class  = "dhkphmjinmj"
)

// obj8 的 wire 字段位置；顺序为 STR、VAL、VAL、OPT、U16STR、U32。
const (
	obj8FRecipe    = 0 // cemjmlpafmc 配方 systemID
	obj8FTools     = 1 // oifjpmejkob 工具槽
	obj8FMaterials = 2 // odmioelpcpi 材料槽
	obj8FName      = 4 // phoglfnffah 自定义名
	obj8FQty       = 5 // ekjiebkgboh 数量
)

// maxCraftTimes 单次制作的份数上限（防止异常 qty 把材料一次扣光）。
const maxCraftTimes = 100

// bundleCategory 套盒类别（shop_rules.BUNDLE_CATEGORY）。
const bundleCategory = 55

// ---- 请求解析 ----

// craftSlot 表示 koelkolmmee 槽位：f0 是需求 id，f1 是物品实例 GUID。
// 部分界面使用客户端临时 GUID，服务端会再按需求 id 定位材料。
type craftSlot struct {
	ItemID string
	GUID   string
}

// craftPayload 9/1 / 9/4 共用的制作载荷。
type craftPayload struct {
	Recipe    string
	Qty       uint64
	Name      string
	Tools     []craftSlot
	Materials []craftSlot
	Source    string // "obj8"、"strings" 或 "none"
}

// craftPayloadOf 从请求树读取制作载荷。优先解析 obj8，缺失时回退到顶层字符串。
func craftPayloadOf(ctx *Ctx) craftPayload {
	out := craftPayload{Source: "none"}
	if node := findObj8(ctx.Tree); node != nil && node.FieldCount() > obj8FQty {
		out.Recipe = node.Fields[obj8FRecipe].StrOr()
		out.Name = node.Fields[obj8FName].StrOr()
		if n := node.Fields[obj8FQty].Num; n != nil {
			out.Qty = u64Of(n)
		}
		out.Tools = slotList(node.Fields[obj8FTools].Val)
		out.Materials = slotList(node.Fields[obj8FMaterials].Val)
		out.Source = "obj8"
		return out
	}
	if strs := deepStrings(ctx.Tree); len(strs) > 0 {
		out.Recipe = strs[0]
		out.Source = "strings"
	}
	out.Materials = slotList(ctx.Tree)
	return out
}

// findObj8 在请求树里找最外层的 obj8（dhkphmjinmj）；没有则 nil。
// 对象沿字段值下钻，容器沿子项下钻。
func findObj8(root *proto.Value) *proto.Value {
	var found *proto.Value
	var walk func(v *proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || found != nil {
			return
		}
		if v.Kind == proto.KindObj && (v.TypeID == obj8TypeID || v.Class == obj8Class) {
			found = v
			return
		}
		walkChildren(v, walk)
	}
	walk(root)
	return found
}

// slotList 收集 koelkolmmee 槽位。对象有三个字段且第二个为 GUID 时即视为槽位，
// 不依赖类名。
func slotList(tree *proto.Value) []craftSlot {
	var out []craftSlot
	var walk func(v *proto.Value)
	walk = func(v *proto.Value) {
		if v == nil {
			return
		}
		if v.Kind == proto.KindObj && v.FieldCount() == 3 {
			g := v.Fields[1]
			if g.Op == "GUID16" || (g.Op == "OPTGUID" && g.Hex != "") {
				out = append(out, craftSlot{ItemID: v.Fields[0].StrOr(), GUID: g.Hex})
			}
		}
		walkChildren(v, walk)
	}
	walk(tree)
	return out
}

// walkChildren 遍历容器、数组和对象的子值。
func walkChildren(v *proto.Value, walk func(*proto.Value)) {
	switch v.Kind {
	case proto.KindArgs, proto.KindValList, proto.KindArray,
		proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
		for _, it := range v.Items {
			walk(it)
		}
	case proto.KindObj:
		for i := range v.Fields {
			if v.Fields[i].Val != nil {
				walk(v.Fields[i].Val)
			}
		}
	}
}

// deepStrings 按前序遍历收集非空字符串，包括 args 中的字符串和对象的
// STR、U16STR、U32N 字段。
func deepStrings(v *proto.Value) []string {
	var out []string
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindStr:
			if x.Str != nil && *x.Str != "" {
				out = append(out, *x.Str)
			}
		case proto.KindArgs, proto.KindValList, proto.KindArray,
			proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				switch x.Fields[i].Op {
				case "STR", "U16STR", "U32N":
					if s := x.Fields[i].StrPtr(); s != nil && *s != "" {
						out = append(out, *s)
					}
				}
			}
		}
	}
	walk(v)
	return out
}

// ---- 配方与材料需求 ----

// craftDemand 表示配方材料需求，也是 materials_json 的元素形状。
// item_id 可为空 —— 那种需求只约束类别（categories）或标签组（tags）。
type craftDemand struct {
	DemandID     string   `json:"demand_id"`
	ItemID       string   `json:"item_id"`
	Qty          *int64   `json:"qty"`
	Alternatives []string `json:"alternatives"`
	Categories   []int64  `json:"categories"`
	Tags         []int64  `json:"tags"`
}

// need 返回需求数量；未配置时为 1。
func (m craftDemand) need() int64 {
	if m.Qty == nil {
		return 1
	}
	return *m.Qty
}

// parseDemands 解析配方材料清单；materials_json 为空时返回 nil。
func parseDemands(materialsJSON string) []craftDemand {
	if materialsJSON == "" {
		return nil
	}
	var out []craftDemand
	if err := json.Unmarshal([]byte(materialsJSON), &out); err != nil {
		return nil
	}
	return out
}

// demandAccepts 返回该需求可接受的物品 id。alternatives 过滤空串后优先；
// 为空时仅使用 item_id。
func demandAccepts(m craftDemand) []string {
	alts := make([]string, 0, len(m.Alternatives))
	for _, a := range m.Alternatives {
		if a != "" {
			alts = append(alts, a)
		}
	}
	if len(alts) > 0 {
		return alts
	}
	return []string{m.ItemID}
}

// allDemandsExplicit 判断所有需求是否都指定了物品 id。存在仅按类别或标签
// 约束的需求时返回 false，此时需要读取客户端提交的材料槽。
func allDemandsExplicit(ms []craftDemand) bool {
	for _, m := range ms {
		if m.ItemID == "" {
			return false
		}
	}
	return true
}

// scaleDemands 按制作份数放大材料需求；times 为 1 时原样返回。
func scaleDemands(ms []craftDemand, times int64) []craftDemand {
	if times <= 1 {
		return ms
	}
	out := make([]craftDemand, len(ms))
	copy(out, ms)
	for i := range out {
		q := out[i].need() * times
		out[i].Qty = &q
	}
	return out
}

// resolveRecipe 依次按配方 id、产出物 id 和 recipe_ 前缀解析配方键。
func (s *Server) resolveRecipe(ctx context.Context, key string) *store.Recipe {
	if key == "" {
		return nil
	}
	if r, err := s.DB.GetRecipe(ctx, key); err == nil && r != nil {
		return r
	}
	if r, err := s.DB.GetRecipeByOutput(ctx, key); err == nil && r != nil {
		return r
	}
	if r, err := s.DB.GetRecipe(ctx, "recipe_"+key); err == nil && r != nil {
		return r
	}
	return nil
}

// ---- 9/1 制作闭环 ----

// hCraft 处理 9/1 制作：解析配方、校验材料、事务内扣料、创建独立新实例并写流水。
// 制作产出不并入已有同种堆，803.f1 返回新实例，f6 返回背包增量，f8 返回移除项。
//
// 未知配方、材料不足和入库失败均返回 803(false)，避免客户端等待缺失响应。
func hCraft(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	req := craftPayloadOf(ctx)
	recipe := srv.resolveRecipe(cctx, req.Recipe)
	if recipe == nil {
		ctx.Log.Info("9/1 未知配方", "recipe", req.Recipe,
			"source", req.Source, "materials", len(req.Materials))
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	outputID := recipe.OutputID
	if outputID == "" {
		outputID = req.Recipe
	}
	times := int64(req.Qty)
	if times < 1 || times > maxCraftTimes {
		times = 1
	}
	uid := ctx.Sess.Userid
	demands := parseDemands(recipe.MaterialsJSON)
	isBaitCraft := strings.HasPrefix(recipe.RecipeID, "craft_bait_")

	// 先在事务外构造物品；构造失败时尚未扣材料。
	producedRow, err := srv.prepareNewItem(cctx, uid, outputID, "backpack", times, "")
	if err != nil {
		ctx.Log.Warn("9/1 产出物不可构造", "output", outputID, "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}

	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("9/1 开启事务失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	defer func() { _ = tx.Rollback() }()

	touched := []string{}
	fail := func(reason string, args ...any) ([]byte, bool) {
		ctx.Log.Warn("9/1 制作失败："+reason, args...)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}

	if len(demands) > 0 && allDemandsExplicit(demands) {
		need := scaleDemands(demands, times)
		rows, lerr := listItemsTx(cctx, tx, uid, []string{"backpack", "rig"})
		if lerr != nil {
			return fail("读取材料失败", "err", lerr)
		}
		for _, demand := range need {
			remaining := demand.need()
			for _, accept := range demandAccepts(demand) {
				if remaining <= 0 {
					break
				}
				for _, it := range rows {
					if remaining <= 0 || it.ItemID != accept || it.Qty <= 0 {
						continue
					}
					take := remaining
					if have := stackCountTx(cctx, tx, it); have < take {
						take = have
					}
					got, cerr := consumeItemQtyTx(cctx, tx, it, take)
					if cerr != nil {
						return fail("扣材料失败", "item", accept, "err", cerr)
					}
					remaining -= got
					if got > 0 && !containsGUID(touched, it.GUID) {
						touched = append(touched, it.GUID)
					}
				}
			}
			if remaining > 0 {
				return fail("材料不足", "demand", demand.DemandID, "missing", remaining)
			}
		}
	} else if len(demands) > 0 {
		if len(req.Materials) == 0 {
			return fail("配方要求材料但请求没有材料槽")
		}
		byDemand := map[string]craftDemand{}
		for _, d := range demands {
			if d.DemandID != "" {
				byDemand[d.DemandID] = d
			}
		}
		rows, lerr := listItemsTx(cctx, tx, uid, []string{"backpack", "rig"})
		if lerr != nil {
			return fail("读取材料失败", "err", lerr)
		}
		byGUID := map[string]*store.Item{}
		for _, it := range rows {
			byGUID[it.GUID] = it
		}
		for _, slot := range req.Materials {
			if slot.GUID == "" {
				continue
			}
			consumed := false
			consumedGUID := ""
			if row := byGUID[slot.GUID]; row != nil && row.Qty > 0 {
				if _, cerr := consumeItemQtyTx(cctx, tx, row, 1); cerr != nil {
					return fail("扣指定材料失败", "guid", slot.GUID, "err", cerr)
				}
				consumed = true
				consumedGUID = row.GUID
			}
			if !consumed {
				demand, ok := byDemand[slot.ItemID]
				if !ok {
					return fail("材料槽既不在背包也无对应需求", "demand", slot.ItemID, "guid", slot.GUID)
				}
				alts := make(map[string]bool)
				for _, a := range demand.Alternatives {
					if a != "" {
						alts[a] = true
					}
				}
				cats := make(map[int64]bool)
				for _, c := range demand.Categories {
					cats[c] = true
				}
				for _, it := range rows {
					if it.Qty <= 0 {
						continue
					}
					match := false
					if len(alts) > 0 {
						match = alts[it.ItemID]
					} else if len(cats) > 0 {
						realCat, ok := effectiveCategoryTx(cctx, tx, it.ItemID)
						match = ok && cats[realCat]
					}
					if !match {
						continue
					}
					if _, cerr := consumeItemQtyTx(cctx, tx, it, 1); cerr != nil {
						return fail("按需求扣材料失败", "item", it.ItemID, "err", cerr)
					}
					consumed = true
					consumedGUID = it.GUID
					break
				}
			}
			if !consumed {
				return fail("材料槽无法从背包定位", "demand", slot.ItemID, "guid", slot.GUID)
			}
			if consumedGUID != "" && !containsGUID(touched, consumedGUID) {
				touched = append(touched, consumedGUID)
			}
		}
		if len(touched) == 0 {
			return fail("配方要求材料但未扣到任何材料")
		}
	}

	// 鱼饵和作者现有可堆叠物品一样并入同编号背包堆，避免每做一次多出一个 30 个的独立堆。
	row, merged, err := grantPreparedItemTx(cctx, tx, producedRow, isBaitCraft)
	if err != nil {
		return fail("产出物入库失败", "output", outputID, "err", err)
	}
	_ = merged
	if err := addTxnTx(cctx, tx, uid, "craft", outputID, "",
		0, 0, fmt.Sprintf("recipe=%s qty=%d", recipe.RecipeID, times)); err != nil {
		return fail("制作流水写入失败", "err", err)
	}
	if err := tx.Commit(); err != nil {
		return fail("事务提交失败", "err", err)
	}
	masteryGain := 0.0
	if isBaitCraft {
		before, skillErr := srv.DB.GetSkillPoints(cctx, uid, captureBaitSkillID)
		if skillErr != nil {
			ctx.Log.Warn("9/1 读取捕获鱼饵熟练度失败", "err", skillErr)
		} else if before < baitMasteryCap {
			after := math.Min(baitMasteryCap, math.Round((before+baitMasteryStep)*10)/10)
			if err := srv.DB.AddSkillPoints(cctx, uid, captureBaitSkillID, baitMasteryStep); err != nil {
				ctx.Log.Warn("9/1 捕获鱼饵熟练度写入失败", "err", err)
			} else {
				masteryGain = after - before
			}
		}
	}
	ctx.Log.Info("9/1 制作入库", "output", outputID, "qty", times,
		"took", len(touched), "recipe", recipe.RecipeID, "mastery_gain", masteryGain)

	produced := make([]*proto.Value, 0, 1)
	if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
		produced = append(produced, tree)
	}
	upsert := append([]*proto.Value{}, produced...)
	var removed []string
	for _, g := range touched {
		r2, gerr := srv.DB.GetItemByGUID(cctx, g)
		if gerr != nil || r2 == nil {
			removed = append(removed, g)
		} else if tree, terr := srv.ItemToTree(cctx, r2); terr == nil {
			upsert = append(upsert, tree)
		}
	}
	ctx.AfterResponse(func() {
		if masteryGain > 0 {
			ctx.Sess.Push2_4SkillTree("制作鱼饵 " + outputID)
		}
	})
	return srv.replyCraftResultWithMastery(ctx, true, produced, upsert, removed, captureBaitSkillID, masteryGain)
}

// alignItemRowQty 将 item.qty 与可计件物品字段中的数量对齐，非计件物品保持 1。
// 扣料和材料校验都读取行数量，因此两处必须一致。
func (s *Server) alignItemRowQty(ctx context.Context, row *store.Item) {
	if row == nil || row.GUID == "" {
		return
	}
	count := int64(1)
	if def, err := s.DB.GetItemDef(ctx, row.NumericID); err == nil && def != nil &&
		countableCategories[def.Category] {
		if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
			if q := fieldIdxByCS(row.WireClass, csQty); q >= 0 && q < len(fields) && fields[q].Num != nil {
				if v := int64(u64Of(fields[q].Num)); v > count {
					count = v
				}
			}
		}
	}
	if row.Qty != count {
		if err := s.DB.UpdateItemFields(ctx, row.GUID, row.FieldsJSON, count,
			row.Durability, row.Workmanship, row.RigGUID); err == nil {
			row.Qty = count
		}
	}
}

// hasMaterials 校验背包是否满足全部材料需求。
// alternatives 是可接受的 item id 列表，任一命中即可计入。
func (s *Server) hasMaterials(ctx context.Context, uid int64, ms []craftDemand) bool {
	rows, err := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return false
	}
	have := map[string]int64{}
	for _, it := range rows {
		have[it.ItemID] += s.stackCountOf(ctx, it)
	}
	for _, m := range ms {
		need := m.need()
		var got int64
		for _, a := range demandAccepts(m) {
			got += have[a]
		}
		if got < need {
			return false
		}
	}
	return true
}

// consumeMaterials 按需求扣除背包材料，支持 alternatives，并优先扣 item_id。
// 可堆叠材料按份扣除；返回被修改的 GUID 列表。
func (s *Server) consumeMaterials(ctx context.Context, uid int64, ms []craftDemand) []string {
	rows, err := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return nil
	}
	touched := []string{}
	for _, m := range ms {
		need := m.need()
		for _, accept := range demandAccepts(m) {
			if need <= 0 {
				break
			}
			for _, it := range rows {
				if need <= 0 {
					break
				}
				if it.ItemID != accept {
					continue
				}
				take := need
				if cnt := s.stackCountOf(ctx, it); cnt > 0 && cnt < take {
					take = cnt // min(need, 该堆现有份数)
				}
				got := s.consumeItemQty(ctx, it, take)
				need -= got
				if got > 0 && !containsGUID(touched, it.GUID) {
					touched = append(touched, it.GUID)
				}
			}
		}
	}
	return touched
}

// consumeDeclared 按客户端提交的材料槽扣除。槽位 f0 是需求 id，f1 是实例 GUID。
// 临时 GUID 无法在背包中定位时，再按需求 id 查找可用材料。
func (s *Server) consumeDeclared(ctx *Ctx, cctx context.Context, uid int64, slots []craftSlot, demands []craftDemand) []string {
	byDemand := map[string]craftDemand{}
	for _, d := range demands {
		if d.DemandID != "" {
			byDemand[d.DemandID] = d
		}
	}
	rows, _ := s.DB.ListItemsByContainers(cctx, uid, []string{"backpack", "rig"})
	byGUID := map[string]*store.Item{}
	for _, it := range rows {
		byGUID[it.GUID] = it
	}
	touched := []string{}
	var missing []string
	for _, sl := range slots {
		g := sl.GUID
		if g == "" {
			continue
		}
		if row, ok := byGUID[g]; ok {
			if s.consumeItemQty(cctx, row, 1) > 0 && !containsGUID(touched, g) {
				touched = append(touched, g)
			}
			continue
		}
		if demand, ok := byDemand[sl.ItemID]; ok {
			if hit := s.consumeByDemand(cctx, uid, demand); hit != "" {
				if !containsGUID(touched, hit) {
					touched = append(touched, hit)
				}
				continue
			}
		}
		missing = append(missing, fmt.Sprintf("%s/%s", sl.ItemID, shortGUID(g)))
	}
	if len(missing) > 0 {
		ctx.Log.Warn("9/1 材料槽有物品既不在背包、也无法按需求类目定位（仍放行产出）",
			"missing", missing)
	}
	return touched
}

// consumeByDemand 按一条需求的可接受类别/备选 item id 在背包里扣 1 份
// （9_1._consume_by_demand）。返回被扣物品的 guid；没扣到返回 ""。
func (s *Server) consumeByDemand(ctx context.Context, uid int64, d craftDemand) string {
	alts := make([]string, 0, len(d.Alternatives))
	for _, a := range d.Alternatives {
		if a != "" {
			alts = append(alts, a)
		}
	}
	cats := map[int64]bool{}
	for _, c := range d.Categories {
		cats[c] = true
	}
	rows, _ := s.DB.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
	for _, it := range rows {
		iid := it.ItemID
		switch {
		case len(alts) > 0:
			if !containsStr(alts, iid) {
				continue
			}
		case len(cats) > 0:
			// effectiveCategory 与 class_for_item 同一口径（套盒解包到内含物）
			realCat, ok := s.effectiveCategory(ctx, iid)
			if !ok || !cats[realCat] {
				continue
			}
		default:
			// 只有标签组（gfhgeippgdj）：服务端无法从标签反查物品，交给下一条
			return ""
		}
		if s.consumeItemQty(ctx, it, 1) > 0 {
			ctxLog := s.log
			ctxLog.Info("9/1 材料按需求类目扣除", "item", iid, "demand", d.DemandID)
			return it.GUID
		}
	}
	return ""
}

// consumeItemQty 从实例中按份扣除 n，返回实际扣除数量。
//
// 可堆叠物品的数量同时保存在 item.qty 和 ljnnpokapig 字段中，两处必须同步。
// 数量降至 0 或不足时删除整件及其 container_slot、rig_component 关联。
// 当前数量以 stackCountOf 为准，无字段时回退到定义值。
func (s *Server) consumeItemQty(ctx context.Context, row *store.Item, n int64) int64 {
	if row == nil || row.GUID == "" {
		return 0
	}
	have := s.stackCountOf(ctx, row)
	if have < 1 {
		have = 1
	}
	take := n
	if take < 1 {
		take = 1
	}
	if have <= take {
		if err := s.DB.DeleteItemCascade(ctx, row.GUID); err != nil {
			return 0
		}
		row.Qty = 0
		return have
	}
	left := have - take
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil && len(fields) > 0 {
		// set_item_fields 路径：改 ljnnpokapig + qty 镜像列 + 抬版本戳
		if q := fieldIdxByCS(row.WireClass, csQty); q >= 0 && q < len(fields) {
			fields[q].Num = ptrU64(uint64(left))
		}
		bumpCreatedTicks(fields, row.WireClass)
		if fj, jerr := FieldsToJSON(fields); jerr == nil {
			_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, left, row.Durability, row.Workmanship, row.RigGUID)
			row.FieldsJSON = fj
		}
	} else {
		// set_item_qty 路径：退化行只更 qty 列（不动 fields_json）
		_ = s.DB.UpdateItemQty(ctx, row.GUID, left)
	}
	row.Qty = left
	return take
}

// effectiveCategory 物品的有效类别（shop_rules._effective 的类别半边：
// 套盒(55)解包到内含物 attrs nbkhfhenbag[0].cemjmlpafmc 的类别）。
func (s *Server) effectiveCategory(ctx context.Context, itemID string) (int64, bool) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return 0, false
	}
	if def.Category != bundleCategory {
		return def.Category, true
	}
	if bag, ok := defAttrs(def)["nbkhfhenbag"].([]any); ok {
		for _, ent := range bag {
			m, ok := ent.(map[string]any)
			if !ok {
				continue
			}
			inner, _ := m["cemjmlpafmc"].(string)
			if inner == "" {
				continue
			}
			if di, derr := s.DB.GetItemDefByItemID(ctx, inner); derr == nil && di != nil {
				return di.Category, true
			}
			break // 内含物无定义时保留套盒自身类别
		}
	}
	return def.Category, true
}

// ---- 9/3 分解/采集 ----

// shovelBaitDrop 是铲子可挖出的自然诱饵。
// 每次挖出 1-3 种，每种 1-5 个；同类诱饵并入已有背包堆。
type shovelBaitDrop struct {
	ItemID string
	Name   string
}

type shovelBaitLoot struct {
	shovelBaitDrop
	Qty int
}

var shovelBaitDrops = []shovelBaitDrop{
	{ItemID: "worm_01", Name: "蚯蚓"},
	{ItemID: "vypolzok_01", Name: "地龙"},
	{ItemID: "maggot", Name: "蛆虫"},
	{ItemID: "maggot_caster", Name: "蛆虫蛹"},
	{ItemID: "bloodworm", Name: "红虫"},
	{ItemID: "waterbug", Name: "龙虱甲虫"},
}

func isShovelItemID(itemID string) bool {
	return itemID == "tool_shovel" || itemID == "shovel_ny25" || itemID == "tool_scoop"
}

const baitCatchSkillCat = 4

// rollShovelBaitLoot 抽取 1-3 种不重复诱饵，每种数量 1-5。
func rollShovelBaitLoot(rng *rand.Rand) []shovelBaitLoot {
	if rng == nil || len(shovelBaitDrops) == 0 {
		return nil
	}
	nTypes := 1 + rng.Intn(3)
	if nTypes > len(shovelBaitDrops) {
		nTypes = len(shovelBaitDrops)
	}
	order := rng.Perm(len(shovelBaitDrops))[:nTypes]
	out := make([]shovelBaitLoot, 0, len(order))
	for _, idx := range order {
		out = append(out, shovelBaitLoot{
			shovelBaitDrop: shovelBaitDrops[idx],
			Qty:            1 + rng.Intn(5),
		})
	}
	return out
}

// hCraftMine2 处理 9/3：铲子触发挖饵，其它物品保持原有分解行为。
func hCraftMine2(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	// 同一命令也被客户端用于铲子/红虫长柄勺挖饵。先识别工具；
	// 不是挖饵工具时才继续走作者原有的分解分支。
	if body, handled := srv.tryBaitDig(ctx); handled {
		return body, true
	}
	guids := uniqueStrings(deepGUIDs(ctx.Tree))
	if len(guids) == 0 {
		return nil, false // 没有 GUID 时交给兜底链
	}
	cctx := ctx.Sess.pushCtx()
	if len(guids) == 1 {
		if row, err := srv.DB.GetItemByGUID(cctx, guids[0]); err == nil && row != nil &&
			row.UID == ctx.Sess.Userid && isShovelItemID(row.ItemID) {
			return srv.digBaitWithShovel(ctx, row)
		}
	}
	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("9/3 开启事务失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	defer func() { _ = tx.Rollback() }()
	n, err := removeItemsTx(cctx, tx, ctx.Sess.Userid, guids)
	if err != nil {
		ctx.Log.Warn("9/3 删除物品失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	if err := addTxnTx(cctx, tx, ctx.Sess.Userid, "mine", "", guids[0], 0, 0,
		fmt.Sprintf("decompose %d", n)); err != nil {
		ctx.Log.Warn("9/3 流水写入失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("9/3 提交失败", "err", err)
		return srv.replyCraftResult(ctx, false, nil, nil, nil)
	}
	ctx.Log.Info("9/3 分解", "count", n)
	return srv.replyCraftResult(ctx, n > 0, nil, nil, nil)
}

// digBaitWithShovel 完成一次铲子挖饵：铲子保留，诱饵入背包并随 803.f1 返回。
func (s *Server) digBaitWithShovel(ctx *Ctx, shovel *store.Item) ([]byte, bool) {
	cctx := ctx.Sess.pushCtx()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	loot := rollShovelBaitLoot(rng)
	if len(loot) == 0 {
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}

	prepared := make([]*store.Item, 0, len(loot))
	for _, item := range loot {
		it, err := s.prepareNewItemWithCount(cctx, ctx.Sess.Userid, item.ItemID, "backpack", int64(item.Qty), "")
		if err != nil {
			ctx.Log.Warn("9/3 挖饵构造失败", "item", item.ItemID, "err", err)
			return s.replyCraftResult(ctx, false, nil, nil, nil)
		}
		prepared = append(prepared, it)
	}

	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		ctx.Log.Warn("9/3 挖饵开启事务失败", "err", err)
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}
	defer func() { _ = tx.Rollback() }()

	granted := make([]*store.Item, 0, len(prepared))
	parts := make([]string, 0, len(loot))
	for i, it := range prepared {
		row, _, err := grantPreparedItemTx(cctx, tx, it, true)
		if err != nil {
			ctx.Log.Warn("9/3 挖饵入包失败", "item", it.ItemID, "err", err)
			return s.replyCraftResult(ctx, false, nil, nil, nil)
		}
		granted = append(granted, row)
		parts = append(parts, fmt.Sprintf("%sx%d", loot[i].ItemID, loot[i].Qty))
	}
	if err := addTxnTx(cctx, tx, ctx.Sess.Userid, "dig_bait", shovel.ItemID, shovel.GUID, 0, 0,
		"shovel "+strings.Join(parts, ",")); err != nil {
		ctx.Log.Warn("9/3 挖饵流水写入失败", "err", err)
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("9/3 挖饵提交失败", "err", err)
		return s.replyCraftResult(ctx, false, nil, nil, nil)
	}

	produced := make([]*proto.Value, 0, len(granted))
	for _, row := range granted {
		tree, err := s.ItemToTree(cctx, row)
		if err != nil {
			ctx.Log.Warn("9/3 挖饵响应构造失败", "item", row.ItemID, "err", err)
			return s.replyCraftResult(ctx, true, nil, nil, nil)
		}
		produced = append(produced, tree)
	}
	skillGain := s.awardSkillProficiency(cctx, ctx.Sess.Userid, baitCatchSkillCat)
	progress := wiregen.MustMakeObj("kegeppimjbm", map[int]*proto.Value{
		0: proto.U64(0),
		1: proto.U32(0),
		2: proto.U32(baitCatchSkillCat),
		3: proto.F32(float32(skillGain)),
		4: proto.Null(),
		5: proto.U32(0),
		6: proto.Args(proto.S("31")),
	})
	skillTree := s.buildSingleSkill312(cctx, ctx.Sess.Userid, baitCatchSkillCat)
	ctx.Log.Info("9/3 铲子挖饵", "shovel", shortGUID(shovel.GUID), "types", len(loot),
		"items", strings.Join(parts, ","), "skill_gain", skillGain)
	return s.replyCraftResultWithSkill(ctx, true, produced, produced, nil, progress, skillTree)
}

// removeItems 删除当前玩家的若干物品并清理关联记录，返回删除件数。
func (s *Server) removeItems(ctx context.Context, uid int64, guids []string) int64 {
	n := int64(0)
	for _, g := range guids {
		deleted, err := s.DB.DeleteItemOfUID(ctx, uid, g)
		if err == nil && deleted {
			n++
		}
		_ = s.DB.DeleteItemRefs(ctx, g)
	}
	return n
}

// ---- 9/4 制作询价 ----

// hCraftCost 处理 9/4 制作询价，返回 501 价格行：
// STR 配方 systemID、U64 银价、U64 金价、U32 数量。
// 当前制作只扣材料、不收费，因此价格为 0；数量缺省为 1。
func hCraftCost(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	req := craftPayloadOf(ctx)
	qty := int64(req.Qty)
	if qty == 0 {
		qty = 1
	}
	row := wiregen.MustMakeObj("ifdefblccde", map[int]*proto.Value{
		0: proto.StrVal(req.Recipe), // f0 systemID（必须与请求一致）
		1: proto.U64(0),             // f1 银价（制作不收货币）
		2: proto.U64(0),             // f2 金币价
		3: proto.U32(uint32(qty)),   // f3 数量（请求里的制作数量）
	})
	ctx.Log.Info("9/4 询价", "recipe", req.Recipe, "qty", qty,
		"materials", len(req.Materials), "tools", len(req.Tools), "source", req.Source)
	body, err := proto.EncodeValue(row, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 9/6 添加配方模板 ----

// hCraftAddTemplate 处理 9/6 配方模板请求，返回 171 配方详情。
// 请求键可以是配方 id 或产出物 id；未指定或未知时使用第一条配方，
// 配方表为空时返回 null。
func hCraftAddTemplate(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	recipeID := ""
	if strs := deepStrings(ctx.Tree); len(strs) > 0 {
		recipeID = strs[0]
	}
	recipe := srv.resolveRecipe(cctx, recipeID)
	if recipe == nil {
		if list, err := srv.DB.ListRecipes(cctx); err == nil && len(list) > 0 {
			recipe = list[0]
		}
	}
	if recipe == nil {
		return []byte{0x00}, true // ctx.reply(None)
	}
	guid := uuid5OIDHex(recipe.RecipeID)
	tmpl := craftTemplate(guid, recipe.Name, recipe.Kind, craftDemandObjs(recipe))
	body, err := proto.EncodeValue(tmpl, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	ctx.Log.Info("9/6 配方模板", "recipe", recipe.RecipeID, "guid", guid)
	return body, true
}

// craftTemplate 构造 171 配方模板：f0 GUID、f1 名称、f2 分类、
// f3 输入材料、f4 输出产品。9/6 不返回输出产品，f4 使用空 args("61")。
func craftTemplate(guid, name, kind string, inputs []*proto.Value) *proto.Value {
	return wiregen.MustMakeObj("dmolhiobcbb", map[int]*proto.Value{
		0: proto.Guid(guid),
		1: proto.StrVal(name),
		2: proto.StrVal(kind),
		3: proto.Args(proto.S(listCodeForObjs(inputs)), inputs...),
		4: proto.Args(proto.S("61")),
	})
}

// craftDemandObjs 根据权威配方数据构造 501 配方需求条目。
// materials_json 为空时返回 nil，对应空的需求列表。
func craftDemandObjs(recipe *store.Recipe) []*proto.Value {
	if recipe.MaterialsJSON == "" {
		return nil
	}
	var mats []craftDemand
	if err := json.Unmarshal([]byte(recipe.MaterialsJSON), &mats); err != nil {
		return nil
	}
	out := make([]*proto.Value, 0, len(mats))
	for _, m := range mats {
		// f4 item id 列表（utf-8 字节，非 null 数组；空也要给数组）
		hexes := make([]string, 0, len(m.Alternatives)+1)
		for _, a := range demandAccepts(m) {
			if a == "" {
				continue
			}
			hexes = append(hexes, hex.EncodeToString([]byte(a)))
		}
		out = append(out, wiregen.MustMakeObj("kdpbhdffjaf", map[int]*proto.Value{
			0: proto.StrVal(fmt.Sprintf("%s_req", recipe.RecipeID)), // 需求 key
			1: proto.Str(nil),                                       // f1 固定为 null
			2: proto.U32(0),                                         // lmmhdlhogdc 容器：背包
			3: proto.Null(),                                         // f3 可接受槽位（无 gdgifedhinj → null）
			4: proto.ArrayOf(hexes),
			5: proto.U32(uint32(m.need())),
			// f6..f8 OPT=0 由 MakeObj 零值填充
		}))
	}
	return out
}

// ---- 803 构造 ----

// replyCraftResult 构造 803 制作/分解结果：
// f0 成败、f1 产出物品、f6 背包增量、f8 被移除的 GUID。可选字段为空时不写入。
func (s *Server) replyCraftResult(ctx *Ctx, ok bool, produced, upsert []*proto.Value, removed []string) ([]byte, bool) {
	return s.replyCraftResultWithSkill(ctx, ok, produced, upsert, removed, nil, nil)
}

// replyCraftResultWithMastery 保留固定熟练度增长调用，并复用统一的 803 回包构造。
func (s *Server) replyCraftResultWithMastery(ctx *Ctx, ok bool, produced, upsert []*proto.Value,
	removed []string, skillID int64, masteryGain float64) ([]byte, bool) {
	var progress *proto.Value
	if masteryGain > 0 {
		progress = masteryGainValue(skillID, masteryGain)
	}
	return s.replyCraftResultWithSkill(ctx, ok, produced, upsert, removed, progress, nil)
}

// replyCraftResultWithSkill 额外写入 f2 310 进度块和 f3 单大类技能树。
func (s *Server) replyCraftResultWithSkill(ctx *Ctx, ok bool, produced, upsert []*proto.Value,
	removed []string, progress, skillTree *proto.Value) ([]byte, bool) {
	obj := wiregen.MustMakeObj("beckiiffkem", map[int]*proto.Value{
		0: proto.U32(boolU32(ok)),
		1: proto.Args(proto.S(listCodeForObjs(produced)), produced...),
		2: proto.Null(),
	})
	if progress != nil {
		obj.Fields[2].Val = progress
	}
	if skillTree != nil {
		obj.Fields[3].Val = skillTree
	}
	if len(upsert) > 0 {
		// f6 iingicaldbk 使用 args("61") 包装背包增量。
		obj.Fields[6].Val = proto.Args(proto.S("61"), upsert...)
	}
	if len(removed) > 0 {
		// f8 iaimladeglm 是裸 Guid[]，必须使用 0x06 guidarr，不能套 args 外壳。
		obj.Fields[8].Val = proto.GuidArr(removed)
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Warn("803 编码失败", "err", err)
		return nil, false
	}
	return body, true
}

// listCodeForObjs 根据首个对象元素的 type_id 推导数组容器 code。
// by_elem 优先，by_type_id 兜底；都无法确定或结果为 524 时返回 61。
func listCodeForObjs(items []*proto.Value) string {
	a := loadArgcode()
	for _, it := range items {
		if it == nil || !it.IsKind(proto.KindObj) {
			continue
		}
		k := strconv.FormatUint(uint64(it.TypeID), 10)
		if v, ok := a.ByElem[k]; ok && v != "" && v != "524" {
			return v
		}
		if v, ok := a.ByTypeID[k]; ok && v != "" && v != "524" {
			return v
		}
	}
	return "61"
}

// uuid5OIDHex 按 RFC 4122 生成 NAMESPACE_OID 下的 UUIDv5，用于确定性的模板 GUID。
func uuid5OIDHex(name string) string {
	// NAMESPACE_OID = 6ba7b812-9dad-11d1-80b4-00c04fd430c8（网络序原始字节）
	ns, _ := hex.DecodeString("6ba7b8129dad11d180b400c04fd430c8")
	h := sha1.New()
	h.Write(ns)
	h.Write([]byte(name))
	sum := h.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return hex.EncodeToString(sum)
}

// ---- 小工具 ----

func containsGUID(list []string, g string) bool {
	for _, x := range list {
		if x == g {
			return true
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// shortGUID 返回 GUID 前 8 位，用于日志截断。
