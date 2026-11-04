package store

import (
	"context"
	"fmt"
	"sort"
)

// StaticTables 是运行时只读的静态资料表。seed 只允许替换这些表。
var StaticTables = []string{
	"item_def", "item_catalog", "item_price", "recipe", "skill_def", "skill_cat_def",
	"spot_def", "shop_item", "species_weight", "species_grade", "loc_fish", "loc_alias",
	"loc_bait", "loc_bait_fish", "loc_wiki_fish", "spot_fish", "fish_bait_pref",
	"sonar_profile", "map_fish_roster", "companion_bait_fish", "companion_fish_meta",
}

// PlayerTables 是随玩家操作变化的可写表。place_access 必须在这里登记，
// 否则玩家库拆分/备份会漏掉地图买断状态。
var PlayerTables = []string{
	"profile", "item", "container_slot", "owned_items", "rig", "rig_component",
	"rod_pod_setting", "rod_pod_alarm", "rod_setting", "txn", "mail", "keepnet_items",
	"skill", "skill_node", "player_counters", "player_achievements", "device",
	"catch_log", "competition", "competition_member", "competition_result", "named_fish", "place_access",
	"fishing_state", "item_damage_log", "membership_order", "reel_performance",
	"player_condition", "campfire_state",
}

// RuntimeTables 是运营/运行时/迁移元数据表，不属于静态资料或玩家存档。
var RuntimeTables = []string{
	"tunables", "admin_user", "audit_log", "gift_pack", "gw_token", "gw_handoff",
	"gateway_account",
	"happy_hour_config",
	"kv", "admin_queue", "achievement_def", "static_dataset_meta", "schema_migrations",
	"request_receipts", "market_offer", "shop_item_override",
	"fish_weight_override", "fish_bite_setting", "fish_grade_lines",
}

// TableOwnership 返回每张用户表的唯一归属。静态、玩家、运行时三组必须互不相交。
func TableOwnership() map[string]string {
	out := make(map[string]string, len(StaticTables)+len(PlayerTables)+len(RuntimeTables))
	for _, names := range []struct {
		kind   string
		tables []string
	}{
		{kind: "static", tables: StaticTables},
		{kind: "player", tables: PlayerTables},
		{kind: "runtime", tables: RuntimeTables},
	} {
		for _, name := range names.tables {
			if prev, ok := out[name]; ok {
				panic(fmt.Sprintf("table %s 同时登记为 %s 和 %s", name, prev, names.kind))
			}
			out[name] = names.kind
		}
	}
	return out
}

// ValidateTableOwnership 校验迁移后的实际表集合与代码声明完全一致。
// 新增表必须显式登记，避免 split/backup 工具静默漏表。
func ValidateTableOwnership(ctx context.Context, db *DB) error {
	if db == nil {
		return fmt.Errorf("数据库未连接")
	}
	want := TableOwnership()
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("列出用户表: %w", err)
	}
	defer rows.Close()
	have := make(map[string]bool, len(want))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("读取表名: %w", err)
		}
		have[name] = true
		if _, ok := want[name]; !ok {
			return fmt.Errorf("发现未登记归属的表 %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历用户表: %w", err)
	}
	missing := make([]string, 0)
	for name := range want {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("缺少归属表 %v", missing)
	}
	return nil
}

// SeedStarterBundle 在一个 IMMEDIATE 事务中写入完整 starter 包和快捷栏。
// 任一物品或快捷栏失败都会整体回滚，不会留下半个背包。
func (d *DB) SeedStarterBundle(ctx context.Context, uid int64, items []*Item, shortcuts map[int64]string) (bool, error) {
	if len(items) == 0 {
		return false, fmt.Errorf("starter 物品为空")
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it == nil {
			return false, fmt.Errorf("starter 含空物品")
		}
		if it.UID != uid {
			return false, fmt.Errorf("starter 物品 %s uid=%d，期望 %d", it.GUID, it.UID, uid)
		}
		if it.GUID == "" || seen[it.GUID] {
			return false, fmt.Errorf("starter 物品 guid 为空或重复: %q", it.GUID)
		}
		seen[it.GUID] = true
	}
	slots := make([]int64, 0, len(shortcuts))
	for slot, guid := range shortcuts {
		if !seen[guid] {
			return false, fmt.Errorf("快捷栏槽%d 指向非 starter 物品 %s", slot, guid)
		}
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("获取 starter 事务连接: %w", err)
	}
	defer conn.Close()
	inTx := false
	committed := false
	defer func() {
		if inTx && !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return false, fmt.Errorf("开始 starter 事务: %w", err)
	}
	inTx = true

	var n int64
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM item
		WHERE uid=? AND container IN ('backpack','rig','keepnet')`, uid).Scan(&n); err != nil {
		return false, fmt.Errorf("检查 starter 是否已播种: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	for _, it := range items {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO item(guid, uid, item_id, numeric_id, wire_class, type_id, container, slot,
			                 rig_guid, qty, buy_price, fields_json, created_ts, durability, workmanship)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			it.GUID, it.UID, nullIfEmpty(it.ItemID), it.NumericID, it.WireClass, it.TypeID,
			it.Container, it.Slot, nullIfEmpty(it.RigGUID), it.Qty, it.BuyPrice, it.FieldsJSON,
			it.CreatedTS, it.Durability, it.Workmanship); err != nil {
			return false, fmt.Errorf("写入 starter 物品 %s: %w", it.GUID, err)
		}
	}
	for _, slot := range slots {
		guid := shortcuts[slot]
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO container_slot(uid, container, slot, guid) VALUES(?,0,?,?)
			ON CONFLICT(uid, container, slot) DO UPDATE SET guid=excluded.guid`,
			uid, slot, guid); err != nil {
			return false, fmt.Errorf("写入 starter 快捷栏槽%d: %w", slot, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return false, fmt.Errorf("提交 starter 事务: %w", err)
	}
	committed = true
	return true, nil
}
