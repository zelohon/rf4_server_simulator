package gameserver

import (
	"context"
	"math"

	"rf4server/internal/store"
)

const castAttrWaterWeight = "ijbjficbhog"

const (
	castAttrReelType      = "fopbhlmkcpe"
	castAttrLineMaterial  = "fopbhlmkcpe"
	castAttrDefaultSpool  = "dlfjnokncfl"
	castAttrSpoolSize     = "gkfbafmbpam"
	castAttrSpoolCapacity = "djcepbpigpe"
)

// itemDefCastWeightG 读取物品抛投重量。多数装备使用 dhlkahnodoo，
// 钩、沉子、喂食器和部分饵使用 ijbjficbhog（水中/工作重量）。
func itemDefCastWeightG(def *store.ItemDef) float64 {
	if v := itemDefNumericScalar(def, gearAttrWeight); v > 0 {
		return v
	}
	return itemDefNumericScalar(def, castAttrWaterWeight)
}

// itemDefNumericScalar 同时兼容静态库中的 F32 位型和普通 JSON 数字。
func itemDefNumericScalar(def *store.ItemDef, key string) float64 {
	if def == nil {
		return 0
	}
	raw := defAttrs(def)[key]
	var n float64
	switch v := raw.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	default:
		return 0
	}
	// IEEE-754 F32 位型通常在十亿量级；普通克重/Test 数字远小于此值。
	if n >= 1_000_000 {
		if f, ok := decodeU32F32(raw); ok {
			n = f
		}
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1_000_000 {
		return 0
	}
	return n
}

// castLineStats 是当前主线的真实静态/实例参数。长度优先读取物品实例字段，
// 确保断线后剩余长度会直接限制抛投距离。
type castLineStats struct {
	DiameterMM       float64
	LengthM          float64
	Stretch          float64
	Material         int64
	LinearDensityGPM float64
}

func (s *Server) castLineStats(ctx context.Context, rodGUID, lineItemID string) castLineStats {
	var out castLineStats
	if s == nil || s.DB == nil || lineItemID == "" {
		return out
	}
	def, err := s.DB.GetItemDefByItemID(ctx, lineItemID)
	if err != nil || def == nil {
		return out
	}
	out.DiameterMM = lineDiameterMM(def)
	out.LengthM = float64(lineLengthM(def))
	out.Stretch = lineStretch(def)
	out.Material = itemDefInt(def, castAttrLineMaterial)
	out.LinearDensityGPM = lineDensityGPM(def)
	lineGUID := s.rigComponentGuidOf(ctx, rodGUID, catLine)
	if row, rerr := s.DB.GetItemByGUID(ctx, lineGUID); rerr == nil && row != nil {
		if n := s.itemLength(ctx, row); n > 0 {
			out.LengthM = float64(n)
		}
	}
	return out
}

// castPayloadWeights 从竿实例的真实 f5 装配表汇总终端线组重量与饵重。
// 主线、轮子和鱼竿本体不属于飞行载荷；拟饵按饵重单列。
func (s *Server) castPayloadWeights(ctx context.Context, rodGUID string) (rigWeightG, baitWeightG float64) {
	if s == nil || s.DB == nil || rodGUID == "" {
		return 0, 0
	}
	comps := s.storedRigComponents(ctx, rodGUID)
	if len(comps) == 0 {
		// 兼容尚未投影到 f5 的旧档案；正常新档案以 f5 为权威。
		if rows, err := s.DB.ListRigComponents(ctx, rodGUID); err == nil {
			for _, row := range rows {
				if row != nil {
					comps[rigCompKey(row.Slot, row.Ordinal)] = rigComp{
						Category: row.Slot, Ordinal: row.Ordinal, GUID: row.GUID, Extra: row.Extra,
					}
				}
			}
		}
	}
	seen := make(map[string]bool, len(comps))
	for _, comp := range comps {
		if comp.GUID == "" || seen[comp.GUID] || comp.Category == catReel || comp.Category == catLine {
			continue
		}
		seen[comp.GUID] = true
		row, err := s.DB.GetItemByGUID(ctx, comp.GUID)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err != nil || def == nil {
			continue
		}
		weight := itemDefCastWeightG(def)
		if comp.Category == catBait || comp.Category == catLure {
			baitWeightG += weight
		} else {
			rigWeightG += weight
		}
	}
	return rigWeightG, baitWeightG
}

