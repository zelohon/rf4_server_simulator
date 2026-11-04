package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/store"
)

func TestCastBaseDistanceProfiles(t *testing.T) {
	cases := []struct {
		name    string
		rigType int64
		want    float32
	}{
		{name: "float", rigType: 1, want: 7.5461488},
		{name: "bottom", rigType: 12, want: 7.5461488},
		{name: "lure", rigType: 5, want: 7.1598907},
	}
	for _, tc := range cases {
		got := math.Float32frombits(castBaseDistanceBits(tc.rigType))
		if math.Abs(float64(got-tc.want)) > 1e-6 {
			t.Fatalf("%s: got %.7f want %.7f", tc.name, got, tc.want)
		}
	}
}

func TestReelCastFrictionImprovesWithBearingOilAndDurability(t *testing.T) {
	wornBasic := reelCastFriction(10, 15, 0.4)
	newBasic := reelCastFriction(10, 15, 1.0)
	premium := reelCastFriction(100, 100, 1.0)
	if !(wornBasic > newBasic && newBasic > premium) {
		t.Fatalf("摩擦系数应随轴承/油品/耐久改善而下降: worn=%f basic=%f premium=%f",
			wornBasic, newBasic, premium)
	}
	if math.Abs(premium-0.05) > 1e-9 {
		t.Fatalf("满品质轮子摩擦=%f，期望0.05", premium)
	}
}

func TestCastWeightAndSkillFactors(t *testing.T) {
	light := rodTestWeightFactor(7, 2, 10)
	medium := rodTestWeightFactor(18, 2, 10)
	heavy := rodTestWeightFactor(40, 2, 10)
	if !(medium > light && heavy > medium) {
		t.Fatalf("其他参数不变时载荷越重应抛得越远: 7g=%f 18g=%f 40g=%f", light, medium, heavy)
	}
	if !(castSkillFactor(100, 0) > castSkillFactor(50, 0) &&
		castSkillFactor(50, 10) > castSkillFactor(50, 0)) {
		t.Fatal("熟练度和已学习技能节点应提高抛投效率")
	}
}

func TestCastHeavierPayloadRemainsMonotonic(t *testing.T) {
	eighteen := rodTestWeightFactor(18, 2, 10)
	forty := rodTestWeightFactor(40, 2, 10)
	hundred := rodTestWeightFactor(100, 2, 10)
	if !(eighteen < forty && forty < hundred) {
		t.Fatalf("超 Test 后应继续单调增加: 18g=%f 40g=%f 100g=%f", eighteen, forty, hundred)
	}
	if hundred <= 1 {
		t.Fatalf("重量系数不应在1.0封顶，100g实得%f", hundred)
	}
	for _, payload := range []float64{7, 18, 40, 400, 4000} {
		d, factors := calculateCastDistance(CastDistanceContext{
			RigType: 5, CastAction: 1, CastPower: 1,
			RodTestMinG: 2, RodTestMaxG: 10, PayloadWeightG: payload,
		})
		if math.IsNaN(d) || math.IsInf(d, 0) || d < castMinPoweredM {
			t.Fatalf("载荷 %.0fg 计算异常: distance=%f factors=%+v", payload, d, factors)
		}
	}
}

func castCalibrationDistance(payloadG float64, spoolSize int64) float64 {
	d, _ := calculateCastDistance(CastDistanceContext{
		RigType: 5, CastAction: 1, CastPower: 1,
		RodLengthM: 3.0, RodTaper: 0.5, RodTestMinG: 5, RodTestMaxG: 30,
		PayloadWeightG: payloadG, ReelEfficiency: 0.95, ReelType: 1,
		SpoolSize: spoolSize, SpoolFill: 0.8, LineDiameterMM: 0.20,
		LineStretch: 0.001, LineMaterial: 3,
		PlayerPrimary: defaultPlayerConditionPrimary, PlayerSecondary: defaultPlayerConditionSecondary,
		SkillMastery: 50,
	})
	return d
}

func TestCastCalibrationBands(t *testing.T) {
	for _, payload := range []float64{8, 10} {
		largeSpool := castCalibrationDistance(payload, 5) // 6000
		smallSpool := castCalibrationDistance(payload, 2) // 2000
		if largeSpool < 28 || largeSpool > 31 {
			t.Fatalf("6000线杯 %.0fg 距离=%0.2f，期望28～30米区间", payload, largeSpool)
		}
		if smallSpool < 32 || smallSpool > 36.5 {
			t.Fatalf("2000线杯 %.0fg 距离=%0.2f，期望32～36米区间", payload, smallSpool)
		}
		if smallSpool <= largeSpool+2 {
			t.Fatalf("小线杯应比6000线杯远至少2米: 小杯=%0.2f 大杯=%0.2f", smallSpool, largeSpool)
		}
	}
}

func TestCastHighEndGearCanReachFortyFiveMeters(t *testing.T) {
	d, _ := calculateCastDistance(CastDistanceContext{
		RigType: 5, CastAction: 1, CastPower: 1,
		RodLengthM: 4.2, RodTaper: 1.0, RodTestMinG: 5, RodTestMaxG: 30,
		PayloadWeightG: 8, ReelEfficiency: 0.95, ReelType: 1,
		SpoolSize: 1, SpoolFill: 1.0, LineDiameterMM: 0.10,
		LineStretch: 0.001, LineMaterial: 3,
		PlayerPrimary: defaultPlayerConditionPrimary, PlayerSecondary: defaultPlayerConditionSecondary,
		SkillMastery: 100, SkillNodeEffect: 10,
	})
	if d < 44.9 {
		t.Fatalf("高阶装备应能进入45米精准抛投区间，实得%0.2f", d)
	}
}

