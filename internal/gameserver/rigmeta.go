package gameserver

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// 钓组元数据，以及 14/14 f23 钩 GUID 的解析。

// rigTypeNames 将钓组类型枚举映射到 rig_* 资源名；未知类型返回空字符串。
var rigTypeNames = map[int64]string{
	1: "rig_FloatSimple", 2: "rig_FloatBolognese", 3: "rig_FloatSlider",
	4: "rig_FloatLiveFish", 5: "rig_Lure", 6: "rig_SimpleJig",
	7: "rig_Texas", 8: "rig_DropShot", 9: "rig_Carolina",
	10: "rig_ThreeWay", 11: "rig_Wacky", 12: "rig_BottomSimple",
	13: "rig_BottomPaternoster", 14: "rig_BottomInline", 15: "rig_BottomLoop",
	16: "rig_BottomMethod", 17: "rig_BottomLiveFish", 18: "rig_BottomFeedRocket",
	19: "rig_SbirolinoLure", 20: "rig_SbirolinoSoftPlastic", 21: "rig_FloatWaggler",
	22: "rig_JerkbaitRig", 23: "rig_BottomMarker", 24: "rig_BottomHair",
	25: "rig_BottomSnowman", 26: "rig_BottomPopup", 27: "rig_BottomHingedStiff",
	28: "rig_BottomChod", 29: "rig_BottomMaggot",
	30: "rig_FloatCarpHair", 31: "rig_FloatCarpPopUp", 32: "rig_BottomMethodPopUp",
	33: "rig_Fly", 34: "rig_BottomFixedZigRig", 35: "rig_HairyJigRig",
	50: "rig_MarinePilker", 51: "rig_MarineLureJig", 52: "rig_MarineFilletJig",
	53: "rig_MarineGiganticLureJig", 54: "rig_MarineGiganticFishJig",
	55: "rig_MarineBottomClassic", 56: "rig_MarineBottomWithRattlingSinker",
	57: "rig_MarineBottomPaternoster", 58: "rig_MarineBottomWithFlyingColarLure",
	59: "rig_MarineBottomWithDeadFish", 60: "rig_MarineBottomDropShot",
}

// rodRigAllowed 定义竿类型允许使用的钓组类型；0 或未知类型不做限制。
var rodRigAllowed = map[int64][]int64{
	1:  {5, 6, 7, 8, 9, 10, 11, 19, 20, 35},
	2:  {5, 6, 7, 8, 9, 10, 11, 19, 20, 35},
	3:  {1, 3, 30, 31},
	4:  {1, 2, 3, 19, 20, 30, 31},
	5:  {1, 3, 4, 19, 20, 21, 30, 31},
	7:  {33},
	8:  {33},
	9:  {12, 13, 14, 15, 16, 17, 24, 25, 26, 27, 28, 29, 32, 34},
	10: {12, 13, 14, 15, 16, 17, 24, 25, 26, 27, 28, 29, 32, 34},
	11: {12, 13, 14, 15, 16, 17, 18, 24, 25, 26, 27, 28, 29, 32, 34},
	12: {5, 6, 7, 8, 9, 10, 11, 19, 20, 22, 35},
	13: {18},
	14: {23},
	15: {50, 51, 52, 53, 54},
	16: {50, 51, 52, 53, 54, 55, 56, 57, 58, 59},
}

// rodDefaultRigType 为同一允许矩阵内需要区分默认子族的竿型指定首选钓组。
// 海竿 15（Pilker 竿）默认垂直铁板钓组；船竿 16（Bottom 竿）默认经典海钓沉底。
var rodDefaultRigType = map[int64]int64{
	15: 50, // rig_MarinePilker
	16: 55, // rig_MarineBottomClassic
}

// bottomRigTypes 沉底族（无浮漂槽、竿尖梢必需）。
var bottomRigTypes = map[int64]bool{
	12: true, 13: true, 14: true, 15: true, 16: true, 17: true, 18: true,
	23: true, 24: true, 25: true, 26: true, 27: true, 28: true, 29: true,
	32: true, 34: true,
}

// rigTypeLureFamily 收索拟饵族：鱼在收线中攻击拟饵，咬钩节奏与"就位"守门单独走。
var rigTypeLureFamily = map[int64]bool{
	5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 11: true,
	19: true, 20: true, 22: true,
}

// defaultRigTypeForRod 按竿型选择第一个有资源名映射的允许钓组。
func (s *Server) defaultRigTypeForRod(ctx context.Context, rodItemID string) int64 {
	def, err := s.DB.GetItemDefByItemID(ctx, rodItemID)
	if err != nil || def == nil {
		return 1
	}
	rodType := int64(0)
	if v, ok := defAttrF64(def, "fopbhlmkcpe"); ok {
		rodType = int64(v)
	}
	if allowed, ok := rodRigAllowed[rodType]; ok {
		if preferred, ok := rodDefaultRigType[rodType]; ok &&
			allowedContains(allowed, preferred) && rigTypeNames[preferred] != "" {
			return preferred
		}
		for _, rt := range allowed {
			if rigTypeNames[rt] != "" {
				return rt
			}
		}
	}
	return 1
}

