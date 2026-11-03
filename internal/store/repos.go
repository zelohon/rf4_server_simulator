package store

import (
	"context"
	"database/sql"
	"time"
)

// Profile 玩家档案（profile 表行）。
type Profile struct {
	UID        int64
	Name       string
	Level      int64
	XP         int64
	Silver     int64
	Gold       int64
	Region     string
	Membership int64
	// MembershipExpireAt 会员到期时间（unix 秒，0 = 从未开通）。
	// 会员是否有效以它为准，不看 Membership 标志——否则过期后仍显示已激活。
	MembershipExpireAt int64
	Stars              int64
	Bank       int64
	Email      string
	Role       int64
	Banned     bool
	CreatedTS  float64
	UpdatedTS  float64
}

// EnsureProfile 无档案时创建默认档案。默认昵称为 "player"；不要生成
// player_<uid>，客户端首次连接依赖这个固定名称。
func (d *DB) EnsureProfile(ctx context.Context, uid int64) (*Profile, error) {
	now := float64(time.Now().Unix())
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO profile(uid, name, created_ts, updated_ts)
		VALUES(?, ?, ?, ?) ON CONFLICT (uid) DO NOTHING`,
		uid, "player", now, now)
	if err != nil {
		return nil, err
	}
	return d.GetProfile(ctx, uid)
}

// GetProfile 取档案。
func (d *DB) GetProfile(ctx context.Context, uid int64) (*Profile, error) {
	row := d.SQL.QueryRowContext(ctx, `
		SELECT uid, name, level, xp, silver, gold, COALESCE(region,'CN'),
		       membership, COALESCE(membership_expire_at,0), stars, bank,
		       COALESCE(email,''), role,
		       COALESCE(banned,0), COALESCE(created_ts,0), COALESCE(updated_ts,0)
		FROM profile WHERE uid=?`, uid)
	var p Profile
	var banned int64
	err := row.Scan(&p.UID, &p.Name, &p.Level, &p.XP, &p.Silver, &p.Gold,
		&p.Region, &p.Membership, &p.MembershipExpireAt, &p.Stars, &p.Bank,
		&p.Email, &p.Role,
		&banned, &p.CreatedTS, &p.UpdatedTS)
	if err != nil {
		return nil, err
	}
	p.Banned = banned != 0
	return &p, nil
}

// UpdateBalance 更新银币/金币/银行（全量覆盖，调用方负责读改写）。
func (d *DB) UpdateBalance(ctx context.Context, uid, silver, gold, bank int64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE profile SET silver=?, gold=?, bank=?, updated_ts=? WHERE uid=?`,
		silver, gold, bank, float64(time.Now().Unix()), uid)
	return err
}

// AddXP 增加经验（可能升级的处理在业务层）。
func (d *DB) AddXP(ctx context.Context, uid, delta int64) error {
	_, err := d.SQL.ExecContext(ctx, `
		UPDATE profile SET xp = xp + ?, updated_ts=? WHERE uid=?`,
		delta, float64(time.Now().Unix()), uid)
	return err
}

// AddTxn 记一条货币流水。
func (d *DB) AddTxn(ctx context.Context, uid int64, kind, itemID, guid string, silverDelta, goldDelta int64, note string) error {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO txn(uid, ts, kind, item_id, guid, silver_delta, gold_delta, note)
		VALUES(?,?,?,?,?,?,?,?)`,
		uid, float64(time.Now().Unix()), kind, nullIfEmpty(itemID), nullIfEmpty(guid), silverDelta, goldDelta, note)
	return err
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// Item 背包物品实例（item 表行；fields_json 保真重建报文）。
type Item struct {
	GUID        string
	UID         int64
	ItemID      string
	NumericID   int64
	WireClass   string
	TypeID      int64
	Container   string
	Slot        int64
	RigGUID     string
	Qty         int64
	BuyPrice    int64
	FieldsJSON  string
	Durability  float64
	Workmanship float64
	CreatedTS   float64
}

// itemCols 物品查询的公共列清单（Scan 顺序）。
const itemCols = `
		guid, uid, COALESCE(item_id,''), COALESCE(numeric_id,0), COALESCE(wire_class,''),
		COALESCE(type_id,0), container, slot, COALESCE(rig_guid,''), qty, buy_price,
		COALESCE(fields_json,''), durability, workmanship, COALESCE(created_ts,0)`

func scanItem(row *sql.Row) (*Item, error) {
	it := &Item{}
	err := row.Scan(&it.GUID, &it.UID, &it.ItemID, &it.NumericID, &it.WireClass,
		&it.TypeID, &it.Container, &it.Slot, &it.RigGUID, &it.Qty, &it.BuyPrice,
		&it.FieldsJSON, &it.Durability, &it.Workmanship, &it.CreatedTS)
	if err != nil {
		return nil, err
	}
	return it, nil
}

// ListItemsByContainer 取某容器全部物品。
func (d *DB) ListItemsByContainer(ctx context.Context, uid int64, container string) ([]*Item, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT `+itemCols+` FROM item WHERE uid=? AND container=? ORDER BY slot, created_ts`, uid, container)
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

func intToStr(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
