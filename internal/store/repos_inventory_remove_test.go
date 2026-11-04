package store

import (
	"context"
	"fmt"
	"testing"
)

func seedRemoveTestFish(t *testing.T, db *DB, uid int64, id int, imported bool) string {
	t.Helper()
	guid := fmt.Sprintf("%032x", id)
	if err := db.KeepnetInsert(context.Background(), &KeepnetEntry{UID: uid, EchoGUID: guid, Species: "asp", WeightG: 300, LengthM: .4, Cat: 2, TS: 1}); err != nil {
		t.Fatal(err)
	}
	if imported {
		if err := db.InsertItem(context.Background(), &Item{GUID: guid, UID: uid, WireClass: "ecknbkpeeji", TypeID: 25, Container: "keepnet", Qty: 1, FieldsJSON: "[]", Durability: 1, Workmanship: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return guid
}

func TestRemoveOwnedFishRollsBackBothRepresentations(t *testing.T) {
	db := openIntegrityTestDB(t)
	const uid int64 = 7357
	old := seedRemoveTestFish(t, db, uid, 1, true)
	fresh := seedRemoveTestFish(t, db, uid, 2, false)
	foreign := seedRemoveTestFish(t, db, uid+1, 3, true)
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_old_fish_cleanup BEFORE DELETE ON item
		BEGIN SELECT RAISE(ABORT,'snapshot unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RemoveOwnedItems(context.Background(), uid, []string{fresh, old}); err == nil {
		t.Fatal("cleanup failure must abort the entire removal")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 3)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE container='keepnet'`, 2)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_old_fish_cleanup`); err != nil {
		t.Fatal(err)
	}
	removed, fish, err := db.RemoveOwnedItems(context.Background(), uid, []string{fresh, old, old, foreign})
	if err != nil || len(removed) != 2 || len(fish) != 2 {
		t.Fatalf("removed=%v fish=%v err=%v", removed, fish, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE container='keepnet'`, 1)
	if _, err := db.KeepnetGet(context.Background(), uid+1, foreign); err != nil {
		t.Fatal("other player's fish changed")
	}
	removed, fish, err = db.RemoveOwnedItems(context.Background(), uid, []string{fresh, old})
	if err != nil || len(removed) != 0 || len(fish) != 0 {
		t.Fatal("repeated removal must be idempotent")
	}
}

func TestRemoveAndAdminKeepnetDeleteRespectOfferLock(t *testing.T) {
	db := openIntegrityTestDB(t)
	const uid int64 = 7357
	guid := seedRemoveTestFish(t, db, uid, 10, true)
	if _, err := db.SQL.Exec(`INSERT INTO market_offer(offer_id,fish_guid,seller_uid,price_cents,status,created_ts,updated_ts)
		VALUES('locked',?,?,100,'open',1,1)`, guid, uid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RemoveOwnedItems(context.Background(), uid, []string{guid}); err == nil {
		t.Fatal("release bypassed the market lock")
	}
	if err := db.KeepnetDelete(context.Background(), uid, []string{guid}); err == nil {
		t.Fatal("admin deletion bypassed the market lock")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE container='keepnet'`, 1)
	if _, err := db.SQL.Exec(`UPDATE market_offer SET status='cancelled' WHERE offer_id='locked'`); err != nil {
		t.Fatal(err)
	}
	if err := db.KeepnetDelete(context.Background(), uid, []string{guid, guid}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE container='keepnet'`, 0)
}
