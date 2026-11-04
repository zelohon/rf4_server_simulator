package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

func TestCastSetsCastTsAndHookedSchedulePreservesIt(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)

	srv.scheduleBite(sess, st)
	st.mu.Lock()
	castTs := st.CastTs
	hooked := st.Hooked
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.mu.Unlock()
	if castTs.IsZero() {
		t.Fatal("14/2 scheduleBite 必须写入 CastTs")
	}
	if hooked {
		t.Fatal("新抛竿应 Hooked=false")
	}

	oldCast := castTs.Add(-3 * time.Second)
	st.mu.Lock()
	st.CastTs = oldCast
	st.CastActive = true
	st.Resolved = false
	st.Pending = nil
	st.Hooked = true
	st.mu.Unlock()
	srv.scheduleBiteForHooked(sess, st)
	st.mu.Lock()
	if !st.Hooked {
		t.Fatal("14/11 补判必须保留 Hooked=true")
	}
	if !st.CastTs.Equal(oldCast) {
		t.Fatalf("14/11 补判不能重置 CastTs: got=%v want=%v", st.CastTs, oldCast)
	}
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.mu.Unlock()
}

func TestFindObjSecondU32ForCastSpot(t *testing.T) {
	obj := proto.Obj("olieodeoaof", 0,
		proto.Field{Op: "U32", Num: ptrU64(111)},
		proto.Field{Op: "U32", Num: ptrU64(222)},
		proto.Field{Op: "STR", Str: strPtr("loc1_02")},
	)
	tree := proto.Args524(obj)
	if got := findObjNthU32(tree, "olieodeoaof", 1); got != 222 {
		t.Fatalf("spot=%d，期望第二个 U32=222", got)
	}
}

func TestFindCastActionByte(t *testing.T) {
	obj := proto.Obj("olieodeoaof", 0,
		proto.Field{Op: "U32", Num: ptrU64(uint64(proto.F32Bits(18.5)))},
		proto.Field{Op: "BYTE", Num: ptrU64(2)},
		proto.Field{Op: "BYTE", Num: ptrU64(7)},
	)
	tree := proto.Args524(obj)
	if got := findObjNthByte(tree, "olieodeoaof", 0); got != 2 {
		t.Fatalf("action=%d，期望首个 BYTE=2", got)
	}
	if got := findObjNthByte(tree, "olieodeoaof", 1); got != 7 {
		t.Fatalf("action=%d，期望第二个 BYTE=7", got)
	}
}

func TestFishingTickRecomputesSettledAndLocation(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	makeTick := func(anim uint64, loc string) *proto.Value {
		fields := make([]proto.Field, 7)
		fields[5] = proto.Field{Op: "U32", Num: ptrU64(anim)}
		fields[6] = proto.Field{Op: "STR", Str: strPtr(loc)}
		return proto.Args524(proto.Obj("lobemkloehp", 0, fields...))
	}
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.CastActive = true
	st.mu.Unlock()
	if _, ok := hFishingStep(&Ctx{Sess: sess, Main: 14, Sub: 7,
		Tree: makeTick(0x200, "loc12_01"), DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/7 未处理")
	}
	st.mu.Lock()
	if !st.RigSettled || !st.InWater || st.Loc != "loc12_01" {
		st.mu.Unlock()
		t.Fatalf("首帧 settled=%v in_water=%v loc=%q", st.RigSettled, st.InWater, st.Loc)
	}
	st.mu.Unlock()

	if _, ok := hFishingStep(&Ctx{Sess: sess, Main: 14, Sub: 7,
		Tree: makeTick(0, "loc12_02"), DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/7 第二帧未处理")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.RigSettled {
		t.Fatal("动画位消失后 RigSettled 必须复位")
	}
	if st.Loc != "loc12_02" {
		t.Fatalf("14/7 loc=%q，期望 loc12_02", st.Loc)
	}
}

func TestKeepnetVersionTicksStrictlyIncreaseAndKeepMap(t *testing.T) {
	e := &store.KeepnetEntry{EchoGUID: "11111111111111111111111111111111", Species: "c.roach", WeightG: 100, LengthM: 0.2, Cat: 1, KeepLevel: "level_019_american_pond"}
	a := buildKeepnet25(e, "level_001_lake")
	time.Sleep(2 * time.Millisecond)
	b := buildKeepnet25(e, "level_001_lake")
	fa, fb := a.Field(22), b.Field(22)
	if fa.U64() == 0 || fb.U64() <= fa.U64() {
		t.Fatalf("25.f22 必须非 0 且严格递增: %d -> %d", fa.U64(), fb.U64())
	}
	f10 := a.Field(10)
	if got := f10.StrOr(); got != "level_019_american_pond" {
		t.Fatalf("25.f10=%q，期望捕获地图", got)
	}
}

func TestCatchLogWrittenOnceOnKeep(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	f := &RolledFish{FishGUID: "22222222222222222222222222222222", Species: "c.roach", Grade: 1, WeightG: 186, LengthM: 0.1981, LocLevel: "level_019_american_pond"}
	if r := srv.settleCatchXP(ctx, uid, f, 1); r == nil {
		t.Fatal("settleCatchXP 返回 nil")
	}
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = f
	st.Loc = "loc19_03"
	st.Spot = 123
	st.Bait = "worm_01"
	st.mu.Unlock()
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.Guid("33333333333333333333333333333333"), proto.Guid(f.FishGUID))
	body, ok := srv.fishingKeep(&Ctx{Sess: sess, Main: 14, Sub: 5, Tree: tree, DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("14/5 未处理")
	}
	var n int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM catch_log WHERE uid=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("入护后 catch_log 行数=%d，期望 1", n)
	}
	var source string
	if err := db.SQL.QueryRowContext(ctx, `SELECT source FROM catch_log WHERE uid=?`, uid).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "keep" {
		t.Fatalf("catch_log source=%q，期望 keep", source)
	}
}

func TestFishingLeftBumpsEscapes(t *testing.T) {
	srv, db := newDetachTestServer(t)
	const uid = int64(1000001)
	st := srv.stateFor(uid)
	st.mu.Lock()
	st.Pending = &RolledFish{FishGUID: "44444444444444444444444444444444", Species: "c.roach"}
	st.CastActive = true
	st.mu.Unlock()
	obj := proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(2)})
	tree := proto.Args524(obj)
	sess := newProbeSess(srv)
	if _, ok := hFishingLeft(&Ctx{Sess: sess, Main: 14, Sub: 12, Tree: tree, DecodeOK: true, Log: logging.Discard()}); !ok {
		t.Fatal("14/12 未处理")
	}
	var n int
	if err := db.SQL.QueryRow(`SELECT value FROM player_counters WHERE uid=? AND key='escapes'`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("escapes=%d，期望1", n)
	}
}

func TestMailInfoShapeAndGUID(t *testing.T) {
	srv, _ := newDetachTestServer(t)
	sess := newProbeSess(srv)
	body, ok := hMailInfo(&Ctx{Sess: sess, Main: 19, Sub: 5, DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("19/5 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("19/5 响应不可解码: %v", err)
	}
	if dec.Class != "pbndmldbfai" || dec.TypeID != 133 {
		t.Fatalf("19/5 顶层=%s/%d，期望 pbndmldbfai/133", dec.Class, dec.TypeID)
	}
	if g := mailIDToGUID(1); len(g) != 32 || g != "00000000000000000000000000000001" {
		t.Fatalf("mailIDToGUID(1)=%q", g)
	}
}

func strPtr(s string) *string { return &s }
