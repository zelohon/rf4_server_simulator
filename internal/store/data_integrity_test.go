package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureProfileUsesDefaultName(t *testing.T) {
	db := openIntegrityTestDB(t)
	p, err := db.EnsureProfile(context.Background(), 123456)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "player" {
		t.Fatalf("默认昵称=%q，期望 player", p.Name)
	}
}

func TestCatchLogTimestampKeepsSubsecondPrecision(t *testing.T) {
	db := openIntegrityTestDB(t)
	before := float64(time.Now().UnixNano()) / 1e9
	if err := db.CatchLogInsert(context.Background(), 1, "loc", 1, "roach", "", 100,
		0.1, "keep", "worm", 1, 10, 1, "level"); err != nil {
		t.Fatal(err)
	}
	var ts float64
	if err := db.SQL.QueryRow(`SELECT ts FROM catch_log WHERE uid=1 AND fish_key='roach'`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	after := float64(time.Now().UnixNano()) / 1e9
	if ts < before || ts > after {
		t.Fatalf("catch_log.ts=%f 不在写入时间窗 [%f,%f]", ts, before, after)
	}
	if math.Abs(ts-math.Round(ts)) < 1e-9 {
		t.Fatalf("catch_log.ts=%f 被截成整秒，FILETIME 精度丢失", ts)
	}
}

func TestNamedFishInsertRejectsDuplicateConcurrentWinner(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	nf := &NamedFish{FishID: "abc", Species: "perch", Name: "First", Grade: 4}
	if err := db.NamedFishInsert(ctx, nf); err != nil {
		t.Fatal(err)
	}
	later := &NamedFish{FishID: "abc", Species: "perch", Name: "Second", Grade: 4}
	if err := db.NamedFishInsert(ctx, later); err == nil {
		t.Fatal("重复命名必须返回冲突，不能误报成功")
	}
	got, err := db.NamedFishGet(ctx, "abc")
	if err != nil || got.Name != "First" {
		t.Fatalf("冲突后必须保留首个名字: got=%+v err=%v", got, err)
	}
}

func TestMigrateAddsItemConditionColumns(t *testing.T) {
	db := openIntegrityTestDB(t)
	if _, err := db.SQL.Exec(`ALTER TABLE item DROP COLUMN durability`); err != nil {
		t.Fatalf("构造缺少列的 schema: %v", err)
	}
	if _, err := db.SQL.Exec(`ALTER TABLE item DROP COLUMN workmanship`); err != nil {
		t.Fatalf("构造缺少列的 schema: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	var n int
	if err := db.SQL.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('item')
		WHERE name IN ('durability','workmanship')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("item 表补列数=%d，期望 2", n)
	}
}

func TestTableOwnershipMatchesSchema(t *testing.T) {
	db := openIntegrityTestDB(t)
	if err := ValidateTableOwnership(context.Background(), db); err != nil {
		var names []string
		rows, _ := db.SQL.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
		if rows != nil {
			for rows.Next() {
				var name string
				_ = rows.Scan(&name)
				names = append(names, name)
			}
			_ = rows.Close()
		}
		t.Logf("actual tables: %v", names)
		t.Fatal(err)
	}
	owner := TableOwnership()
	if owner["membership_order"] != "player" {
		t.Fatalf("membership_order 归属=%q，期望 player", owner["membership_order"])
	}
	if owner["place_access"] != "player" {
		t.Fatalf("place_access 归属=%q，期望 player", owner["place_access"])
	}
	if owner["item_def"] != "static" || owner["kv"] != "runtime" {
		t.Fatalf("关键表归属错误: item_def=%q kv=%q", owner["item_def"], owner["kv"])
	}
}

func TestDataIntegrityMigrationCleansAndConstrains(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 7)
	if _, err := db.SQL.Exec(`
		DROP INDEX IF EXISTS uq_catch_log_business_key;
		DROP INDEX IF EXISTS uq_rig_component_guid;
		DROP INDEX IF EXISTS uq_container_slot_guid;
		DELETE FROM schema_migrations WHERE name='migrations/00008_data_integrity.sql';
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.SQL.Exec(`
		INSERT INTO rig(guid,uid,rig_type,name,created_ts) VALUES
			('rig-authority',1,1,'float',0),
			('rig-stale',1,1,'float',0),
			('rig-second',1,1,'float',0);
		INSERT INTO item(guid,uid,container,slot,rig_guid) VALUES
			('owned-item',1,'rig',11,'rig-authority'),
			('second-item',1,'rig',12,'rig-second'),
			('third-item',1,'rig',13,NULL);
		INSERT INTO rig_component(rig_guid,slot,guid,extra) VALUES
			('rig-stale',11,'owned-item',0),
			('rig-authority',11,'owned-item',100),
			('rig-stale',14,'second-item',250),
			('rig-second',12,'second-item',250),
			('rig-authority',15,'third-item',0),
			('rig-stale',16,'third-item',0);
		INSERT INTO container_slot(uid,container,slot,guid) VALUES
			(1,0,1,'owned-item'),
			(1,0,2,'owned-item');
		INSERT INTO catch_log(ts,loc,spot,fish_key,weight_g,source,uid) VALUES
			(1,NULL,NULL,'roach',100,'keep',1),
			(2,NULL,NULL,'roach',100,'keep',1),
			(3,'lake',NULL,'roach',100,'keep',1),
			(4,'lake',NULL,'roach',100,'keep',1);
	`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("重跑 00008 清理迁移: %v", err)
	}

	assertCount(t, db, `SELECT COUNT(*) FROM rig_component WHERE guid='owned-item'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM rig_component WHERE guid='owned-item' AND rig_guid='rig-authority'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM rig_component WHERE guid='second-item'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM rig_component WHERE guid='second-item' AND rig_guid='rig-second'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM rig_component WHERE guid='third-item'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM container_slot WHERE guid='owned-item'`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE source='keep' AND fish_key='roach' AND weight_g=100 AND loc IS NULL`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM catch_log WHERE source='keep' AND fish_key='roach' AND weight_g=100 AND loc='lake'`, 1)

	if _, err := db.SQL.Exec(`INSERT INTO catch_log(ts,loc,spot,fish_key,weight_g,source,uid,catch_guid)
		VALUES(5,NULL,NULL,'roach',100,'keep',1,'new-fish')`); err != nil {
		t.Fatalf("新鱼不能被旧内容键合并: %v", err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO catch_log(ts,source,uid,catch_guid)
		VALUES(6,'keep',1,'new-fish')`); err == nil {
		t.Fatal("同一条鱼的重复记录必须被唯一索引拒绝")
	}
	if _, err := db.SQL.Exec(`
		INSERT INTO rig_component(rig_guid,slot,guid,extra)
		VALUES('rig-second',99,'owned-item',0)`); err == nil {
		t.Fatal("rig_component.guid 跨 rig 重复必须被唯一索引拒绝")
	}
	if _, err := db.SQL.Exec(`
		INSERT INTO container_slot(uid,container,slot,guid)
		VALUES(1,0,3,'owned-item')`); err == nil {
		t.Fatal("container_slot.guid 多槽重复必须被唯一索引拒绝")
	}
}

func TestSeedStarterBundleRollsBackOnLateFailure(t *testing.T) {
	db := openIntegrityTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL.Exec(`
		INSERT INTO item(guid,uid,container,slot) VALUES('collision',999,'backpack',0)`); err != nil {
		t.Fatal(err)
	}
	items := []*Item{
		{GUID: "fresh", UID: 1, Container: "backpack", Slot: 0, Qty: 1, Durability: 1, Workmanship: 1},
		{GUID: "collision", UID: 1, Container: "backpack", Slot: 1, Qty: 1, Durability: 1, Workmanship: 1},
	}
	if _, err := db.SeedStarterBundle(ctx, 1, items, map[int64]string{0: "fresh"}); err == nil {
		t.Fatal("后段唯一键冲突必须让 starter 事务失败")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE uid=1`, 0)
	if _, err := db.SeedStarterBundle(ctx, 1, items[:1], map[int64]string{0: "missing"}); err == nil {
		t.Fatal("悬空快捷栏必须被拒绝")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='fresh' AND uid=1`, 0)
	seeded, err := db.SeedStarterBundle(ctx, 1, items[:1], map[int64]string{0: "fresh"})
	if err != nil || !seeded {
		t.Fatalf("有效 starter 包应成功: seeded=%v err=%v", seeded, err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM item WHERE guid='fresh' AND uid=1`, 1)
}

func openIntegrityTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "integrity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertCount(t *testing.T, db *DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.SQL.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %d，期望 %d", query, got, want)
	}
}
