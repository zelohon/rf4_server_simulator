package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/proto"
)

// TestBuildProfile307Membership 验证 f10/f11 由「到期时间」驱动。
//
// 这是顶栏显示「已激活 · 剩余 N 天」的唯一数据来源：
//   f10 = 会员标志（0/1），f11 = 到期时间（FILETIME）
// 过期后必须回落到 0/0，否则客户端会一直显示已激活。
func TestBuildProfile307Membership(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000010)

	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	mk := func(membership, expireAt int64) {
		t.Helper()
		if _, err := db.SQL.ExecContext(ctx,
			`UPDATE profile SET membership=?, membership_exp=?, stars=stars WHERE uid=?`,
			membership, expireAt, uid); err != nil {
			t.Fatal(err)
		}
	}
	flags := func() (uint32, uint64) {
		t.Helper()
		obj, err := srv.BuildProfile307(ctx, uid)
		if err != nil {
			t.Fatalf("BuildProfile307: %v", err)
		}
		return u32Of(obj.Field(10).Num), u64Of(obj.Field(11).Num)
	}

	exp := time.Now().Add(365 * 24 * time.Hour).Unix()

	// 1) 有效会员 → f10=1，f11=到期时间的 FILETIME
	mk(1, exp)
	f10, f11 := flags()
	if f10 != 1 {
		t.Fatalf("有效会员 f10=%d, want 1", f10)
	}
	if want := uint64(proto.UnixToFiletime(exp)); f11 != want {
		t.Fatalf("f11=%d, want %d（到期时间的 FILETIME）", f11, want)
	}

	// 2) 已过期 → f10=0、f11=0
	mk(1, time.Now().Add(-time.Hour).Unix())
	f10, f11 = flags()
	if f10 != 0 || f11 != 0 {
		t.Fatalf("过期会员应回落为 0/0，got f10=%d f11=%d", f10, f11)
	}

	// 3) 计时会员但从未写到期时间（exp=0）→ 视为未激活
	mk(1, 0)
	f10, f11 = flags()
	if f10 != 0 || f11 != 0 {
		t.Fatalf("expire=0 应视为未激活，got f10=%d f11=%d", f10, f11)
	}

	// 4) 标志为 0 → f10=0
	mk(0, exp)
	f10, _ = flags()
	if f10 != 0 {
		t.Fatalf("membership=0 时 f10=%d, want 0", f10)
	}

	// 5) 永久会员 → f10=2，f11=0（不写到到期时间）
	mk(2, 0)
	f10, f11 = flags()
	if f10 != 2 || f11 != 0 {
		t.Fatalf("永久会员应 f10=2/f11=0，got f10=%d f11=%d", f10, f11)
	}
}

// TestGrantMembershipExtends 验证续期是「顺延」而不是「覆盖」。
func TestGrantMembershipExtends(t *testing.T) {
	_, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000011)

	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	const day = int64(86400)
	start := time.Now().Unix()

	exp1, err := db.GrantMembership(ctx, uid, 30)
	if err != nil {
		t.Fatalf("首次开通: %v", err)
	}
	if d := exp1 - start; d < 29*day || d > 31*day {
		t.Fatalf("首次开通后到期时间偏移 %d 秒，不在 30 天附近", d)
	}

	exp2, err := db.GrantMembership(ctx, uid, 30)
	if err != nil {
		t.Fatalf("续期: %v", err)
	}
	if d := exp2 - exp1; d < 30*day-5 || d > 30*day+5 {
		t.Fatalf("续期应在原到期日上顺延 30 天，实际差 %d 秒", d)
	}

	// 永久：days<=0 → kind=2，到期时间写 0
	exp3, err := db.GrantMembership(ctx, uid, 0)
	if err != nil {
		t.Fatalf("永久开通: %v", err)
	}
	if exp3 != 0 {
		t.Fatalf("永久会员到期时间应为 0，实得 %d", exp3)
	}
	p, err := db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Membership != 2 || p.MembershipExp != 0 {
		t.Fatalf("永久会员应 kind=2/exp=0，got kind=%d exp=%d", p.Membership, p.MembershipExp)
	}

	// 取消
	if err := db.RevokeMembership(ctx, uid); err != nil {
		t.Fatalf("取消会员: %v", err)
	}
	p, err = db.GetProfile(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Membership != 0 || p.MembershipExp != 0 {
		t.Fatalf("取消后应归零，got membership=%d expire=%d",
			p.Membership, p.MembershipExp)
	}
}

// TestGrantMembershipUnknownUID 对不存在的 uid 应返回错误而不是静默成功。
func TestGrantMembershipUnknownUID(t *testing.T) {
	_, db := newProgressSrv(t)
	if _, err := db.GrantMembership(context.Background(), 99999999, 30); err == nil {
		t.Fatal("不存在的 uid 应返回错误")
	}
}
