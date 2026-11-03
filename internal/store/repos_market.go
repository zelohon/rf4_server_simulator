package store

import (
	"context"
	"database/sql"
	"math"
	"strconv"
	"strings"
)

// 钓鱼经济域存储：地点解析、鱼种名册、饵效、工坊和命名鱼读取。
// 静态鱼库由 seed 从 server_data/static.sqlite3 导入；本文件只做读取和最小写入。

// ---- 地点解析 ----

// sublocPrefix 从场景名（loc1_02 / lvl2_01 / lvl_004_05）提取 level 前缀（level_001_）。
// 客户端在钓鱼时上报的是场景名，编号可能带前导下划线和三位补零，必须一并兼容。
func sublocPrefix(loc string) string {
	body := loc
	if strings.HasPrefix(body, "loc") {
		body = body[3:]
	} else if strings.HasPrefix(body, "lvl") {
		body = body[3:]
	} else {
		return ""
	}
	body = strings.TrimLeft(body, "_")
	if body == "" {
		return ""
	}
	numPart := body
	if us := strings.IndexByte(body, '_'); us > 0 {
		numPart = body[:us]
	}
	n, err := strconv.Atoi(numPart)
	if err != nil || n <= 0 {
		return ""
	}
	return "level_" + fmt03(n) + "_"
}

func fmt03(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// ResolveStatLoc 游戏地点 → 统计站地点名（loc_alias.loc）。
// level_ 前缀按全名精确查；场景名按 level_NNN_ 前缀 LIKE 查。
// 无法解析时返回空串。
func (d *DB) ResolveStatLoc(ctx context.Context, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if strings.HasPrefix(loc, "level_") {
		var out string
		_ = d.SQL.QueryRowContext(ctx,
			`SELECT loc FROM loc_alias WHERE alias=?`, loc).Scan(&out)
		return out
	}
	if prefix := sublocPrefix(loc); prefix != "" {
		var out string
		_ = d.SQL.QueryRowContext(ctx,
			`SELECT loc FROM loc_alias WHERE alias LIKE ? ORDER BY alias LIMIT 1`,
			prefix+"%").Scan(&out)
		return out
	}
	return ""
}

// ResolveLevelKey 将场景名转换为游戏 level key。无法解析时返回空串。
func (d *DB) ResolveLevelKey(ctx context.Context, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if strings.HasPrefix(loc, "level_") {
		return loc
	}
	if prefix := sublocPrefix(loc); prefix != "" {
		var out string
		_ = d.SQL.QueryRowContext(ctx,
			`SELECT alias FROM loc_alias WHERE alias LIKE ? ORDER BY alias LIMIT 1`,
			prefix+"%").Scan(&out)
		return out
	}
	return ""
}

// ---- 鱼库名册 ----

// FishRosterRow 鱼种权重行（wiki 名册为全集，统计捕获数作权重，缺统计保底 1）。
type FishRosterRow struct {
	FishKey string
	Catches float64
}

// LocRoster 该统计站地点的鱼种名册：
// 优先读取 loc_wiki_fish 全集并连接 loc_fish 的捕获计数；为空时回退到
// catches>0 的 loc_fish 行。
func (d *DB) LocRoster(ctx context.Context, statLoc string) ([]*FishRosterRow, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT w.fish_key, COALESCE(l.catches, 1)
		FROM loc_wiki_fish w LEFT JOIN loc_fish l
		  ON l.loc = w.loc AND l.fish_key = w.fish_key
		WHERE w.loc=?`, statLoc)
	if err == nil {
		out, scanErr := scanRoster(rows)
		if scanErr == nil && len(out) > 0 {
			return out, nil
		}
	}
	rows, err = d.SQL.QueryContext(ctx,
		`SELECT fish_key, catches FROM loc_fish WHERE loc=? AND catches>0`, statLoc)
	if err != nil {
		return nil, err
	}
	return scanRoster(rows)
}

func scanRoster(rows *sql.Rows) ([]*FishRosterRow, error) {
	defer rows.Close()
	var out []*FishRosterRow
	for rows.Next() {
		r := &FishRosterRow{}
		if err := rows.Scan(&r.FishKey, &r.Catches); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GradeLines 该鱼种三档位线（g）：(达标线 trophy_g, 奖杯线 trophy_line_g,
// 蓝冠线 super_trophy_line_g)；缺失字段返回 nil。
func (d *DB) GradeLines(ctx context.Context, fishKey string) (val, tro, sup *int64) {
	var v, t, s sql.NullInt64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT trophy_g, trophy_line_g, super_trophy_line_g FROM species_weight WHERE fish_key=?`,
		fishKey).Scan(&v, &t, &s)
	if err != nil {
		return nil, nil, nil
	}
	if v.Valid {
		x := v.Int64
		val = &x
	}
	if t.Valid {
		x := t.Int64
		tro = &x
	}
	if s.Valid {
		x := s.Int64
		sup = &x
	}
	return
}

