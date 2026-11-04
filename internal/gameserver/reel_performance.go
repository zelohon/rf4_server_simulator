package gameserver

import (
	"context"
	"math"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

const reelDurabilityStep = 0.20

const reelPartQualityAttr = "obifdihmcfn"

// reelLockMultiplier 按耐久阶段给出锁轮承力倍率：
// 100%=1.4、80%=1.3、60%=1.2、40%=1.1、20%=1.0。
func reelLockMultiplier(durability float64) float64 {
	if durability < reelDurabilityStep {
		durability = reelDurabilityStep
	}
	if durability > 1 {
		durability = 1
	}
	stage := math.Ceil((durability - 1e-9) / reelDurabilityStep)
	if stage < 1 {
		stage = 1
	}
	if stage > 5 {
		stage = 5
	}
	return 0.9 + stage*0.1
}

// reelBearingFactor 把轴承品质映射为传力效率。低档轴承会让制动力产生少量
// 损失，高档轴承接近完整传递摩擦片的制动力。
func reelBearingFactor(quality float64) float64 {
	quality = math.Max(0, math.Min(100, quality))
	return 0.90 + quality/1000
}

// reelOilWearFactor 返回超载磨损倍率。品质越高，跨过一个 20% 耐久档位
// 所需的超载次数越多；15 品质约 1.175 倍，100 品质为 0.75 倍。
func reelOilWearFactor(quality float64) float64 {
	quality = math.Max(0, math.Min(100, quality))
	return 1.25 - quality/200
}

// reelCastFriction 把轴承、润滑油和轮子耐久换算为放线摩擦系数。
// 润滑油越好、轴承越顺、轮子耐久越高，摩擦越小。返回值越大，
// 客户端能够使用的全力抛投距离越短。
func reelCastFriction(bearingQuality, oilQuality, durability float64) float64 {
	bearingQuality = math.Max(0, math.Min(100, bearingQuality))
	oilQuality = math.Max(0, math.Min(100, oilQuality))
	durability = math.Max(0, math.Min(1, durability))
	friction := 0.05 +
		0.18*(1-bearingQuality/100) +
		0.16*(1-oilQuality/100) +
		0.12*(1-durability)
	return math.Max(0.05, math.Min(0.55, friction))
}

// reelPartDef 返回内嵌轮子零件对应的静态定义。
func (s *Server) reelPartDef(ctx context.Context, child *proto.Value) *store.ItemDef {
	if s == nil || s.DB == nil || child == nil || child.Kind != proto.KindObj {
		return nil
	}
	idx := fieldIdxByCS(child.Class, csNumericID)
	if idx < 0 || idx >= child.FieldCount() || child.Fields[idx].Num == nil {
		return nil
	}
	def, err := s.DB.GetItemDef(ctx, int64(child.Fields[idx].U32()))
	if err != nil {
		return nil
	}
	return def
}

// selectedReelPart 返回轮子树中当前安装的指定品类零件及其静态定义。
// 工坊改装后的传动比、摩擦力和轴承油品都必须优先从这里读取。
func (s *Server) selectedReelPart(ctx context.Context, fields []proto.Field, category int64) (*proto.Value, *store.ItemDef, int, bool) {
	for i := range fields {
		child := fields[i].Val
		if child == nil || child.Kind != proto.KindObj {
			continue
		}
		def := s.reelPartDef(ctx, child)
		if def != nil && def.Category == category {
			return child, def, i, true
		}
	}
	return nil, nil, -1, false
}

// reelBaseDragFromRow 从当前已安装的摩擦片实例读取基础制动力。
// 老数据的子实例若缺物理字段，会回退到所选零件定义，而不是母轮出厂定义。
func (s *Server) reelBaseDragFromRow(ctx context.Context, row *store.Item) float64 {
	if row == nil {
		return 0
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err == nil {
		for i := range fields {
			child := fields[i].Val
			def := s.reelPartDef(ctx, child)
			if def == nil || def.Category != 9 {
				continue
			}
			if idx := fieldIdxByCS(child.Class, csDragForce); idx >= 0 && idx < child.FieldCount() {
				if raw := child.Fields[idx].Num; raw != nil {
					if v := float64(proto.F32FromBits(uint32(*raw))); v > 0 {
						return v
					}
				}
			}
			if v := itemDefF32(def, gearAttrDragMax) * row.Workmanship; v > 0 {
				return v
			}
		}
	}
	if def, derr := s.itemDefForRow(ctx, row); derr == nil {
		return reelDragMaxKg(def) * row.Workmanship
	}
	return 0
}

// reelPartQualityFromRow 读取轮子当前内嵌零件及静态品质。没有该零件时返回
// 100 品质，以兼容缺少旧版内嵌配置的历史物品。
func (s *Server) reelPartQualityFromRow(ctx context.Context, row *store.Item, category int64) (string, float64) {
	if row == nil {
		return "", 100
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return "", 100
	}
	for i := range fields {
		def := s.reelPartDef(ctx, fields[i].Val)
		if def == nil || def.Category != category {
			continue
		}
		quality := float64(itemDefInt(def, reelPartQualityAttr))
		quality = math.Max(0, math.Min(100, quality))
		return def.ItemID, quality
	}
	return "", 100
}

// syncReelPerformance 重新计算并持久化一只轮子的基础/锁轮承力。
func (s *Server) syncReelPerformance(ctx context.Context, row *store.Item) (*store.ReelPerformance, error) {
	base := s.reelBaseDragFromRow(ctx, row)
	bearingItemID, bearingQuality := s.reelPartQualityFromRow(ctx, row, 7)
	oilItemID, oilQuality := s.reelPartQualityFromRow(ctx, row, 10)
	bearingFactor := reelBearingFactor(bearingQuality)
	durability := row.Durability
	if durability < reelDurabilityStep {
		durability = reelDurabilityStep
	}
	if durability > 1 {
		durability = 1
	}
	v := &store.ReelPerformance{
		ReelGUID:       row.GUID,
		UID:            row.UID,
		BaseDragKg:     base,
		LockedDragKg:   base * reelLockMultiplier(durability) * bearingFactor,
		Durability:     durability,
		BearingItemID:  bearingItemID,
		BearingQuality: bearingQuality,
		BearingFactor:  bearingFactor,
		OilItemID:      oilItemID,
		OilQuality:     oilQuality,
		OilWearFactor:  reelOilWearFactor(oilQuality),
		CastFriction:   reelCastFriction(bearingQuality, oilQuality, durability),
	}
	v.CastEfficiency = 1 - v.CastFriction
	// 重算改装参数时不能清掉尚未跨档的润滑磨损进度。
	if old, err := s.DB.GetReelPerformance(ctx, row.GUID); err == nil && old != nil {
		v.WearProgress = old.WearProgress
	}
	if err := s.DB.UpsertReelPerformance(ctx, *v); err != nil {
		return nil, err
	}
	return v, nil
}

// RebuildReelPerformance 为现有轮子补齐/刷新物理参数表。
func (s *Server) RebuildReelPerformance(ctx context.Context) (int, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `SELECT guid FROM item WHERE wire_class=?`, "dgjinoebdop")
	if err != nil {
		return 0, err
	}
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			rows.Close()
			return 0, err
		}
		guids = append(guids, guid)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	count := 0
	for _, guid := range guids {
		row, rerr := s.DB.GetItemByGUID(ctx, guid)
		if rerr != nil || row == nil {
			continue
		}
		if _, rerr = s.syncReelPerformance(ctx, row); rerr != nil {
			return count, rerr
		}
		count++
	}
	return count, nil
}
