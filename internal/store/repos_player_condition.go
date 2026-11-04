package store

import (
	"context"
	"time"
)

const (
	DefaultPlayerConditionPrimary   = 0.85
	DefaultPlayerConditionSecondary = 0.9946
)

type PlayerCondition struct {
	UID       int64
	Primary   float64
	Secondary float64
	UpdatedTS float64
}

func (d *DB) EnsurePlayerCondition(ctx context.Context, uid int64) (*PlayerCondition, error) {
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(?,?,?,?) ON CONFLICT(uid) DO NOTHING`,
		uid, DefaultPlayerConditionPrimary, DefaultPlayerConditionSecondary, float64(time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	var v PlayerCondition
	err = d.SQL.QueryRowContext(ctx, `
		SELECT uid, primary_value, secondary_value, updated_ts
		FROM player_condition WHERE uid=?`, uid).Scan(&v.UID, &v.Primary, &v.Secondary, &v.UpdatedTS)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (d *DB) UpsertPlayerCondition(ctx context.Context, v PlayerCondition) error {
	if v.Primary < 0 {
		v.Primary = 0
	}
	if v.Secondary < 0 {
		v.Secondary = 0
	}
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO player_condition(uid, primary_value, secondary_value, updated_ts)
		VALUES(?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET
			primary_value=excluded.primary_value,
			secondary_value=excluded.secondary_value,
			updated_ts=excluded.updated_ts`, v.UID, v.Primary, v.Secondary, v.UpdatedTS)
	return err
}
