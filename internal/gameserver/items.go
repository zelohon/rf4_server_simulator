package gameserver

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// ---- 物品类别到 wire 类的映射 ----

type wireClassEntry struct {
	Class  string
	TypeID int64
}

// categoryWireClass 按客户端类别枚举选择 wire 类。
// 13/14/15 为前导、31 为食笼、44 为 Sbirolino、46 为成品窝料，
// 48..54 为渔轮子部件，59/60 为打窝器和弹弓。
var categoryWireClass = map[int64]wireClassEntry{
	3:  {"cnolibojkao", 157},
	4:  {"hgabifpnhmk", 156},
	5:  {"knocoeggfdk", 159}, // 导环（spin 竿实例使用 rcs_basic_spin）
	6:  {"dgjinoebdop", 149},
	7:  {"oiebifopill", 146},
	8:  {"lcajgpjolbo", 151},
	9:  {"loedbndnill", 147},
	10: {"plefjjahdgh", 150},
	11: {"ohciikgjghe", 140},
	12: {"agedjipcnpe", 131},
	13: {"oldfpclaabo", 137}, // 前导（f1/f2 钢丝/配件子实例物化）
	14: {"donkbnnmgjm", 139}, // 前导锁/配件
	15: {"cjigljdikhe", 138}, // 钢丝/编织前导
	17: {"ioombppcnlh", 141}, // 拟饵（f0 gpoaffnjbdo=args(60) 出厂钩数组）
	18: {"bigdipenecj", 124},
	22: {"hcahlnccaek", 114},
	23: {"blpddffbgjg", 115},
	25: {"nffjdjfkjem", 118},
	26: {"aafdnnmleob", 128},
	27: {"kbnbcppjkaf", 100},
	28: {"gjdmdcpoifh", 101},
	29: {"igjimikeadh", 161},
	31: {"cdgdbeoejeo", 119}, // 食笼
	34: {"bndpebpblfk", 148}, // 握丸（13 字段 reader；渔轮 f5 必需）
	35: {"dbmbnkpkafb", 164},
	36: {"kjggkpflhjp", 155},
	37: {"gfkbfjgkjoh", 116},
	39: {"bplepbfgpmk", 162},
	40: {"dbojlbebaca", 143},
	41: {"jceogpbfpjh", 163}, // 工坊工具；定义类型 12056 夹在票据实例 162 与露营灯 164 之间
	42: {"nacllfpgomk", 142}, // 工坊材料，可堆叠；定义类型 12026 夹在拟饵 141 与地图 143 之间
	44: {"fcdgfdomnjm", 160}, // Sbirolino
	46: {"cilmgoaoofg", 120}, // 成品窝料
	47: {"eebdgfednim", 700}, // 铃铛/咬钩信号器 dadgbbiijki
	48: {"npnkhkicfkg", 613}, // 渔轮导线环
	49: {"bnfpgceodij", 616}, // 制动钮
	50: {"jjikombcnnj", 614}, // 轮手柄
	51: {"gaoapgffamm", 615}, // 星形制动
	52: {"ibinolcnogf", 610},
	53: {"phdkhaocdai", 611},   // 齿轮 LED
	54: {"gpldfemlfch", 612},   // 蜗杆轴
	59: {"iknkgeomkip", 3200},  // Cobra 打窝器
	60: {"ifnaghdaolg", 3201},  // Slingshot 弹弓
	79: {"abmokghdbhf", 11500}, // 竿架（rod pod），客户端模型 pphmpibfgea
	81: {"dbjjakbbbfc", 12601}, // 扩展类别映射
}

const fallbackWireClass = "ohciikgjghe"
const fallbackTypeID = 140

// 客户端计件类别（可堆叠合并）；钓线(11)/前导(15) 同名字段是长度 m；浮漂(28) 显式非计件。
var countableCategories = map[int64]bool{
	14: true, 18: true, 19: true, 20: true, 26: true, 27: true,
	29: true, 30: true, 42: true, 45: true, 46: true, 58: true,
}

// fieldIdxByCS 按 cs 名（描述符属性名）定位字段 idx。
func fieldIdxByCS(class, csName string) int {
	meta := wiregen.ClassMeta[class]
	for _, fi := range meta.Fields {
		if fi.Name == csName {
			return fi.Idx
		}
	}
	return -1
}

// fieldOpAt 类的第 idx 个 op。
func fieldOpAt(class string, idx int) string {
	ops := wiregen.ClassOps[class]
	if idx < 0 || idx >= len(ops) {
		return ""
	}
	return ops[idx]
}

// fieldIsFloat 该位置的 op 是否浮点。
func fieldIsFloat(class string, idx int) bool {
	op := fieldOpAt(class, idx)
	return op == "FLOAT" || op == "DOUBLE"
}

// ---- 物品树重建 ----