// rodDefTips 返回竿定义中的可用尖梢名单。
func (s *Server) rodDefTips(ctx context.Context, rodItemID string) []string {
	def, err := s.DB.GetItemDefByItemID(ctx, rodItemID)
	if err != nil || def == nil {
		return nil
	}
	m := defAttrs(def)
	if m == nil {
		return nil
	}
	raw, _ := m["onlobkhillh"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if sv, ok := v.(string); ok {
			out = append(out, sv)
		}
	}
	return out
}

// rodDefRodType 返回竿定义中的竿类型。
func rodDefRodType(def *store.ItemDef) int64 {
	if def == nil {
		return 0
	}
	if v, ok := defAttrF64(def, "fopbhlmkcpe"); ok {
		return int64(v)
	}
	return 0
}

func defAttrF64(def *store.ItemDef, key string) (float64, bool) {
	m := defAttrs(def)
	if m == nil {
		return 0, false
	}
	if v, ok := m[key].(float64); ok {
		return v, true
	}
	return 0, false
}

// uuidLike 由父 GUID 和槽名派生稳定的子件 GUID。
func uuidLike(parentGUID, salt string) string {
	sum := md5.Sum([]byte(parentGUID + "|" + salt))
	return fmt.Sprintf("%x", sum[:])
}

// ---- 14/14 f23 钩 GUID ----
//
// 客户端按 f23 GUID 在钓组组件中查找线末端钩。
// 浮钓竿取槽 12 的钩实例；路亚竿取拟饵实例中已落库的出厂钩。

// mountedHookGuid 返回竿上当前线末端钩的 GUID。
// 先查装配记录，再查竿实例组件表，最后回退到路亚拟饵的出厂钩。
func (s *Server) mountedHookGuid(ctx context.Context, rodGUID string) string {
	if rodGUID == "" {
		return ""
	}
	if g, err := s.DB.GetItemGUIDByRigSlot(ctx, rodGUID, 12); err == nil && g != "" {
		return g
	}
	if g := s.rigComponentGuidOf(ctx, rodGUID, 12); g != "" {
		return g
	}
	return s.mountedLureHookFallback(ctx, rodGUID)
}

// rigComponentGuidOf 从竿实例的钓组组件表中按品类查找 GUID。
func (s *Server) rigComponentGuidOf(ctx context.Context, rodGUID string, cat int64) string {
	row, err := s.DB.GetItemByGUID(ctx, rodGUID)
	if err != nil || row == nil {
		return ""
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return ""
	}
	// 按字段名查找钓组对象，不能假定固定字段位置。
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) || fields[idx].Val == nil {
		return ""
	}
	compField := fields[idx].Val.Field(2)
	comps := compField.Val
	if comps == nil {
		return ""
	}
	for _, c := range comps.Items {
		f0, f1 := c.Field(0), c.Field(1)
		if int64(f0.U32()) == cat {
			return f1.GuidHex()
		}
	}
	return ""
}

// mountedLureGUID 返回竿上拟饵的 GUID。
func (s *Server) mountedLureGUID(ctx context.Context, rodGUID string) string {
	if g, err := s.DB.GetItemGUIDByRigSlot(ctx, rodGUID, 17); err == nil && g != "" {
		return g
	}
	return s.rigComponentGuidOf(ctx, rodGUID, 17)
}

// mountedLureHookFallback 返回路亚拟饵中活动出厂钩的 GUID。
func (s *Server) mountedLureHookFallback(ctx context.Context, rodGUID string) string {
	lureGUID := s.mountedLureGUID(ctx, rodGUID)
	if lureGUID == "" {
		return ""
	}
	return s.lureActiveHookGUID(ctx, lureGUID)
}

