package store

import (
	"context"
	"path/filepath"
	"testing"
)

const shortcutOwnGUID = "01000000000000000000000000000000"
const shortcutForeignGUID = "02000000000000000000000000000000"
const shortcutStoredGUID = "03000000000000000000000000000000"

func shortcutTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "shortcuts.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		guid      string
		uid       int
		container string
	}{{shortcutOwnGUID, 1, "backpack"}, {shortcutForeignGUID, 2, "backpack"}, {shortcutStoredGUID, 1, "native:17"}} {
		if _, err = db.SQL.Exec("INSERT INTO item(guid,uid,container) VALUES(?,?,?)", row.guid, row.uid, row.container); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestShortcutAliasesSurviveCanonicalUnbindAndRestart(t *testing.T) {
	db := shortcutTestDB(t)
	ctx := context.Background()
	for _, slot := range []int64{1, 4} {
		if err := db.BindShortcut(ctx, 1, slot, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
	}
	bindings, err := db.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("aliases=%v err=%v", bindings, err)
	}
	var count int
	if err = db.SQL.QueryRow("SELECT COUNT(*) FROM container_slot WHERE guid=?", shortcutOwnGUID).Scan(&count); err != nil || count != 1 {
		t.Fatal("physical uniqueness changed")
	}
	if err = db.BindShortcut(ctx, 1, 1, ""); err != nil {
		t.Fatal(err)
	}
	bindings, err = db.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 1 || bindings[0].Slot != 4 {
		t.Fatalf("remaining alias lost: %v %v", bindings, err)
	}
	path := db.Path
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	bindings, err = reopened.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 1 || bindings[0].Slot != 4 {
		t.Fatalf("restart changed bindings: %v %v", bindings, err)
	}
	if err = reopened.DeleteItems(ctx, []string{shortcutOwnGUID}); err != nil {
		t.Fatal(err)
	}
	bindings, err = reopened.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 0 {
		t.Fatal("deleted item still bound")
	}
}

func TestShortcutAliasMutationIsAtomicAndChecksOwnership(t *testing.T) {
	db := shortcutTestDB(t)
	ctx := context.Background()
	if err := db.BindShortcut(ctx, 1, 1, shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	for _, guid := range []string{shortcutForeignGUID, shortcutStoredGUID} {
		if err := db.BindShortcut(ctx, 1, 4, guid); err == nil {
			t.Fatal("unavailable item was bound")
		}
	}
	if _, err := db.SQL.Exec(`CREATE TRIGGER fail_shortcut_alias BEFORE INSERT ON kv WHEN NEW.k LIKE 'shortcut-aliases:%' BEGIN SELECT RAISE(ABORT,'late alias failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.BindShortcut(ctx, 1, 4, shortcutOwnGUID); err == nil {
		t.Fatal("expected late alias failure")
	}
	bindings, err := db.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 1 || bindings[0].Slot != 1 {
		t.Fatal("alias failure changed canonical binding")
	}
}

func TestShortcutAliasesClearInsideDeleteAndMoveTransactions(t *testing.T) {
	db := shortcutTestDB(t)
	ctx := context.Background()
	for _, slot := range []int64{1, 4} {
		if err := db.BindShortcut(ctx, 1, slot, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("DELETE FROM item WHERE guid=?", shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	bindings, err := db.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 2 {
		t.Fatal("rollback did not restore aliases")
	}
	if _, err = db.SQL.Exec("UPDATE item SET container='native:17' WHERE guid=?", shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	bindings, err = db.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 0 {
		t.Fatal("moved item still bound")
	}
	var raw string
	if err = db.SQL.QueryRow("SELECT v FROM kv WHERE k=?", shortcutAliasKey(1)).Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("stale alias data %q err=%v", raw, err)
	}
	if _, err = db.SQL.Exec("UPDATE item SET container='backpack' WHERE guid=?", shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []int64{1, 4} {
		if err := db.BindShortcut(ctx, 1, slot, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.SQL.Exec("UPDATE item SET uid=2 WHERE guid=?", shortcutOwnGUID); err != nil {
		t.Fatal(err)
	}
	bindings, err = db.ListShortcuts(ctx, 1)
	if err != nil || len(bindings) != 0 {
		t.Fatal("ownership transfer retained old aliases")
	}
	bindings, err = db.ListShortcuts(ctx, 2)
	if err != nil || len(bindings) != 0 {
		t.Fatal("ownership transfer copied aliases to another user")
	}
	for _, slot := range []int64{1, 4} {
		if err = db.BindShortcut(ctx, 2, slot, shortcutOwnGUID); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.DeleteItems(ctx, []string{shortcutOwnGUID}); err != nil {
		t.Fatal(err)
	}
	bindings, err = db.ListShortcuts(ctx, 2)
	if err != nil || len(bindings) != 0 {
		t.Fatal("deleted item aliases remained")
	}
	if err = db.SQL.QueryRow("SELECT v FROM kv WHERE k=?", shortcutAliasKey(2)).Scan(&raw); err != nil || raw != "{}" {
		t.Fatal("delete did not clean alias JSON")
	}
}
