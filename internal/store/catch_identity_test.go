package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"
)

func openCatchHistoryDBAtVersion(t *testing.T, version int64) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "catch-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL.Exec(`CREATE TABLE schema_migrations(
		version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at REAL NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if migrationVersion(name) > version {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL.Exec(string(body)); err != nil {
			t.Fatalf("旧库迁移 %s: %v", name, err)
		}
		if _, err := db.SQL.Exec(`INSERT INTO schema_migrations VALUES(?,?,0)`, migrationVersion(name), name); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestCatchIdentityUpgradePreservesHistoryAndRollsBack(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 8)
	if _, err := db.SQL.Exec(`INSERT INTO catch_log(id,ts,source,uid,fish_key,weight_g) VALUES
		(41,1,'keep',10,'perch',100),(71,2,'keep',20,'roach',200),(99,3,'keep',10,'ruffe',300);
		DELETE FROM catch_log WHERE id=99;
		CREATE TRIGGER fail_catch_migration BEFORE INSERT ON schema_migrations
		WHEN NEW.name='migrations/00018_catch_identity.sql'
		BEGIN SELECT RAISE(ABORT,'migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("迁移历史保存失败必须整体回滚")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM pragma_table_info('catch_log') WHERE name='catch_guid'`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE id IN (41,71)`, 2)
	assertCount(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name='catch_log_next'`, 0)
	if _, err := db.SQL.Exec(`DROP TRIGGER fail_catch_migration`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE (id=41 AND uid=10 AND weight_g=100)
		OR (id=71 AND uid=20 AND weight_g=200)`, 2)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE catch_guid IS NULL`, 2)
	if err := db.CatchLogInsert(context.Background(), 10, "loc", 1, "perch", "", 100, .2, "keep", "worm", 1, 3, 1, "level"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE id>99 AND catch_guid IS NOT NULL`, 1)
}

func TestCatchIdentityKeepsEqualFishAndDifferentPlayers(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	for _, entry := range []*KeepnetEntry{
		{EchoGUID: "11111111111111111111111111111111", UID: 10},
		{EchoGUID: "22222222222222222222222222222222", UID: 10},
		{EchoGUID: "33333333333333333333333333333333", UID: 20},
	} {
		entry.Species, entry.WeightG, entry.KeepLoc, entry.KeepSpot = "perch", 100, "loc", 1
		entry.LengthM, entry.TS = .2, 1
		if err := db.KeepFishingCatch(ctx, entry, 10, 1); err != nil {
			t.Fatal(err)
		}
		if err := db.KeepFishingCatch(ctx, entry, 10, 1); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log`, 3)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE uid=10`, 2)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE uid=20`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM keepnet_items`, 3)
	for range 2 {
		if err := db.CatchLogInsert(ctx, 10, "loc", 1, "perch", "", 100, .2, "keep", "worm", 1, 10, 1, "level"); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log`, 5)
}
