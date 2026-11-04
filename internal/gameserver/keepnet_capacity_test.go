package gameserver

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestKeepnetCapacitySnapshotAndMembership(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if got, err := srv.keepnetCapacity(ctx, uid); err != nil || got != 100 {
		t.Fatalf("普通容量=%d err=%v，期望100", got, err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	if _, err := db.SQL.Exec(`UPDATE profile SET membership=1,membership_exp=? WHERE uid=?`, expires, uid); err != nil {
		t.Fatal(err)
	}
	if got, err := srv.keepnetCapacity(ctx, uid); err != nil || got != 150 {
		t.Fatalf("会员容量=%d err=%v，期望150", got, err)
	}
	key := fmt.Sprintf("native-import:%d:inventory", uid)
	if err := db.KVSet(ctx, key, `{"countsByKind":{"5":{"A":2,"B":3}}}`); err != nil {
		t.Fatal(err)
	}
	if got, err := srv.keepnetCapacity(ctx, uid); err != nil || got != 3 {
		t.Fatalf("导入会员容量=%d err=%v，期望3", got, err)
	}
}
