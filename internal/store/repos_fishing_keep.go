package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrKeepnetFull = errors.New("护网已满")

// KeepFishingCatch 护网、钓获记录和待处理鱼一起提交，失败时全部保留原状。
func (d *DB) KeepFishingCatch(ctx context.Context, e *KeepnetEntry, xp, silver int64) error {
	_, err := d.KeepFishingCatchWithCapacity(ctx, e, xp, silver, -1)
	return err
}

// 已处理后移除的鱼只消费残留 pending，不会再次入护。
func (d *DB) KeepFishingCatchWithCapacity(ctx context.Context, e *KeepnetEntry, xp, silver, capacity int64) (bool, error) {
	if e == nil || e.UID == 0 || e.EchoGUID == "" {
		return false, fmt.Errorf("入护缺少玩家或鱼标识")
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var owner int64
	err = tx.QueryRowContext(ctx, `SELECT uid FROM keepnet_items WHERE echo_guid=?`, e.EchoGUID).Scan(&owner)
	if err == nil && owner != e.UID {
		return false, fmt.Errorf("鱼标识已属于其他玩家")
	}
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	kept := err == nil
	if err == sql.ErrNoRows {
		var consumed bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM catch_log WHERE uid=? AND source='keep' AND catch_guid=?
			UNION ALL SELECT 1 FROM kv WHERE k=?)`, e.UID, e.EchoGUID,
			fmt.Sprintf("catch-release:%d:%s", e.UID, strings.ToLower(e.EchoGUID))).Scan(&consumed)
		if err != nil {
			return false, err
		}
		if !consumed && capacity >= 0 {
			var used int64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM keepnet_items WHERE uid=?`, e.UID).Scan(&used); err != nil {
				return false, err
			}
			if used >= capacity {
				return false, ErrKeepnetFull
			}
		}
		if !consumed {
			_, err = tx.ExecContext(ctx, `INSERT INTO keepnet_items(
			echo_guid,uid,species,name_zh,weight_g,length_m,cat,keep_loc,keep_spot,keep_bait,keep_level,ts)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				e.EchoGUID, e.UID, e.Species, e.NameZh, e.WeightG, e.LengthM, e.Cat,
				e.KeepLoc, e.KeepSpot, e.KeepBait, e.KeepLevel, e.TS)
			if err != nil {
				return false, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO catch_log(
			ts,loc,spot,fish_key,name_zh,weight_g,length_m,source,bait,grade,xp,silver,loc_level,uid,catch_guid)
			VALUES(?,?,?,?,?,?,?,'keep',?,?,?,?,?,?,?)
			ON CONFLICT(uid,source,catch_guid) WHERE catch_guid IS NOT NULL DO NOTHING`,
				e.TS, e.KeepLoc, e.KeepSpot, e.Species, e.NameZh, e.WeightG, e.LengthM,
				e.KeepBait, e.Cat, xp, silver, e.KeepLevel, e.UID, e.EchoGUID)
			if err != nil {
				return false, err
			}
			kept = true
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE fishing_state SET pending_json=NULL,
		cast_active=0,hooked=0,resolved=1 WHERE uid=? AND json_extract(pending_json,'$.fish_guid')=?`,
		e.UID, e.EchoGUID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return kept, nil
}
