package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestKeepFishingCatchRollsBackAndConsumesPendingOnce(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const uid = int64(123456)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	const fishGUID = "11111111111111111111111111111111"
	if err := db.SaveFishingState(ctx, &FishingState{UID: uid, Hooked: true,
		Pending: &FishingFish{FishGUID: fishGUID, Species: "perch", WeightG: 321}}); err != nil {
		t.Fatal(err)
	}
	entry := &KeepnetEntry{UID: uid, EchoGUID: fishGUID, Species: "perch", WeightG: 321,
		LengthM: .2, KeepLoc: "loc1", KeepSpot: 2, KeepLevel: "level_001_lake", TS: 123.5}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_keep_log BEFORE INSERT ON catch_log
		BEGIN SELECT RAISE(ABORT,'log unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.KeepFishingCatch(ctx, entry, 20, 3); err == nil {
		t.Fatal("日志失败必须使入护失败")
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM keepnet_items`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("失败后护网未回滚: %d %v", count, err)
	}
	saved, err := db.LoadFishingState(ctx, uid)
	if err != nil || saved.Pending == nil || !saved.Hooked {
		t.Fatalf("失败后 pending 丢失: %+v %v", saved, err)
	}
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_keep_log`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.KeepFishingCatch(ctx, entry, 20, 3); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"keepnet_items", "catch_log"} {
		if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s 行数=%d err=%v", table, count, err)
		}
	}
	saved, err = db.LoadFishingState(ctx, uid)
	if err != nil || saved.Pending != nil || saved.Hooked || !saved.Resolved {
		t.Fatalf("成功后 pending 未原子消费: %+v %v", saved, err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.KeepFishingCatch(ctx, entry, 20, 3); err != nil {
		t.Fatal(err)
	}
}

func TestKeepFishingCapacityIsAtomicAndExistingFishCanReplay(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const uid int64 = 123456
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := db.KeepFishingCatchWithCapacity(ctx, &KeepnetEntry{UID: uid,
				EchoGUID: fmt.Sprintf("%032x", index), Species: "perch", WeightG: 300, TS: 1}, 20, 3, 1)
			results <- err
		}(index)
	}
	wg.Wait()
	close(results)
	success, full := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrKeepnetFull) {
			full++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || full != 1 {
		t.Fatalf("capacity race: success=%d full=%d", success, full)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log`, 1)
	list, _ := db.KeepnetList(ctx, uid)
	if kept, err := db.KeepFishingCatchWithCapacity(ctx, list[0], 20, 3, 0); err != nil || !kept {
		t.Fatalf("existing fish replay incorrectly required an extra slot: kept=%v err=%v", kept, err)
	}
}

func TestConsumedCatchIsNotReinsertedEvenWhenKeepnetIsFull(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	const uid int64 = 123456
	entry := &KeepnetEntry{UID: uid, EchoGUID: "33333333333333333333333333333333", Species: "perch", WeightG: 300, TS: 1}
	if err := db.KeepFishingCatch(ctx, entry, 20, 3); err != nil {
		t.Fatal(err)
	}
	if err := db.KeepnetDelete(ctx, uid, []string{entry.EchoGUID}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFishingState(ctx, &FishingState{UID: uid, Hooked: true,
		Pending: &FishingFish{FishGUID: entry.EchoGUID, Species: entry.Species, WeightG: entry.WeightG}}); err != nil {
		t.Fatal(err)
	}
	kept, err := db.KeepFishingCatchWithCapacity(ctx, entry, 20, 3, 0)
	if err != nil || kept {
		t.Fatalf("already consumed catch was reinserted or blocked: kept=%v err=%v", kept, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log`, 1)
	saved, _ := db.LoadFishingState(ctx, uid)
	if saved.Pending != nil || !saved.Resolved {
		t.Fatal("stale consumed pending was not cleared")
	}
}
