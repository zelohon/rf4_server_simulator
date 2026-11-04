package gameserver

import (
	"context"
	"reflect"
	"testing"
)

func TestCombinationBaitMountReplaceDetachAndRestore(t *testing.T) {
	s, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := s.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	mount := func(id string, index int64) string {
		t.Helper()
		item, err := s.CreateItem(ctx, uid, id, "backpack", 5)
		if err != nil {
			t.Fatal(err)
		}
		result := s.rigSetComponent(ctx, uid, rod.GUID, 27, item.GUID, index, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
		if result.Rejected || result.MountedGUID == "" {
			t.Fatalf("mount %s rejected: %+v", id, result)
		}
		return result.MountedGUID
	}
	first := mount("worm_01", 350)
	second := mount("muckworm", 351)
	check := func(ids []string, ordinals []int64) {
		t.Helper()
		// 丢弃派生索引再恢复，验证实例、存档与投影使用同一饵位。
		if err := db.ReplaceRigComponents(ctx, rod.GUID, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.reconcileRigProjection(ctx, rod.GUID); err != nil {
			t.Fatal(err)
		}
		var gotIDs []string
		comps := s.storedRigComponents(ctx, rod.GUID)
		for _, key := range rigCompKeysOfCategory(comps, 27) {
			item, err := db.GetItemByGUID(ctx, comps[key].GUID)
			if err != nil || item == nil {
				t.Fatalf("mounted bait=%v err=%v", item, err)
			}
			gotIDs = append(gotIDs, item.ItemID)
		}
		if !reflect.DeepEqual(gotIDs, ids) {
			t.Fatalf("current baits=%v want=%v", gotIDs, ids)
		}
		rows, err := db.ListRigComponents(ctx, rod.GUID)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, row := range rows {
			if row.Slot == 27 {
				got = append(got, row.Ordinal)
				if row.Extra != 350+row.Ordinal {
					t.Fatalf("bait index drifted: %+v", row)
				}
			}
		}
		if !reflect.DeepEqual(got, ordinals) {
			t.Fatalf("ordinals=%v want=%v", got, ordinals)
		}
	}
	check([]string{"worm_01", "muckworm"}, []int64{0, 1})
	// 普通换饵不能误替换副饵，也不能自动增加第三种饵。
	third := mount("fly", 0)
	check([]string{"fly", "muckworm"}, []int64{0, 1})
	if row, _ := db.GetItemByGUID(ctx, first); row != nil && row.RigGUID == rod.GUID {
		t.Fatal("old primary bait remained mounted")
	}
	if result := s.rigRemoveItemComponent(ctx, uid, rod.GUID, third); result == nil {
		t.Fatal("primary bait detach failed")
	}
	check([]string{"muckworm"}, []int64{1})
	mount("worm_01", 350)
	check([]string{"worm_01", "muckworm"}, []int64{0, 1})
	if result := s.rigRemoveItemComponent(ctx, uid, rod.GUID, second); result == nil {
		t.Fatal("secondary bait detach failed")
	}
	check([]string{"worm_01"}, []int64{0})
}