// alignFieldsToOps 将字段快照对齐到类的 op 数量。
// 数量一致时原样返回，缺失字段补零；数量不符时按身份字段重建，
// 避免截断后把值写入错误的 op 类型。
func alignFieldsToOps(it *store.Item, fields []proto.Field) []proto.Field {
	ops := wiregen.ClassOps[it.WireClass]
	if len(ops) == 0 {
		return fields
	}
	if len(fields) == len(ops) {
		return fields
	}
	if len(fields) < len(ops) {
		out := make([]proto.Field, len(ops))
		copy(out, fields)
		for i := len(fields); i < len(ops); i++ {
			zf, err := proto.ZeroField(ops[i])
			if err != nil {
				return fields
			}
			out[i] = zf
		}
		return out
	}
	// 字段数量不符时按身份字段重建。
	out := make([]proto.Field, len(ops))
	for i, op := range ops {
		zf, err := proto.ZeroField(op)
		if err != nil {
			return fields
		}
		out[i] = zf
	}
	for i, op := range ops {
		switch op {
		case "GUID16":
			if out[i].Hex == "00000000000000000000000000000000" && it.GUID != "" {
				out[i].Hex = it.GUID
			}
		case "FLOAT", "DOUBLE":
			if cs := fieldCSName(it.WireClass, i); cs == "hjilekjlgbk" {
				out[i].Float = it.Workmanship
			} else if cs == "nmidlgejlcp" {
				out[i].Float = it.Durability
			}
		}
	}
	if idx := fieldIdxByCS(it.WireClass, csNumericID); idx >= 0 && idx < len(out) && out[idx].Num != nil {
		out[idx].Num = ptrU64(uint64(it.NumericID))
	}
	return out
}

// fieldCSName 类的第 idx 个字段 cs 名（无则空串）。
func fieldCSName(class string, idx int) string {
	meta := wiregen.ClassMeta[class]
	for _, fi := range meta.Fields {
		if fi.Idx == idx {
			return fi.Name
		}
	}
	return ""
}

