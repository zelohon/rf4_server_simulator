package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func reelPartByCategory(t *testing.T, srv *Server, rowGUID string, category int64) (*proto.Value, string) {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(context.Background(), rowGUID)
	if err != nil || row == nil {
		t.Fatalf("读取轮子失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fields {
		child := fields[i].Val
		def := srv.reelPartDef(context.Background(), child)
		if def != nil && def.Category == category {
			return child, def.ItemID
		}
	}
	t.Fatalf("轮子 %s 缺少品类 %d", row.ItemID, category)
	return nil, ""
}

func TestWorkshopExposesAllCompatibleReelParts(t *testing.T) {
	srv, _ := newRigTestServer(t)
	if n, _, err := srv.LoadCompatTable(); err != nil || n < 1000 {
		t.Fatalf("兼容表未完整加载: alternatives=%d err=%v", n, err)
	}
	ctx := context.Background()
	reel, err := srv.CreateItem(ctx, 1000001, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	parts := srv.repairPartsOf(ctx, reel)
	counts := map[uint32]int{}
	for _, part := range parts {
		counts[part.Cat] = len(srv.partAlternatives(ctx, reel.ItemID, part))
	}
	// Spark 2 2000S 的精确表含 6 个线杯、6 个齿轮、2 个摩擦片，
	// 再加当前零件自身；不得被旧的 3/4 个上限截断。
	if counts[8] != 7 {
		t.Fatalf("线杯工单=%d，期望当前件+6个兼容件", counts[8])
	}
	if counts[52] != 7 {
		t.Fatalf("齿轮工单=%d，期望当前件+6个兼容件", counts[52])
	}
	if counts[9] != 3 {
		t.Fatalf("摩擦片工单=%d，期望当前件+2个兼容件", counts[9])
	}
	if counts[10] != 21 {
		t.Fatalf("润滑油工单=%d，期望静态库全部21种品质油", counts[10])
	}
	if _, ok := counts[34]; !ok {
		t.Fatal("摇柄应出现在轮子零件诊断中")
	}
}

func TestWorkshopOrderRefreshesDragVersionAndPerformance(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}
	p, _ := db.EnsureProfile(ctx, uid)
	if err := db.UpdateBalance(ctx, uid, 1_000_000, p.Gold, p.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	beforeFields, _ := FieldsFromJSON(reel.FieldsJSON)
	createdIdx := fieldIdxByCS(reel.WireClass, csCreated)
	beforeVersion := u64Of(beforeFields[createdIdx].Num)

	order := wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
		0: proto.U32(9), 1: proto.StrVal("ef-fb-sp.orig-5805"),
	})
	req := proto.Args524(
		proto.StrVal("workshop_01"),
		wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
			0: proto.Guid(reel.GUID),
			1: proto.Args(proto.S("503"), order),
			2: proto.U32(0),
			3: proto.U64(100),
		}),
	)
	sess := newProbeSess(srv)
	hctx := &Ctx{Sess: sess, Main: 21, Sub: 3, Tree: req, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard()}
	body, ok := hWorkshopOrder(hctx)
	if !ok || len(body) == 0 {
		t.Fatal("21/3 更换摩擦片未返回更新物品")
	}
	if _, _, err := proto.DecodeValue(body, 0, srv.Table()); err != nil {
		t.Fatalf("21/3 响应不可解码: %v", err)
	}
	if len(hctx.afterResponse) != 1 {
		t.Fatalf("工作坊成功后应注册一次 4/22 刷新，实得 %d", len(hctx.afterResponse))
	}

	fresh, _ := db.GetItemByGUID(ctx, reel.GUID)
	freshFields, _ := FieldsFromJSON(fresh.FieldsJSON)
	if got := u64Of(freshFields[createdIdx].Num); got <= beforeVersion {
		t.Fatalf("物品版本戳未递增: %d -> %d", beforeVersion, got)
	}
	drag, itemID := reelPartByCategory(t, srv, reel.GUID, 9)
	if itemID != "ef-fb-sp.orig-5805" {
		t.Fatalf("摩擦片仍为 %s", itemID)
	}
	dragIdx := fieldIdxByCS(drag.Class, csDragForce)
	dragField := drag.Field(dragIdx)
	gotDrag := float64(proto.F32FromBits(dragField.U32()))
	if math.Abs(gotDrag-3.5) > 0.001 {
		t.Fatalf("改装摩擦力=%f，期望 3.5kg", gotDrag)
	}
	perf, err := db.GetReelPerformance(ctx, reel.GUID)
	if err != nil {
		t.Fatal(err)
	}
	wantLocked := 3.5 * 1.4 * reelBearingFactor(10)
	if math.Abs(perf.BaseDragKg-3.5) > 0.001 || math.Abs(perf.LockedDragKg-wantLocked) > 0.001 {
		t.Fatalf("轮子物理参数=%+v，期望 base=3.5 locked=%f", perf, wantLocked)
	}
	if perf.BearingItemID != "ef-sp_001_bb" || perf.BearingQuality != 10 ||
		perf.OilItemID != "ef_reel_oil" || perf.OilQuality != 15 {
		t.Fatalf("轴承/润滑油参数未写入物理表: %+v", perf)
	}
	if math.Abs(perf.CastFriction-reelCastFriction(10, 15, 1)) > 1e-9 ||
		math.Abs(perf.CastEfficiency-(1-perf.CastFriction)) > 1e-9 {
		t.Fatalf("轮子抛投摩擦参数未随工坊零件刷新: %+v", perf)
	}
}

