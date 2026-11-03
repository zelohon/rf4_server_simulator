package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AdminItemView 管理后台物品列表行。fields_json 不直接下发，避免页面被大块
// 快照淹没；需要时仍可按 guid 在详情接口中查看摘要。
type AdminItemView struct {
	GUID        string  `json:"guid"`
	ItemID      string  `json:"item_id"`
	Name        string  `json:"name"`
	Category    int64   `json:"category"`
	Kind        string  `json:"kind"`
	Brand       string  `json:"brand"`
	NumericID   int64   `json:"numeric_id"`
	WireClass   string  `json:"wire_class"`
	Container   string  `json:"container"`
	Slot        int64   `json:"slot"`
	RigGUID     string  `json:"rig_guid"`
	Qty         int64   `json:"qty"`
	Durability  float64 `json:"durability"`
	Workmanship float64 `json:"workmanship"`
	CreatedTS   float64 `json:"created_ts"`
}

// AdminSkillView 技能大类熟练度。skill.points 是 0..100 的百分比熟练度，
// 不是可分配技能点；节点等级单独存 skill_node。
type AdminSkillView struct {
	SkillID      int64   `json:"skill_id"`
	Name         string  `json:"name"`
	CategoryKind int64   `json:"category_kind"`
	Points       float64 `json:"points"`
	NodeCount    int64   `json:"node_count"`
}

// AdminStoreView 商店汇总。目录中的 205 家店即使暂无可用货架也会出现；
// CatalogKnown=false 表示该店来自额外的目录定义。
type AdminStoreView struct {
	StoreID       string   `json:"store_id"`
	ItemCount     int64    `json:"item_count"`
	TotalStock    int64    `json:"total_stock"`
	OverrideCount int64    `json:"override_count"`
	Kind          int      `json:"kind"`
	MapKey        string   `json:"map_key"`
	Brands        []string `json:"brands"`
	CatalogKnown  bool     `json:"catalog_known"`
	ShelfSource   string   `json:"shelf_source"`
}

// AdminStoreItemView 有效货架行（静态值 + 后台覆盖）。
type AdminStoreItemView struct {
	StoreID     string `json:"store_id"`
	ItemID      string `json:"item_id"`
	Name        string `json:"name"`
	Category    int64  `json:"category"`
	Silver      int64  `json:"silver"`
	Gold        int64  `json:"gold"`
	Stock       int64  `json:"stock"`
	BaseSilver  int64  `json:"base_silver"`
	BaseGold    int64  `json:"base_gold"`
	BaseStock   int64  `json:"base_stock"`
	HasOverride bool   `json:"has_override"`
}

// FishAreaView 可配置鱼口的水域。
type FishAreaView struct {
	Loc       string `json:"loc"`
	FishCount int64  `json:"fish_count"`
}

type FishConfigRow struct {
	FishKey     string  `json:"fish_key"`
	NameZh      string  `json:"name_zh"`
	BaseWeight  float64 `json:"base_weight"`
	Weight      float64 `json:"weight"`
	Enabled     bool    `json:"enabled"`
	HasOverride bool    `json:"has_override"`
}

type FishBiteSetting struct {
	Loc              string  `json:"loc"`
	BiteProbability  float64 `json:"bite_probability"`
	BiteDelayFixed   float64 `json:"bite_delay_fixed"`
	BiteDelayScale   float64 `json:"bite_delay_scale"`
	BiteDelayMax     float64 `json:"bite_delay_max"`
	BiteDelayLureMax float64 `json:"bite_delay_lure_max"`
	HasOverride      bool    `json:"has_override"`
	UpdatedTS        float64 `json:"updated_ts"`
}

type FishWeightOverride struct {
	Loc     string
	FishKey string
	Weight  float64
	Enabled bool
}

type AdminFishConfig struct {
	Area     string          `json:"area"`
	Setting  FishBiteSetting `json:"setting"`
	Fish     []FishConfigRow `json:"fish"`
	FishPool []string        `json:"fish_pool"`
}

type FishConfigSave struct {
	Area      string          `json:"area"`
	Setting   FishBiteSetting `json:"setting"`
	Fish      []FishConfigRow `json:"fish"`
	UpdatedBy string          `json:"updated_by"`
}

