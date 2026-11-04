package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func newFishingRecoveryServer(t *testing.T, db *store.DB) *Server {
	t.Helper()
	srv := NewServer(config.Game{
		Tunnel: config.Tunnel{PSK: "fishing-recovery-test"},
	}, logging.Discard(), nil)
	srv.SetDB(db)
	t.Cleanup(func() { stopFishingRecoveryTimers(srv) })
	return srv
}

func stopFishingRecoveryTimers(srv *Server) {
	srv.fishMu.Lock()
	states := make([]*FishState, 0, len(srv.fishStates))
	for _, st := range srv.fishStates {
		states = append(states, st)
	}
	srv.fishMu.Unlock()
	for _, st := range states {
		st.mu.Lock()
		if st.biteTimer != nil {
			st.biteTimer.Stop()
			st.biteTimer = nil
		}
		if st.pendingTimer != nil {
			st.pendingTimer.Stop()
			st.pendingTimer = nil
		}
		st.mu.Unlock()
	}
}

func addFishingRecoverySession(srv *Server, uid int64, sid uint64) {
	sess := &Session{
		ID:     sid,
		Token:  "recovery-test-token",
		Userid: uid,
		Srv:    srv,
		stopCh: make(chan struct{}),
		state:  map[string]any{},
		log:    logging.Discard(),
	}
	srv.mu.Lock()
	srv.sessions[sid] = sess
	srv.byToken[sess.Token] = sess
	srv.mu.Unlock()
}

// 硬重启恢复 pending 时必须逐字段复用原鱼，不能重新 roll GUID/重量。
func TestFishingStateRestorePendingNoReroll(t *testing.T) {
	db := connectTestDB(t)
	uid := int64(1000001)
	castAt := time.Now().Add(-2 * time.Second)
	biteAt := castAt.Add(13 * time.Second)
	want := &RolledFish{
		FishGUID: "22222222222222222222222222222222",
		Species:  "c.roach",
		Grade:    2,
		WeightG:  318,
		LengthM:  0.245,
		XP:       21,
		Silver:   17,
		LocLevel: "level_019_american_pond",
		NamedID:  "named-fish-1",
		Name:     "Silver",
	}

	first := newFishingRecoveryServer(t, db)
	st := first.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = "11111111111111111111111111111111"
	st.RigType = 12
	st.Loc = "level_019_american_pond"
	st.Spot = 9876
	st.DepthCM = 375
	st.CastActive = true
	st.CastTs = castAt
	st.FallSeen = true
	st.FallTs = castAt.Add(time.Second)
	st.RigSettled = true
	st.TickSeen = true
	st.BiteAt = biteAt
	st.PushSent = true
	st.Hooked = true
	st.Pending = want
	st.Bait = "worm_01"
	st.LastSkill = 3
	st.mu.Unlock()

	restarted := newFishingRecoveryServer(t, db)
	got := restarted.stateFor(uid)
	got.mu.Lock()
	defer got.mu.Unlock()
	if got.Pending == nil {
		t.Fatal("重启后 pending 丢失")
	}
	if *got.Pending != *want {
		t.Fatalf("重启后 pending 被重 roll:\n got=%+v\nwant=%+v", *got.Pending, *want)
	}
	if !got.CastActive || !got.Hooked || !got.PushSent || got.Resolved {
		t.Fatalf("恢复状态机标志不符: %+v", got)
	}
	if !got.BiteAt.Equal(biteAt) {
		t.Fatalf("BiteAt 未恢复: got=%v want=%v", got.BiteAt, biteAt)
	}
	if !got.persist.recoverPending {
		t.Fatal("从 DB 恢复 pending 后应标记需要重推同一尾鱼")
	}
}

// 无存活会话时 timer 到点只放弃本次触发，不丢永久状态；重连后按原 BiteAt
// 重排。测试使用未来时刻并立即停表，避免真的进入判鱼/推送。
func TestFishingStateNoSessionThenReconnect(t *testing.T) {
	db := connectTestDB(t)
	uid := int64(1000001)
	castAt := time.Now().Add(-2 * time.Second)
	biteAt := time.Now().Add(time.Hour)

	first := newFishingRecoveryServer(t, db)
	st := first.stateFor(uid)
	st.mu.Lock()
	st.GearGuid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	st.RigType = 5
	st.Loc = "level_001_lake"
	st.Spot = 42
	st.CastActive = true
	st.CastTs = castAt
	st.FallSeen = true
	st.FallTs = castAt.Add(time.Second)
	st.RigSettled = true
	st.TickSeen = true
	st.BiteAt = biteAt
	st.Bonus = fishingSkillBonus{BiteSpeed: 0.75, TrophyLuck: 0.1, FightTolerance: 0.2}
	st.mu.Unlock()

	restarted := newFishingRecoveryServer(t, db)
	got := restarted.stateFor(uid)
	restarted.onBiteTimer(uid, restarted.stateFor(uid).BiteGen) // 模拟 timer 到点时玩家仍离线
	got.mu.Lock()
	if !got.CastActive || got.Resolved || !got.FallSeen || !got.RigSettled {
		got.mu.Unlock()
		t.Fatalf("无会话触发不得清掉永久状态: %+v", got)
	}
	if got.biteTimer != nil {
		got.mu.Unlock()
		t.Fatal("无会话触发后旧 timer 引用应已释放")
	}
	got.mu.Unlock()

	persisted, err := db.LoadFishingState(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || !persisted.CastActive || persisted.Resolved ||
		!persisted.BiteAt.Equal(biteAt) || persisted.GearGUID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("DB 永久状态被无会话 timer 破坏: %+v", persisted)
	}

	addFishingRecoverySession(restarted, uid, 77)
	got = restarted.stateFor(uid)
	got.mu.Lock()
	timer := got.biteTimer
	recoveredAt := got.BiteAt
	got.mu.Unlock()
	if timer == nil {
		t.Fatal("重连后未按持久化 BiteAt 重排 timer")
	}
	timer.Stop()
	if !recoveredAt.Equal(biteAt) {
		t.Fatalf("重连后 BiteAt 漂移: got=%v want=%v", recoveredAt, biteAt)
	}
}

func TestBuildFish27MissingHookFallsBackToFishGUID(t *testing.T) {
	srv := NewServer(config.Game{
		Tunnel: config.Tunnel{PSK: "fishing-hook-fallback-test"},
	}, logging.Discard(), nil)
	f := &RolledFish{
		FishGUID: "99999999999999999999999999999999",
		Species:  "c.roach",
		Grade:    1,
		WeightG:  186,
		LengthM:  0.1981,
	}
	obj := srv.buildFish27(f, "", "11111111111111111111111111111111")
	if got := obj.Field(23).Hex; got != f.FishGUID {
		t.Fatalf("27.f23 缺 hook 时应回退鱼 GUID %s，实得 %s", f.FishGUID, got)
	}
}
