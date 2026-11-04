package gameserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

type dlineTestEnv struct {
	srv  *Server
	db   *store.DB
	sess *Session
}

func newDLineTestEnv(t *testing.T, uid int64) *dlineTestEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "dline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "dline"}}, logging.Discard(), nil)
	srv.SetDB(db)
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	return &dlineTestEnv{srv: srv, db: db, sess: sess}
}

func (e *dlineTestEnv) ctx(main, sub uint8, tree *proto.Value) *Ctx {
	return &Ctx{
		Sess: e.sess, Main: int(main), Sub: int(sub), Tree: tree,
		DecodeOK: tree != nil, ExpectResponse: true, Log: logging.Discard(),
	}
}

func (e *dlineTestEnv) invoke(t *testing.T, main, sub uint8, tree *proto.Value) *proto.Value {
	t.Helper()
	h := e.srv.dispatch.lookup(main, sub)
	if h == nil {
		t.Fatalf("handler %d/%d not registered", main, sub)
	}
	body, handled := h(e.ctx(main, sub, tree))
	if !handled {
		t.Fatalf("handler %d/%d not handled", main, sub)
	}
	v, n, err := proto.DecodeValue(body, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("decode %d/%d: %v", main, sub, err)
	}
	if n != len(body) {
		t.Fatalf("decode %d/%d consumed=%d len=%d", main, sub, n, len(body))
	}
	return v
}

func decodeWrappedBytes(t *testing.T, v *proto.Value) *proto.Value {
	t.Helper()
	if v.Kind != proto.KindBytes {
		t.Fatalf("expected bytes, got %s", v.Kind)
	}
	raw, err := hex.DecodeString(v.Hex)
	if err != nil {
		t.Fatal(err)
	}
	inner, _, err := proto.DecodeValue(raw, 0, wiregen.Table())
	if err != nil {
		t.Fatalf("decode wrapped object: %v", err)
	}
	return inner
}

func TestAchievementsAndFishTrophiesAreDynamic(t *testing.T) {
	env := newDLineTestEnv(t, 42)
	registerAchievementHandlers(env.srv.dispatch)
	ctx := context.Background()

	if err := env.db.UnlockAchievement(ctx, 42, 110, 0.75, 100); err != nil {
		t.Fatal(err)
	}
	v := env.invoke(t, 3, 3, nil)
	if v.Kind != proto.KindArgs || v.Code == nil || *v.Code != "114" || len(v.Items) != 130 {
		t.Fatalf("unexpected 3/3 shape: kind=%s code=%v len=%d", v.Kind, v.Code, len(v.Items))
	}
	var achieved *proto.Value
	for _, item := range v.Items {
		if item.Fields[0].Num != nil && *item.Fields[0].Num == 110 {
			achieved = item
			break
		}
	}
	if achieved == nil || achieved.Fields[1].Num == nil || *achieved.Fields[1].Num != 1 {
		t.Fatalf("achievement 110 state missing: %+v", achieved)
	}
	if achieved.Fields[2].Num == nil || proto.F32FromBits(uint32(*achieved.Fields[2].Num)) != 0.75 {
		t.Fatalf("progress=%v", achieved.Fields[2].Num)
	}
	if achieved.Fields[4].Num == nil || *achieved.Fields[4].Num != unixToFiletime100ns(100) {
		t.Fatalf("filetime=%v", achieved.Fields[4].Num)
	}

	for _, row := range []struct {
		fish     string
		weight   int64
		grade    int64
		locLevel string
	}{
		{"c.roach", 100, 1, "level_a"},
		{"c.roach", 300, 2, "level_a"},
		{"perch", 900, 3, "level_a"},
		{"pike", 500, 2, "level_b"},
	} {
		if err := env.db.CatchLogInsert(ctx, 42, "loc", row.weight, row.fish, row.fish, row.weight,
			0.3, "keep", "worm", row.grade, 10, 20, row.locLevel); err != nil {
			t.Fatal(err)
		}
	}
	v = env.invoke(t, 3, 17, nil)
	if v.Kind != proto.KindArgs || v.Code == nil || *v.Code != "132" || len(v.Items) != 2 {
		t.Fatalf("unexpected 3/17 shape: %+v", v)
	}
	if v.Items[0].Fields[0].Str == nil || *v.Items[0].Fields[0].Str != "level_a" {
		t.Fatalf("first trophy group=%+v", v.Items[0].Fields[0])
	}
	if v.Items[0].Fields[1].Val.Code == nil || *v.Items[0].Fields[1].Val.Code != "129" {
		t.Fatalf("fish card list container code=%v, want 129", v.Items[0].Fields[1].Val.Code)
	}
	cards := v.Items[0].Fields[1].Val.Items
	if len(cards) != 2 {
		t.Fatalf("level_a should contain one best card per fish: %+v", cards)
	}
	if cards[0].Fields[0].Str == nil || *cards[0].Fields[0].Str != "c.roach" || *cards[0].Fields[2].Num != 300 {
		t.Fatalf("c.roach best card=%+v", cards[0])
	}
	if cards[1].Fields[0].Str == nil || *cards[1].Fields[0].Str != "perch" || *cards[1].Fields[2].Num != 900 {
		t.Fatalf("perch card=%+v", cards[1])
	}
	if v.Items[1].Fields[0].Str == nil || *v.Items[1].Fields[0].Str != "level_b" {
		t.Fatalf("second trophy group=%+v", v.Items[1].Fields[0])
	}
}

