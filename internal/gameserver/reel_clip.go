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

// buildRig153WithClip 构造 153 钓组对象（不携带挂饵方式，f9 落 0）。
func buildRig153WithClip(name string, rigType int64, comps map[int64]rigComp, depth, clipM int64) *proto.Value {
	return buildRig153Full(name, rigType, comps, depth, clipM, 0)
}

// buildRig153Full 构造 153 钓组对象，并显式写入挂饵方式位掩码。
//
// f9（cfdddmphebg）是客户端的 BaitMountType 掩码：0 = 未选择，
// 2 = 煮饵沉底、4 = 颗粒，其余位见客户端 kBaitMountType_* 枚举。
// 客户端读 0 时不提供任何挂饵方式，鱼饵列表也会被判为不适配，
// 因此重建钓组时必须把玩家已选的值原样带回去，不能每次落 0。
func buildRig153Full(name string, rigType int64, comps map[int64]rigComp, depth, clipM, baitMount int64) *proto.Value {
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
		8: proto.U32(0),
		9: proto.U32(uint32(clampBaitMount(baitMount))),
	})
}

// clampBaitMount 把挂饵方式位掩码收进 u32 低 16 位。
func clampBaitMount(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > 0xffff {
		return 0xffff
	}
	return v
}

// rigBaitMountOf 读 153 对象 f9（挂饵方式位掩码）。
func rigBaitMountOf(v *proto.Value) int64 {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 9 {
		return 0
	}
	f := v.Field(9)
	if f.Num == nil {
		return 0
	}
	return clampBaitMount(int64(u32Of(f.Num)))
}

// setRigBaitMount 就地改写 153 对象 f9。
func setRigBaitMount(v *proto.Value, mask int64) {
	if v == nil || v.Kind != proto.KindObj || v.FieldCount() <= 9 {
		return
	}
	v.Fields[9].Num = ptrU64(uint64(clampBaitMount(mask)))
}

const baitMountSettingKey = "baitmount"

// defaultBaitMountMask 是钓组渲染时给未选择过的竿用的挂饵方式位掩码。
// 客户端读到 0 会认为钓组没有安装方式：安装弹窗为空、鱼饵列表全部不适配。
// 先在已确认可用的取值上放开，玩家一旦自己选过就以玩家选择为准。
var defaultBaitMountMask int64 = 0

// baitMountFor 返回渲染钓组时应写入 f9 的挂饵方式：
// 玩家选过的值优先，没选过时取默认掩码。
func (s *Server) baitMountFor(ctx context.Context, rodGUID string) int64 {
	if v, ok := s.savedBaitMount(ctx, rodGUID); ok {
		return v
	}
	return defaultBaitMountMask
}

// applyRigBaitMount 把挂饵方式写进竿实例的 153 对象（f9），并刷新版本戳。
// 17/6 已经有响应快照，客户端的安装栏直接读这里落库的值。
func (s *Server) applyRigBaitMount(ctx context.Context, rodGUID string, mask int64) error {
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
	setRigBaitMount(fields[idx].Val, mask)
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
