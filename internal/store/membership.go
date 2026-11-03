package store

import (
	"context"
	"database/sql"
	"time"
)

// MembershipForeverExpireAt 是「长期会员」的到期时间（2100-01-01 00:00:00 UTC）。
const MembershipForeverExpireAt int64 = 4102444800

// GrantMembership 开通或续期会员，返回写入后的到期时间（unix 秒）。
//
// days <= 0 视为长期。续期从「原到期时间」顺延而不是从现在算起，
// 这样连续开通两次 30 天会得到 60 天，而不是被覆盖成 30 天。
func (d *DB) GrantMembership(ctx context.Context, uid int64, days int) (int64, error) {
	now := time.Now()
	var exp int64
	if days <= 0 {
		exp = MembershipForeverExpireAt
	} else {
		base := now
		if p, err := d.GetProfile(ctx, uid); err == nil && p.MembershipExpireAt > now.Unix() {
			base = time.Unix(p.MembershipExpireAt, 0)
		}
		exp = base.Add(time.Duration(days) * 24 * time.Hour).Unix()
	}
	res, err := d.SQL.ExecContext(ctx, `
		UPDATE profile SET membership=1, membership_expire_at=?, updated_ts=?
		WHERE uid=?`, exp, float64(now.Unix()), uid)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, sql.ErrNoRows
	}
	return exp, nil
}

// RevokeMembership 取消会员。
func (d *DB) RevokeMembership(ctx context.Context, uid int64) error {
	res, err := d.SQL.ExecContext(ctx, `
		UPDATE profile SET membership=0, membership_expire_at=0, updated_ts=?
		WHERE uid=?`, float64(time.Now().Unix()), uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MembershipActive 判断会员当前是否有效。
//
// 唯一判定入口：别处不要再写 `Membership == 1` 作为条件，
// 否则过期会员会被当成有效。
func (p *Profile) MembershipActive(now time.Time) bool {
	return p.Membership == 1 && p.MembershipExpireAt > now.Unix()
}
