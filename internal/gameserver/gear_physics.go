package gameserver

import (
	"context"
	"math"
	"strings"

	"rf4server/internal/store"
)

// 钓具物理约束。
//
// 数据源为 item_def.attrs_json。物理标量以 U32 承载 F32 位型，
// 例如 1096821190 表示 14.01kg，不能直接按整数使用。
const (
	gearCatRod        = int64(3)
	gearCatReel       = int64(6)
	gearCatLine       = int64(11)
	gearCatLeader     = int64(13)
	gearCatLeaderWire = int64(15)

	gearAttrMaxLoad  = "kngaaefkpka"
	gearAttrDiameter = "ciichedlnjh"
	gearAttrLength   = "hkgndoipdlc"
	gearAttrWeight   = "dhlkahnodoo"
	gearAttrTestMin  = "dakogfmkclb"
	gearAttrTestMax  = "jfebacgmlmb"
	gearAttrTaper    = "ddebfbbjppk"
	gearAttrStretch  = "aomkdnjcgji"
	gearAttrLineLen  = "ljnnpokapig"
	gearAttrDrag     = "mlfjkaoicdn"
	gearAttrGear     = "oiaplokhcii"
	gearAttrDragMax  = "omghfmdfjpc"
	gearAttrRatio    = "dpgbopbanal"
	gearAttrLeaderIn = "mbnambppbom"
)

// rigGearIDs 返回当前钓组用于物理判定的 systemID。
// 空字符串表示数据缺失，该部件不参与最弱环计算。
type rigGearIDs struct {
	Rod              string
	Reel             string
	Line             string
	Leader           string
	RodDurability    float64
	ReelDurability   float64
	LineDurability   float64
	LeaderDurability float64
}

// gearPhysicsSpec 是 item_def 解码后的物理量快照（kg/mm/m/比例）。
type gearPhysicsSpec struct {
	RodPowerKg       float64
	LineStrengthKg   float64
	LeaderStrengthKg float64
	ReelDragKg       float64 // 摩擦制动力，只供对照；断件计算不使用它
	LineStretch      float64
}

// decodeU32F32 把 attrs_json 中的 U32 数值按 IEEE-754 F32 位型还原。
// JSON 数字经 encoding/json 解到 any 后是 float64，因此先转回 uint32。
func decodeU32F32(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	var u uint64
	switch n := v.(type) {
	case float64:
		if n < 0 || n > float64(^uint32(0)) || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		u = uint64(n)
	case float32:
		if n < 0 || float64(n) > float64(^uint32(0)) || math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return 0, false
		}
		u = uint64(n)
	case int:
		if n < 0 {
			return 0, false
		}
		u = uint64(n)
	case int64:
		if n < 0 {
			return 0, false
		}
		u = uint64(n)
	case uint64:
		u = n
	case uint32:
		u = uint64(n)
	default:
		return 0, false
	}
	if u > math.MaxUint32 {
		return 0, false
	}
	f := float64(math.Float32frombits(uint32(u)))
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1e9 {
		return 0, false
	}
	return f, true
}

func itemDefF32(def *store.ItemDef, key string) float64 {
	if def == nil {
		return 0
	}
	attrs := defAttrs(def)
	f, _ := decodeU32F32(attrs[key])
	return f
}

func itemDefInt(def *store.ItemDef, key string) int64 {
	if def == nil {
		return 0
	}
	attrs := defAttrs(def)
	switch n := attrs[key].(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	default:
		return 0
	}
}

func itemDefNested(def *store.ItemDef, key string) map[string]any {
	if def == nil {
		return nil
	}
	attrs := defAttrs(def)
	m, _ := attrs[key].(map[string]any)
	return m
}

func nestedF32(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	f, _ := decodeU32F32(m[key])
	return f
}

func rodPowerKg(def *store.ItemDef) float64     { return itemDefF32(def, gearAttrMaxLoad) }
func rodLengthM(def *store.ItemDef) float64     { return itemDefF32(def, gearAttrLength) }
func lineStrengthKg(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrMaxLoad) }
func lineDiameterMM(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrDiameter) }
func lineLengthM(def *store.ItemDef) int64      { return itemDefInt(def, gearAttrLineLen) }
func lineStretch(def *store.ItemDef) float64    { return itemDefF32(def, gearAttrStretch) }

func leaderStrengthKg(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	if inner := itemDefNested(def, gearAttrLeaderIn); inner != nil {
		if f := nestedF32(inner, gearAttrMaxLoad); f > 0 {
			return f
		}
	}
	return itemDefF32(def, gearAttrMaxLoad)
}

func reelDragMaxKg(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	return nestedF32(itemDefNested(def, gearAttrDrag), gearAttrDragMax)
}

func reelGearRatio(def *store.ItemDef) float64 {
	if def == nil {
		return 0
	}
	return nestedF32(itemDefNested(def, gearAttrGear), gearAttrRatio)
}

func rodTestG(def *store.ItemDef) (float64, float64) {
	return itemDefF32(def, gearAttrTestMin), itemDefF32(def, gearAttrTestMax)
}

