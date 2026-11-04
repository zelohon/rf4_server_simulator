package store

import (
	"context"
	"database/sql"
)

// 竿组拆卸的存储入口。
//
// 件装子实例直接删除；普通物品则清空 rig_guid 并退回背包。两条路径都集中
// 在这里，确保物品不会继续被标记为已装配而无法在背包中使用。

// DetachReturnToBackpack 将物品退回背包：清空 rig_guid，container 设为
// backpack，slot 置 0。该操作幂等，行不存在时返回 nil。
func (d *DB) DetachReturnToBackpack(ctx context.Context, guid string) error {
	if guid == "" {
		return nil
	}
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET rig_guid=NULL, container='backpack', slot=0 WHERE guid=?`, guid)
	return err
}

// DetachReturnToBackpackTx 是 DetachReturnToBackpack 的事务内版本，
// 供需要与钓组摘除同事务提交的调用方使用。
func (d *DB) DetachReturnToBackpackTx(ctx context.Context, tx *sql.Tx, guid string) error {
	if guid == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE item SET rig_guid=NULL, container='backpack', slot=0 WHERE guid=?`, guid)
	return err
}

// DetachRestoreSourceStack 更新来源堆的数量。调用方负责同步物品字段中的
// 数量快照。
func (d *DB) DetachRestoreSourceStack(ctx context.Context, guid string, count int64) error {
	if guid == "" {
		return nil
	}
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE item SET qty=? WHERE guid=?`, count, guid)
	return err
}
