package gameserver

// 4/8 与 9/3 的挖饵逻辑。数据全部走作者现有 SQLite：
// - 工具：铲子 tool_shovel、红虫长柄勺 tool_scoop
// - 熟练度：技能大类 4（捕获鱼饵）
// - 产物：普通 item 行，复用现有可堆叠物品机制
import (
	"fmt"
	"math"
	"math/rand"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

const (
	captureBaitSkillID = 4
	baitShovelItemID   = "tool_shovel"
	baitScoopItemID    = "tool_scoop"
	baitMasteryStep    = 0.1
	baitMasteryCap     = 100.0
)

type baitLootEntry struct {
	Key    string
	Weight int
}

type baitLootBand struct {
	Min   int
	Table []baitLootEntry
}

var baitDigBands = map[string][]baitLootBand{
	baitShovelItemID: {
		{0, []baitLootEntry{{"worm_01", 90}, {"maggot", 10}}},
		{20, []baitLootEntry{{"worm_01", 65}, {"maggot", 25}, {"naiad", 10}}},
		{40, []baitLootEntry{{"worm_01", 45}, {"maggot", 25}, {"naiad", 20}, {"caddisfly", 10}}},
		{60, []baitLootEntry{{"worm_01", 35}, {"maggot", 20}, {"naiad", 20}, {"caddisfly", 15}, {"unibeetle_larva", 10}}},
		{80, []baitLootEntry{{"worm_01", 25}, {"maggot", 15}, {"naiad", 15}, {"caddisfly", 15}, {"unibeetle_larva", 15}, {"stonefly_nymph", 10}, {"scolytinae", 5}}},
	},
	baitScoopItemID: {
		{0, []baitLootEntry{{"bloodworm", 70}, {"worm_01", 30}}},
		{20, []baitLootEntry{{"bloodworm", 80}, {"naiad", 10}, {"worm_01", 10}}},
		{40, []baitLootEntry{{"bloodworm", 70}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 5}}},
		{60, []baitLootEntry{{"bloodworm", 60}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 10}, {"scolytinae", 5}}},
		{80, []baitLootEntry{{"bloodworm", 50}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 10}, {"scolytinae", 10}, {"unibeetle_larva", 5}}},
	},
}

func isBaitDigTool(itemID string) bool {
	itemID = strings.ToLower(strings.TrimSpace(itemID))
	return itemID == baitShovelItemID || itemID == "shovel_ny25" || itemID == baitScoopItemID
}

func chooseBaitLoot(tool string, mastery int, rng func(int) int) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "shovel_ny25" {
		tool = baitShovelItemID
	}
	bands := baitDigBands[tool]
	if len(bands) == 0 {
		return ""
	}
	chosen := bands[0]
	for _, b := range bands {
		if mastery >= b.Min {
			chosen = b
		} else {
			break
		}
	}
	total := 0
	for _, entry := range chosen.Table {
		total += entry.Weight
	}
	if total <= 0 {
		return ""
	}
	n := rng(total)
	for _, entry := range chosen.Table {
		if n < entry.Weight {
			return entry.Key
		}
		n -= entry.Weight
	}
	return chosen.Table[len(chosen.Table)-1].Key
}

func baitQuantityForMastery(mastery float64, rng func(int) int) int64 {
	qty := int64(1)
	if mastery >= 20 && rng(2) == 1 {
		qty++
	}
	if mastery >= 60 && rng(2) == 1 {
		qty++
	}
	return qty
}

