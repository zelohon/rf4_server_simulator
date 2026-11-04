package gameserver

import (
	"context"
	"strconv"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 基础实现：轮管卡默认关闭，按竿持久化卡米数。
const (
	reelClipSettingKey = "reel_clip_m"
	maxReelClipM       = 10000
)

func clampReelClipM(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > maxReelClipM {
		return maxReelClipM
	}
	return v
}

func rigClipOf(v *proto.Value) int64 {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 5 {
		return 0
	}
	f := v.Field(5)
	if f.Num == nil {
		return 0
	}
	return clampReelClipM(int64(u32Of(f.Num)))
}

// buildRig153WithClip 构造 153 钓组对象（不带挂饵方式，f8/f9 全 0）。
func buildRig153WithClip(name string, rigType int64, comps map[int64]rigComp, depth, clipM int64) *proto.Value {
	return buildRig153Full(name, rigType, comps, depth, clipM, 0, 0)
}

// buildRig153Full 构造 153 钓组对象，写入挂饵方式的两个字段。
//
// 客户端 BaitMountType 在 153 里占两个字段，同为 cacifnemgdl 枚举：
//   - f8（fihgclhkkci）= 当前选中的安装方式，0 = 未选择，安装栏显示「没选择」；
//   - f9（cfdddmphebg）= 可选安装方式位掩码，0 = 一个选项都没有，弹窗全空。
//
// 2026-11-04 实测：f9=126 时「选择安装方式」弹窗列出 6 项
// （下沉香煮成饵 / 颗粒 / 沉+沉 / 颗+颗 / 颗+沉 / 沉+Pop-up），
// 玩家点选后客户端回 17/6，携带的是单个位（4/8/16/32/64）。
// 只把该值写进 f9 时弹窗能开、但安装栏始终显示「没选择」——选中的是 f8。
func buildRig153Full(name string, rigType int64, comps map[int64]rigComp, depth, clipM, baitMountSel, baitMountAvail int64) *proto.Value {
	if name == "" {
		name = "rig_FloatSimple"
	}
	return wiregen.MustMakeObj("gbbhdkefnkn", map[int]*proto.Value{
		0: proto.StrVal(name),
		1: proto.U32(uint32(rigType)),
		2: proto.Args(proto.S("81"), sortedRigCompItems(comps, rigType)...),
		3: proto.U32(0),
		4: proto.U32(uint32(depth)),
		5: proto.U32(uint32(clampReelClipM(clipM))),
		6: proto.U32(15),
		7: proto.U32(15),
		8: proto.U32(uint32(clampBaitMount(baitMountSel))),
		9: proto.U32(uint32(clampBaitMount(baitMountAvail))),
	})
}

// buildRig153Like 以已有的 153 对象为模板重建，保留挂饵方式的选中值与可选项。
// 模板缺失或可选项为 0 时补默认可选项，避免弹窗又变回空白。
func buildRig153Like(src *proto.Value, name string, rigType int64, comps map[int64]rigComp, depth, clipM int64) *proto.Value {
	sel, avail := rigBaitMountsOf(src)
	if avail == 0 {
		avail = defaultBaitMountAvail
	}
	return buildRig153Full(name, rigType, comps, depth, clipM, sel, avail)
}

// clampBaitMount 把挂饵方式收进 u32；位宽不设上限，
// 客户端 BaitMountType 是两个独立枚举值（选中 f8 / 可选掩码 f9），
// 枚举项数量未知，窄掩码会悄悄截掉高位选项。
func clampBaitMount(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > 0xffffffff {
		return 0xffffffff
	}
	return v
}

// rigBaitMountsOf 读 153 对象的 (选中值 f8, 可选项掩码 f9)。
func rigBaitMountsOf(v *proto.Value) (sel, avail int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 9 {
		return 0, 0
	}
	if f := v.Field(8); f.Num != nil {
		sel = clampBaitMount(int64(u32Of(f.Num)))
	}
	if f := v.Field(9); f.Num != nil {
		avail = clampBaitMount(int64(u32Of(f.Num)))
	}
	return sel, avail
}

// rigBaitMountSelOf 读 153 对象 f8（当前选中的挂饵方式）。
func rigBaitMountSelOf(v *proto.Value) int64 {
	sel, _ := rigBaitMountsOf(v)
	return sel
}

// setRigBaitMountSel 就地改写 153 对象 f8。
func setRigBaitMountSel(v *proto.Value, sel int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 8 {
		return
	}
	v.Fields[8].Num = ptrU64(uint64(clampBaitMount(sel)))
}

// setRigBaitMountAvail 就地改写 153 对象 f9。
func setRigBaitMountAvail(v *proto.Value, avail int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 9 {
		return
	}
	v.Fields[9].Num = ptrU64(uint64(clampBaitMount(avail)))
}

const baitMountSettingKey = "baitmount"

// defaultBaitMountAvail 是「可选安装方式」的默认掩码。
// 全 1：客户端自己按竿型 + 已挂鱼饵筛掉不适用的项，服务端不再猜位宽。
// 2026-11-04 实测 126 只覆盖低位（下沉煮饵/颗粒族），0x1F80 只覆盖人造玉米族，
// 都会让弹窗缺项；全 1 时弹窗给出该钓组完整的一组，玩家选中后按 f8 记忆。
var defaultBaitMountAvail int64 = 0xffffffff

// baitMountFor 返回渲染钓组时应写入 f8 的选中值：玩家选过的优先，否则 0（未选择）。
func (s *Server) baitMountFor(ctx context.Context, rodGUID string) int64 {
	if v, ok := s.savedBaitMount(ctx, rodGUID); ok {
		return v
	}
	return 0
}

// applyRigBaitMount 把玩家选中的安装方式写进竿实例 153.f8，并刷新版本戳。
// f9（可选项）保持不变，否则弹窗会只剩玩家刚点的那一项。
func (s *Server) applyRigBaitMount(ctx context.Context, rodGUID string, sel int64) error {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return err
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return nil
	}
	setRigBaitMountSel(fields[idx].Val, sel)
	if _, avail := rigBaitMountsOf(fields[idx].Val); avail == 0 {
		setRigBaitMountAvail(fields[idx].Val, defaultBaitMountAvail)
	}
	s.saveItemFields(ctx, row, fields)
	return nil
}

// savedBaitMount 读取玩家在某根竿上选过的挂饵方式。
func (s *Server) savedBaitMount(ctx context.Context, rodGUID string) (int64, bool) {
	if s.DB == nil || rodGUID == "" {
		return 0, false
	}
	raw, err := s.DB.GetRodSetting(ctx, rodGUID, baitMountSettingKey)
	if err != nil || raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return clampBaitMount(v), true
}

func (s *Server) savedReelClipM(ctx context.Context, rodGUID string) (int64, bool) {
	if s.DB == nil || rodGUID == "" {
		return 0, false
	}
	raw, err := s.DB.GetRodSetting(ctx, rodGUID, reelClipSettingKey)
	if err != nil || raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return clampReelClipM(v), true
}