// ItemToTree 由 PG item 行重建完整物品树（4/5、14/2、4/22 共用）。
func (s *Server) ItemToTree(ctx context.Context, it *store.Item) (*proto.Value, error) {
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return nil, err
	}
	fields = alignFieldsToOps(it, fields)
	obj := proto.Obj(it.WireClass, uint32(it.TypeID), fields...)

	class := it.WireClass

	// 状态位零值兜底（存量行为 0 → 客户端当未初始化）
	def, derr := s.DB.GetItemDef(ctx, it.NumericID)
	if derr != nil {
		def = nil
	}
	if def != nil {
		stateFlagsZeroFill(class, obj, def)
		// 先补齐嵌套子配置，再按定义+条件统一投影物理字段。这里不是“零值兜底”：
		// 强度、曳力、成色、容器枚举都由权威数据推导，保证发给客户端的树一致。
		s.materializeInto(ctx, class, def, obj, it.GUID, it.Workmanship, it.Durability)
		s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
	}

	// 鱼竿动态接管（竿上组件/钓组/挂线挂轮必须现算，快照过期会让插槽列表失真）
	if class == "cnolibojkao" {
		if err := s.rebuildRod(ctx, it, obj); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

// materializeInto 物化嵌套子配置：渔轮 f2..f14、前导 f1..f2、拟饵 f0 出厂钩。
// 只填充空槽，已有子对象的槽保持不变。
func (s *Server) materializeInto(ctx context.Context, class string, def *store.ItemDef, obj *proto.Value, guid string, workmanship, durability float64) {
	attrs := defAttrs(def)
	if attrs == nil {
		return
	}
	fill := func(keys []string, isArgs bool) {
		subs := s.materializeSubObjects(ctx, attrs, keys, guid, workmanship, durability)
		for key, sub := range subs {
			idx := fieldIdxByCS(class, key)
			if idx < 0 || idx >= len(obj.Fields) {
				continue
			}
			if obj.Fields[idx].Val != nil && obj.Fields[idx].Val.Kind != proto.KindNull {
				continue
			}
			obj.Fields[idx].Val = sub
		}
	}
	switch class {
	case "dgjinoebdop": // 渔轮 f2..f14
		fill(reelPartKeys, false)
	case "oldfpclaabo": // 前导 f1/f2（f3 线槽 def 无配置恒 null）
		fill(leaderPartKeys, false)
		// f0 强度等于钢丝配置 kngaaefkpka 乘以工艺。
		if idx := fieldIdxByCS(class, csStrength); idx >= 0 && fieldIsFloat(class, idx) && obj.Fields[idx].Float == 0 {
			if wireCfg, ok := attrs["mbnambppbom"].(map[string]any); ok {
				if bits, ok := wireCfg["kngaaefkpka"].(float64); ok {
					obj.Fields[idx].Float = float64(proto.F32FromBits(uint32(bits)) * float32(workmanship))
				}
			}
		}
	case "ioombppcnlh": // 拟饵 f0 = args(60) 出厂钩
		idx := fieldIdxByCS(class, "gpoaffnjbdo")
		if idx >= 0 && idx < len(obj.Fields) {
			if obj.Fields[idx].Val == nil || obj.Fields[idx].Val.Kind == proto.KindNull {
				if hooks := s.lureHooks(ctx, attrs, guid, workmanship, durability); hooks != nil {
					obj.Fields[idx].Val = hooks
				}
			}
		}
	}
}

// canonicalizeItemPhysicalFields 是物品物理字段的统一投影入口。
//
// fields_json 保存物品身份和状态，但工艺、耐久、强度、刹车片曳力和容器枚举
// 由数据模型权威派生，不能被快照中的空值覆盖。新建和读取物品共用此函数。
func (s *Server) canonicalizeItemPhysicalFields(ctx context.Context, it *store.Item, fields []proto.Field) bool {
	if s.DB == nil || it == nil || len(fields) == 0 {
		return false
	}
	changed := false
	class := it.WireClass
	for _, pair := range []struct {
		cs string
		v  float64
	}{{csWorkmanship, it.Workmanship}, {csDurability, it.Durability}} {
		idx := fieldIdxByCS(class, pair.cs)
		if setWireFloat(fields, class, idx, pair.v) {
			changed = true
		}
	}
	if idx := fieldIdxByCS(class, csSlotEnum); idx >= 0 && fieldOpAt(class, idx) == "BYTE" {
		// 根物品即使已装配，wire 容器枚举仍为 0。
		// container='rig' 是服务端归属，不是物品对象的 lmmhdlhogdc；
		// 只有母物品内部的嵌套子实例使用 rig(4)。
		if u64Of(fields[idx].Num) != 0 {
			fields[idx].Num = ptrU64(0)
			changed = true
		}
	}

	def, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil || def == nil {
		return changed
	}
	// 渔轮顶部 f0 表示当前齿轮传动比。必须先读已安装齿轮，只有找不到
	// 零件时才回退到母轮出厂定义；否则每次上线都会重置收线速度。
	reelGearApplied := false
	if class == "dgjinoebdop" {
		if _, gearDef, _, ok := s.selectedReelPart(ctx, fields, 52); ok {
			if ratio := itemDefF32(gearDef, gearAttrRatio); ratio > 0 {
				idx := fieldIdxByCS(class, csStrength)
				if setWireFloat(fields, class, idx, ratio*it.Workmanship) {
					changed = true
				}
				reelGearApplied = true
			}
		}
	}
	if !reelGearApplied {
		if strength, ok := strengthBitsFor(def, it.Durability); ok {
			idx := fieldIdxByCS(class, csStrength)
			if setWireFloat(fields, class, idx, strength) {
				changed = true
			}
		}
	}

	// 渔轮刹车片子是嵌套实例，客户端使用它自己的 f0。其值必须来自当前
	// 安装摩擦片的定义，而不是母轮出厂摩擦片，否则重登后摩擦力会归位。
	if class == "dgjinoebdop" {
		sub, dragDef, _, ok := s.selectedReelPart(ctx, fields, 9)
		if ok && sub.Class == "loedbndnill" && dragDef != nil {
			for _, pair := range []struct {
				cs string
				v  float64
			}{{csWorkmanship, it.Workmanship}, {csDurability, it.Durability}} {
				si := fieldIdxByCS(sub.Class, pair.cs)
				// 刹车片耐久是零件自己的磨损数据（工坊修复/换件依赖它），
				// 只在缺失（nil/0）时用母轮耐久初始化，已有值必须保留。
				if pair.cs == csDurability && si >= 0 && si < len(sub.Fields) {
					if f := sub.Fields[si]; f.Num != nil && proto.F32FromBits(uint32(*f.Num)) > 0 {
						continue
					}
				}
				if setWireFloat(sub.Fields, sub.Class, si, pair.v) {
					changed = true
				}
			}
			force := itemDefF32(dragDef, gearAttrDragMax) * it.Workmanship
			si := fieldIdxByCS(sub.Class, csDragForce)
			if setWireFloat(sub.Fields, sub.Class, si, force) {
				changed = true
			}
		}
	}
	if canonicalizeNestedItemContainers(class, fields) {
		changed = true
	}
	if s.canonicalizeNestedItemPhysicalFields(ctx, class, fields, it.Workmanship, it.Durability) {
		changed = true
	}
	return changed
}

// canonicalizeNestedItemPhysicalFields 将母物品内嵌子实例的强度、工艺和耐久
// 投影到客户端树。静态配置产生的子实例也必须具有非零强度。
func (s *Server) canonicalizeNestedItemPhysicalFields(ctx context.Context, class string, fields []proto.Field, workmanship, durability float64) bool {
	var objs []*proto.Value
	appendRange := func(start, end int) {
		for i := start; i <= end && i < len(fields); i++ {
			if v := fields[i].Val; v != nil && v.Kind == proto.KindObj {
				objs = append(objs, v)
			}
		}
	}
	switch class {
	case "cnolibojkao":
		appendRange(0, 2)
	case "dgjinoebdop":
		appendRange(2, 14)
	case "oldfpclaabo":
		appendRange(1, 2)
	case "ioombppcnlh":
		if len(fields) > 0 && fields[0].Val != nil {
			objs = append(objs, fields[0].Val.Items...)
		}
	}
	changed := false
	for _, obj := range objs {
		if obj == nil || obj.Kind != proto.KindObj {
			continue
		}
		if setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csWorkmanship), workmanship) {
			changed = true
		}
		// 子件耐久是零件自己的磨损数据（工坊修复/换件依赖它），
		// 只在缺失（nil/0）时用母物品耐久初始化，已有值必须保留。
		if si := fieldIdxByCS(obj.Class, csDurability); si >= 0 && si < len(obj.Fields) {
			cur := 0.0
			if f := obj.Fields[si]; f.Num != nil {
				cur = float64(proto.F32FromBits(uint32(*f.Num)))
			}
			if cur <= 0 && setWireFloat(obj.Fields, obj.Class, si, durability) {
				changed = true
			}
		}
		ni := fieldIdxByCS(obj.Class, csNumericID)
		if ni < 0 || ni >= len(obj.Fields) || obj.Fields[ni].Num == nil {
			continue
		}
		def, err := s.DB.GetItemDef(ctx, int64(u64Of(obj.Fields[ni].Num)))
		if err != nil || def == nil {
			continue
		}
		strength := 0.0
		strengthOK := false
		if def.Category == 52 {
			strength = itemDefF32(def, gearAttrRatio) * workmanship
			strengthOK = strength > 0
		} else {
			strength, strengthOK = strengthBitsFor(def, durability)
		}
		if strengthOK && strength > 0 {
			if setWireFloat(obj.Fields, obj.Class, fieldIdxByCS(obj.Class, csStrength), strength) {
				changed = true
			}
		}
	}
	return changed
}