// lureActiveHookGUID 返回拟饵实例中活动出厂钩的 GUID。
// 活动下标缺省为 0，越界时回退到第 0 颗；没有落库钩时返回空字符串。
func (s *Server) lureActiveHookGUID(ctx context.Context, lureGUID string) string {
	row, err := s.DB.GetItemByGUID(ctx, lureGUID)
	if err != nil || row == nil {
		return ""
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil || len(fields) == 0 {
		return ""
	}
	active := 0
	if len(fields) > 1 && fields[1].Num != nil {
		if v := int(u64Of(fields[1].Num)); v >= 0 {
			active = v
		}
	}
	// 落库出厂钩：f0 args(60) 每项的第一个 GUID 字段。
	if hooks := storedLureHookGUIDs(&fields[0]); len(hooks) > 0 {
		if active >= len(hooks) {
			active = 0
		}
		return hooks[active]
	}
	return ""
}

// storedLureHookGUIDs 按顺序提取拟饵 f0 args(60) 中每颗出厂钩的 GUID。
func storedLureHookGUIDs(field0 *proto.Field) []string {
	if field0 == nil || field0.Val == nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, it := range field0.Val.Items {
		if it == nil {
			continue
		}
		for i := range it.Fields {
			if it.Fields[i].Op == "GUID16" && it.Fields[i].Hex != "" {
				out = append(out, it.Fields[i].Hex)
				break
			}
		}
	}
	return out
}

// currentBait 返回当前钩上或拟饵位的 item_id，用于选择鱼种。
// 优先读取饵品类 27，其次读取拟饵品类 17；item_id 为空时按 numeric_id 反查定义。
func (s *Server) currentBait(ctx context.Context, rodGUID string) string {
	if rodGUID == "" {
		return ""
	}
	for _, cat := range []int64{27, 17} {
		g := s.rigComponentGuidOf(ctx, rodGUID, cat)
		if g == "" {
			continue
		}
		row, err := s.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			continue
		}
		if row.ItemID != "" {
			return row.ItemID
		}
		if def, derr := s.DB.GetItemDef(ctx, row.NumericID); derr == nil && def != nil {
			return def.ItemID
		}
	}
	return ""
}

// currentBaits 按槽序读取全部钩上饵；没有天然饵时再读取拟饵。
func (s *Server) currentBaits(ctx context.Context, rodGUID string) []string {
	comps := s.storedRigComponents(ctx, rodGUID)
	for _, category := range []int64{27, 17} {
		var ids []string
		for _, key := range rigCompKeysOfCategory(comps, category) {
			row, err := s.DB.GetItemByGUID(ctx, comps[key].GUID)
			if err != nil || row == nil {
				continue
			}
			if s.itemBrokenForMount(ctx, row) {
				continue
			}
			if category == 27 && baitQuantity(row) <= 0 {
				continue
			}
			id := row.ItemID
			if id == "" {
				if def, err := s.DB.GetItemDef(ctx, row.NumericID); err == nil && def != nil {
					id = def.ItemID
				}
			}
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return ids
		}
	}
	return []string{}
}

func baitQuantity(row *store.Item) int64 {
	if row == nil {
		return 0
	}
	if fields, err := FieldsFromJSON(row.FieldsJSON); err == nil {
		if idx := fieldIdxByCS(row.WireClass, csQty); idx >= 0 && idx < len(fields) && fields[idx].Num != nil {
			return int64(u64Of(fields[idx].Num))
		}
	}
	return row.Qty
}

// rigTypeOfItem 返回竿实例的钓组类型；缺失时按竿型派生。
func (s *Server) rigTypeOfItem(ctx context.Context, row *store.Item) int64 {
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err == nil {
		if idx := fieldIdxByCS(row.WireClass, csRigObject); idx >= 0 && idx < len(fields) {
			if f5 := fields[idx].Val; f5 != nil && f5.FieldCount() > 1 {
				if rt := int64(u32Of(f5.Field(1).Num)); rt > 0 {
					return rt
				}
			}
		}
	}
	return s.defaultRigTypeForRod(ctx, row.ItemID)
}

// ensureRodTip 确保竿实例的尖梢字段非空并落库，返回是否新写入。
// 已有玩家选择时保持不变；否则优先使用 rod_setting 中的选择，再使用竿定义的第一支尖梢。
// 客户端在抛竿前校验该字段，因此新写入后调用方应推送 4/22 全量物品。
func (s *Server) ensureRodTip(ctx context.Context, row *store.Item) bool {
	if row == nil || s.DB == nil {
		return false
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return false
	}
	tidx := fieldIdxByCS(row.WireClass, csRigTip)
	if tidx < 0 || tidx >= len(fields) {
		return false // 字段不存在时不修改
	}
	if fields[tidx].Str != nil && *fields[tidx].Str != "" {
		return false // 玩家已选/已收敛
	}
	// 优先使用玩家已保存的尖梢选择。
	tip, _ := s.DB.GetRodSetting(ctx, row.GUID, "quivertip")
	tip = strings.TrimSpace(tip)
	if tip == "" {
		tips := s.rodDefTips(ctx, row.ItemID)
		if len(tips) == 0 {
			return false // 无尖梢的竿（手竿/路亚等）
		}
		tip = tips[0]
	}
	fields[tidx].Str = &tip
	// fields_json 只在 STR 分支读取字符串，因此落库前将 op 规范为 STR。
	// 线上编码仍按类 ops 的 U32N 规则处理。
	fields[tidx].Op = "STR"
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return false
	}
	if err := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID); err != nil {
		return false
	}
	return true
}
