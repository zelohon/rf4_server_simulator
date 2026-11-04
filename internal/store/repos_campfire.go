package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CampfireState 是玩家最近一次点燃的篝火。篝火按账号保存，重连后仍能恢复到期时间。
type CampfireState struct {
	UID         int64
	Loc         string
	Spot        int64
	DurationMin int64
	ExpiresFT   int64
	UpdatedTS   float64
}

func (d *DB) GetCampfireState(ctx context.Context, uid int64) (*CampfireState, error) {
	var v CampfireState
	err := d.SQL.QueryRowContext(ctx, `
		SELECT uid, loc, spot, duration_min, expires_ft, updated_ts
		FROM campfire_state WHERE uid=?`, uid).Scan(
		&v.UID, &v.Loc, &v.Spot, &v.DurationMin, &v.ExpiresFT, &v.UpdatedTS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) UpsertCampfireState(ctx context.Context, v CampfireState) error {
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO campfire_state(uid, loc, spot, duration_min, expires_ft, updated_ts)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET
			loc=excluded.loc,
			spot=excluded.spot,
			duration_min=excluded.duration_min,
			expires_ft=excluded.expires_ft,
			updated_ts=excluded.updated_ts`,
		v.UID, v.Loc, v.Spot, v.DurationMin, v.ExpiresFT, v.UpdatedTS)
	return err
}