// canonicalizeNestedItemContainers 把真实装配子实例的 base 容器统一为 rig(4)。
// 只处理明确由物品定义物化出来的嵌套对象，避免误改钓组/曲线等非物品树。
func canonicalizeNestedItemContainers(class string, fields []proto.Field) bool {
	changed := false
	setObj := func(obj *proto.Value) {
		if obj == nil || obj.Kind != proto.KindObj {
			return
		}
		si := fieldIdxByCS(obj.Class, csSlotEnum)
		if si >= 0 && fieldOpAt(obj.Class, si) == "BYTE" && u64Of(obj.Field(si).Num) != 4 {
			obj.Fields[si].Num = ptrU64(4)
			changed = true
		}
	}
	switch class {
	case "cnolibojkao":
		for i := 0; i < 3 && i < len(fields); i++ {
			setObj(fields[i].Val)
		}
	case "dgjinoebdop":
		for i := 2; i <= 14 && i < len(fields); i++ {
			setObj(fields[i].Val)
		}
	case "oldfpclaabo":
		for i := 1; i <= 2 && i < len(fields); i++ {
			setObj(fields[i].Val)
		}
	case "ioombppcnlh":
		if len(fields) > 0 && fields[0].Val != nil {
			for _, it := range fields[0].Val.Items {
				setObj(it)
			}
		}
	}
	return changed
}

// setWireFloat 写入 wire 浮点字段；兼容 FLOAT/DOUBLE 与 U32 承载 IEEE754 的
// registry 布局。返回是否真的改变，供一次性数据修复决定是否落库。
func setWireFloat(fields []proto.Field, class string, idx int, v float64) bool {
	if idx < 0 || idx >= len(fields) {
		return false
	}
	switch fieldOpAt(class, idx) {
	case "FLOAT", "DOUBLE":
		if fields[idx].Float == v {
			return false
		}
		fields[idx].Float = v
		return true
	case "U32":
		bits := math.Float32bits(float32(v))
		if uint32(u64Of(fields[idx].Num)) == bits {
			return false
		}
		fields[idx].Num = ptrU64(uint64(bits))
		return true
	default:
		return false
	}
}

// rebuildRod 鱼竿 idx {0,1,2,3,4,5,16,18} 动态重建。
func (s *Server) rebuildRod(ctx context.Context, it *store.Item, obj *proto.Value) error {
	def, err := s.DB.GetItemDef(ctx, it.NumericID)
	if err != nil {
		return nil // 无定义：保留快照
	}
	// 出厂部件 f0/f1/f2（attrs: ajkflmejnih/ofpkgghjmmf/ddbadcmdkcl，值是内嵌
	// 部件定义对象，systemID 在 nhjofmmmngl —— 不能当字符串取）
	slots := []string{"ajkflmejnih", "ofpkgghjmmf", "ddbadcmdkcl"}
	for i, attr := range slots {
		if i >= 3 {
			break
		}
		sysID, ok := rodPartSystemID(def, attr)
		if !ok || sysID == "" {
			continue
		}
		// 部件以独立子对象重建，GUID 由竿 GUID 和槽名稳定派生。
		// 状态位必须取竿配置中的基值，不能使用部件独立定义中的装配位，
		// 否则客户端会拒绝渔轮收线输入。
		child, cerr := s.buildFactoryChild(ctx, it, sysID, i)
		if cerr != nil || child == nil {
			continue
		}
		obj.Fields[i].Val = child
	}

	// f3 挂线 / f4 挂轮（DB rig_guid 反查优先，否则沿用实例已有值）
	if g, err := s.DB.GetItemGUIDByRigSlot(ctx, it.GUID, 11); err == nil && g != "" {
		obj.Fields[3].Hex = g
	}
	if g, err := s.DB.GetItemGUIDByRigSlot(ctx, it.GUID, 6); err == nil && g != "" {
		obj.Fields[4].Hex = g
	}

	// f5 钓组对象（153）——绝不能 null
	if obj.Fields[5].Val == nil || obj.Fields[5].Val.Kind == proto.KindNull {
		obj.Fields[5].Val = s.defaultRig153(ctx, it)
	}
	// 出厂部件必须同时出现在 f5 的零件列表里：客户端的工作坊/装配界面
	// 都按 f5 的 gcfcdnfmnbh 逐行渲染，缺了它们零件行就是空的。
	s.appendFactoryPartsToRig(ctx, obj)

	// f16 Ticks now / f18 状态位 = 定义 | 2048
	if idx := fieldIdxByCS("cnolibojkao", csCreated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(netTicksNow()))
	}
	if idx := fieldIdxByCS("cnolibojkao", "hgaijieklek"); idx >= 0 {
		st := u64Of(obj.Fields[idx].Num)
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = defBits
		}
		obj.Fields[idx].Num = ptrU64(st | 2048)
	}
	return nil
}