func TestReelWorkshopStatsSurviveRelogTreeBuild(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}
	p, _ := db.EnsureProfile(ctx, uid)
	if err := db.UpdateBalance(ctx, uid, 1_000_000, p.Gold, p.Bank); err != nil {
		t.Fatal(err)
	}
	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}

	orders := proto.Args(proto.S("503"),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(9), 1: proto.StrVal("ef-fb-sp.orig-5805"),
		}),
		wiregen.MustMakeObj("gcllbdkgkna", map[int]*proto.Value{
			0: proto.U32(52), 1: proto.StrVal("rgm_zmn_jet_jsg-r"),
		}),
	)
	req := proto.Args524(
		proto.StrVal("workshop_01"),
		wiregen.MustMakeObj("nfmkdcbnkhp", map[int]*proto.Value{
			0: proto.Guid(reel.GUID),
			1: orders,
			2: proto.U32(0),
			3: proto.U64(100),
		}),
	)
	sess := newProbeSess(srv)
	hctx := &Ctx{Sess: sess, Main: 21, Sub: 3, Tree: req, DecodeOK: true,
		ExpectResponse: true, Log: logging.Discard()}
	if body, ok := hWorkshopOrder(hctx); !ok || len(body) == 0 {
		t.Fatal("21/3 更换摩擦片和齿轮失败")
	}

	fresh, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || fresh == nil {
		t.Fatalf("重新读取轮子失败: %v", err)
	}
	// 模拟下线再上线时重新构造 4/5 和 157 使用的物品树。
	tree, err := srv.ItemToTree(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	si := fieldIdxByCS(tree.Class, csStrength)
	if si < 0 || si >= tree.FieldCount() || tree.Fields[si].Num == nil {
		t.Fatal("轮子树缺少传动比字段")
	}
	gotRatio := float64(proto.F32FromBits(uint32(u64Of(tree.Fields[si].Num))))
	_, gearDef, _, ok := srv.selectedReelPart(ctx, tree.Fields, 52)
	if !ok || gearDef == nil || gearDef.ItemID != "rgm_zmn_jet_jsg-r" {
		t.Fatalf("重登后齿轮=%v ok=%v，期望保留JSG-R", gearDef, ok)
	}
	wantRatio := itemDefF32(gearDef, gearAttrRatio) * fresh.Workmanship
	if math.Abs(gotRatio-wantRatio) > 0.001 {
		t.Fatalf("重登后传动比=%f，期望保留改装值%f", gotRatio, wantRatio)
	}

	drag, dragDef, _, ok := srv.selectedReelPart(ctx, tree.Fields, 9)
	if !ok || dragDef == nil || dragDef.ItemID != "ef-fb-sp.orig-5805" {
		t.Fatalf("重登后摩擦片=%v ok=%v，期望保留5805", dragDef, ok)
	}
	di := fieldIdxByCS(drag.Class, csDragForce)
	if di < 0 || drag.Fields[di].Num == nil {
		t.Fatal("摩擦片缺少制动力字段")
	}
	gotDrag := float64(proto.F32FromBits(uint32(u64Of(drag.Fields[di].Num))))
	if math.Abs(gotDrag-3.5) > 0.001 {
		t.Fatalf("重登后摩擦力=%f，期望保留3.5kg", gotDrag)
	}

}

