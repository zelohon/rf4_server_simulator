package gameserver

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
)

func TestRollShovelBaitLootBounds(t *testing.T) {
	for seed := int64(1); seed <= 500; seed++ {
		rng := rand.New(rand.NewSource(seed))
		loot := rollShovelBaitLoot(rng)
		if len(loot) < 1 || len(loot) > 3 {
			t.Fatalf("seed=%d 种类=%d, want 1..3", seed, len(loot))
		}
		seen := map[string]bool{}
		for _, item := range loot {
			if item.ItemID == "" || seen[item.ItemID] {
				t.Fatalf("seed=%d 诱饵重复或为空: %+v", seed, loot)
			}
			seen[item.ItemID] = true
			if item.Qty < 1 || item.Qty > 5 {
				t.Fatalf("seed=%d %s 数量=%d, want 1..5", seed, item.ItemID, item.Qty)
			}
		}
	}
}

func TestIsShovelItemID(t *testing.T) {
	for _, itemID := range []string{"tool_shovel", "shovel_ny25", "tool_scoop"} {
		if !isShovelItemID(itemID) {
			t.Fatalf("%s 应识别为铲子", itemID)
		}
	}
	for _, itemID := range []string{"", "tool_pliers", "worm_01"} {
		if isShovelItemID(itemID) {
			t.Fatalf("%s 不应识别为铲子", itemID)
		}
	}
}

func TestAwardBaitCatchSkillProficiency(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	gain := srv.awardSkillProficiencyWithRolls(ctx, uid, baitCatchSkillCat, 0, 0)
	if math.Abs(gain-0.2) > 1e-9 {
		t.Fatalf("首次捕获诱饵熟练度增量=%v, want 0.2", gain)
	}
	pts, err := db.GetSkillPoints(ctx, uid, baitCatchSkillCat)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(pts-0.2) > 1e-9 {
		t.Fatalf("捕获诱饵熟练度=%v, want 0.2", pts)
	}

	if _, err := db.SQL.ExecContext(ctx, `INSERT OR REPLACE INTO skill(uid, skill_id, points) VALUES(?,?,100)`, uid, baitCatchSkillCat); err != nil {
		t.Fatal(err)
	}
	if gain := srv.awardSkillProficiencyWithRolls(ctx, uid, baitCatchSkillCat, 0, 0); gain != 0 {
		t.Fatalf("满熟练度增量=%v, want 0", gain)
	}
}

func TestBaitScoopDigIsHandled(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	scoop, err := srv.CreateItem(ctx, uid, "tool_scoop", "backpack", 1)
	if err != nil {
		t.Fatalf("创建红虫长柄勺失败: %v", err)
	}
	body, ok := hCraftMine2(&Ctx{
		Sess: newProbeSess(srv), Main: 9, Sub: 3,
		Tree: proto.Args524(proto.Guid(scoop.GUID)), DecodeOK: true,
		Log: logging.Discard(),
	})
	if !ok || len(body) == 0 {
		t.Fatal("9/3 红虫长柄勺挖饵未处理")
	}
	v := decodeTreeValue(t, srv, body)
	if got := u64Of(v.Field(0).Num); got != 1 {
		t.Fatalf("803.f0=%d, want 1", got)
	}
	kept, err := db.GetItemByGUID(ctx, scoop.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if kept == nil || kept.ItemID != "tool_scoop" {
		t.Fatal("使用红虫长柄勺后工具不应被删除")
	}
}

func TestShovelDigReturnsBaitsAndKeepsShovel(t *testing.T) {
	srv, db := newDetachTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	shovel, err := srv.CreateItem(ctx, uid, "tool_shovel", "backpack", 1)
	if err != nil {
		t.Fatalf("创建铲子失败: %v", err)
	}
	body, ok := hCraftMine2(&Ctx{
		Sess: newProbeSess(srv), Main: 9, Sub: 3,
		Tree: proto.Args524(proto.Guid(shovel.GUID)), DecodeOK: true,
		Log: logging.Discard(),
	})
	if !ok || len(body) == 0 {
		t.Fatal("9/3 铲子挖饵未处理")
	}

	v := decodeTreeValue(t, srv, body)
	if v.TypeID != 803 {
		t.Fatalf("响应类型=%d, want 803", v.TypeID)
	}
	if got := u64Of(v.Field(0).Num); got != 1 {
		t.Fatalf("803.f0=%d, want 1", got)
	}
	produced := v.Field(1).Val
	if produced == nil || produced.ArgCount() < 1 || produced.ArgCount() > 3 {
		t.Fatalf("803.f1 种类=%v, want 1..3", produced)
	}
	upsert := v.Field(6).Val
	if upsert == nil || upsert.ArgCount() != produced.ArgCount() {
		t.Fatalf("803.f6 背包增量=%v, want %d 件", upsert, produced.ArgCount())
	}
	progress := v.Field(2).Val
	if progress == nil || progress.Class != "kegeppimjbm" || u32Of(progress.Field(2).Num) != baitCatchSkillCat {
		t.Fatalf("803.f2 应为捕获诱饵 310 进度块: %v", progress)
	}
	gain := float64(proto.F32FromBits(u32Of(progress.Field(3).Num)))
	pts, err := db.GetSkillPoints(ctx, uid, baitCatchSkillCat)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(pts-gain) > 1e-6 {
		t.Fatalf("捕获诱饵熟练度 DB=%v, 803 增量=%v", pts, gain)
	}
	skillTree := v.Field(3).Val
	if skillTree == nil || skillTree.Class != "kmepameiibi" || u32Of(skillTree.Field(0).Num) != baitCatchSkillCat {
		t.Fatalf("803.f3 应为捕获诱饵技能树: %v", skillTree)
	}

	rows, err := db.SQL.QueryContext(ctx, `SELECT item_id, qty FROM item
		WHERE uid=? AND container='backpack' AND item_id<>? ORDER BY item_id`, uid, shovel.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var itemID string
		var qty int64
		if err := rows.Scan(&itemID, &qty); err != nil {
			t.Fatal(err)
		}
		got[itemID] = qty
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) < 1 || len(got) > 3 {
		t.Fatalf("背包新增诱饵种类=%d, want 1..3", len(got))
	}
	if len(got) != produced.ArgCount() {
		t.Fatalf("803.f1 种类=%d, DB 种类=%d", produced.ArgCount(), len(got))
	}
	allowed := map[string]bool{}
	for _, drop := range shovelBaitDrops {
		allowed[drop.ItemID] = true
	}
	for itemID, qty := range got {
		if !allowed[itemID] {
			t.Fatalf("挖出未授权诱饵 %s", itemID)
		}
		if qty < 1 || qty > 5 {
			t.Fatalf("%s 数量=%d, want 1..5", itemID, qty)
		}
	}

	kept, err := db.GetItemByGUID(ctx, shovel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if kept == nil || kept.ItemID != "tool_shovel" {
		t.Fatal("使用铲子后铲子不应被删除")
	}
}
