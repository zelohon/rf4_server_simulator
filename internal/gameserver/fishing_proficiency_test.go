package gameserver

import (
	"context"
	"math"
	"testing"
)

func TestFishingSkillCatForRig(t *testing.T) {
	cases := []struct {
		rig  int64
		want int64
	}{
		{1, 1},   // 浮钓
		{4, 1},   // 浮钓活饵
		{5, 2},   // 路亚/纺车
		{7, 2},   // Texas 属于路亚族
		{12, 3},  // 沉底
		{34, 3},  // 固定 Zig
		{50, 11}, // 海钓
		{59, 11},
		{99, 1}, // 未知识别为浮钓
	}
	for _, c := range cases {
		if got := fishingSkillCatForRig(c.rig); got != c.want {
			t.Fatalf("fishingSkillCatForRig(%d)=%d, want %d", c.rig, got, c.want)
		}
	}
}

func TestFishingProficiencyGainFormula(t *testing.T) {
	if p := fishingProficiencyGainChance(0); math.Abs(p-0.9) > 1e-9 {
		t.Fatalf("0%% 增长概率=%v, want 0.9", p)
	}
	if p := fishingProficiencyGainChance(25); math.Abs(p-0.45) > 1e-9 {
		t.Fatalf("25%% 增长概率=%v, want 0.45", p)
	}
	if p := fishingProficiencyGainChance(50); math.Abs(p-0.3) > 1e-9 {
		t.Fatalf("50%% 增长概率=%v, want 0.3", p)
	}
	if p := fishingProficiencyGainChance(75); math.Abs(p-0.225) > 1e-9 {
		t.Fatalf("75%% 增长概率=%v, want 0.225", p)
	}
	if p := fishingProficiencyGainChance(100); p != 0 {
		t.Fatalf("满熟练度增长概率=%v, want 0", p)
	}

	cases := []struct {
		name              string
		current, gainRoll float64
		amountRoll        float64
		want              float64
	}{
		{"低熟练度命中0.2", 0, 0.89, 0.10, 0.2},
		{"低熟练度命中0.1", 0, 0.89, 0.85, 0.1},
		{"低熟练度未命中", 0, 0.90, 0.00, 0},
		{"中熟练度命中0.2", 50, 0.29, 0.10, 0.2},
		{"中熟练度命中0.1", 50, 0.29, 0.60, 0.1},
		{"中熟练度未命中", 50, 0.30, 0.00, 0},
		{"满熟练度永不加", 100, 0.00, 0.00, 0},
	}
	for _, c := range cases {
		got := fishingProficiencyGainForRoll(c.current, c.gainRoll, c.amountRoll)
		if math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFishingProficiencyCategoryAndCap(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)

	cases := []struct {
		rig int64
		cat int64
	}{
		{1, 1},
		{5, 2},
		{12, 3},
		{55, 11},
	}
	for _, c := range cases {
		if _, err := db.SQL.ExecContext(ctx,
			`INSERT OR REPLACE INTO skill(uid, skill_id, points) VALUES(?,?,100)`, uid, c.cat); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			gotCat, gain := srv.awardFishingProficiency(ctx, uid, c.rig)
			if gotCat != c.cat || gain != 0 {
				t.Fatalf("rig=%d: got cat=%d gain=%v, want cat=%d gain=0", c.rig, gotCat, gain, c.cat)
			}
		}
		pts, err := db.GetSkillPoints(ctx, uid, c.cat)
		if err != nil {
			t.Fatal(err)
		}
		if pts != profCap {
			t.Fatalf("cat=%d 满熟练度被改变为 %v", c.cat, pts)
		}
	}
}

func TestSettleCatchXPSkillFollowsActualRigAtCap(t *testing.T) {
	srv, db := newProgressSrv(t)
	ctx := context.Background()
	const uid = int64(1000001)
	for _, cat := range []int64{1, 2, 3, 11} {
		if _, err := db.SQL.ExecContext(ctx,
			`INSERT OR REPLACE INTO skill(uid, skill_id, points) VALUES(?,?,100)`, uid, cat); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		rig int64
		cat int64
	}{{1, 1}, {5, 2}, {12, 3}, {55, 11}} {
		f := &RolledFish{
			FishGUID: newFishGUID(), Species: "perch", Grade: 2, WeightG: 642,
			LengthM: 0.28, LocLevel: "level_001_lake",
		}
		r := srv.settleCatchXP(ctx, uid, f, c.rig)
		if r.skillID != c.cat || r.skillGain != 0 {
			t.Fatalf("rig=%d: got skill=%d gain=%v, want skill=%d gain=0", c.rig, r.skillID, r.skillGain, c.cat)
		}
	}
}