func (d *DB) ListPlayerItemsAdmin(ctx context.Context, uid int64) ([]*AdminItemView, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT i.guid, COALESCE(i.item_id,''), COALESCE(c.name_zh,c.name,''), COALESCE(d.category,0),
		       COALESCE(d.kind,''), COALESCE(d.brand,''), COALESCE(i.numeric_id,0),
		       COALESCE(i.wire_class,''), COALESCE(i.container,''), COALESCE(i.slot,0),
		       COALESCE(i.rig_guid,''), COALESCE(i.qty,1), COALESCE(i.durability,1),
		       COALESCE(i.workmanship,1), COALESCE(i.created_ts,0)
		FROM item i
		LEFT JOIN item_def d ON d.numeric_id=i.numeric_id
		LEFT JOIN item_catalog c ON c.item_id=COALESCE(NULLIF(d.item_id,''),i.item_id)
		WHERE i.uid=? ORDER BY i.container,i.slot,i.created_ts,i.guid`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*AdminItemView, 0)
	for rows.Next() {
		v := &AdminItemView{}
		if err := rows.Scan(&v.GUID, &v.ItemID, &v.Name, &v.Category, &v.Kind, &v.Brand,
			&v.NumericID, &v.WireClass, &v.Container, &v.Slot, &v.RigGUID, &v.Qty,
			&v.Durability, &v.Workmanship, &v.CreatedTS); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) ListPlayerSkillsAdmin(ctx context.Context, uid int64) ([]*AdminSkillView, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT c.cat_id, COALESCE(c.title_key,''), COALESCE(c.category_kind,0),
		       COALESCE(s.points,0),
		       (SELECT COUNT(*) FROM skill_def d WHERE d.cat_id=c.cat_id)
		FROM skill_cat_def c
		LEFT JOIN skill s ON s.uid=? AND s.skill_id=c.cat_id
		ORDER BY c.ord,c.cat_id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := adminSkillCategoryNames()
	out := make([]*AdminSkillView, 0)
	for rows.Next() {
		v := &AdminSkillView{}
		if err := rows.Scan(&v.SkillID, &v.Name, &v.CategoryKind, &v.Points, &v.NodeCount); err != nil {
			return nil, err
		}
		if zh := names[v.SkillID]; zh != "" {
			v.Name = zh
		}
		if v.Name == "" {
			v.Name = fmt.Sprintf("技能 %d", v.SkillID)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func adminSkillCategoryNames() map[int64]string {
	return map[int64]string{
		1: "浮子钓鱼", 2: "纺车钓鱼", 3: "底钓", 4: "饵料制作",
		5: "技能 801-815", 6: "烹饪", 7: "混合诱饵", 8: "路亚手工",
		9: "技能 1-18", 10: "技能 901-904", 11: "海钓",
	}
}

func (d *DB) SetPlayerSkillPointsAdmin(ctx context.Context, uid, skillID int64, points float64) error {
	if points < 0 {
		points = 0
	}
	if points > maxSkillPoints {
		points = maxSkillPoints
	}
	var exists int
	if err := d.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skill_cat_def WHERE cat_id=?`, skillID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("技能大类 %d 不存在", skillID)
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO skill(uid, skill_id, points) VALUES(?,?,?)
		ON CONFLICT(uid,skill_id) DO UPDATE SET points=excluded.points`, uid, skillID, points)
	return err
}

// skillBonusKey 是管理员追加/扣减未分配技能点时使用的 kv 键。
func skillBonusKey(uid int64) string { return fmt.Sprintf("skill_bonus:%d", uid) }

// skillPointsTotalForAdmin 复刻游戏服的等级累计技能点公式，供后台展示与换算。
func skillPointsTotalForAdmin(level int64) int64 {
	var sum int64
	for l := int64(2); l <= level; l++ {
		switch {
		case l < 20:
			sum++
		case l < 30:
			sum += 2
		case l < 40:
			sum += 3
		case l < 50:
			sum += 4
		default:
			sum += 5
		}
	}
	return sum
}

// SkillPointsAvailable 返回等级累计点数、已投入点数、管理员偏移与未分配点数。
func (d *DB) SkillPointsAvailable(ctx context.Context, uid int64) (total, spent, bonus, avail int64) {
	p, err := d.EnsureProfile(ctx, uid)
	if err == nil && p != nil {
		total = skillPointsTotalForAdmin(p.Level)
	}
	spent, _ = d.SkillNodeLevelSum(ctx, uid)
	if v, ok, kerr := d.KVGet(ctx, skillBonusKey(uid)); kerr == nil && ok {
		bonus, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	}
	avail = total - spent + bonus
	if avail < 0 {
		avail = 0
	}
	return
}

// SetSkillBonus 写入管理员偏移；调用方负责把目标未分配点数换算成偏移量。
func (d *DB) SetSkillBonus(ctx context.Context, uid, bonus int64) error {
	return d.KVSet(ctx, skillBonusKey(uid), strconv.FormatInt(bonus, 10))
}

// UnlimitedSkillPoints 是开启无限技能点时下发给客户端的可用点数。
// 取 9999 而非 int64 上限：客户端面板按整数展示，过大数值会溢出或撑破布局。
const UnlimitedSkillPoints int64 = 9999

// skillUnlimitedKey 是"无限技能点"开关的 kv 键。
func skillUnlimitedKey(uid int64) string { return fmt.Sprintf("skill_unlimited:%d", uid) }

// SkillUnlimited 返回玩家是否开启了无限技能点。
func (d *DB) SkillUnlimited(ctx context.Context, uid int64) bool {
	v, ok, err := d.KVGet(ctx, skillUnlimitedKey(uid))
	if err != nil || !ok {
		return false
	}
	return strings.TrimSpace(v) == "1"
}

// SetSkillUnlimited 打开或关闭无限技能点。
// 关闭时顺带把管理员偏移清零，让可用点数回到等级累计的自然值，
// 否则之前为凑数写入的偏移会残留成"凭空多出来的点数"。
func (d *DB) SetSkillUnlimited(ctx context.Context, uid int64, on bool) error {
	if on {
		return d.KVSet(ctx, skillUnlimitedKey(uid), "1")
	}
	if err := d.KVSet(ctx, skillUnlimitedKey(uid), "0"); err != nil {
		return err
	}
	return d.SetSkillBonus(ctx, uid, 0)
}

// MaxPlayerSkills 一键技能满：把所有技能大类熟练度拉到 100，
// 把全部可升级节点直接写成 max_level，并打开无限技能点开关。
//
// 说明：
//   - 节点按 skill_def.max_level 写满，max_level=0 的信息型节点保持 0 级；
//   - 不按 group_id 做互斥过滤——"满技能"语义就是把树全部点亮，
//     客户端会按 311 返回的等级逐节点渲染，互斥只在正常学习路径上生效；
//   - 整批写入放在事务里，避免中途失败留下半满的树。
func (d *DB) MaxPlayerSkills(ctx context.Context, uid int64) (cats, nodes int64, err error) {
	if err = d.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skill_cat_def`).Scan(&cats); err != nil {
		return 0, 0, err
	}
	if err = d.SQL.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skill_def WHERE max_level > 0`).Scan(&nodes); err != nil {
		return 0, 0, err
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO skill(uid, skill_id, points)
		SELECT ?, cat_id, ? FROM skill_cat_def WHERE 1
		ON CONFLICT(uid, skill_id) DO UPDATE SET points=excluded.points`,
		uid, maxSkillPoints); err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO skill_node(uid, node_id, level)
		SELECT ?, node_id, max_level FROM skill_def WHERE max_level > 0
		ON CONFLICT(uid, node_id) DO UPDATE SET level=excluded.level`,
		uid); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	if err = d.SetSkillUnlimited(ctx, uid, true); err != nil {
		return 0, 0, err
	}
	return cats, nodes, nil
}

func (d *DB) ListAdminStores(ctx context.Context) ([]*AdminStoreView, error) {
	catalog, err := embeddedShopKindCatalog()
	if err != nil {
		return nil, err
	}
	recorded, err := d.loadRecordedShopShelves(ctx)
	if err != nil {
		return nil, err
	}
	allOverrides, err := d.loadAllShopOverrides(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(catalog.Stores)+len(recorded))
	for storeID := range catalog.Stores {
		ids[storeID] = struct{}{}
	}
	for storeID := range recorded {
		ids[storeID] = struct{}{}
	}
	storeIDs := make([]string, 0, len(ids))
	for storeID := range ids {
		storeIDs = append(storeIDs, storeID)
	}
	sort.Strings(storeIDs)

	out := make([]*AdminStoreView, 0, len(storeIDs))
	for _, storeID := range storeIDs {
		base, source, def, known := resolveShopBase(storeID, catalog, recorded)
		overrides := allOverrides[storeID]
		effective := applyShopOverrides(base, overrides)
		baseIDs := make(map[string]struct{}, len(base))
		for _, row := range base {
			baseIDs[row.ItemID] = struct{}{}
		}
		var totalStock, overrideCount int64
		for _, row := range effective {
			totalStock += row.Stock
		}
		for itemID := range overrides {
			if _, ok := baseIDs[itemID]; ok {
				overrideCount++
			}
		}
		v := &AdminStoreView{
			StoreID:       storeID,
			ItemCount:     int64(len(effective)),
			TotalStock:    totalStock,
			OverrideCount: overrideCount,
			Kind:          def.Kind,
			MapKey:        def.MapKey,
			Brands:        append([]string(nil), def.Brands...),
			CatalogKnown:  known,
			ShelfSource:   source,
		}
		out = append(out, v)
	}
	return out, nil
}

type adminShopItemMeta struct {
	Name     string
	Category int64
}

func (d *DB) loadAdminShopItemMeta(ctx context.Context, itemIDs []string) (map[string]adminShopItemMeta, error) {
	out := make(map[string]adminShopItemMeta, len(itemIDs))
	const chunkSize = 500
	for start := 0; start < len(itemIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(itemIDs) {
			end = len(itemIDs)
		}
		chunk := itemIDs[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, itemID := range chunk {
			placeholders[i] = "?"
			args[i] = itemID
		}
		query := `SELECT d.item_id,
			COALESCE(MAX(NULLIF(c.name_zh,'')), MAX(c.name), ''),
			COALESCE(MAX(d.category),0)
			FROM item_def d
			LEFT JOIN item_catalog c ON c.item_id=d.item_id
			WHERE d.item_id IN (` + strings.Join(placeholders, ",") + `)
			GROUP BY d.item_id`
		rows, err := d.SQL.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var itemID string
			var meta adminShopItemMeta
			if err := rows.Scan(&itemID, &meta.Name, &meta.Category); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[itemID] = meta
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

func (d *DB) ListAdminStoreItems(ctx context.Context, storeID, query string, limit int) ([]*AdminStoreItemView, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		return nil, fmt.Errorf("store_id 不能为空")
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	shelf, err := d.effectiveShopShelf(ctx, storeID)
	if err != nil {
		return nil, err
	}
	baseByID := make(map[string]*ShopPrice, len(shelf.Base))
	for _, row := range shelf.Base {
		baseByID[row.ItemID] = row
	}
	itemIDs := make([]string, 0, len(shelf.Effective))
	for _, row := range shelf.Effective {
		itemIDs = append(itemIDs, row.ItemID)
	}
	meta, err := d.loadAdminShopItemMeta(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	effective := copyShopPrices(shelf.Effective)
	sort.Slice(effective, func(i, j int) bool { return effective[i].ItemID < effective[j].ItemID })
	out := make([]*AdminStoreItemView, 0, len(effective))
	for _, row := range effective {
		m := meta[row.ItemID]
		if needle != "" && !strings.Contains(strings.ToLower(row.ItemID), needle) &&
			!strings.Contains(strings.ToLower(m.Name), needle) {
			continue
		}
		base := baseByID[row.ItemID]
		if base == nil {
			continue
		}
		_, hasOverride := shelf.Overrides[row.ItemID]
		out = append(out, &AdminStoreItemView{
			StoreID:     storeID,
			ItemID:      row.ItemID,
			Name:        m.Name,
			Category:    m.Category,
			Silver:      row.Silver,
			Gold:        row.Gold,
			Stock:       row.Stock,
			BaseSilver:  base.Silver,
			BaseGold:    base.Gold,
			BaseStock:   base.Stock,
			HasOverride: hasOverride,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (d *DB) SetAdminShopOverride(ctx context.Context, storeID, itemID string, silver, gold, stock int64, updatedBy string) error {
	storeID = strings.TrimSpace(storeID)
	itemID = strings.TrimSpace(itemID)
	if storeID == "" || itemID == "" {
		return fmt.Errorf("store_id 和 item_id 不能为空")
	}
	if silver < 0 || gold < 0 || stock < 0 {
		return fmt.Errorf("价格和库存不能为负数")
	}
	if _, err := d.ShopItemGet(ctx, storeID, itemID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("商品不在该店生效货架: %s/%s", storeID, itemID)
		}
		return err
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO shop_item_override(store_id,item_id,silver,gold,stock,updated_ts,updated_by)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(store_id,item_id) DO UPDATE SET
			silver=excluded.silver, gold=excluded.gold, stock=excluded.stock,
			updated_ts=excluded.updated_ts, updated_by=excluded.updated_by`,
		storeID, itemID, silver, gold, stock, float64(time.Now().UnixNano())/1e9, updatedBy)
	return err
}