func TestCastDynamicModifiersAreMonotonic(t *testing.T) {
	if !(castPowerFactor(1) > castPowerFactor(0.5) && castPowerFactor(0.5) > castPowerFactor(0.1)) {
		t.Fatal("抛投力度应单调增加距离")
	}
	if !(castLineDiameterFactor(0.12) > castLineDiameterFactor(0.40)) {
		t.Fatal("更粗主线应缩短距离")
	}
	if !(castLineStretchFactor(0.001) > castLineStretchFactor(0.87)) {
		t.Fatal("高拉伸主线应降低能量传递效率")
	}
	if !(castLineMaterialFactor(3) > castLineMaterialFactor(2)) {
		t.Fatal("编织线低拉伸材质应优于高拉伸材质")
	}
	if !(castLineMassFactor(1) > castLineMassFactor(20)) {
		t.Fatal("更重的运行主线应缩短距离")
	}
	if !(castRodTaperFactor(0.9) > castRodTaperFactor(0.1)) {
		t.Fatal("快调竿梢应小幅提高能量传递")
	}
}

func TestPlayerConditionUsesDatabase(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := db.UpsertPlayerCondition(ctx, store.PlayerCondition{
		UID: uid, Primary: 0.62, Secondary: 0.71, UpdatedTS: 123,
	}); err != nil {
		t.Fatal(err)
	}
	primary, secondary := srv.castPlayerCondition(ctx, uid)
	if math.Abs(primary-0.62) > 1e-9 || math.Abs(secondary-0.71) > 1e-9 {
		t.Fatalf("玩家状态未从 SQLite 读取: primary=%f secondary=%f", primary, secondary)
	}
	got, err := db.EnsurePlayerCondition(ctx, uid)
	if err != nil || got == nil || math.Abs(got.Primary-0.62) > 1e-9 || math.Abs(got.Secondary-0.71) > 1e-9 {
		t.Fatalf("玩家状态未持久化: got=%+v err=%v", got, err)
	}
}

func TestCastDistanceUsesMountedReelAndClip(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	line, err := srv.CreateItem(ctx, uid, "mono_40853", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rigSetType(ctx, uid, rod.GUID, 16) {
		t.Fatal("设置底钓 Method 钓组失败")
	}
	feeder, err := srv.CreateItem(ctx, uid, "feeder_30037", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, feeder.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, bait.GUID)
	if err := db.AddSkillPoints(ctx, uid, 3, 50); err != nil {
		t.Fatal(err)
	}

	p := srv.castDistanceForRig(ctx, uid, rod.GUID, 16)
	if p.ReelGUID != reel.GUID || p.DistanceM <= 10 || p.Friction <= 0 || p.Efficiency >= 1 {
		t.Fatalf("真实轮子抛投参数未生效: %+v", p)
	}
	if p.BaitItemID != "worm_01" || p.BaitWeightG <= 0 {
		t.Fatalf("当前饵未与抛投载荷对齐: bait=%q weight=%f", p.BaitItemID, p.BaitWeightG)
	}
	if p.RodLengthM <= 0 || p.RodTestMaxG <= 0 || p.RigWeightG <= 0 || p.BaitWeightG <= 0 ||
		p.PayloadWeightG <= p.BaitWeightG || p.SkillCategory != 3 || math.Abs(p.SkillMastery-50) > 1e-9 ||
		p.LineDiameterMM <= 0 || p.LineLengthM <= 0 || p.LineLinearDensityGPM <= 0 || p.LineRunningWeightG <= 0 ||
		p.PlayerPrimary <= 0 || p.PlayerSecondary <= 0 {
		t.Fatalf("竿长/锥度/Test/主线/线组/饵/玩家状态/技能数据未完整进入模型: %+v", p)
	}
	perf, err := db.GetReelPerformance(ctx, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(perf.CastFriction-p.Friction) > 1e-9 || math.Abs(perf.CastEfficiency-p.Efficiency) > 1e-9 {
		t.Fatalf("抛投参数未落入 reel_performance: perf=%+v profile=%+v", perf, p)
	}
	if err := db.AddSkillPoints(ctx, uid, 3, 50); err != nil {
		t.Fatal(err)
	}
	mastered := srv.castDistanceForRig(ctx, uid, rod.GUID, 16)
	if mastered.DistanceM <= p.DistanceM || mastered.SkillMastery != 100 {
		t.Fatalf("熟练度未提高抛投距离: 50%%=%+v 100%%=%+v", p, mastered)
	}

	if err := db.UpsertRodSetting(ctx, rod.GUID, reelClipSettingKey, "18"); err != nil {
		t.Fatal(err)
	}
	clipped := srv.castDistanceForRig(ctx, uid, rod.GUID, 16)
	if math.Abs(clipped.DistanceM-18) > 1e-6 || clipped.ClipLimitM != 18 {
		t.Fatalf("卡线夹未限制抛投距离: %+v", clipped)
	}
}
