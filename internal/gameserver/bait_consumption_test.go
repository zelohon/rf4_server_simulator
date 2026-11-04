package gameserver

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func baitTestMount(t *testing.T, s *Server, rod, id string, qty, index int64) (string, string) {
	t.Helper()
	item, err := s.CreateItem(t.Context(), 1000001, id, "backpack", qty)
	if err != nil {
		t.Fatal(err)
	}
	if s.stackableItem(t.Context(), item) {
		s.setStackCount(t.Context(), item, qty)
	}
	r := s.rigSetComponent(t.Context(), 1000001, rod, 0, item.GUID, index, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if r.Rejected || r.MountedGUID == "" {
		t.Fatalf("mount %s: %+v", id, r)
	}
	return item.GUID, r.MountedGUID
}

func baitTestRod(t *testing.T, s *Server) string {
	t.Helper()
	rod, err := s.CreateItem(t.Context(), 1000001, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	return rod.GUID
}

func TestBaitConsumptionReserveReplayEmptyAndRestart(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	source, child := baitTestMount(t, s, rod, "worm_01", 5, 350)
	use := func(server *Server, event string) {
		t.Helper()
		if _, err := server.consumeBaitEvent(t.Context(), 1000001, rod, event, []string{child}, false); err != nil {
			t.Fatal(err)
		}
	}
	use(s, "bite-1")
	use(s, "bite-1")
	row, _ := db.GetItemByGUID(t.Context(), source)
	if row == nil || row.Qty != 4 || baitQuantity(row) != 4 {
		t.Fatalf("mount reserved 1, eating it must leave 4 in stock, source=%+v", row)
	}
	// 从数据库重开服务后同一咬口不能再次扣除，pending 的饵快照也须保留。
	f := &RolledFish{FishGUID: "pending", BaitsKnown: true, BaitGUIDs: child}
	st := s.stateFor(1000001)
	st.mu.Lock()
	st.GearGuid = rod
	st.Pending = f
	st.mu.Unlock()
	path := db.Path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "bait-restart"}}, logging.Discard(), nil)
	server.SetDB(reopened)
	if restored := server.stateFor(1000001).Pending; restored == nil || !restored.BaitsKnown || restored.BaitGUIDs != child {
		t.Fatal("lost bait snapshot on restart")
	}
	use(server, "bite-1")
	for i := 2; i <= 5; i++ {
		if _, err := server.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
			t.Fatal(err)
		}
		snapshot, _ := server.biteBaitSnapshot(t.Context(), rod)
		ids := strings.Fields(snapshot)
		if len(ids) != 1 {
			t.Fatalf("reload did not mount bait: %v", ids)
		}
		child = ids[0]
		use(server, fmt.Sprintf("bite-%d", i))
	}
	if row, _ := reopened.GetItemByGUID(t.Context(), source); row != nil {
		t.Fatalf("source remained: %+v", row)
	}
	if row, _ := reopened.GetItemByGUID(t.Context(), child); row != nil {
		t.Fatalf("eaten bait remained: %+v", row)
	}
	if len(server.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("empty rig still has bait")
	}
	if server.canFishWithBait(t.Context(), 1000001, rod) {
		t.Fatal("empty hook produced fish with both switches off")
	}
}