func (d *DB) ResetAdminShopOverride(ctx context.Context, storeID, itemID string) error {
	_, err := d.SQL.ExecContext(ctx,
		`DELETE FROM shop_item_override WHERE store_id=? AND item_id=?`, storeID, itemID)
	return err
}

func (d *DB) ListFishAreasAdmin(ctx context.Context) ([]*FishAreaView, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT loc, COUNT(*) FROM loc_fish GROUP BY loc ORDER BY loc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*FishAreaView, 0)
	for rows.Next() {
		v := &FishAreaView{}
		if err := rows.Scan(&v.Loc, &v.FishCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) GetFishBiteSetting(ctx context.Context, loc string) (*FishBiteSetting, error) {
	v := &FishBiteSetting{
		Loc:              loc,
		BiteProbability:  d.tunableFloat(ctx, "bite_probability", 1.0),
		BiteDelayFixed:   d.tunableFloat(ctx, "bite_delay_fixed", 0),
		BiteDelayScale:   d.tunableFloat(ctx, "bite_delay_scale", 1.0),
		BiteDelayMax:     d.tunableFloat(ctx, "bite_delay_max", 20.0),
		BiteDelayLureMax: d.tunableFloat(ctx, "bite_delay_lure_max", 6.0),
	}
	var prob, fixed, scale, maxD, lure sql.NullFloat64
	err := d.SQL.QueryRowContext(ctx, `
		SELECT bite_probability,bite_delay_fixed,bite_delay_scale,bite_delay_max,
		       bite_delay_lure_max,updated_ts
		FROM fish_bite_setting WHERE loc=?`, loc).Scan(&prob, &fixed, &scale, &maxD, &lure, &v.UpdatedTS)
	if err == sql.ErrNoRows {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	v.HasOverride = true
	if prob.Valid {
		v.BiteProbability = prob.Float64
	}
	if fixed.Valid {
		v.BiteDelayFixed = fixed.Float64
	}
	if scale.Valid {
		v.BiteDelayScale = scale.Float64
	}
	if maxD.Valid {
		v.BiteDelayMax = maxD.Float64
	}
	if lure.Valid {
		v.BiteDelayLureMax = lure.Float64
	}
	return v, nil
}

func (d *DB) tunableFloat(ctx context.Context, key string, def float64) float64 {
	var raw string
	if err := d.SQL.QueryRowContext(ctx, `SELECT value FROM tunables WHERE key=?`, key).Scan(&raw); err != nil {
		return def
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%f", &v); err != nil {
		return def
	}
	return v
}

func (d *DB) GetAdminFishConfig(ctx context.Context, loc string) (*AdminFishConfig, error) {
	roster, err := d.LocRoster(ctx, loc)
	if err != nil {
		return nil, err
	}
	setting, err := d.GetFishBiteSetting(ctx, loc)
	if err != nil {
		return nil, err
	}
	ovs, err := d.FishWeightOverrides(ctx, loc)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT fish_key, COALESCE(NULLIF(name_zh,''),fish_key) FROM loc_fish WHERE loc=?
		UNION
		SELECT fish_key, COALESCE(NULLIF(fish_zh,''),fish_key) FROM loc_wiki_fish WHERE loc=?`, loc, loc)
	if err == nil {
		for rows.Next() {
			var k, n string
			if rows.Scan(&k, &n) == nil {
				names[k] = n
			}
		}
		_ = rows.Close()
	}
	cfg := &AdminFishConfig{Area: loc, Setting: *setting, Fish: make([]FishConfigRow, 0, len(roster))}
	cfg.FishPool = make([]string, 0, len(roster))
	seen := make(map[string]bool, len(roster))
	for _, r := range roster {
		seen[r.FishKey] = true
		name := names[r.FishKey]
		if name == "" {
			name = r.FishKey
		}
		row := FishConfigRow{FishKey: r.FishKey, NameZh: name, BaseWeight: r.Catches, Weight: r.Catches, Enabled: true}
		if ov, ok := ovs[r.FishKey]; ok {
			row.Weight, row.Enabled, row.HasOverride = ov.Weight, ov.Enabled, true
		}
		cfg.Fish = append(cfg.Fish, row)
		cfg.FishPool = append(cfg.FishPool, r.FishKey)
	}
	for key, ov := range ovs {
		if seen[key] {
			continue
		}
		name := names[key]
		if name == "" {
			name = key
		}
		cfg.Fish = append(cfg.Fish, FishConfigRow{
			FishKey: key, NameZh: name, BaseWeight: 0, Weight: ov.Weight,
			Enabled: ov.Enabled, HasOverride: true,
		})
	}
	return cfg, nil
}

func (d *DB) SaveAdminFishConfig(ctx context.Context, in FishConfigSave, updatedBy string) error {
	if strings.TrimSpace(in.Area) == "" {
		return fmt.Errorf("水域不能为空")
	}
	now := float64(time.Now().UnixNano()) / 1e9
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	s := in.Setting
	if s.BiteProbability < 0 {
		s.BiteProbability = 0
	}
	if s.BiteProbability > 1 {
		s.BiteProbability = 1
	}
	if s.BiteDelayFixed < 0 {
		s.BiteDelayFixed = 0
	}
	if s.BiteDelayScale < 0 {
		s.BiteDelayScale = 0
	}
	if s.BiteDelayMax < 1 {
		s.BiteDelayMax = 1
	}
	if s.BiteDelayLureMax < 0.5 {
		s.BiteDelayLureMax = 0.5
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO fish_bite_setting(loc,bite_probability,bite_delay_fixed,bite_delay_scale,
			bite_delay_max,bite_delay_lure_max,updated_ts,updated_by)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(loc) DO UPDATE SET
			bite_probability=excluded.bite_probability,
			bite_delay_fixed=excluded.bite_delay_fixed,
			bite_delay_scale=excluded.bite_delay_scale,
			bite_delay_max=excluded.bite_delay_max,
			bite_delay_lure_max=excluded.bite_delay_lure_max,
			updated_ts=excluded.updated_ts, updated_by=excluded.updated_by`,
		in.Area, s.BiteProbability, s.BiteDelayFixed, s.BiteDelayScale,
		s.BiteDelayMax, s.BiteDelayLureMax, now, updatedBy); err != nil {
		return err
	}
	for _, f := range in.Fish {
		if strings.TrimSpace(f.FishKey) == "" {
			continue
		}
		if f.Weight < 0 {
			f.Weight = 0
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_ts,updated_by)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(loc,fish_key) DO UPDATE SET
				weight=excluded.weight, enabled=excluded.enabled,
				updated_ts=excluded.updated_ts, updated_by=excluded.updated_by`,
			in.Area, f.FishKey, f.Weight, boolInt(f.Enabled), now, updatedBy); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ResetAdminFishConfig(ctx context.Context, loc string) error {
	if strings.TrimSpace(loc) == "" {
		return fmt.Errorf("水域不能为空")
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM fish_weight_override WHERE loc=?`, loc); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fish_bite_setting WHERE loc=?`, loc); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) FishWeightOverrides(ctx context.Context, loc string) (map[string]FishWeightOverride, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT loc,fish_key,weight,enabled FROM fish_weight_override WHERE loc=?`, loc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FishWeightOverride{}
	for rows.Next() {
		var v FishWeightOverride
		var enabled int
		if err := rows.Scan(&v.Loc, &v.FishKey, &v.Weight, &enabled); err != nil {
			return nil, err
		}
		v.Enabled = enabled != 0
		out[v.FishKey] = v
	}
	return out, rows.Err()
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