// castSkillStats 读取当前钓法的真实熟练度和已学习节点效果。
func (s *Server) castSkillStats(ctx context.Context, uid, rigType int64) (category int64, mastery, nodeEffect float64) {
	category = fishingSkillCatForRig(rigType)
	if s == nil || s.DB == nil {
		return category, 0, 0
	}
	points, _ := s.DB.ListSkills(ctx, uid)
	nodes, _ := s.DB.ListSkillNodes(ctx, uid)
	mastery = clampFloat(points[category], 0, profCap)
	for _, def := range s.skillDefsOfCat(category) {
		if level := nodes[def.NodeID]; level > 0 {
			nodeEffect += float64(level) * skillNodeEffectPoints(def)
		}
	}
	return category, mastery, nodeEffect
}

// castReelSpoolStats 从当前轮子实例优先读取工作坊安装的线杯；老轮子没有内嵌
// 子实例时回退到轮子静态定义里的出厂线杯。容量公式来自客户端静态契约。
func (s *Server) castReelSpoolStats(ctx context.Context, reel *store.Item, lineItemID string) (reelType, spoolSize int64, capacityM float64) {
	if s == nil || s.DB == nil || reel == nil {
		return 0, 0, 0
	}
	var reelDef *store.ItemDef
	if def, err := s.itemDefForRow(ctx, reel); err == nil {
		reelDef = def
		reelType = itemDefInt(def, castAttrReelType)
	}

	var spool map[string]any
	if fields, err := FieldsFromJSON(reel.FieldsJSON); err == nil {
		for i := range fields {
			def := s.reelPartDef(ctx, fields[i].Val)
			if def == nil || def.Category != 8 {
				continue
			}
			spool = defAttrs(def)
			break
		}
	}
	if spool == nil && reelDef != nil {
		spool = itemDefNested(reelDef, castAttrDefaultSpool)
	}
	spoolSize = nestedInt(spool, castAttrSpoolSize)
	coef := nestedNumericScalar(spool, castAttrSpoolCapacity)
	if coef <= 0 || lineItemID == "" {
		return reelType, spoolSize, 0
	}
	lineDef, err := s.DB.GetItemDefByItemID(ctx, lineItemID)
	if err != nil || lineDef == nil {
		return reelType, spoolSize, 0
	}
	diameter := lineDiameterMM(lineDef)
	if diameter <= 0 {
		return reelType, spoolSize, 0
	}
	// 客户端对低轮廓/鼓轮与固定线杯轮使用不同的容量常数。
	k := 0.16588345
	if reelType == 2 || reelType == 3 {
		k = 0.10153155
	}
	capacityM = math.Max(k*coef/diameter, 5)
	return reelType, spoolSize, capacityM
}

func nestedInt(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	default:
		return 0
	}
}

func nestedNumericScalar(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	var n float64
	switch v := m[key].(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	default:
		return 0
	}
	if n >= 1_000_000 {
		if f, ok := decodeU32F32(m[key]); ok {
			n = f
		}
	}
	return n
}

func (s *Server) castPlayerCondition(ctx context.Context, uid int64) (primary, secondary float64) {
	primary, secondary = defaultPlayerConditionPrimary, defaultPlayerConditionSecondary
	if s == nil || s.DB == nil {
		return primary, secondary
	}
	condition, err := s.DB.EnsurePlayerCondition(ctx, uid)
	if err != nil || condition == nil {
		return primary, secondary
	}
	return condition.Primary, condition.Secondary
}