func TestSocialHandlersUseRequestedUIDAndWater(t *testing.T) {
	env := newDLineTestEnv(t, 100)
	registerSocialHandlers(env.srv.dispatch)
	ctx := context.Background()

	if _, err := env.db.EnsureProfile(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.EnsureProfile(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.SQL.ExecContext(ctx, `UPDATE profile SET name='target-player', level=37, region='CN' WHERE uid=200`); err != nil {
		t.Fatal(err)
	}

	v := env.invoke(t, 24, 18, proto.Args524(proto.U32(200)))
	card := decodeWrappedBytes(t, v)
	if card.Class != "feaidppnpch" || card.Fields[0].Num == nil || *card.Fields[0].Num != 200 {
		t.Fatalf("24/18 card=%+v", card)
	}
	v = env.invoke(t, 24, 19, proto.Args524(proto.U32(200)))
	card = decodeWrappedBytes(t, v)
	if card.Class != "dgbmlekfpbg" || *card.Fields[0].Num != 200 ||
		card.Fields[1].Str == nil || *card.Fields[1].Str != "target-player" || *card.Fields[4].Num != 37 {
		t.Fatalf("24/19 card=%+v", card)
	}

	if err := env.db.CatchLogInsert(ctx, 100, "level_a", 1, "c.roach", "蟑螂", 400, 0.2, "keep", "worm", 1, 5, 10, "level_a"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.CatchLogInsert(ctx, 100, "level_a", 2, "c.roach", "蟑螂", 800, 0.4, "keep", "worm", 2, 8, 20, "level_a"); err != nil {
		t.Fatal(err)
	}
	v = env.invoke(t, 24, 22, proto.Args524(proto.StrVal("level_a")))
	if v.Kind != proto.KindObj || v.Class != "kijhfkcpngc" || *v.Fields[1].Num != 800 {
		t.Fatalf("24/22 best=%+v", v)
	}
	v = env.invoke(t, 24, 22, proto.Args524(proto.StrVal("missing")))
	if v.Kind != proto.KindNull {
		t.Fatalf("24/22 missing should be null, got %s", v.Kind)
	}

	v = env.invoke(t, 24, 21, proto.Args524(proto.StrVal("absolute"), proto.U32(7), proto.StrVal("FULL"), proto.StrVal("zh-CN")))
	var board map[string]any
	if err := json.Unmarshal(mustHex(t, v.Hex), &board); err != nil {
		t.Fatal(err)
	}
	if board["id"] != "absolute" || board["type"] != "FULL" || board["locale"] != "zh-CN" {
		t.Fatalf("24/21 board=%v", board)
	}
	v = env.invoke(t, 24, 24, proto.Args524(proto.StrVal("level_x")))
	var named map[string]any
	if err := json.Unmarshal(mustHex(t, v.Hex), &named); err != nil {
		t.Fatal(err)
	}
	if named["placeid"] != "level_x" {
		t.Fatalf("24/24 named=%v", named)
	}
}

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCompetitionHandlerLifecycleAndResults(t *testing.T) {
	env := newDLineTestEnv(t, 1)
	registerCompetitionHandlers(env.srv.dispatch)
	ctx := context.Background()

	v := env.invoke(t, 6, 4, proto.Args524(
		proto.StrVal("Cup"), proto.StrVal("level_a"), proto.U32(4), proto.U32(2),
	))
	if v.Class != "idmlhhjaepi" || v.Fields[0].Str == nil || v.Fields[3].Num == nil || *v.Fields[3].Num != uint64(store.CompetitionRegistration) {
		t.Fatalf("6/4 create=%+v", v)
	}
	compID := *v.Fields[0].Str
	list := env.invoke(t, 6, 1, nil)
	if list.Code == nil || *list.Code != "3" || len(list.Items) != 1 || *list.Items[0].Fields[0].Str != compID {
		t.Fatalf("6/1 list=%+v", list)
	}
	loaded := env.invoke(t, 6, 2, nil)
	if loaded.Class != "idmlhhjaepi" || *loaded.Fields[0].Str != compID {
		t.Fatalf("6/2 load=%+v", loaded)
	}
	env.sess.Userid = 2
	env.invoke(t, 6, 7, proto.Args524(proto.StrVal(compID)))
	env.invoke(t, 6, 7, proto.Args524(proto.StrVal(compID)))
	c, err := env.db.GetCompetition(ctx, compID)
	if err != nil || len(c.Members) != 2 {
		t.Fatalf("idempotent join members=%d err=%v", len(c.Members), err)
	}
	env.invoke(t, 6, 10, proto.Args524(proto.StrVal(compID)))
	env.invoke(t, 6, 10, proto.Args524(proto.StrVal(compID)))
	env.invoke(t, 6, 12, proto.Args524(proto.StrVal(compID)))
	c, _ = env.db.GetCompetition(ctx, compID)
	if c.State != store.CompetitionRegistration {
		t.Fatalf("non-owner changed state to %d", c.State)
	}

	env.sess.Userid = 1
	env.invoke(t, 6, 12, proto.Args524(proto.StrVal(compID)))
	c, _ = env.db.GetCompetition(ctx, compID)
	if c.State != store.CompetitionRunning {
		t.Fatalf("owner start state=%d", c.State)
	}
	if err := env.db.CatchLogInsert(ctx, 1, "level_a", 1, "c.roach", "蟑螂", 500, 0.2, "keep", "worm", 1, 5, 10, "level_a"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.CatchLogInsert(ctx, 2, "level_a", 2, "perch", "鲈鱼", 900, 0.3, "keep", "worm", 2, 8, 20, "level_a"); err != nil {
		t.Fatal(err)
	}
	v = env.invoke(t, 6, 3, nil)
	if v.Class != "kabpkpdlcbc" || v.Fields[0].Val == nil || len(v.Fields[0].Val.Items) != 2 {
		t.Fatalf("6/3 result shape=%+v", v)
	}
	first := v.Fields[0].Val.Items[0]
	if *first.Fields[5].Num != 2 || *first.Fields[2].Num != 900 {
		t.Fatalf("6/3 ranking first=%+v", first)
	}
}