func rodTaper(def *store.ItemDef) float64 { return itemDefF32(def, gearAttrTaper) }

// gearBreakLimitKg 返回整副钓组最弱一环的拉力；0 表示没有可信物理数据。
// 摩擦制动力是打滑力，拨到 29 档不应把齿轮当成断件。
func gearBreakLimitKg(spec gearPhysicsSpec) float64 {
	parts := []float64{spec.RodPowerKg, spec.LineStrengthKg, spec.LeaderStrengthKg}
	limit := 0.0
	for _, p := range parts {
		if p <= 0 {
			continue
		}
		if limit == 0 || p < limit {
			limit = p
		}
	}
	return limit
}

func gearWeakestPart(spec gearPhysicsSpec) string {
	parts := []struct {
		name string
		kg   float64
	}{
		{"rod", spec.RodPowerKg},
		{"line", spec.LineStrengthKg},
		{"leader", spec.LeaderStrengthKg},
	}
	name := ""
	limit := 0.0
	for _, p := range parts {
		if p.kg <= 0 || (limit > 0 && p.kg >= limit) {
			continue
		}
		name, limit = p.name, p.kg
	}
	return name
}

// canLandGear 判断鱼能否拉上岸，并返回最弱断件（rod/line/leader）。
// pull = fish_weight_kg * k，默认 k 为 0.35。
func canLandGear(spec gearPhysicsSpec, fishWeightKg, k float64) (bool, string) {
	if fishWeightKg <= 0 {
		return true, ""
	}
	if k <= 0 {
		k = 0.35
	}
	pull := fishWeightKg * k
	weakestName := gearWeakestPart(spec)
	weakestKg := gearBreakLimitKg(spec)
	if weakestKg == 0 || pull <= weakestKg {
		return true, ""
	}
	return false, weakestName
}

// effectiveLineBuffer 是线材吸收冲击后的拉力系数（1=刚性，越大越能缓冲）。
func effectiveLineBuffer(stretch float64) float64 {
	if stretch < 0 {
		stretch = 0
	}
	if stretch > 1 {
		stretch = 1
	}
	return 1.0 - 0.5*stretch
}

// gearWeightCapKg 是断件阈值换算后的可搏鱼体重上限；0 表示不约束。
func gearWeightCapKg(spec gearPhysicsSpec) float64 {
	limit := gearBreakLimitKg(spec)
	if limit <= 0 {
		return 0
	}
	buf := effectiveLineBuffer(spec.LineStretch)
	if buf < 0.2 {
		buf = 0.2
	}
	return (limit * (1.0 / 0.35)) / buf
}

func capWeightGrams(weightG, capG int64) int64 {
	if capG > 0 && weightG > capG {
		weightG = capG
	}
	if weightG < 1 {
		weightG = 1
	}
	return weightG
}

// loadGearSpec 读取四个物理件的 item_def 并生成快照。
func (s *Server) loadGearSpec(ctx context.Context, ids rigGearIDs) gearPhysicsSpec {
	var spec gearPhysicsSpec
	if s == nil || s.DB == nil {
		return spec
	}
	load := func(itemID string) *store.ItemDef {
		itemID = strings.TrimSpace(itemID)
		if itemID == "" {
			return nil
		}
		def, err := s.DB.GetItemDefByItemID(ctx, itemID)
		if err != nil {
			return nil
		}
		return def
	}
	applyDurability := func(v, durability float64) float64 {
		if v <= 0 {
			return 0
		}
		if durability < 0 {
			durability = 0
		}
		if durability > 1 {
			durability = 1
		}
		return v * durability
	}
	spec.RodPowerKg = applyDurability(rodPowerKg(load(ids.Rod)), ids.RodDurability)
	spec.ReelDragKg = applyDurability(reelDragMaxKg(load(ids.Reel)), ids.ReelDurability)
	lineDef := load(ids.Line)
	spec.LineStrengthKg = applyDurability(lineStrengthKg(lineDef), ids.LineDurability)
	spec.LineStretch = lineStretch(lineDef)
	spec.LeaderStrengthKg = applyDurability(leaderStrengthKg(load(ids.Leader)), ids.LeaderDurability)
	return spec
}