// SpeciesLengthKP 返回体长公式 k*w^p 的系数。NULL 或缺少记录时使用
// LENGTH_K_DEFAULT 和 LENGTH_P_DEFAULT。
func (d *DB) SpeciesLengthKP(ctx context.Context, fishKey string) (k, p float64) {
	k, p = 0.0355, 1.0/3.0
	var lk, lp sql.NullFloat64
	if err := d.SQL.QueryRowContext(ctx,
		`SELECT length_k, length_p FROM species_weight WHERE fish_key=?`,
		fishKey).Scan(&lk, &lp); err != nil {
		return
	}
	if lk.Valid && lk.Float64 > 0 {
		k = lk.Float64
	}
	if lp.Valid && lp.Float64 > 0 {
		p = lp.Float64
	}
	return
}

// ---- companion 饵效 ----

// BaitTiers 饵 systemID → {fish_key: tier}（top5=3 / top20=2 / 在列=1）。
// 表缺失或无数据时返回 nil，不调整鱼种权重。
func (d *DB) BaitTiers(ctx context.Context, baitID string) map[string]int64 {
	if baitID == "" {
		return nil
	}
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT fish_key, tier FROM companion_bait_fish WHERE bait_id=?`, baitID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var fk string
		var tier int64
		if err := rows.Scan(&fk, &tier); err != nil {
			return nil
		}
		out[fk] = tier
	}
	if err := rows.Err(); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// CompanionEvidence fish_key → 该鱼 companion 数据覆盖度（total_evidence）。
// 缺表返回 nil（覆盖率按 0 处理）。
func (d *DB) CompanionEvidence(ctx context.Context) map[string]int64 {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT fish_key, total_evidence FROM companion_fish_meta`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var fk string
		var ev int64
		if err := rows.Scan(&fk, &ev); err != nil {
			return nil
		}
		out[fk] = ev
	}
	return out
}

// ---- 工坊 ----

// ListWornItems 耐久低于 threshold 的物品（修理面板）。
// 结果按 durability 升序排列。
func (d *DB) ListWornItems(ctx context.Context, uid int64, threshold float64) ([]*Item, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+itemCols+` FROM item WHERE uid=? AND durability<? ORDER BY durability`,
		uid, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it := &Item{}
		if err := rows.Scan(&it.GUID, &it.UID, &it.ItemID, &it.NumericID, &it.WireClass,
			&it.TypeID, &it.Container, &it.Slot, &it.RigGUID, &it.Qty, &it.BuyPrice,
			&it.FieldsJSON, &it.Durability, &it.Workmanship, &it.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListRepairItems 返回修理面板所需的完整物品清单（全部随身物品，含满耐久）。
//
// 客户端修理页左侧展示的是“随身物品表”，点击任意一件都会在 21/1 的返回列表里
// 按 GUID 查修理信息；只返回磨损件会让客户端查不到模型，得到空详情。
// 结果按 durability 升序排列，磨损最重的排在前面。
func (d *DB) ListRepairItems(ctx context.Context, uid int64) ([]*Item, error) {
	rows, err := d.SQL.QueryContext(ctx,
		`SELECT `+itemCols+` FROM item WHERE uid=? ORDER BY durability, slot`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it := &Item{}
		if err := rows.Scan(&it.GUID, &it.UID, &it.ItemID, &it.NumericID, &it.WireClass,
			&it.TypeID, &it.Container, &it.Slot, &it.RigGUID, &it.Qty, &it.BuyPrice,
			&it.FieldsJSON, &it.Durability, &it.Workmanship, &it.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SetItemDurability 将耐久限制在 0..1 后写入列。读取路径以该列为准重建字段，
// 不需要同步修改 fields_json。
func (d *DB) SetItemDurability(ctx context.Context, guid string, value float64) error {
	value = math.Max(0.0, math.Min(1.0, value))
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET durability=? WHERE guid=?`, value, guid)
	return err
}

// ---- 命名鱼 ----

// NamedFishIsNamed 报告该鱼是否已被命名。
func (d *DB) NamedFishIsNamed(ctx context.Context, fishID string) bool {
	if fishID == "" {
		return false
	}
	nf, err := d.NamedFishGet(ctx, fishID)
	return err == nil && nf != nil
}

// NamedFishNoteRecatch 将被命名鱼的钓获次数加一。
func (d *DB) NamedFishNoteRecatch(ctx context.Context, fishID string) {
	if fishID == "" {
		return
	}
	_, _ = d.SQL.ExecContext(ctx,
		`UPDATE named_fish SET catches = catches + 1 WHERE fish_id=?`, fishID)
}
