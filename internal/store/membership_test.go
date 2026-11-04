package store

import (
	"context"
	"errors"
	"testing"
)

func membershipCode(err error) string {
	var e *MembershipError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestMembershipMigrationPreservesLegacyAndRollsBack(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 19)
	if _, err := db.SQL.Exec(`INSERT INTO profile(uid,name,xp,gold,membership) VALUES(1,'Old',123,500,1),(2,'Permanent',456,600,2);
        CREATE TRIGGER fail_membership_migration BEFORE INSERT ON schema_migrations
        WHEN NEW.name='migrations/00020_membership.sql' BEGIN SELECT RAISE(ABORT,'migration unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("迁移回执失败应回滚")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('profile') WHERE name='membership_exp'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name='membership_order'`, 0)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_membership_migration`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	first, err := db.GetProfile(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "Old" || first.XP != 123 || first.Gold != 500 || first.Membership != 1 || first.MembershipExp != 0 {
		t.Fatalf("旧档损坏: %+v", first)
	}
	status, err := db.GetMembershipStatus(context.Background(), 1, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	if status.Membership.Active {
		t.Fatal("不能为缺失到期时间的旧临时会员反复伪造有效期")
	}
	status, err = db.GetMembershipStatus(context.Background(), 2, 1700000000)
	if err != nil || !status.Membership.Permanent || !status.Membership.Active {
		t.Fatalf("永久会员失效: %+v %v", status, err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET membership_exp=-1 WHERE uid=1`); err == nil {
		t.Fatal("允许负到期时间")
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET membership_exp=NULL WHERE uid=1`); err == nil {
		t.Fatal("允许空到期时间")
	}
}

func TestMembershipPurchaseRenewalExpiryAndReplay(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const now = int64(1700000000)
	if _, err := db.EnsureProfile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=2000 WHERE uid=42`); err != nil {
		t.Fatal(err)
	}
	first, err := db.PurchaseMembership(ctx, 42, "premium_1d", "first-request", now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.ChargedGold != 100 || first.ExpiresAt != now+86400 {
		t.Fatalf("首购不符: %+v", first)
	}
	again, err := db.PurchaseMembership(ctx, 42, "premium_1d", "first-request", now+3600)
	if err != nil || !again.Replayed || again.ExpiresAt != first.ExpiresAt {
		t.Fatalf("重放又扣款或续期: %+v %v", again, err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "premium_7d", "first-request", now); membershipCode(err) != "request_conflict" {
		t.Fatalf("请求冲突未阻止: %v", err)
	}
	second, err := db.PurchaseMembership(ctx, 42, "premium_7d", "renew-request", now+3600)
	if err != nil {
		t.Fatal(err)
	}
	if second.ExpiresAt != first.ExpiresAt+7*86400 {
		t.Fatalf("续期未累加: %+v", second)
	}
	reopened, err := Open(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	status, err := reopened.GetMembershipStatus(ctx, 42, now+3600)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Membership.Active || status.Membership.ExpiresAt != second.ExpiresAt || status.Balance.Gold != 1400 || status.Membership.XPBonusRate != 1 {
		t.Fatalf("重启丢失会员/余额: %+v", status)
	}
	status, err = reopened.GetMembershipStatus(ctx, 42, second.ExpiresAt)
	if err != nil || status.Membership.Active {
		t.Fatalf("到期边界仍有效: %+v %v", status, err)
	}
	renewed, err := reopened.PurchaseMembership(ctx, 42, "premium_1d", "expired-request", second.ExpiresAt+3600)
	if err != nil || renewed.ExpiresAt != second.ExpiresAt+3600+86400 {
		t.Fatalf("过期续费基准错误: %+v %v", renewed, err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "missing", "invalid-plan-request", now); membershipCode(err) != "invalid_plan" {
		t.Fatalf("无效套餐: %v", err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "premium_1d", "short", now); membershipCode(err) != "invalid_request_id" {
		t.Fatalf("无效请求: %v", err)
	}
	if _, err := db.PurchaseMembership(ctx, 99, "premium_1d", "missing-user-request", now); membershipCode(err) != "user_not_found" {
		t.Fatalf("未知用户: %v", err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET membership=2 WHERE uid=42`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "premium_1d", "permanent-request", now); membershipCode(err) != "already_permanent" {
		t.Fatalf("永久会员重复购买: %v", err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM membership_order WHERE uid=42`, 3)
	assertCount(t, db, `SELECT COUNT(*) FROM txn WHERE uid=42 AND kind='membership'`, 3)
}

func TestMembershipPurchaseInsufficientFundsAndRollback(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const now = int64(1700000000)
	if _, err := db.EnsureProfile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=99 WHERE uid=42`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "premium_1d", "balance-request", now); membershipCode(err) != "insufficient_gold" {
		t.Fatalf("未拦截余额不足: %v", err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=500 WHERE uid=42;
        CREATE TRIGGER fail_membership_queue BEFORE INSERT ON admin_queue BEGIN SELECT RAISE(ABORT,'queue unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "premium_1d", "balance-request", now); err == nil {
		t.Fatal("队列失败未回滚交易")
	}
	p, err := db.GetProfile(ctx, 42)
	if err != nil || p.Gold != 500 || p.Membership != 0 || p.MembershipExp != 0 {
		t.Fatalf("失败后扣款/激活: %+v %v", p, err)
	}
	for _, table := range []string{"membership_order", "txn", "admin_queue"} {
		assertCount(t, db, `SELECT COUNT(*) FROM `+table, 0)
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_membership_queue`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurchaseMembership(ctx, 42, "premium_1d", "balance-request", now); err != nil {
		t.Fatal(err)
	}
	raw, ok, err := db.QueuePop(ctx)
	if err != nil || !ok || raw != `{"op":"refresh_player","uid":42}` {
		t.Fatalf("会员未刷新在线档案: %q %v %v", raw, ok, err)
	}
}

func TestMembershipConcurrentProcessesChargeOnce(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	if _, err := db.EnsureProfile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET gold=500 WHERE uid=42`); err != nil {
		t.Fatal(err)
	}
	other, err := Open(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	results := make(chan *MembershipPurchase, 2)
	errs := make(chan error, 2)
	for _, connection := range []*DB{db, other} {
		go func(d *DB) {
			p, err := d.PurchaseMembership(ctx, 42, "premium_1d", "concurrent-request", 1700000000)
			results <- p
			errs <- err
		}(connection)
	}
	replayed := 0
	for range 2 {
		p, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		if p.Replayed {
			replayed++
		}
	}
	p, err := db.GetProfile(ctx, 42)
	if err != nil || p.Gold != 400 || replayed != 1 {
		t.Fatalf("重复扣款: %+v replayed=%d err=%v", p, replayed, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM membership_order`, 1)
}

func TestAdminMembershipKeepsBalancesAndRollsBackQueueFailure(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 20)
	ctx := context.Background()
	if _, err := db.SQL.Exec("INSERT INTO profile(uid,name,xp,gold) VALUES(1,'Local',1234,500)"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMembership(ctx, 1, 1, 2000, "admin", 1000); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetProfile(ctx, 1)
	if err != nil || p.Membership != 1 || p.MembershipExp != 2000 || p.Gold != 500 || p.XP != 1234 {
		t.Fatalf("设置会员改变其他数据: %+v %v", p, err)
	}
	assertCount(t, db, "SELECT count(*) FROM membership_order", 0)
	assertCount(t, db, "SELECT count(*) FROM admin_queue", 1)
	assertCount(t, db, "SELECT count(*) FROM audit_log WHERE action='membership_update'", 1)
	if _, err := db.SQL.Exec("CREATE TRIGGER reject_member_queue BEFORE INSERT ON admin_queue BEGIN SELECT RAISE(ABORT,'queue unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMembership(ctx, 1, 2, 0, "admin", 1001); err == nil {
		t.Fatal("队列失败未回滚")
	}
	p, _ = db.GetProfile(ctx, 1)
	if p.Membership != 1 || p.MembershipExp != 2000 {
		t.Fatalf("失败后会员被修改: %+v", p)
	}
	assertCount(t, db, "SELECT count(*) FROM audit_log WHERE action='membership_update'", 1)
	for _, v := range [][2]int64{{1, 999}, {0, 2000}, {2, 2000}, {3, 0}} {
		if err := db.SetMembership(ctx, 1, v[0], v[1], "admin", 1000); err == nil {
			t.Fatalf("接受了无效会员参数 %v", v)
		}
	}
}
