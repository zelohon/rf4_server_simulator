package store

import (
	"context"
	"database/sql"
)

// DeleteKeepnetTx 同时移除护网条目及同一玩家的导入快照。
func (d *DB) DeleteKeepnetTx(ctx context.Context, tx *sql.Tx, uid int64, guid string) (bool, error) {
	result, err := tx.ExecContext(ctx, `DELETE FROM keepnet_items WHERE uid=? AND echo_guid=?`, uid, guid)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM item WHERE uid=? AND guid=? AND container='keepnet' AND type_id=25`, uid, guid)
	return err == nil, err
}

// RemoveOwnedItems 让放生和普通物品删除在同一事务中保留玩家边界。
func (d *DB) RemoveOwnedItems(ctx context.Context, uid int64, guids []string) (removed, fish []string, err error) {
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	seen := make(map[string]bool, len(guids))
	for _, guid := range guids {
		if seen[guid] {
			continue
		}
		seen[guid] = true
		kept, err := d.DeleteKeepnetTx(ctx, tx, uid, guid)
		if err != nil {
			return nil, nil, err
		}
		if kept {
			removed = append(removed, guid)
			fish = append(fish, guid)
			continue
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM item WHERE uid=? AND guid=?`, uid, guid)
		if err != nil {
			return nil, nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, nil, err
		}
		if count != 0 {
			removed = append(removed, guid)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return removed, fish, nil
}