// appendFactoryPartsToRig 把出厂部件（f0..f2 内嵌子对象）补进 f5 的零件列表。
//
// f5 的 field[2] 是 gcfcdnfmnbh 列表（品类、guid、附加值），客户端工作坊
// 与装配界面都按它逐行渲染零件。只写 f0..f2 而不登记进 f5 时，零件行为空。
// 已存在的 guid 不重复添加；非竿或没有 f5 时直接返回。
func (s *Server) appendFactoryPartsToRig(ctx context.Context, obj *proto.Value) {
	if obj == nil || obj.Kind != proto.KindObj || obj.FieldCount() <= 5 {
		return
	}
	rig := obj.Fields[5].Val
	if rig == nil || rig.Kind != proto.KindObj || rig.FieldCount() <= 2 {
		return
	}
	arr := rig.Fields[2].Val
	if arr == nil {
		return
	}
	seen := map[string]bool{}
	for _, e := range arr.Items {
		if e == nil || e.Kind != proto.KindObj || e.FieldCount() < 2 {
			continue
		}
		if g := e.Fields[1].Hex; g != "" {
			seen[g] = true
		}
	}
	for i := 0; i < 3 && i < obj.FieldCount(); i++ {
		child := obj.Fields[i].Val
		if child == nil || child.Kind != proto.KindObj {
			continue
		}
		gidx := fieldIdxByCS(child.Class, csGUID)
		if gidx < 0 || gidx >= child.FieldCount() {
			continue
		}
		g := child.Fields[gidx].Hex
		if g == "" || seen[g] {
			continue
		}
		var cat uint64
		if nidx := fieldIdxByCS(child.Class, csNumericID); nidx >= 0 && nidx < child.FieldCount() {
			if def, err := s.DB.GetItemDef(ctx, int64(u64Of(child.Fields[nidx].Num))); err == nil && def != nil {
				cat = uint64(def.Category)
			}
		}
		arr.Items = append(arr.Items, wiregen.MustMakeObj("gcfcdnfmnbh", map[int]*proto.Value{
			0: proto.U32(uint32(cat)),
			1: proto.Guid(g),
			2: proto.U32(0),
		}))
		seen[g] = true
	}
}

