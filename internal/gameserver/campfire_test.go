package gameserver

import (
	"math"
	"testing"
	"time"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestCampfireStartReturnsActiveFireAndPersists(t *testing.T) {
	srv, db := newRigTestServer(t)
	sess := newProbeSess(srv)
	st := srv.stateFor(sess.Userid)
	st.mu.Lock()
	st.Loc = "level_001_lake"
	st.Spot = 222
	st.mu.Unlock()

	ctx := &Ctx{Sess: sess, Main: 3, Sub: 11, DecodeOK: true, Log: logging.Discard()}
	body, ok := hCampfireStart(ctx)
	if !ok || len(body) == 0 {
		t.Fatal("3/11 篝火点火未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil || dec.Class != "ihaaofcmopp" {
		t.Fatalf("3/11 应回 802 ihaaofcmopp，实得 %v err=%v", dec, err)
	}
	clientNow := sessionGameMinuteAt(sess, time.Now())
	wantExpiry := clientNow + float64(defaultCampfireDurationMin)
	gotExpiry := float64(dec.Fields[0].U32())
	if math.Abs(gotExpiry-wantExpiry) > 2 {
		t.Fatalf("篝火结束游戏分钟=%v，当前=%.2f，期望约=%.2f", gotExpiry, clientNow, wantExpiry)
	}
	if f1 := dec.Fields[1].Val; f1 == nil || f1.Kind != proto.KindArgs || f1.ArgCodeStr() != "61" || f1.ArgCount() != 0 {
		t.Fatalf("802.f1 应为空物品实例数组，实得 %v", f1)
	}
	if f2 := dec.Fields[2].Val; f2 == nil || f2.Kind != proto.KindArgs || f2.ArgCodeStr() != "61" || f2.ArgCount() != 0 {
		t.Fatalf("802.f2 应为空物品实例数组，实得 %v", f2)
	}
	if f3 := dec.Fields[3].Val; f3 == nil || f3.Kind != proto.KindGuidArr {
		t.Fatalf("802.f3 应为空 GUID 数组，实得 %v", f3)
	}

	state, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || state == nil {
		t.Fatalf("篝火状态未落库: %+v err=%v", state, err)
	}
	if state.Loc != "level_001_lake" || state.Spot != 222 || state.DurationMin != defaultCampfireDurationMin || state.ExpiresFT <= int64(proto.WindowsFiletimeNow()) {
		t.Fatalf("篝火落库状态不符: %+v", state)
	}
	remainingRealSec := float64(state.ExpiresFT-int64(proto.WindowsFiletimeNow())) / 10_000_000
	if math.Abs(remainingRealSec-gameMinutesToRealDuration(defaultCampfireDurationMin).Seconds()) > 3 {
		t.Fatalf("篝火现实剩余秒=%.2f，期望约=%.2f", remainingRealSec, gameMinutesToRealDuration(defaultCampfireDurationMin).Seconds())
	}
	expiry := state.ExpiresFT
	firstClientExpiry := dec.Fields[0].U32()

	// 再次按 Y 不应把已经在燃烧的篝火重置或延长。
	ctx2 := &Ctx{Sess: sess, Main: 3, Sub: 11, DecodeOK: true, Log: logging.Discard()}
	body2, ok := hCampfireStart(ctx2)
	if !ok || len(body2) == 0 {
		t.Fatal("重复点火未处理")
	}
	dec2, _, err := proto.DecodeValue(body2, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Fields[0].U32() != firstClientExpiry {
		t.Fatalf("重复点火不应改变客户端结束分钟: first=%d second=%d", firstClientExpiry, dec2.Fields[0].U32())
	}
	state2, err := db.GetCampfireState(t.Context(), sess.Userid)
	if err != nil || state2 == nil || state2.ExpiresFT != expiry {
		t.Fatalf("重复点火不应改变到期时间: first=%d second=%+v err=%v", expiry, state2, err)
	}
}
