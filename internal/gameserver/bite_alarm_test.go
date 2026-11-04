package gameserver

import (
	"context"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
	"testing"
)

func TestBiteAlarmUsesNativeItemAndBottomSlot(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rows, err := db.SQL.QueryContext(ctx, `SELECT item_id FROM item_def WHERE category=47 ORDER BY item_id`)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		t.Fatal("missing alarm catalog")
	}
	for _, key := range keys {
		row, err := srv.CreateItem(ctx, uid, key, "backpack", 1)
		if err != nil {
			t.Fatal(err)
		}
		if row.WireClass != "eebdgfednim" || row.TypeID != 700 {
			t.Fatalf("%s created as %s/%d", key, row.WireClass, row.TypeID)
		}
		tree, err := srv.ItemToTree(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := proto.EncodeValue(tree, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		decoded, n, err := proto.DecodeValue(raw, 0, srv.Table())
		if err != nil || n != len(raw) || decoded.TypeID != 700 || decoded.Fields[fieldIdxByCS(row.WireClass, csNumericID)].U32() != uint32(row.NumericID) {
			t.Fatalf("%s native item roundtrip failed: %v", key, err)
		}
	}
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bell, err := srv.CreateItem(ctx, uid, "feeder_bell", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mounted := mountGearForTest(t, srv, newProbeSess(srv), rod.GUID, bell.GUID)
	fresh := mustItem(t, db, mounted.MountedGUID)
	if fresh.WireClass != "eebdgfednim" || fresh.RigGUID != rod.GUID || rigStoredGUID(t, fresh, csParent) != rod.GUID {
		t.Fatal("bell lost native class or mount parent")
	}
	comps := rigCompsOf(rigStoredF5(t, mustItem(t, db, rod.GUID)))
	_, component, found := rigCompKeyForGUID(comps, fresh.GUID)
	if !found || component.Category != 47 || component.Extra != 100 {
		t.Fatalf("bottom alarm slot missing: %+v", component)
	}
	runRodRemove(t, srv, newProbeSess(srv), rod.GUID, 47, fresh.GUID, 100)
	detached := mustItem(t, db, fresh.GUID)
	if detached.WireClass != "eebdgfednim" || detached.Container != "backpack" || detached.RigGUID != "" || rigStoredGUID(t, detached, csParent) != "" {
		t.Fatal("detached bell lost its type or retained the mount parent")
	}
}

func TestRepairLegacyBiteAlarmPreservesAttachedIdentity(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	def, err := db.GetItemDefByItemID(ctx, "feeder_bell")
	if err != nil {
		t.Fatal(err)
	}
	obj := wiregen.MustMakeObj(fallbackWireClass, nil)
	guid := NewItemGUID()
	obj.Fields[fieldIdxByCS(fallbackWireClass, csGUID)].Hex = guid
	obj.Fields[fieldIdxByCS(fallbackWireClass, csNumericID)].Num = ptrU64(uint64(def.NumericID))
	fields, err := FieldsToJSON(obj.Fields)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &store.Item{GUID: guid, UID: uid, ItemID: def.ItemID, NumericID: def.NumericID, WireClass: fallbackWireClass, TypeID: fallbackTypeID, Container: "backpack", Qty: 1, Durability: .7, Workmanship: .9, FieldsJSON: fields}
	if err := db.InsertItem(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	mounted := mountGearForTest(t, srv, newProbeSess(srv), rod.GUID, guid)
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	repaired := mustItem(t, db, mounted.MountedGUID)
	if repaired.WireClass != "eebdgfednim" || repaired.TypeID != 700 {
		t.Fatal("legacy alarm still a fishing line")
	}
	if repaired.RigGUID != rod.GUID || repaired.Container != "rig" || rigStoredGUID(t, repaired, csParent) != rod.GUID || repaired.Durability != .7 || repaired.Workmanship != .9 || repaired.Qty != 1 {
		t.Fatalf("repair changed attached identity: %+v", repaired)
	}
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	again := mustItem(t, db, repaired.GUID)
	if *again != *repaired {
		t.Fatal("repeated repair changed alarm")
	}
}

func TestRepairLegacyBiteAlarmDoesNotRestoreBrokenOrExhaustedItem(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	def, err := db.GetItemDefByItemID(ctx, "feeder_bell")
	if err != nil {
		t.Fatal(err)
	}
	row := &store.Item{GUID: NewItemGUID(), UID: 1000001, ItemID: def.ItemID, NumericID: def.NumericID, WireClass: fallbackWireClass, TypeID: fallbackTypeID, Container: "backpack", Qty: 0, Durability: 0, Workmanship: 0, FieldsJSON: "[]"}
	if err := db.InsertItem(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := mustItem(t, db, row.GUID)
	if fresh.WireClass != "eebdgfednim" || fresh.Qty != 0 || fresh.Durability != 0 || fresh.Workmanship != 0 {
		t.Fatal("repair restored consumed/broken bell")
	}
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	again := mustItem(t, db, row.GUID)
	if *again != *fresh {
		t.Fatal("repeated exhausted bell repair changed data")
	}
}