// tryBaitDig 只处理 9/3 中包含本号铲子/长柄勺的请求。
// 返回 handled=false 时，hCraftMine2 继续走作者原有的分解逻辑。
func (s *Server) tryBaitDig(ctx *Ctx) ([]byte, bool) {
	guids := uniqueStrings(deepGUIDs(ctx.Tree))
	if len(guids) == 0 {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	var tool *store.Item
	for _, guid := range guids {
		row, err := s.DB.GetItemByGUID(cctx, guid)
		if err != nil || row == nil || row.UID != ctx.Sess.Userid {
			continue
		}
		if isBaitDigTool(row.ItemID) {
			tool = row
			break
		}
	}
	if tool == nil {
		return nil, false
	}

	mastery, err := s.DB.GetSkillPoints(cctx, ctx.Sess.Userid, captureBaitSkillID)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "读取捕获鱼饵熟练度失败: "+err.Error()), true
	}
	if mastery < 0 {
		mastery = 0
	}
	if mastery > baitMasteryCap {
		mastery = baitMasteryCap
	}
	baitKey := chooseBaitLoot(tool.ItemID, int(mastery), rand.Intn)
	if baitKey == "" {
		return s.replyBaitDigFailed(ctx, "熟练度阶段没有可用饵表"), true
	}
	qty := baitQuantityForMastery(mastery, rand.Intn)

	prepared, err := s.prepareNewItem(cctx, ctx.Sess.Userid, baitKey, "backpack", 1, "")
	if err != nil {
		return s.replyBaitDigFailed(ctx, "饵记录构造失败: "+err.Error()), true
	}
	if err := setPreparedItemQuantity(prepared, qty); err != nil {
		return s.replyBaitDigFailed(ctx, "饵数量写入失败: "+err.Error()), true
	}

	tx, err := beginEconomyTx(cctx, s.DB)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵事务开启失败: "+err.Error()), true
	}
	defer func() { _ = tx.Rollback() }()
	row, merged, err := grantPreparedItemTx(cctx, tx, prepared, true)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "饵入包失败: "+err.Error()), true
	}
	if err := addTxnTx(cctx, tx, ctx.Sess.Userid, "baitdig", baitKey, row.GUID, 0, 0,
		fmt.Sprintf("tool=%s qty=%d merged=%t", tool.ItemID, qty, merged)); err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵流水写入失败: "+err.Error()), true
	}
	if err := tx.Commit(); err != nil {
		return s.replyBaitDigFailed(ctx, "挖饵事务提交失败: "+err.Error()), true
	}

	after := mastery
	if mastery < baitMasteryCap {
		after = math.Min(baitMasteryCap, math.Round((mastery+baitMasteryStep)*10)/10)
		if err := s.DB.AddSkillPoints(cctx, ctx.Sess.Userid, captureBaitSkillID, baitMasteryStep); err != nil {
			ctx.Log.Warn("捕获鱼饵熟练度写入失败", "err", err)
			after = mastery
		}
	}
	gain := after - mastery
	tree, err := s.ItemToTree(cctx, row)
	if err != nil {
		return s.replyBaitDigFailed(ctx, "饵物品树构造失败: "+err.Error()), true
	}
	body, ok := s.replyBaitDigResult(ctx, tree, gain)
	if !ok {
		return nil, true
	}
	ctx.Log.Info("9/3 挖饵", "tool", tool.ItemID, "bait", baitKey, "qty", qty,
		"stack", stackCountFromRow(row), "mastery", fmt.Sprintf("%.1f->%.1f", mastery, after))
	ctx.AfterResponse(func() {
		ctx.Sess.Push2_4SkillTree("挖饵 " + baitKey)
		ctx.Sess.Push(4, 22, proto.Args(proto.S("61"), tree))
	})
	return body, true
}

func setPreparedItemQuantity(it *store.Item, qty int64) error {
	if it == nil || qty < 1 {
		return fmt.Errorf("无效数量 %d", qty)
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(it.WireClass, csQty)
	if idx < 0 || idx >= len(fields) {
		return fmt.Errorf("物品 %s 没有数量字段", it.ItemID)
	}
	fields[idx].Num = ptrU64(uint64(qty))
	bumpCreatedTicks(fields, it.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	it.FieldsJSON = fj
	it.Qty = qty
	return nil
}

func stackCountFromRow(it *store.Item) int64 {
	if it == nil {
		return 0
	}
	if it.Qty > 0 {
		return it.Qty
	}
	return 1
}

func (s *Server) replyBaitDigResult(ctx *Ctx, tree *proto.Value, masteryGain float64) ([]byte, bool) {
	items := proto.Args(proto.S("61"), tree)
	obj := wiregen.MustMakeObj("beckiiffkem", map[int]*proto.Value{
		0: proto.U32(1),
		1: items,
		2: masteryGainValue(captureBaitSkillID, masteryGain),
		3: proto.Null(),
		6: items,
		7: items,
	})
	obj.Fields[3].Val = s.buildSingleSkill312(ctx.Sess.pushCtx(), ctx.Sess.Userid, captureBaitSkillID)
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Warn("挖饵 803 编码失败", "err", err)
		return nil, false
	}
	return body, true
}

func (s *Server) replyBaitDigFailed(ctx *Ctx, reason string) []byte {
	ctx.Log.Warn("9/3 挖饵失败", "reason", reason)
	obj := wiregen.MustMakeObj("beckiiffkem", map[int]*proto.Value{
		0: proto.U32(0), 1: proto.Args(proto.S("61")), 2: proto.Null(),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return []byte{0x0F}
	}
	return body
}

func masteryGainValue(skillID int64, gain float64) *proto.Value {
	if gain < 0 {
		gain = 0
	}
	return wiregen.MustMakeObj("kegeppimjbm", map[int]*proto.Value{
		0: proto.U64(0),
		1: proto.U32(0),
		2: proto.U32(uint32(skillID)),
		3: proto.F32(float32(gain)),
		4: proto.Args(proto.S("245")),
		5: proto.U32(0),
		6: proto.Null(),
	})
}

func (s *Session) Push2_4SkillTree(why string) {
	tree, err := s.Srv.BuildSkillTree311(s.pushCtx(), s.Userid, ^uint64(0))
	if err != nil {
		s.log.Warn("2/4 技能树构造失败", "why", why, "err", err)
		return
	}
	s.Push(2, 4, proto.Args524(tree))
}
