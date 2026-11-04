package gameserver

import (
	"context"
	"math"
	"rf4server/internal/store"
	"slices"
	"testing"

	"rf4server/internal/proto"
)

func rigTestCategoryComps(t *testing.T, f5 *proto.Value, category int64) []rigComp {
	t.Helper()
	comps := rigCompsOf(f5)
	var out []rigComp
	for _, key := range rigCompKeysOfCategory(comps, category) {
		out = append(out, comps[key])
	}
	return out
}

func rigTestFieldFloat(t *testing.T, f proto.Field) float64 {
	t.Helper()
	if f.Num != nil {
		return float64(proto.F32FromBits(uint32(*f.Num)))
	}
	return f.Float
}

func TestRigMultiSlotSameCategoryAndExactDetach(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 24) { // BottomHair 有双沉子槽
		t.Fatal("设置双沉子钓组类型失败")
	}
	sinkA, err := srv.CreateItem(ctx, uid, "arsinker_30076", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sinkB, err := srv.CreateItem(ctx, uid, "arsinker_30077", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{sinkA.GUID, sinkB.GUID} {
		res := srv.rigSetComponent(ctx, uid, rod.GUID, 0, item, 0,
			rigSetOpts{ParentType: familyRod, EnforceCategory: true})
		if res.Rejected {
			t.Fatalf("装配沉子 %s 被拒", shortGUID(item))
		}
	}

	rodRow, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || rodRow == nil {
		t.Fatal(err)
	}
	f5comps := rigTestCategoryComps(t, rigStoredF5(t, rodRow), 29)
	if len(f5comps) != 2 || f5comps[0].GUID != sinkA.GUID || f5comps[1].GUID != sinkB.GUID {
		t.Fatalf("f5 应保留两件同品类沉子，实得 %+v", f5comps)
	}
	if f5comps[0].Ordinal != 0 || f5comps[1].Ordinal != 1 {
		t.Fatalf("f5 ordinal 应为 0/1，实得 %+v", f5comps)
	}
	rows, err := db.ListRigComponents(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	var sinkRows []int64
	for _, row := range rows {
		if row.Slot == 29 {
			if row.Extra != 200 {
				t.Fatalf("沉子 canonical extra 应 200，实得 %d", row.Extra)
			}
			sinkRows = append(sinkRows, row.Ordinal)
		}
	}
	if len(sinkRows) != 2 || sinkRows[0] != 0 || sinkRows[1] != 1 {
		t.Fatalf("rig_component 应保存 ordinal 0/1，实得 %+v", sinkRows)
	}

	// 删除派生投影后再拆第二件，验证拆卸依赖实例组件而不是派生索引。
	if err := db.ReplaceRigComponents(ctx, rod.GUID, nil); err != nil {
		t.Fatal(err)
	}
	res := srv.rigRemoveItemComponent(ctx, uid, rod.GUID, sinkB.GUID)
	if res == nil || res.CompGUID != sinkB.GUID {
		t.Fatalf("应按 GUID 精确拆下第二件沉子，实得 %+v", res)
	}
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	remaining := rigTestCategoryComps(t, rigStoredF5(t, rodRow), 29)
	if len(remaining) != 1 || remaining[0].GUID != sinkA.GUID {
		t.Fatalf("拆第二件不得影响第一件，实得 %+v", remaining)
	}
	rowB, _ := db.GetItemByGUID(ctx, sinkB.GUID)
	if rowB == nil || rowB.Container != "backpack" || rowB.RigGUID != "" {
		t.Fatalf("第二件沉子应退回背包，实得 %+v", rowB)
	}
	rowA, _ := db.GetItemByGUID(ctx, sinkA.GUID)
	if rowA == nil || rowA.Container != "rig" || rowA.RigGUID != rod.GUID {
		t.Fatalf("第一件沉子应保持挂载，实得 %+v", rowA)
	}
	rows, _ = db.ListRigComponents(ctx, rod.GUID)
	rows = slices.DeleteFunc(rows, func(row *store.RigComponent) bool { return row.Slot != 29 })
	if len(rows) != 1 || rows[0].GUID != sinkA.GUID || rows[0].Ordinal != 0 {
		t.Fatalf("派生表回算后只应剩第一件，实得 %+v", rows)
	}

	// 装回第二件后执行 remove_all；ordinal 重排时仍必须拆掉两件。
	srv.rigSetComponent(ctx, uid, rod.GUID, 0, sinkB.GUID, 200,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	results := srv.rigRemoveAll(ctx, uid, rod.GUID)
	if len(results) < 2 {
		t.Fatalf("remove_all 应返回两件沉子，实得 %+v", results)
	}
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	if got := rigTestCategoryComps(t, rigStoredF5(t, rodRow), 29); len(got) != 0 {
		t.Fatalf("remove_all 后 f5 不应剩沉子，实得 %+v", got)
	}
	for _, guid := range []string{sinkA.GUID, sinkB.GUID} {
		row, _ := db.GetItemByGUID(ctx, guid)
		if row == nil || row.Container != "backpack" || row.RigGUID != "" {
			t.Fatalf("remove_all 后 %s 应回背包，实得 %+v", shortGUID(guid), row)
		}
	}
}

func TestRigProjectionCanonicalExtraDropsGhosts(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := srv.CreateItem(ctx, uid, "arsinker_30076", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 24) {
		t.Fatal("设置钓组类型失败")
	}
	srv.rigSetComponent(ctx, uid, rod.GUID, 0, sink.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	if err := db.UpsertRigComponentAt(ctx, rod.GUID, 29, 7, "ghost-item", 999); err != nil {
		t.Fatal(err)
	}
	rigType := int64(24)
	if !srv.rigItemMutate(ctx, rod.GUID, nil, nil, &rigType) {
		t.Fatal("触发 f5 投影重建失败")
	}
	rows, err := db.ListRigComponents(ctx, rod.GUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.GUID == "ghost-item" {
			t.Fatal("ghost projection survived")
		}
	}
	rows = slices.DeleteFunc(rows, func(row *store.RigComponent) bool { return row.Slot != 29 })
	if len(rows) != 1 || rows[0].GUID != sink.GUID || rows[0].Slot != 29 || rows[0].Extra != 200 {
		t.Fatalf("投影应由 f5 全量回算并删除 ghost，实得 %+v", rows)
	}
}

func TestExplicitRigTypeIsNotOverwrittenByCompatibilityInference(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10126_FD390", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 12) {
		t.Fatal("显式选择简易水底失败")
	}
	cage, err := srv.CreateItem(ctx, uid, "feeder_30037", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	srv.rigSetComponent(ctx, uid, rod.GUID, 0, cage.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true})
	row, _ := db.GetItemByGUID(ctx, rod.GUID)
	_, got, ok := rigIdentity(rigStoredF5(t, row))
	if !ok || got != 12 {
		t.Fatalf("玩家显式选择的 rig_type=12 不应被组件推断覆盖，实得 %d", got)
	}
}

func TestWearPersistsColumnFieldsAndVersion(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	line, err := srv.CreateItem(ctx, uid, "mono_15097", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := srv.CreateItem(ctx, uid, "clas_20163", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	lineBefore, _ := db.GetItemByGUID(ctx, line.GUID)
	hookBefore, _ := db.GetItemByGUID(ctx, hook.GUID)
	if err := db.UpsertRigComponentAt(ctx, "rod-wear", catLine, 0, line.GUID, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRigComponentAt(ctx, "rod-wear", catHook, 0, hook.GUID, 0); err != nil {
		t.Fatal(err)
	}

	lineFieldsBefore, _ := FieldsFromJSON(lineBefore.FieldsJSON)
	lineVerIdx := fieldIdxByCS(lineBefore.WireClass, csCreated)
	lineVerBefore := u64Of(lineFieldsBefore[lineVerIdx].Num)
	srv.wearRigForCatchPersisted(ctx, "rod-wear")

	lineAfter, _ := db.GetItemByGUID(ctx, line.GUID)
	hookAfter, _ := db.GetItemByGUID(ctx, hook.GUID)
	if math.Abs(lineAfter.Durability-(lineBefore.Durability-wearPerCatch["line"])) > 1e-9 {
		t.Fatalf("鱼线列耐久磨损错误: %v -> %v", lineBefore.Durability, lineAfter.Durability)
	}
	if math.Abs(hookAfter.Durability-(hookBefore.Durability-wearPerCatch["hook"])) > 1e-9 {
		t.Fatalf("鱼钩列耐久磨损错误: %v -> %v", hookBefore.Durability, hookAfter.Durability)
	}
	lineFieldsAfter, _ := FieldsFromJSON(lineAfter.FieldsJSON)
	gotD := rigTestFieldFloat(t, lineFieldsAfter[fieldIdxByCS(lineAfter.WireClass, csDurability)])
	if math.Abs(gotD-lineAfter.Durability) > 1e-6 {
		t.Fatalf("fields_json 耐久未同步: column=%v field=%v", lineAfter.Durability, gotD)
	}
	if gotVer := u64Of(lineFieldsAfter[lineVerIdx].Num); gotVer <= lineVerBefore {
		t.Fatalf("磨损后版本戳应严格递增: %d -> %d", lineVerBefore, gotVer)
	}
}