func TestReelOverloadUsesDurabilityStages(t *testing.T) {
	for durability, want := range map[float64]float64{
		1.0: 1.4, 0.8: 1.3, 0.6: 1.2, 0.4: 1.1, 0.2: 1.0,
	} {
		if got := reelLockMultiplier(durability); math.Abs(got-want) > 1e-9 {
			t.Fatalf("耐久 %.1f 倍率=%f，期望 %f", durability, got, want)
		}
	}
	for quality, want := range map[float64]float64{0: 0.90, 10: 0.91, 75: 0.975, 100: 1.0} {
		if got := reelBearingFactor(quality); math.Abs(got-want) > 1e-9 {
			t.Fatalf("轴承品质 %.0f 传力系数=%f，期望 %f", quality, got, want)
		}
	}
	for quality, want := range map[float64]float64{15: 1.175, 70: 0.90, 100: 0.75} {
		if got := reelOilWearFactor(quality); math.Abs(got-want) > 1e-9 {
			t.Fatalf("润滑油品质 %.0f 磨损系数=%f，期望 %f", quality, got, want)
		}
	}

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
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, reel.GUID)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)

	decision, err := srv.resolveGearBreak(ctx, uid, rod.GUID, line.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Part != "reel" || decision.GUID != reel.GUID {
		t.Fatalf("锁轮应为最弱环: %+v", decision)
	}
	outcome, err := srv.breakLineOrRod(ctx, uid, rod.GUID, line.GUID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ReelDamaged || outcome.LineBroken || outcome.BreakPart != "reel" {
		t.Fatalf("轮子超载结果错误: %+v", outcome)
	}
	fresh, _ := db.GetItemByGUID(ctx, reel.GUID)
	if math.Abs(fresh.Durability-0.8) > 1e-9 {
		t.Fatalf("轮子耐久=%f，期望 0.8", fresh.Durability)
	}
	perf, _ := db.GetReelPerformance(ctx, reel.GUID)
	if math.Abs(perf.LockedDragKg-perf.BaseDragKg*1.3*perf.BearingFactor) > 0.001 {
		t.Fatalf("80%% 耐久锁轮参数错误: %+v", perf)
	}
	if got, _ := db.GetItemByGUID(ctx, reel.GUID); got == nil {
		t.Fatal("轮子超载只掉耐久，不应从钓组删除")
	}
}

func TestHighQualityReelOilSlowsStageWear(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	reel, err := srv.CreateItem(ctx, uid, "spin_5354", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Zeiman 原厂油品质 70，单次只累计 18% 档位磨损，不应立即掉 20% 耐久。
	if _, err := srv.damageReelOverload(ctx, uid, zeroGUID, reel.GUID); err != nil {
		t.Fatal(err)
	}
	fresh, _ := db.GetItemByGUID(ctx, reel.GUID)
	if math.Abs(fresh.Durability-1.0) > 1e-9 {
		t.Fatalf("高品质油首次超载耐久=%f，期望仍为1.0", fresh.Durability)
	}
	perf, _ := db.GetReelPerformance(ctx, reel.GUID)
	if perf.OilQuality != 70 || math.Abs(perf.WearProgress-0.18) > 1e-9 {
		t.Fatalf("高品质油首次磨损累计错误: %+v", perf)
	}
	if _, err := srv.damageReelOverload(ctx, uid, zeroGUID, reel.GUID); err != nil {
		t.Fatal(err)
	}
	fresh, _ = db.GetItemByGUID(ctx, reel.GUID)
	if math.Abs(fresh.Durability-0.8) > 1e-9 {
		t.Fatalf("高品质油第二次超载耐久=%f，期望0.8", fresh.Durability)
	}
	perf, _ = db.GetReelPerformance(ctx, reel.GUID)
	if math.Abs(perf.WearProgress-0.16) > 1e-9 {
		t.Fatalf("跨档后剩余磨损累计错误: %+v", perf)
	}
}

func TestHandRodIgnoresBackpackReel(t *testing.T) {
	srv, _ := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)
	rod, _ := srv.CreateItem(ctx, uid, "tele_10180_4", "backpack", 1)
	line, _ := srv.CreateItem(ctx, uid, "mono_15095", "backpack", 1)
	_, _ = srv.CreateItem(ctx, uid, "spin_5820", "backpack", 1)
	sess := newProbeSess(srv)
	mountGearForTest(t, srv, sess, rod.GUID, line.GUID)
	ids := srv.rigGearIDs(ctx, uid, rod.GUID)
	if ids.Reel != "" || ids.ReelGUID != "" {
		t.Fatalf("手竿错误引用了背包轮子: %+v", ids)
	}
	spec := srv.loadGearSpecForRig(ctx, ids, rod.GUID)
	if spec.ReelPresent {
		t.Fatal("手竿承力链不应包含轮子")
	}
}
