package store

import "testing"

func TestBaitConsumptionMigrationKeepsHistoryAndDeduplicates(t *testing.T) {
	db := openCatchHistoryDBAtVersion(t, 24)
	if _, err := db.SQL.Exec(`INSERT INTO txn(uid,ts,kind,guid,note) VALUES(1,1,'old','same','历史流水'),(1,2,'old','same','重复历史流水')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.SQL.QueryRow(`SELECT COUNT(*) FROM txn WHERE kind='old'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("history lost: %d %v", count, err)
	}
	for _, kind := range []string{"bait_bite", "bait_cast"} {
		if _, err := db.SQL.Exec(`INSERT INTO txn(uid,ts,kind,guid,note) VALUES(1,3,?,'event','使用')`, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL.Exec(`INSERT INTO txn(uid,ts,kind,guid,note) VALUES(1,4,?,'event','重复')`, kind); err == nil {
			t.Fatal("duplicate bait event accepted")
		}
	}
	path := db.Path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SQL.QueryRow(`SELECT COUNT(*) FROM txn`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("restart records=%d %v", count, err)
	}
}
