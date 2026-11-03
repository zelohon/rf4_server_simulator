package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ---- 技能 ----

// maxSkillPoints 是单个技能大类的熟练度上限。
const maxSkillPoints = 100.0

// ListSkills 取玩家技能熟练度。
func (d *DB) ListSkills(ctx context.Context, uid int64) (map[int64]float64, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT skill_id, points FROM skill WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		var pts float64
		if err := rows.Scan(&id, &pts); err != nil {
			return nil, err
		}
		if pts > maxSkillPoints {
			pts = maxSkillPoints
		}
		out[id] = pts
	}
	return out, rows.Err()
}

// AddSkillPoints 增加熟练度（UPSERT 累加）。
func (d *DB) AddSkillPoints(ctx context.Context, uid, skillID int64, delta float64) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO skill(uid, skill_id, points) VALUES(?,?,MIN(?,?))
		ON CONFLICT (uid, skill_id) DO UPDATE
		SET points = MIN(points + excluded.points, ?)`,
		uid, skillID, delta, maxSkillPoints, maxSkillPoints)
	return err
}

// ListSkillNodes 取玩家技能节点等级。
func (d *DB) ListSkillNodes(ctx context.Context, uid int64) (map[int64]int64, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT node_id, level FROM skill_node WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var n, lv int64
		if err := rows.Scan(&n, &lv); err != nil {
			return nil, err
		}
		out[n] = lv
	}
	return out, rows.Err()
}

// ---- 快捷栏 container_slot ----

// Shortcut 快捷栏绑定。
type Shortcut struct {
	Slot int64
	GUID string
}

// ListShortcuts 取绑定（container=0）。
func (d *DB) ListShortcuts(ctx context.Context, uid int64) ([]Shortcut, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT slot, COALESCE(guid,'') FROM container_slot
		WHERE uid=? AND container=0 AND guid IS NOT NULL AND guid<>'' ORDER BY slot`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Shortcut
	for rows.Next() {
		var s Shortcut
		if err := rows.Scan(&s.Slot, &s.GUID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// BindShortcut 绑定/解绑（guid 空串=解绑）。
func (d *DB) BindShortcut(ctx context.Context, uid, slot int64, guid string) error {
	if guid == "" {
		_, err := d.SQL.ExecContext(ctx, `DELETE FROM container_slot WHERE uid=? AND container=0 AND slot=?`, uid, slot)
		return err
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO container_slot(uid, container, slot, guid) VALUES(?,0,?,?)
		ON CONFLICT (uid, container, slot) DO UPDATE SET guid=?`, uid, slot, guid, guid)
	return err
}

// ---- 鱼护 keepnet ----

// KeepnetEntry 鱼护条目。
type KeepnetEntry struct {
	EchoGUID  string  `json:"echo_guid"`
	UID       int64   `json:"uid"`
	Species   string  `json:"species"`
	NameZh    string  `json:"name_zh"`
	WeightG   int64   `json:"weight_g"`
	LengthM   float64 `json:"length_m"`
	Cat       int64   `json:"cat"`
	KeepLoc   string  `json:"keep_loc"`
	KeepSpot  int64   `json:"keep_spot"`
	KeepBait  string  `json:"keep_bait"`
	KeepLevel string  `json:"keep_level"`
	TS        float64 `json:"ts"`
}

// KeepnetList 全量条目（按时间升序）。
func (d *DB) KeepnetList(ctx context.Context, uid int64) ([]*KeepnetEntry, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT echo_guid, uid, COALESCE(species,''), COALESCE(name_zh,''), weight_g, length_m,
		       cat, COALESCE(keep_loc,''), COALESCE(keep_spot,0), COALESCE(keep_bait,''),
		       COALESCE(keep_level,''), COALESCE(ts,0)
		FROM keepnet_items WHERE uid=? ORDER BY ts`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*KeepnetEntry
	for rows.Next() {
		e := &KeepnetEntry{}
		if err := rows.Scan(&e.EchoGUID, &e.UID, &e.Species, &e.NameZh, &e.WeightG, &e.LengthM,
			&e.Cat, &e.KeepLoc, &e.KeepSpot, &e.KeepBait, &e.KeepLevel, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// KeepnetInsert 入护。
func (d *DB) KeepnetInsert(ctx context.Context, e *KeepnetEntry) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO keepnet_items(echo_guid, uid, species, name_zh, weight_g, length_m, cat,
		                          keep_loc, keep_spot, keep_bait, keep_level, ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (echo_guid) DO NOTHING`,
		e.EchoGUID, e.UID, e.Species, e.NameZh, e.WeightG, e.LengthM, e.Cat,
		e.KeepLoc, e.KeepSpot, e.KeepBait, e.KeepLevel, e.TS)
	return err
}

// KeepnetGet 按 echo_guid 取。
func (d *DB) KeepnetGet(ctx context.Context, uid int64, echoGUID string) (*KeepnetEntry, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT echo_guid, uid, COALESCE(species,''), COALESCE(name_zh,''), weight_g, length_m,
		       cat, COALESCE(keep_loc,''), COALESCE(keep_spot,0), COALESCE(keep_bait,''),
		       COALESCE(keep_level,''), COALESCE(ts,0)
		FROM keepnet_items WHERE uid=? AND echo_guid=?`, uid, echoGUID)
	e := &KeepnetEntry{}
	err := row.Scan(&e.EchoGUID, &e.UID, &e.Species, &e.NameZh, &e.WeightG, &e.LengthM,
		&e.Cat, &e.KeepLoc, &e.KeepSpot, &e.KeepBait, &e.KeepLevel, &e.TS)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// KeepnetDelete 卖鱼出护。
func (d *DB) KeepnetDelete(ctx context.Context, uid int64, echoGUIDs []string) error {
	if len(echoGUIDs) == 0 {
		return nil
	}
	q, args := placeholders(`DELETE FROM keepnet_items WHERE uid=? AND echo_guid IN (`, echoGUIDs, ")")
	_, err := d.SQL.ExecContext(ctx, q, append([]interface{}{uid}, args...)...)
	return err
}

// ---- 钓获日志 ----

// CatchLogInsert 写钓获记录（UNIQUE(source,fish_key,weight_g,loc,spot) 去重由 DB 承担）。
func (d *DB) CatchLogInsert(ctx context.Context, uid int64, loc string, spot int64, fishKey, nameZh string, weightG int64, lengthM float64, source, bait string, grade, xp, silver int64, locLevel string) error {
	// 保留亚秒精度，避免同秒事件排序退化；记录卡时间可精确到 100ns。
	ts := float64(time.Now().UnixNano()) / 1e9
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO catch_log(ts, loc, spot, fish_key, name_zh, weight_g, length_m, source,
		                      bait, grade, xp, silver, loc_level, uid)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (source, fish_key, weight_g, loc, spot) DO NOTHING`,
		ts, loc, spot, fishKey, nameZh, weightG, lengthM, source,
		bait, grade, xp, silver, locLevel, uid)
	return err
}

// CatchLogMaxWeight 玩家某鱼种历史最大重量。
func (d *DB) CatchLogMaxWeight(ctx context.Context, uid int64, fishKey string) (int64, error) {
	var mw int64
	err := d.SQL.QueryRowContext(ctx, `SELECT COALESCE(MAX(weight_g),0) FROM catch_log WHERE uid=? AND fish_key=?`,
		uid, fishKey).Scan(&mw)
	return mw, err
}

// CatchLogTotals 统计（3/2 用）：总钓获/奖杯/超稀有。
func (d *DB) CatchLogTotals(ctx context.Context, uid int64) (total, trophy, superN int64, err error) {
	err = d.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(CASE WHEN grade>=3 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN grade>=4 THEN 1 ELSE 0 END)
		FROM catch_log WHERE uid=? AND source='catch'`, uid).Scan(&total, &trophy, &superN)
	return
}

// ---- 地点买断 ----

// PlaceAccessList 玩家已买断地点。
func (d *DB) PlaceAccessList(ctx context.Context, uid int64) ([]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT place FROM place_access WHERE uid=? AND kind='buyout'`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PlaceAccessInsert 记录买断。
func (d *DB) PlaceAccessInsert(ctx context.Context, uid int64, place, kind string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?,?,?)
		ON CONFLICT (uid, place, kind) DO NOTHING`,
		uid, place, kind, float64(time.Now().Unix()))
	return err
}

// ---- 物品 CRUD ----

// InsertItem 新增物品行。
func (d *DB) InsertItem(ctx context.Context, it *Item) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO item(guid, uid, item_id, numeric_id, wire_class, type_id, container, slot,
		                 rig_guid, qty, buy_price, fields_json, created_ts, durability, workmanship)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.GUID, it.UID, nullIfEmpty(it.ItemID), it.NumericID, it.WireClass, it.TypeID, it.Container, it.Slot,
		nullIfEmpty(it.RigGUID), it.Qty, it.BuyPrice, it.FieldsJSON, it.CreatedTS, it.Durability, it.Workmanship)
	return err
}

// UpdateItemWire 改写实例的 wire 类、类型号、字段快照和数量。
func (d *DB) UpdateItemWire(ctx context.Context, guid, wireClass string, typeID int64, fieldsJSON string, qty int64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE item SET wire_class=?, type_id=?, fields_json=?, qty=?
		WHERE guid=?`, wireClass, typeID, fieldsJSON, qty, guid)
	return err
}

// UpdateItemFields 更新字段快照/数量/耐久/工艺/装配。
func (d *DB) UpdateItemFields(ctx context.Context, guid string, fieldsJSON string, qty int64, durability, workmanship float64, rigGUID string) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE item SET fields_json=?, qty=?, durability=?, workmanship=?, rig_guid=?
		WHERE guid=?`, fieldsJSON, qty, durability, workmanship, nullIfEmpty(rigGUID), guid)
	return err
}

// UpdateItemFieldsTx 在事务内更新字段快照、数量、耐久、工艺和装配。
func (d *DB) UpdateItemFieldsTx(ctx context.Context, tx *sql.Tx, guid string, fieldsJSON string, qty int64, durability, workmanship float64, rigGUID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE item SET fields_json=?, qty=?, durability=?, workmanship=?, rig_guid=?
		WHERE guid=?`, fieldsJSON, qty, durability, workmanship, nullIfEmpty(rigGUID), guid)
	return err
}

// DeleteItems 删除物品。
func (d *DB) DeleteItems(ctx context.Context, guid []string) error {
	if len(guid) == 0 {
		return nil
	}
	q, args := placeholders(`DELETE FROM item WHERE guid IN (`, guid, ")")
	_, err := d.SQL.ExecContext(ctx, q, args...)
	return err
}

// CountItemsByContainers 统计某 uid 在指定容器集合的物品数。
func (d *DB) CountItemsByContainers(ctx context.Context, uid int64, containers []string) (int64, error) {
	var n int64
	q, args := placeholders(`SELECT COUNT(*) FROM item WHERE uid=? AND container IN (`, containers, ")")
	err := d.SQL.QueryRowContext(ctx, q, append([]interface{}{uid}, args...)...).Scan(&n)
	return n, err
}

// ItemGUIDsByContainers 指定容器集合的 guid 列表（按 slot/created 排序）。
func (d *DB) ItemGUIDsByContainers(ctx context.Context, uid int64, containers []string) ([]string, error) {
	q, args := placeholders(`
		SELECT guid FROM item WHERE uid=? AND container IN (`, containers, ") ORDER BY slot, created_ts, guid")
	rows, err := d.SQL.QueryContext(ctx, q, append([]interface{}{uid}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// placeholders 生成 "?,?,?" 占位串（SQLite 顺序参数）。
func placeholders(prefix string, vals []string, suffix string) (string, []interface{}) {
	marks := make([]string, len(vals))
	args := make([]interface{}, len(vals))
	for i, v := range vals {
		marks[i] = "?"
		args[i] = v
	}
	return prefix + strings.Join(marks, ",") + suffix, args
}

// ---- 静态定义 ----

// ItemDef 物品定义行。
type ItemDef struct {
	NumericID int64
	ItemID    string
	Category  int64
	Kind      string
	Brand     string
	AttrsJSON string
}

// GetItemDef 按 numeric_id 取定义。
func (d *DB) GetItemDef(ctx context.Context, numericID int64) (*ItemDef, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE numeric_id=?`, numericID)
	def := &ItemDef{}
	if err := row.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind, &def.Brand, &def.AttrsJSON); err != nil {
		return nil, err
	}
	return def, nil
}

// GetItemDefByItemID 按 systemID 取定义。
func (d *DB) GetItemDefByItemID(ctx context.Context, itemID string) (*ItemDef, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT numeric_id, COALESCE(item_id,''), COALESCE(category,0), COALESCE(kind,''),
		       COALESCE(brand,''), COALESCE(attrs_json,'')
		FROM item_def WHERE item_id=?`, itemID)
	def := &ItemDef{}
	if err := row.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.Kind, &def.Brand, &def.AttrsJSON); err != nil {
		return nil, err
	}
	return def, nil
}

// GetItemByGUID 取单个物品行。
func (d *DB) GetItemByGUID(ctx context.Context, guid string) (*Item, error) {
	row := d.SQL.QueryRowContext(ctx, `SELECT `+itemCols+` FROM item WHERE guid=?`, guid)
	return scanItem(row)
}

// GetItemByGUIDTx 在事务内读取单个物品行。
func (d *DB) GetItemByGUIDTx(ctx context.Context, tx *sql.Tx, guid string) (*Item, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+itemCols+` FROM item WHERE guid=?`, guid)
	return scanItem(row)
}

// GetItemGUIDByRigSlot 查钓组槽位装的物品 guid（rig_component）。
func (d *DB) GetItemGUIDByRigSlot(ctx context.Context, rigGUID string, slot int64) (string, error) {
	var g string
	err := d.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(guid,'') FROM rig_component
		WHERE rig_guid=? AND slot=? ORDER BY ordinal LIMIT 1`, rigGUID, slot).Scan(&g)
	if err != nil {
		return "", err
	}
	return g, nil
}

// UpsertRig 幂等建档钓组。
func (d *DB) UpsertRig(ctx context.Context, guid string, uid, rigType int64, name, fieldsJSON string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO rig(guid, uid, rig_type, name, fields_json, created_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT (guid) DO NOTHING`,
		guid, uid, rigType, name, fieldsJSON, float64(time.Now().Unix()))
	return err
}

// UpsertRigComponent 幂等登记组件。
func (d *DB) UpsertRigComponent(ctx context.Context, rigGUID string, slot int64, guid string, extra int64) error {
	return d.UpsertRigComponentAuto(ctx, rigGUID, slot, guid, extra)
}

// ListRigComponents 取钓组组件（按 extra 升序）。
type RigComponent struct {
	Slot    int64
	Ordinal int64
	GUID    string
	Extra   int64
}

func (d *DB) ListRigComponents(ctx context.Context, rigGUID string) ([]*RigComponent, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT slot, ordinal, COALESCE(guid,''), extra FROM rig_component
		WHERE rig_guid=? ORDER BY extra, slot, ordinal, guid`,
		rigGUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RigComponent
	for rows.Next() {
		rc := &RigComponent{}
		if err := rows.Scan(&rc.Slot, &rc.Ordinal, &rc.GUID, &rc.Extra); err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, rows.Err()
}

// UpsertRodSetting 竿设置持久记录（quivertip/baitmount；17/5 双写之一）。
func (d *DB) UpsertRodSetting(ctx context.Context, rodGUID, key, value string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO rod_setting(guid, key, value, updated_ts) VALUES(?,?,?,?)
		ON CONFLICT (guid, key) DO UPDATE SET value=?, updated_ts=?`,
		rodGUID, key, value, float64(time.Now().Unix()), value, float64(time.Now().Unix()))
	return err
}

// ---- 商店/价目 ----

// ShopPrice 货架价（分）。
type ShopPrice struct {
	ItemID string
	Silver int64
	Gold   int64
	Stock  int64
}

// ShopItems 返回某店最终生效货架：优先精确门店数据，其次同 kind/品牌，
// 再使用同类代表货架，最后按 store_id 应用后台覆盖。未知店或缺少货架定义时
// 返回空货架，不使用全局价目伪造商品集合。
func (d *DB) ShopItems(ctx context.Context, storeID string) ([]*ShopPrice, error) {
	shelf, err := d.effectiveShopShelf(ctx, storeID)
	if err != nil {
		return nil, err
	}
	return shelf.Effective, nil
}

// ShopItemGet 单 SKU 的最终价格/库存；商品不在该店生效货架时返回 sql.ErrNoRows。
func (d *DB) ShopItemGet(ctx context.Context, storeID, itemID string) (*ShopPrice, error) {
	shelf, err := d.effectiveShopShelf(ctx, storeID)
	if err != nil {
		return nil, err
	}
	itemID = strings.TrimSpace(itemID)
	for _, p := range shelf.Effective {
		if p.ItemID == itemID {
			v := *p
			return &v, nil
		}
	}
	return nil, sql.ErrNoRows
}

// ItemPriceGet 全局价目兜底。
func (d *DB) ItemPriceGet(ctx context.Context, itemID string) (*ShopPrice, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT item_id, silver, gold, stock FROM item_price WHERE item_id=?`, itemID)
	p := &ShopPrice{}
	if err := row.Scan(&p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
		return nil, err
	}
	return p, nil
}

// ShopItemGetWithFallback 在“已知店铺”的货架上找不到商品时回退到全局价目。
//
// 客户端商店是按全局价目渲染的，有约 16% 的商品只存在于 item_price、
// 不在任何店铺货架里；直接拒绝会让客户端只收到通用失败。
// 未知店铺一律不回退，避免伪造店铺 ID 购买任意商品。
// 返回值 fellBack 表示是否走了全局价目，便于调用方记录诊断日志。
func (d *DB) ShopItemGetWithFallback(ctx context.Context, storeID, itemID string) (*ShopPrice, bool, error) {
	shelf, err := d.effectiveShopShelf(ctx, storeID)
	if err != nil {
		return nil, false, err
	}
	itemID = strings.TrimSpace(itemID)
	for _, p := range shelf.Effective {
		if p.ItemID == itemID {
			v := *p
			return &v, false, nil
		}
	}
	if !shelf.CatalogKnown {
		return nil, false, sql.ErrNoRows
	}
	p, err := d.ItemPriceGet(ctx, itemID)
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// ItemPriceFallbackList 价目兜底货架（非空价前 N 件）。
func (d *DB) ItemPriceFallbackList(ctx context.Context, limit int64) ([]*ShopPrice, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT item_id, silver, gold, stock FROM item_price
		WHERE silver>0 OR gold>0 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ShopPrice
	for rows.Next() {
		p := &ShopPrice{}
		if err := rows.Scan(&p.ItemID, &p.Silver, &p.Gold, &p.Stock); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- 被命名鱼 ----

// NamedFish 命名鱼行。
type NamedFish struct {
	FishID  string
	Species string
	Name    string
	Owner   string
	Place   string
	WeightG int64
	LengthM float64
	Grade   int64
	TS      float64
	Catches int64
}

func (d *DB) NamedFishGet(ctx context.Context, fishID string) (*NamedFish, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT fish_id, species, name, COALESCE(owner,''), COALESCE(place,''),
		       COALESCE(weight_g,0), COALESCE(length_m,0), COALESCE(grade,0), COALESCE(ts,0), catches
		FROM named_fish WHERE fish_id=?`, fishID)
	nf := &NamedFish{}
	if err := row.Scan(&nf.FishID, &nf.Species, &nf.Name, &nf.Owner, &nf.Place,
		&nf.WeightG, &nf.LengthM, &nf.Grade, &nf.TS, &nf.Catches); err != nil {
		return nil, err
	}
	return nf, nil
}

// NamedFishInsert 首次命名。并发下只有第一个 INSERT 成功；冲突必须返回错误，
// 不能把 ON CONFLICT DO NOTHING 的 0 行影响误报成命名成功。
func (d *DB) NamedFishInsert(ctx context.Context, nf *NamedFish) error {
	res, err := d.SQL.ExecContext(ctx, `
		INSERT INTO named_fish(fish_id, species, name, owner, place, weight_g, length_m, grade, ts, catches)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (fish_id) DO NOTHING`,
		nf.FishID, nf.Species, nf.Name, nf.Owner, nf.Place, nf.WeightG, nf.LengthM, nf.Grade, nf.TS, nf.Catches)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("named fish %s already exists", nf.FishID)
	}
	return nil
}

// ---- 运行参数 / KV / 队列（跨进程共享）----

// TunableGet / TunableSet 运行参数（管理后台热更）。
func (d *DB) TunableGet(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.SQL.QueryRowContext(ctx, `SELECT value FROM tunables WHERE key=?`, key).Scan(&v)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// TunableSet 写参数。
func (d *DB) TunableSet(ctx context.Context, key, value, updatedBy string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO tunables(key, value, updated_by, updated_ts) VALUES(?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET value=?, updated_by=?, updated_ts=?`,
		key, value, updatedBy, float64(time.Now().Unix()),
		value, updatedBy, float64(time.Now().Unix()))
	return err
}

// TunableList 全量参数。
func (d *DB) TunableList(ctx context.Context) (map[string]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT key, value FROM tunables ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// KVGet / KVSet 共享键值（在线会话统计等）。
func (d *DB) KVGet(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.SQL.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, key).Scan(&v)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// KVSet 写共享键值。
func (d *DB) KVSet(ctx context.Context, key, value string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO kv(k, v) VALUES(?,?) ON CONFLICT (k) DO UPDATE SET v=?`,
		key, value, value)
	return err
}

// QueuePush 管理指令入队（admin 进程投递）。
func (d *DB) QueuePush(ctx context.Context, payload string) error {
	_, err := d.SQL.ExecContext(ctx,
		`INSERT INTO admin_queue(payload, created_ts) VALUES(?,?)`,
		payload, float64(time.Now().Unix()))
	return err
}

// QueuePop 取最旧一条指令并删除（gameserver 轮询消费）；空队列返回 ok=false。
func (d *DB) QueuePop(ctx context.Context) (string, bool, error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	var payload string
	err = tx.QueryRowContext(ctx,
		`SELECT id, payload FROM admin_queue ORDER BY id LIMIT 1`).Scan(&id, &payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_queue WHERE id=?`, id); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return payload, true, nil
}

var _ = fmt.Sprintf

// RemoveRigComponent 移除钓组某槽位组件行。
func (d *DB) RemoveRigComponent(ctx context.Context, rigGUID string, slot int64) error {
	return d.DeleteRigComponentsBySlot(ctx, rigGUID, slot)
}

// UpdateItemContainer 改物品容器归属（backpack/rig/keepnet）。
func (d *DB) UpdateItemContainer(ctx context.Context, guid, container string) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET container=? WHERE guid=?`, container, guid)
	return err
}

// ---- 钓组装配 ----

// GetRigType 读 rig 表行的 rig_type（行不存在返回 (0,false)）。
func (d *DB) GetRigType(ctx context.Context, guid string) (int64, bool, error) {
	var rt int64
	err := d.SQL.QueryRowContext(ctx,
		`SELECT rig_type FROM rig WHERE guid=?`, guid).Scan(&rt)
	if err != nil {
		return 0, false, err
	}
	return rt, true, nil
}

// UpdateRigType 更新 rig 表的钓组类型；行不存在时无操作，由 UpsertRig 建档。
func (d *DB) UpdateRigType(ctx context.Context, guid string, rigType int64) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE rig SET rig_type=? WHERE guid=?`, rigType, guid)
	return err
}

// SetItemRigLink 将物品挂到钓组，一次写入 rig_guid、slot 和 container。
// rig_component 行由 UpsertRigComponent 单独维护。
func (d *DB) SetItemRigLink(ctx context.Context, guid, rigGUID string, slot int64) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET rig_guid=?, slot=?, container='rig' WHERE guid=?`, rigGUID, slot, guid)
	return err
}

// SetItemBackpack 将物品退回背包：清空 rig_guid，并将 container 设为 backpack。
// 物品字段中的父引用由调用方另行清理。
func (d *DB) SetItemBackpack(ctx context.Context, guid string) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET rig_guid=NULL, container='backpack' WHERE guid=?`, guid)
	return err
}