// buildFactoryChild 出厂部件子实例（guid = md5(竿guid|槽名) 稳定派生）。
// 状态位使用竿定义中该槽配置的 hgaijieklek 基值。
// 必须在 applyDefAttrs 之后写入，否则会被定义属性覆盖。
func (s *Server) buildFactoryChild(ctx context.Context, rod *store.Item, sysID string, partIdx int) (*proto.Value, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, sysID)
	if err != nil || def == nil {
		return nil, err
	}
	cls, ok := categoryWireClass[def.Category]
	if !ok {
		cls = struct {
			Class  string
			TypeID int64
		}{fallbackWireClass, fallbackTypeID}
	}
	slotNames := []string{"rod", "handle", "third"}
	sum := md5.Sum([]byte(rod.GUID + "|" + slotNames[partIdx]))
	childGUID := fmt.Sprintf("%x", sum)
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	setState := func(v uint64) {
		if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setGUID := func() {
		if idx := fieldIdxByCS(cls.Class, csGUID); idx >= 0 {
			obj.Fields[idx].Hex = childGUID
		} else if fieldOpAt(cls.Class, 0) == "GUID16" {
			obj.Fields[0].Hex = childGUID
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setFloat := func(cs string, v float64) {
		idx := fieldIdxByCS(cls.Class, cs)
		if idx < 0 {
			return
		}
		if fieldIsFloat(cls.Class, idx) {
			obj.Fields[idx].Float = v
			return
		}
		if fieldOpAt(cls.Class, idx) == "U32" {
			obj.Fields[idx].Num = ptrU64(uint64(math.Float32bits(float32(v))))
		}
	}
	setGUID()
	setNum(csNumericID, uint64(def.NumericID)) // numeric_id（基类 cs）
	setNum(csSlotEnum, 4)                      // 装配子件：容器为 rig(4)
	// 类前缀字段按 attrs 回填；状态位来源是**部件独立定义行**。
	s.applyDefAttrs(def, cls.Class, obj)
	base := u64Of(firstNonNil(obj.Fields, fieldIdxByCS(cls.Class, csStateEnum)).Num)
	if base == 0 {
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			base = defBits
		}
	}
	// 只有竿坯增加 2048 出厂装配位；手柄和第三件保持部件定义态。
	if partIdx == 0 {
		base |= 2048
	}
	// 子部件必须继承母竿的工艺和耐久，否则客户端会显示“损耗 100%”，
	// 并将部件物理强度按 0 处理。
	setFloat(csWorkmanship, rod.Workmanship)
	setFloat(csDurability, rod.Durability)
	setState(base)
	// parent 指向竿
	if idx := fieldIdxByCS(cls.Class, csParent); idx >= 0 {
		obj.Fields[idx].Hex = rod.GUID
	} else if fieldOpAt(cls.Class, 1) == "GUID16" || fieldOpAt(cls.Class, 1) == "OPTGUID" {
		obj.Fields[1].Hex = rod.GUID
	}
	_ = setNum
	return obj, nil
}

func firstNonNil(fields []proto.Field, idx int) proto.Field {
	if idx >= 0 && idx < len(fields) {
		return fields[idx]
	}
	return proto.Field{}
}

// defaultRig153 在 f5 缺失时按竿型构造默认钓组。
// 钓线和渔轮放在 f3/f4 专用字段中，不进入 f5；沉底族不包含浮漂组件。
func (s *Server) defaultRig153(ctx context.Context, it *store.Item) *proto.Value {
	components := proto.Args(proto.S("81"))
	rigType := s.rigTypeOfItem(ctx, it)
	name := rigNameForType(rigType)
	depth := defaultDepthFor(name, rigType)
	if comps, err := s.DB.ListRigComponents(ctx, it.GUID); err == nil {
		for _, rc := range comps {
			if rc.GUID == "" {
				continue
			}
			if _, special := categorySlotField[rc.Slot]; special {
				continue // 钓线/渔轮：写竿的 f3/f4 专用字段，不进 f5
			}
			if bottomRigTypes[rigType] && rc.Slot == 28 {
				continue // 沉底族无浮漂槽
			}
			components.Items = append(components.Items,
				wiregen.MustMakeObj("gcfcdnfmnbh", map[int]*proto.Value{
					0: proto.U32(uint32(rc.Slot)),
					1: proto.Guid(rc.GUID),
					// 读取时再次规范化槽位索引。
					2: proto.U32(uint32(canonicalIndex(rc.Slot, rc.Extra, rigType))),
				}))
		}
	}
	clipM, _ := s.savedReelClipM(ctx, it.GUID)
	return wiregen.MustMakeObj("gbbhdkefnkn", map[int]*proto.Value{
		0: proto.StrVal(name),
		1: proto.U32(uint32(rigType)),
		2: components,
		4: proto.U32(uint32(depth)),
		5: proto.U32(uint32(clipM)),
		6: proto.U32(15),
		7: proto.U32(15),
	})
}

// applyDefAttrs 将 item_def.attrs_json 回填到对象；浮点值使用 IEEE754 位模式。
func (s *Server) applyDefAttrs(def *store.ItemDef, class string, obj *proto.Value) {
	if def.AttrsJSON == "" {
		return
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(def.AttrsJSON), &attrs); err != nil {
		return
	}
	for cs, val := range attrs {
		idx := fieldIdxByCS(class, cs)
		if idx < 0 || idx >= len(obj.Fields) {
			continue
		}
		switch tv := val.(type) {
		case float64:
			if fieldIsFloat(class, idx) {
				// 配置中的浮点是 IEEE754 位模式整数，入站时必须按 f32 解释。
				if fieldOpAt(class, idx) == "DOUBLE" {
					obj.Fields[idx].Float = proto.F64FromBits(uint64(tv))
				} else {
					obj.Fields[idx].Float = float64(proto.F32FromBits(uint32(tv)))
				}
			} else {
				obj.Fields[idx].Num = ptrU64(uint64(tv))
			}
		case string:
			sv := tv
			obj.Fields[idx].Str = &sv
		case bool:
			if tv {
				obj.Fields[idx].Num = ptrU64(1)
			} else {
				obj.Fields[idx].Num = ptrU64(0)
			}
		}
	}
}

// ---- attrs 读取辅助 ----

func defAttrs(def *store.ItemDef) map[string]any {
	if def == nil || def.AttrsJSON == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(def.AttrsJSON), &m); err != nil {
		return nil
	}
	return m
}

func defAttrF32(def *store.ItemDef, key string) (uint32, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return uint32(v), true
	}
	return 0, false
}

func defAttrU64(def *store.ItemDef, key string) (uint64, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return uint64(v), true
	}
	return 0, false
}

func defAttrStr(def *store.ItemDef, key string) (string, bool) {
	m := defAttrs(def)
	if m == nil {
		return "", false
	}
	if s, ok := m[key].(string); ok {
		return s, true
	}
	return "", false
}