// gearWeightCapForRig 读取当前手持竿的物理件并返回可搏鱼体重上限（kg）。
func (s *Server) gearWeightCapForRig(ctx context.Context, uid int64, rodGUID string) (float64, rigGearIDs, string) {
	if s == nil || s.DB == nil {
		return 0, rigGearIDs{}, ""
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	spec := s.loadGearSpec(ctx, ids)
	return gearWeightCapKg(spec), ids, gearWeakestPart(spec)
}

// rigGearIDs 解析当前钓组，优先读取竿实例：
//   - f3 = 线、f4 = 轮（专用字段）；
//   - f5 组件表 = 前导/其它挂载件；
//   - rig_component 是对应的可查询派生索引。
func (s *Server) rigGearIDs(ctx context.Context, uid int64, rodGUID string) rigGearIDs {
	var out rigGearIDs
	if s == nil || s.DB == nil {
		return out
	}
	row := s.itemByGUIDOrOwnedCategory(ctx, uid, rodGUID, gearCatRod)
	if row == nil {
		return out
	}
	out.Rod = s.itemSystemID(ctx, row)
	out.RodDurability = row.Durability

	compGUIDs := map[int64]string{}
	if comps, err := s.DB.ListRigComponents(ctx, row.GUID); err == nil {
		for _, c := range comps {
			if c != nil && c.GUID != "" {
				compGUIDs[c.Slot] = c.GUID
			}
		}
	}
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil {
		if idx := fieldIdxByCS(row.WireClass, csLine); idx >= 0 && idx < len(fields) {
			if g := fields[idx].GuidHex(); g != "" {
				compGUIDs[gearCatLine] = g
			}
		}
		if idx := fieldIdxByCS(row.WireClass, csReel); idx >= 0 && idx < len(fields) {
			if g := fields[idx].GuidHex(); g != "" {
				compGUIDs[gearCatReel] = g
			}
		}
		if idx := fieldIdxByCS(row.WireClass, csRigObject); idx >= 0 && idx < len(fields) {
			for slot, comp := range rigCompsOf(fields[idx].Val) {
				if comp.GUID != "" {
					compGUIDs[slot] = comp.GUID
				}
			}
		}
	}

	out.Reel, out.ReelDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatReel])
	out.Line, out.LineDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatLine])
	out.Leader, out.LeaderDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatLeaderWire])
	if out.Leader == "" {
		out.Leader, out.LeaderDurability = s.itemSystemIDDurabilityByGUID(ctx, compGUIDs[gearCatLeader])
	}

	// 背包兜底只补竿、轮和线，不猜测前导。
	if out.Reel == "" {
		out.Reel, out.ReelDurability = s.firstOwnedItemSystemIDDurability(ctx, uid, gearCatReel)
	}
	if out.Line == "" {
		out.Line, out.LineDurability = s.firstOwnedItemSystemIDDurability(ctx, uid, gearCatLine)
	}
	return out
}

func (s *Server) itemByGUIDOrOwnedCategory(ctx context.Context, uid int64, guid string, category int64) *store.Item {
	if guid != "" {
		if row, err := s.DB.GetItemByGUID(ctx, guid); err == nil && row != nil && row.UID == uid {
			return row
		}
	}
	guids, err := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return nil
	}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err == nil && def != nil && def.Category == category {
			return row
		}
	}
	return nil
}

func (s *Server) itemSystemID(ctx context.Context, row *store.Item) string {
	if row == nil {
		return ""
	}
	if strings.TrimSpace(row.ItemID) != "" {
		return row.ItemID
	}
	if def, err := s.itemDefForRow(ctx, row); err == nil && def != nil {
		return def.ItemID
	}
	return ""
}

func (s *Server) itemSystemIDByGUID(ctx context.Context, guid string) string {
	if s == nil || s.DB == nil || guid == "" {
		return ""
	}
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		return ""
	}
	return s.itemSystemID(ctx, row)
}

func (s *Server) itemSystemIDDurabilityByGUID(ctx context.Context, guid string) (string, float64) {
	if s == nil || s.DB == nil || guid == "" {
		return "", 1
	}
	row, err := s.DB.GetItemByGUID(ctx, guid)
	if err != nil || row == nil {
		return "", 1
	}
	return s.itemSystemID(ctx, row), row.Durability
}

func (s *Server) firstOwnedItemSystemID(ctx context.Context, uid, category int64) string {
	guids, err := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return ""
	}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err == nil && def != nil && def.Category == category {
			return s.itemSystemID(ctx, row)
		}
	}
	return ""
}

func (s *Server) firstOwnedItemSystemIDDurability(ctx context.Context, uid, category int64) (string, float64) {
	guids, err := s.DB.ItemGUIDsByContainers(ctx, uid, []string{"backpack", "rig"})
	if err != nil {
		return "", 1
	}
	for _, g := range guids {
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		def, err := s.itemDefForRow(ctx, row)
		if err == nil && def != nil && def.Category == category {
			return s.itemSystemID(ctx, row), row.Durability
		}
	}
	return "", 1
}

func (s *Server) itemDefForRow(ctx context.Context, row *store.Item) (*store.ItemDef, error) {
	if row == nil {
		return nil, nil
	}
	if row.NumericID != 0 {
		if def, err := s.DB.GetItemDef(ctx, row.NumericID); err == nil && def != nil {
			return def, nil
		}
	}
	if row.ItemID != "" {
		return s.DB.GetItemDefByItemID(ctx, row.ItemID)
	}
	return nil, nil
}

// itemDefStr 读取 item_def.attrs_json 里的字符串字段（如地图的 dhaenhabecc）。
func itemDefStr(def *store.ItemDef, key string) string {
	if def == nil {
		return ""
	}
	s, _ := defAttrs(def)[key].(string)
	return s
}
