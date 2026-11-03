package gameserver

import (
	"context"
	"math"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 子部件物化。
//
// 标量规格由 applyDefAttrs 回填；嵌套子配置需要物化为子对象；
// 客户端可按数字 id 自查的基类部件无需物化。

// reelPartKeys 按 f2..f14 的顺序保存渔轮子部件配置键。
var reelPartKeys = []string{
	"dlfjnokncfl", "mlfjkaoicdn", "fkmdfljgann", "mglgpmpeond",
	"fblnceginlm", "ofpkgghjmmf", "ndmdiaeeggg", "ddpggjlhgki",
	"oiaplokhcii", "hllnkgmfpkn", "geibgaipbda", "eokpjdaohfm",
	"dfjpnambjcj",
}

// leaderPartKeys 保存前导子实例键：f1 钢丝、f2 配件；f3 没有嵌套配置。
var leaderPartKeys = []string{"mbnambppbom", "lbadjgllglo", "baadhdmaonp"}

// subUnbuildedKinds 列出暂不物化的子部件类型；当前包含 kind 81。
var subUnbuildedKinds = map[float64]bool{81: true}

// subMountFlag 表示子部件已挂载到母物品。
const subMountFlag = 1024

// materializeSubObjects 根据嵌套配置物化子部件对象。
// GUID 由母物品 GUID 和槽位键确定性派生，parent 指向母物品；
// 数字 id 和状态位来自子定义。子定义缺失或类型被豁免时保持空槽。
func (s *Server) materializeSubObjects(ctx context.Context, attrs map[string]any, keys []string, hostGUID string, workmanship, durability float64) map[string]*proto.Value {
	out := map[string]*proto.Value{}
	for _, key := range keys {
		cfg, _ := attrs[key].(map[string]any)
		if cfg == nil {
			continue
		}
		subID, _ := cfg["nhjofmmmngl"].(string)
		if subID == "" {
			continue
		}
		kind, _ := cfg["mgkfaekpnoj"].(float64)
		if subUnbuildedKinds[kind] {
			continue
		}
		subDef, err := s.DB.GetItemDefByItemID(ctx, subID)
		if err != nil || subDef == nil || subDef.NumericID == 0 {
			continue // nid 0 是真实物品 ab_tea_can，缺定义一律跳过
		}
		cls, ok := categoryWireClass[subDef.Category]
		if !ok {
			cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
		}
		if wiregen.ClassOps[cls.Class] == nil {
			continue // 类无 reader：槽保持空
		}
		obj, err := wiregen.MakeObj(cls.Class, nil)
		if err != nil {
			continue
		}
		s.applyDefAttrs(subDef, cls.Class, obj)
		setHex := func(cs, v string) {
			if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 && idx < len(obj.Fields) {
				obj.Fields[idx].Hex = v
			}
		}
		setNum := func(cs string, v uint64) {
			if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 && idx < len(obj.Fields) {
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
		setHex(csGUID, uuidLike(hostGUID, key)) // guid
		setNum(csNumericID, uint64(subDef.NumericID))
		setHex(csParent, hostGUID) // parent
		setNum(csSlotEnum, 4)      // 装配子实例容器=rig
		if base, ok := cfg["hgaijieklek"].(float64); ok {
			setNum(csStateEnum, uint64(base)|subMountFlag)
		} else if defFlags, ok := defAttrU64(subDef, "hgaijieklek"); ok {
			setNum(csStateEnum, defFlags|subMountFlag)
		}
		setFloat(csWorkmanship, workmanship)
		setFloat(csDurability, durability)
		if strength, ok := strengthBitsFor(subDef, durability); ok && strength > 0 {
			setFloat(csStrength, strength)
		}
		// 刹车片的曳力来自母轮嵌套配置 mlfjkaoicdn.omghfmdfjpc，
		// 不能使用母轮顶层强度或子定义的同名字段。
		if subDef.Category == 9 {
			if bits, ok := cfg["omghfmdfjpc"].(float64); ok {
				strength := float64(proto.F32FromBits(uint32(bits))) * workmanship
				setFloat(csDragForce, strength)
			}
		}
		out[key] = obj
	}
	return out
}

// lureHooks 物化拟饵出厂钩，返回 args(60) 容器；定义无钩配置时返回 nil。
// gpoaffnjbdo 是配置数组，每个元素的 nhjofmmmngl 是钩的 item_id。
func (s *Server) lureHooks(ctx context.Context, attrs map[string]any, lureGUID string, workmanship, durability float64) *proto.Value {
	cfgs, _ := attrs["gpoaffnjbdo"].([]any)
	if len(cfgs) == 0 {
		return nil
	}
	items := proto.Args(proto.S("60"))
	for i, raw := range cfgs {
		cfg, _ := raw.(map[string]any)
		if cfg == nil {
			continue
		}
		subID, _ := cfg["nhjofmmmngl"].(string)
		if subID == "" {
			continue
		}
		subDef, err := s.DB.GetItemDefByItemID(ctx, subID)
		if err != nil || subDef == nil || subDef.NumericID == 0 {
			continue
		}
		cls, ok := categoryWireClass[subDef.Category]
		if !ok {
			cls = wireClassEntry{fallbackWireClass, fallbackTypeID}
		}
		if wiregen.ClassOps[cls.Class] == nil {
			continue
		}
		obj, err := wiregen.MakeObj(cls.Class, nil)
		if err != nil {
			continue
		}
		s.applyDefAttrs(subDef, cls.Class, obj)
		setHex := func(cs, v string) {
			if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 && idx < len(obj.Fields) {
				obj.Fields[idx].Hex = v
			}
		}
		setNum := func(cs string, v uint64) {
			if idx := fieldIdxByCS(cls.Class, cs); idx >= 0 && idx < len(obj.Fields) {
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
		setHex(csGUID, uuidLike(lureGUID, "gpoaffnjbdo"+itoa(i)))
		setNum(csNumericID, uint64(subDef.NumericID))
		setHex(csParent, lureGUID)
		setNum(csSlotEnum, 4)
		if base, ok := cfg["hgaijieklek"].(float64); ok {
			setNum(csStateEnum, uint64(base))
		}
		setFloat(csWorkmanship, workmanship)
		setFloat(csDurability, durability)
		if strength, ok := strengthBitsFor(subDef, durability); ok && strength > 0 {
			setFloat(csStrength, strength)
		}
		items.Items = append(items.Items, obj)
	}
	if len(items.Items) == 0 {
		return nil
	}
	return items
}

// itoa 小整数转串（下标派生用）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// strengthBitsFor 返回物品强度。渔轮的齿轮强度是传动比 oiaplokhcii.dpgbopbanal
// （官方观测 3.4..11.7），摩擦制动力 omghfmdfjpc 只写在刹车片子上，两者不能是同一个数。
// 其他类型读取顶层 kngaaefkpka。实例强度等于定义值乘以耐久。
func strengthBitsFor(def *store.ItemDef, durability float64) (float64, bool) {
	if def != nil && def.Category == 6 {
		ratio := reelGearRatio(def)
		if ratio <= 0 {
			return 0, false
		}
		return ratio * durability, true
	}
	attrs := defAttrs(def)
	if attrs == nil {
		return 0, false
	}
	v, ok := attrs["kngaaefkpka"].(float64)
	if !ok {
		return 0, false
	}
	// attrs 中的浮点使用 IEEE754 位模式整数。
	strength := float64(proto.F32FromBits(uint32(v))) * durability
	return strength, true
}

// stateFlagsZeroFill 在状态位为 0 时回填定义基值。
// 保持非零状态不变。
func stateFlagsZeroFill(class string, obj *proto.Value, def *store.ItemDef) {
	idx := fieldIdxByCS(class, csStateEnum)
	if idx < 0 || idx >= len(obj.Fields) {
		return
	}
	if u64Of(obj.Fields[idx].Num) != 0 {
		return
	}
	if flags, ok := defAttrU64(def, "hgaijieklek"); ok && flags != 0 {
		obj.Fields[idx].Num = ptrU64(flags)
	}
}
