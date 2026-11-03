package gameserver

import (
	"context"
	"testing"
)

// TestProfileReservedFieldsStayNeutral 锁住 307.f14 / f16 的官方取值。
//
// 这两格曾被塞进欢乐时光状态做试验（commit c3dd39f），结果客户端把它们当成
// "庆祝/提示"枚举，而 801.f7 与 2/1 每次结算都会带整份档案，
// 于是每钓一条鱼就弹一次「新等级 / 新水域」。
// 这里在欢乐时光处于激活状态时构造档案，确保字段仍然保持 0 / 1。
func TestProfileReservedFieldsStayNeutral(t *testing.T) {
	db := connectTestDB(t)
	srv := NewServerForGearTest(t, db)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	// 欢乐时光默认全天开启、需要会员；开会员让状态确实为激活，
	// 避免测试在"本来就没触发"的情况下空转通过。
	if _, err := db.GrantMembership(ctx, uid, 30); err != nil {
		t.Fatal(err)
	}
	if active, _, _ := srv.HappyHourState(ctx, uid); !active {
		t.Fatal("前置条件失败：欢乐时光应处于激活状态")
	}

	p, err := srv.BuildProfile307(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := u32Of(p.Fields[14].Num); got != 0 {
		t.Fatalf("307.f14 应保持 0（客户端保留枚举），实得 %d", got)
	}
	if got := u32Of(p.Fields[16].Num); got != 1 {
		t.Fatalf("307.f16 应保持 1（客户端保留数值），实得 %d", got)
	}
}