func TestBaitConsumptionCombinationAndRollback(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	_, a := baitTestMount(t, s, rod, "worm_01", 1, 350)
	bItem, err := s.CreateItem(t.Context(), 1000001, "muckworm", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.setStackCount(t.Context(), bItem, 1)
	b := bItem.GUID
	if err := db.SetItemRigLink(t.Context(), b, rod, 27); err != nil {
		t.Fatal(err)
	}
	s.rigComponentWriteAt(t.Context(), rod, 27, 1, b, 351)
	if _, err := db.SQL.Exec(fmt.Sprintf(`CREATE TRIGGER fail_bait_delete BEFORE DELETE ON item WHEN old.guid='%s' BEGIN SELECT RAISE(ABORT,'test'); END`, b)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "both", []string{a, b}, false); err == nil {
		t.Fatal("expected rollback")
	}
	if row, _ := db.GetItemByGUID(t.Context(), a); row == nil {
		t.Fatal("first bait was deducted despite rollback")
	}
	var receipts int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE kind='bait_bite' AND guid='both'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("failed event recorded: %d %v", receipts, err)
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_bait_delete`); err != nil {
		t.Fatal(err)
	}
	change, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "both", []string{a, b, a}, false)
	if err != nil || len(change.Removed) != 2 {
		t.Fatalf("combination consumption=%+v %v", change, err)
	}
	if len(s.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("consumed combo remained mounted")
	}
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "both", []string{a, b}, false); err != nil {
		t.Fatal(err)
	}
}

func TestBaitConsumptionReusableAndAllStaticTypes(t *testing.T) {
	s, db := newRigTestServer(t)
	rows, err := db.SQL.Query(`SELECT numeric_id,item_id,category,attrs_json FROM item_def WHERE category IN (17,27)`)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for rows.Next() {
		def := store.ItemDef{}
		if err := rows.Scan(&def.NumericID, &def.ItemID, &def.Category, &def.AttrsJSON); err != nil {
			t.Fatal(err)
		}
		consumed, wear := baitUseRule(&def, false)
		if def.Category == 17 && consumed {
			t.Fatalf("reusable lure was treated as edible: %s", def.ItemID)
		}
		if def.Category == 27 && !consumed && wear == 0 {
			t.Fatalf("unhandled bait: %s", def.ItemID)
		}
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if checked < 3000 {
		t.Fatalf("checked only %d definitions", checked)
	}
	for _, tt := range []struct {
		id   string
		wear float64
	}{{"zbb_mm_bg_13", 0.005}, {"softplastic_2627", 0.02}, {"spinnerbaits_1244", 0}} {
		rod := baitTestRod(t, s)
		_, child := baitTestMount(t, s, rod, tt.id, 3, 350)
		before, _ := db.GetItemByGUID(t.Context(), child)
		if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "use-"+tt.id, []string{child}, false); err != nil {
			t.Fatal(err)
		}
		after, _ := db.GetItemByGUID(t.Context(), child)
		if after == nil || after.Qty != before.Qty || math.Abs(after.Durability-(before.Durability-tt.wear)) > 1e-6 {
			t.Fatalf("%s before=%+v after=%+v", tt.id, before, after)
		}
	}
}

func TestBaitConsumptionCastSuppliesAndForeignRig(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	_, food := baitTestMount(t, s, rod, "worm_01", 1, 350)
	_, pva := baitTestMount(t, s, rod, "pva_stick", 1, 0)
	_, feed := baitTestMount(t, s, rod, "feed_lesh", 1, 0)
	other := baitTestRod(t, s)
	_, foreign := baitTestMount(t, s, other, "muckworm", 1, 350)
	if _, err := s.consumeBaitEvent(context.Background(), 1000001, rod, "cast-1", []string{food, pva, feed, foreign}, true); err != nil {
		t.Fatal(err)
	}
	_, replacement := baitTestMount(t, s, rod, "pva_stick", 1, 0)
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "cast-1", []string{replacement}, true); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(t.Context(), replacement); row == nil {
		t.Fatal("repeated water frame consumed newly loaded supply")
	}
	for _, guid := range []string{pva, feed} {
		if row, _ := db.GetItemByGUID(t.Context(), guid); row != nil {
			t.Fatal("cast supply remained")
		}
	}
	for _, guid := range []string{food, foreign} {
		if row, _ := db.GetItemByGUID(t.Context(), guid); row == nil {
			t.Fatal("cast consumed hook bait or foreign rig")
		}
	}
	if ids, known := s.biteBaitSnapshot(t.Context(), rod); !known || !strings.Contains(ids, food) {
		t.Fatal("missing bite snapshot")
	}
}

func TestBaitConsumptionLastReservedUnitReturnsAndZeroIsEmpty(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	source, child := baitTestMount(t, s, rod, "worm_01", 2, 350)
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "one", []string{child}, false); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetItemByGUID(t.Context(), source); row == nil || baitQuantity(row) != 1 {
		t.Fatal("unused spare was consumed during the fight")
	}
	if _, err := s.reloadBaitForCast(t.Context(), 1000001, rod); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.biteBaitSnapshot(t.Context(), rod)
	child = strings.Fields(snapshot)[0]
	if result := s.rigRemoveItemComponent(t.Context(), 1000001, rod, child); result == nil {
		t.Fatal("detach failed")
	}
	row, _ := db.GetItemByGUID(t.Context(), child)
	if row == nil || row.Container != "backpack" || baitQuantity(row) != 1 || splitSourceGUID(row) != "" {
		t.Fatal("uneaten last unit was lost or refunded twice")
	}
	_, empty := baitTestMount(t, s, rod, "muckworm", 1, 350)
	row, _ = db.GetItemByGUID(t.Context(), empty)
	s.setStackCount(t.Context(), row, 0)
	if len(s.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("zero quantity was interpreted as a full package")
	}
	if _, err := s.consumeBaitEvent(t.Context(), 1000001, rod, "empty", []string{empty}, false); err == nil {
		t.Fatal("empty bait consumption succeeded")
	}
}

func TestBaitConsumptionThroughBiteAndReplay(t *testing.T) {
	s, db := newRigTestServer(t)
	rod := baitTestRod(t, s)
	source, _ := baitTestMount(t, s, rod, "worm_01", 5, 350)
	addFishingRecoverySession(s, 1000001, 939)
	st := s.stateFor(1000001)
	st.mu.Lock()
	st.GearGuid = rod
	st.Loc = "unknown-map"
	st.Bait = "worm_01"
	st.CastActive = true
	st.InWater = true
	st.CastTs = time.Now().Add(-time.Minute)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	st.biteTimer = timer
	gen := st.BiteGen
	st.mu.Unlock()
	s.onBiteTimer(1000001, gen)
	st.mu.Lock()
	pending := st.Pending
	st.mu.Unlock()
	if pending == nil || !pending.BaitsKnown {
		t.Fatalf("real bite did not save its bait snapshot: %+v", pending)
	}
	row, _ := db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatal("real bite did not consume exactly one unit")
	}
	s.repushPending14_14(1000001)
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatal("replaying fish consumed another bait")
	}
	if len(s.currentBaits(t.Context(), rod)) != 0 {
		t.Fatal("next bait was mounted during the fight")
	}
	sess := s.liveSessionByUID(1000001)
	if err := sess.prepareBaitForCast(st, rod); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 4 {
		t.Fatal("preparation during fight reserved another bait")
	}
	st.mu.Lock()
	st.CastActive = false
	st.mu.Unlock()
	if err := sess.prepareBaitForCast(st, rod); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetItemByGUID(t.Context(), source)
	if row == nil || baitQuantity(row) != 3 || len(s.currentBaits(t.Context(), rod)) != 1 {
		t.Fatal("next cast did not reserve exactly one replacement")
	}
}