// rodPartSystemID 读取鱼竿出厂部件槽的 systemID。
// 三个槽位（竿坯、手柄、第三件）保存的是内嵌部件定义对象，
// systemID 位于对象的 nhjofmmmngl 字段。
func rodPartSystemID(def *store.ItemDef, key string) (string, bool) {
	m := defAttrs(def)
	if m == nil {
		return "", false
	}
	switch v := m[key].(type) {
	case string:
		if v != "" {
			return v, true
		}
	case map[string]any:
		if s, ok := v["nhjofmmmngl"].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func ptrU64(v uint64) *uint64 { return &v }

// rodPartConfigBits 读取竿定义中某部件槽的 hgaijieklek 基值。
// 取不到时返回 (0,false)，调用方回退到部件定义行。
func rodPartConfigBits(def *store.ItemDef, key string) (uint64, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	cfg, ok := m[key].(map[string]any)
	if !ok || cfg == nil {
		return 0, false
	}
	switch v := cfg["hgaijieklek"].(type) {
	case float64:
		return uint64(v), true
	case int64:
		return uint64(v), true
	case int:
		return uint64(v), true
	}
	return 0, false
}

// ---- 新实例创建 ----

// NewItemGUID 生成 32hex 随机 guid。
func NewItemGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}

// netTicksNow .NET Ticks（物品 f7 创建时间用）。
func netTicksNow() int64 {
	// 100ns ticks；必须保留亚秒，f22 依赖严格单调的版本戳。
	return time.Now().UnixNano()/100 + 621_355_968_000_000_000
}

// reclassifyStoredItem 把误落成钓线类的物品改写成该类别自己的实例类。
// 已购工具、材料和铃铛靠这一步恢复实例类型，不必重买。
// guid、容器、数量、耐久和工艺保持不变。类别已经对上，或不是钓线回退类时不改。
func (s *Server) reclassifyStoredItem(ctx context.Context, it *store.Item, def *store.ItemDef) (bool, error) {
	if it == nil || def == nil || (def.Category != 41 && def.Category != 42 && def.Category != 47) {
		return false, nil
	}
	cls, ok := categoryWireClass[def.Category]
	if !ok || (it.WireClass == cls.Class && it.TypeID == cls.TypeID) {
		return false, nil
	}
	if it.WireClass != "" && it.WireClass != fallbackWireClass {
		return false, nil
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return false, err
	}
	s.applyDefAttrs(def, cls.Class, obj)
	if def.Category == 47 {
		old, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			return false, err
		}
		// 换实例类型时保留已挂载铃铛的父引用和共同状态字段。
		for _, field := range wiregen.ClassMeta[cls.Class].Fields {
			idx := fieldIdxByCS(it.WireClass, field.Name)
			if idx >= 0 && idx < len(old) && old[idx].Op == obj.Fields[field.Idx].Op {
				obj.Fields[field.Idx] = old[idx]
			}
		}
	}
	setHex := func(cs, v string) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Hex = v
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setHex(csGUID, it.GUID)
	setNum(csNumericID, uint64(def.NumericID))
	rowQty := it.Qty
	if countableCategories[def.Category] {
		if idx := fieldIdxByCS(cls.Class, csQty); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(uint64(rowQty))
		}
	}
	workmanship := it.Workmanship
	durability := it.Durability
	s.materializeInto(ctx, cls.Class, def, obj, it.GUID, workmanship, durability)
	if idx := fieldIdxByCS(cls.Class, csCreated); idx >= 0 && u64Of(obj.Fields[idx].Num) == 0 {
		obj.Fields[idx].Num = ptrU64(uint64(netTicksNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csUpdated); idx >= 0 && u64Of(obj.Fields[idx].Num) == 0 {
		obj.Fields[idx].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 && (def.Category != 47 || u64Of(obj.Fields[idx].Num) == 0) {
		st := u64Of(obj.Fields[idx].Num)
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = defBits
		}
		obj.Fields[idx].Num = ptrU64(st)
	}
	it.WireClass = cls.Class
	it.TypeID = cls.TypeID
	it.Qty = rowQty
	it.Workmanship = workmanship
	it.Durability = durability
	s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
	fieldsJSON, err := FieldsToJSON(obj.Fields)
	if err != nil {
		return false, err
	}
	it.FieldsJSON = fieldsJSON
	if err := s.DB.UpdateItemWire(ctx, it.GUID, cls.Class, cls.TypeID, fieldsJSON, rowQty); err != nil {
		return false, err
	}
	return true, nil
}

// CreateItem 按 itemID 创建新实例并入包，返回数据库行。
// 查不到定义时返回错误，由调用方处理退款。
func (s *Server) CreateItem(ctx context.Context, uid int64, itemID, container string, units int64) (*store.Item, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, fmt.Errorf("物品定义不存在: %s", itemID)
	}
	cls, ok := categoryWireClass[def.Category]
	if !ok {
		cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
	}
	obj, err := wiregen.MakeObj(cls.Class, nil)
	if err != nil {
		return nil, err
	}
	s.applyDefAttrs(def, cls.Class, obj)

	// 基类 13 字段按 cs 名写入
	guid := NewItemGUID()
	setHex := func(cs, v string) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Hex = v
		}
	}
	setNum := func(cs string, v uint64) {
		if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 {
			obj.Fields[idx].Num = ptrU64(v)
		}
	}
	setHex(csGUID, guid) // guid
	setNum(csNumericID, uint64(def.NumericID))
	rowQty := int64(1)
	// 数量 ljnnpokapig：计件 = 定义单位量×units，item.qty 必须与字段同值；
	// 线(11)/前导(15) 是长度，不在此分支。
	if countableCategories[def.Category] {
		if idx := fieldIdxByCS(cls.Class, csQty); idx >= 0 {
			defQty := u64Of(obj.Fields[idx].Num)
			if defQty == 0 {
				defQty = 1
			}
			fieldQty := defQty * uint64(units)
			obj.Fields[idx].Num = ptrU64(fieldQty)
			rowQty = int64(fieldQty)
		}
	}
	// 嵌套子配置物化（渔轮/前导/拟饵）
	s.materializeInto(ctx, cls.Class, def, obj, guid, 1.0, 1.0)
	// 时间：f7 Ticks / f8 FILETIME
	if idx := fieldIdxByCS(cls.Class, csCreated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(netTicksNow()))
	}
	if idx := fieldIdxByCS(cls.Class, csUpdated); idx >= 0 {
		obj.Fields[idx].Num = ptrU64(uint64(proto.WindowsFiletimeNow()))
	}
	// 状态位 = 定义 hgaijieklek（装备类 |2048）
	if idx := fieldIdxByCS(cls.Class, csStateEnum); idx >= 0 {
		st := u64Of(obj.Fields[idx].Num)
		if defBits, ok := defAttrU64(def, "hgaijieklek"); ok {
			st = defBits
		}
		if cls.Class == "cnolibojkao" || cls.Class == "dgjinoebdop" || cls.Class == "hgabifpnhmk" ||
			cls.Class == "knocoeggfdk" || cls.Class == "dbmbnkpkafb" {
			st |= 2048
		}
		obj.Fields[idx].Num = ptrU64(st)
	}

	it := &store.Item{
		GUID: guid, UID: uid, ItemID: def.ItemID, NumericID: def.NumericID,
		WireClass: cls.Class, TypeID: cls.TypeID, Container: container, Slot: 0,
		Qty: rowQty, Durability: 1.0, Workmanship: 1.0,
		CreatedTS: float64(time.Now().Unix()),
	}
	// 竿的厂件 f0/f1/f2 与默认钓组 f5 必须在创建时落库，不能只靠 ItemToTree。
	if cls.Class == "cnolibojkao" {
		if err := s.rebuildRod(ctx, it, obj); err != nil {
			return nil, err
		}
	}
	s.canonicalizeItemPhysicalFields(ctx, it, obj.Fields)
	fieldsJSON, err := FieldsToJSON(obj.Fields)
	if err != nil {
		return nil, err
	}
	it.FieldsJSON = fieldsJSON
	if err := s.DB.InsertItem(ctx, it); err != nil {
		return nil, err
	}
	return it, nil
}

