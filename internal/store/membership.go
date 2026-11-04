package store

import (
	"context"
	"database/sql"
	"time"
)

// 会员状态模型（与上游一致）：
//
//	membership=0 非会员 / 1 计时会员（membership_exp 为到期 unix 秒）/ 2 永久会员（exp=0）
//
// 是否有效统一用 MembershipAt 判断，别处不要再写 `Membership == 1`，
// 否则过期会员会被当成有效、永久会员会被当成无效。

// GrantMembership 开通或续期会员，返回写入后的到期时间（unix 秒；0 表示永久）。
//
// days <= 0 视为永久会员。续期从「原到期时间」顺延而不是从现在算起，
// 这样连续开通两次 30 天会得到 60 天，而不是被覆盖成 30 天。
func (d *DB) GrantMembership(ctx context.Context, uid int64, days int) (int64, error) {
	now := time.Now()
	p, err := d.GetProfile(ctx, uid)
	if err != nil {
		return 0, err
	}
	// 已是永久会员：不需要再改，直接返回。
	if p.Membership == 2 {
		return 0, nil
	}
	if days <= 0 {
		res, err := d.SQL.ExecContext(ctx, `
			UPDATE profile SET membership=2, membership_exp=0, updated_ts=?
			WHERE uid=?`, float64(now.Unix()), uid)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, sql.ErrNoRows
		}
		return 0, nil
	}
	base := now
	if p.Membership == 1 && p.MembershipExp > now.Unix() {
		base = time.Unix(p.MembershipExp, 0)
	}
	exp := base.Add(time.Duration(days) * 24 * time.Hour).Unix()
	res, err := d.SQL.ExecContext(ctx, `
		UPDATE profile SET membership=1, membership_exp=?, updated_ts=?
		WHERE uid=?`, exp, float64(now.Unix()), uid)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, sql.ErrNoRows
	}
	return exp, nil
}

// RevokeMembership 取消会员（计时与永久一并清除）。
func (d *DB) RevokeMembership(ctx context.Context, uid int64) error {
	res, err := d.SQL.ExecContext(ctx, `
		UPDATE profile SET membership=0, membership_exp=0, updated_ts=?
		WHERE uid=?`, float64(time.Now().Unix()), uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MembershipActive 判断会员当前是否有效（计时未过期或永久）。
//
// 唯一判定入口：别处不要再自行比较 membership 标志。
func (p *Profile) MembershipActive(now time.Time) bool {
	return MembershipAt(p.Membership, p.MembershipExp, now.Unix()).Active
}
