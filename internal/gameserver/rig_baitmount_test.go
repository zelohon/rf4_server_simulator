package gameserver

import (
	"context"
	"testing"
)

// TestBuildRig153CarriesBaitMount 锁定 153 对象 f9（挂饵方式位掩码）的读写。
//
// 客户端 BaitMountType 为 0 时安装栏为空、鱼饵全部不适配；
// 重建钓组必须把玩家选过的值带回去，不能每次都落 0。
func TestBuildRig153CarriesBaitMount(t *testing.T) {
	rig := buildRig153Full("rig_BottomPopup", 26, nil, 100, 0, 66)
	if got := rigBaitMountOf(rig); got != 66 {
		t.Fatalf("f9 = %d，期望 66", got)
	}
	if got := rigBaitMountOf(buildRig153WithClip("rig_BottomPopup", 26, nil, 100, 0)); got != 0 {
		t.Fatalf("不显式传入时 f9 应为 0，实得 %d", got)
	}
	rebuilt := buildRig153Full("rig_BottomPopup", 26, rigCompsOf(rig), 100, 0, rigBaitMountOf(rig))
	if got := rigBaitMountOf(rebuilt); got != 66 {
		t.Fatalf("重建后 f9 = %d，期望 66", got)
	}
	if got := rigBaitMountOf(nil); got != 0 {
		t.Fatalf("空对象 f9 应为 0，实得 %d", got)
	}
	if got := clampBaitMount(-5); got != 0 {
		t.Fatalf("负值应夹到 0，实得 %d", got)
	}
}

// TestApplyRigBaitMountPersists 验证 17/6 的落库路径：
// 写 instance.f5.f9 后重启重建仍保留，rod_setting 也能读回同一个值。
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
	if err := srv.applyRigBaitMount(ctx, rod.GUID, 6); err != nil {
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
	if got := rigBaitMountOf(fields[idx].Val); got != 6 {
		t.Fatalf("落库后 f9 = %d，期望 6", got)
	}

	if err := db.UpsertRodSetting(ctx, rod.GUID, baitMountSettingKey, "6"); err != nil {
		t.Fatal(err)
	}
	if got := srv.baitMountFor(ctx, rod.GUID); got != 6 {
		t.Fatalf("baitMountFor = %d，期望 6", got)
	}
}