// prepareNewItemWithCount 按“实际件数”构造可堆叠物品，绕开定义单位量换算。
// 铲子挖饵需要精确发放 1-5 个，而不是 1 个购买单位（自然饵通常为 30 个/单位）。
func (s *Server) prepareNewItemWithCount(ctx context.Context, uid int64, itemID, container string,
	count int64, explicitGUID string) (*store.Item, error) {
	if count < 1 {
		count = 1
	}
	it, err := s.prepareNewItem(ctx, uid, itemID, container, 1, explicitGUID)
	if err != nil {
		return nil, err
	}
	fields, err := FieldsFromJSON(it.FieldsJSON)
	if err != nil {
		return nil, err
	}
	if idx := fieldIdxByCS(it.WireClass, csQty); idx >= 0 && idx < len(fields) {
		fields[idx].Num = ptrU64(uint64(count))
		fj, ferr := FieldsToJSON(fields)
		if ferr != nil {
			return nil, ferr
		}
		it.FieldsJSON = fj
	}
	it.Qty = count
	return it, nil
}

// AddToStack 为可堆叠物品合并数量，返回现有物品或新行。
func (s *Server) AddToStack(ctx context.Context, uid int64, itemID, container string, units int64) (*store.Item, bool, error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, false, fmt.Errorf("物品定义不存在: %s", itemID)
	}
	if !countableCategories[def.Category] {
		it, err := s.CreateItem(ctx, uid, itemID, container, units)
		return it, false, err
	}
	cls := wireClassOf(def.Category)
	qtyIdx := fieldIdxByCS(cls, "ljnnpokapig")
	defQty := int64(1)
	if v, ok := defAttrU64(def, csQty); ok && v > 0 {
		defQty = int64(v)
	}
	addQty := defQty * units
	// 找同类同 ID 堆
	guids, _ := s.DB.ItemGUIDsByContainers(ctx, uid, []string{container})
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row.ItemID != itemID {
			continue
		}
		fields, _ := FieldsFromJSON(row.FieldsJSON)
		if qtyIdx >= 0 && qtyIdx < len(fields) {
			// 若 row.qty 与字段值不一致，以字段值为基准修正后再累加。
			cur := int64(u64Of(fields[qtyIdx].Num))
			if cur > 0 {
				row.Qty = cur
			}
			fields[qtyIdx].Num = ptrU64(uint64(cur + addQty))
		}
		fj, _ := FieldsToJSON(fields)
		newQty := row.Qty + addQty
		_ = s.DB.UpdateItemFields(ctx, row.GUID, fj, newQty, row.Durability, row.Workmanship, row.RigGUID)
		row.Qty = newQty
		row.FieldsJSON = fj
		return row, true, nil
	}
	it, err := s.CreateItem(ctx, uid, itemID, container, units)
	return it, false, err
}

func wireClassOf(cat int64) string {
	if c, ok := categoryWireClass[cat]; ok {
		return c.Class
	}
	return fallbackWireClass
}
