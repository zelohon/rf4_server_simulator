package gameserver

import (
	"context"
	"testing"
)

// TestBuildRig153CarriesBaitMount 锁定 153 对象 f8/f9（挂饵方式）的读写。
//
// f8 = 当前选中的安装方式，f9 = 可选安装方式位掩码。
// 两者都为 0 时安装弹窗空白且鱼饵全部不适配；只写 f9 时弹窗能开，
// 但安装栏一直显示「没选择」——所以两个字段都必须带上。
func TestBuildRig153CarriesBaitMount(t *testing.T) {
	rig := buildRig153Full("rig_BottomPopup", 26, nil, 100, 0, 64, 126)
	if sel, avail := rigBaitMountsOf(rig); sel != 64 || avail != 126 {
		t.Fatalf("f8/f9 = %d/%d，期望 64/126", sel, avail)
	}
	if sel, avail := rigBaitMountsOf(buildRig153WithClip("rig_BottomPopup", 26, nil, 100, 0)); sel != 0 || avail != 0 {
		t.Fatalf("不显式传入时 f8/f9 应为 0/0，实得 %d/%d", sel, avail)
	}
	rebuilt := buildRig153Like(rig, "rig_BottomPopup", 26, rigCompsOf(rig), 100, 0)
	if sel, avail := rigBaitMountsOf(rebuilt); sel != 64 || avail != 126 {
		t.Fatalf("模板重建后 f8/f9 = %d/%d，期望 64/126", sel, avail)
	}
	// 模板缺失时补默认可选项，弹窗不能再变空白。
	if _, avail := rigBaitMountsOf(buildRig153Like(nil, "rig_BottomPopup", 26, nil, 100, 0)); avail != defaultBaitMountAvail {
		t.Fatalf("空模板可选项 = %d，期望 %d", avail, defaultBaitMountAvail)
	}
	if got := rigBaitMountSelOf(nil); got != 0 {
		t.Fatalf("空对象 f8 应为 0，实得 %d", got)
	}
	if got := clampBaitMount(-5); got != 0 {
		t.Fatalf("负值应夹到 0，实得 %d", got)
	}
}

// TestApplyRigBaitMountPersists 验证 17/6 的落库路径：
// 选中值写 f8、可选项 f9 保持（缺失时补默认），重建钓组后两者都不丢。
func TestApplyRigBaitMountPersists(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	uid := int64(1000004)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil || rod == nil {
		t.Skip("静态库缺 feeder_10125_FD360: ", err)
	}
	if got := srv.baitMountFor(ctx, rod.GUID); got != 0 {
		t.Fatalf("未设置时 baitMountFor = %d，期望 0", got)
	}
	if err := srv.applyRigBaitMount(ctx, rod.GUID, 64); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.GetItemByGUID(ctx, rod.GUID)
	if err != nil || fresh == nil {
		t.Fatal("回读物品失败: ", err)
	}
	fields, err := FieldsFromJSON(fresh.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(fresh.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		t.Fatalf("竿实例缺钓组字段: %s", fresh.WireClass)
	}
	sel, avail := rigBaitMountsOf(fields[idx].Val)
	if sel != 64 {
		t.Fatalf("落库后 f8 = %d，期望 64", sel)
	}
	if avail != defaultBaitMountAvail {
		t.Fatalf("落库后可选项 f9 = %d，期望补默认 %d", avail, defaultBaitMountAvail)
	}

	if err := db.UpsertRodSetting(ctx, rod.GUID, baitMountSettingKey, "64"); err != nil {
		t.Fatal(err)
	}
	if got := srv.baitMountFor(ctx, rod.GUID); got != 64 {
		t.Fatalf("baitMountFor = %d，期望 64", got)
	}
}
