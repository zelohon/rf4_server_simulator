package gameserver

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func f32Attr(v float32) uint32 { return math.Float32bits(v) }

func attrsJSON(t *testing.T, attrs map[string]any) string {
	t.Helper()
	b, err := json.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGearPhysicsU32F32AndWeakestLink(t *testing.T) {
	rod := &store.ItemDef{AttrsJSON: attrsJSON(t, map[string]any{
		gearAttrMaxLoad: f32Attr(14.0102),
		gearAttrLength:  f32Attr(3.9),
	})}
	line := &store.ItemDef{AttrsJSON: attrsJSON(t, map[string]any{
		gearAttrMaxLoad:  f32Attr(10.5),
		gearAttrDiameter: f32Attr(0.32),
		gearAttrLineLen:  100,
		gearAttrStretch:  f32Attr(0.85),
	})}
	reel := &store.ItemDef{AttrsJSON: attrsJSON(t, map[string]any{
		gearAttrDrag: map[string]any{gearAttrDragMax: f32Attr(6.4)},
	})}
	leader := &store.ItemDef{AttrsJSON: attrsJSON(t, map[string]any{
		gearAttrLeaderIn: map[string]any{gearAttrMaxLoad: f32Attr(4.2)},
	})}

	if got := rodPowerKg(rod); math.Abs(got-14.0102) > 0.001 {
		t.Fatalf("rod power=%f", got)
	}
	if got := rodLengthM(rod); math.Abs(got-3.9) > 0.001 {
		t.Fatalf("rod length=%f", got)
	}
	if got := lineStrengthKg(line); math.Abs(got-10.5) > 0.001 {
		t.Fatalf("line strength=%f", got)
	}
	if got := lineDiameterMM(line); math.Abs(got-0.32) > 0.001 {
		t.Fatalf("line diameter=%f", got)
	}
	if got := lineLengthM(line); got != 100 {
		t.Fatalf("line length=%d，纯整数字段不能被当 F32", got)
	}
	if got := reelDragMaxKg(reel); math.Abs(got-6.4) > 0.001 {
		t.Fatalf("reel drag=%f", got)
	}
	if got := leaderStrengthKg(leader); math.Abs(got-4.2) > 0.001 {
		t.Fatalf("leader strength=%f", got)
	}

	spec := gearPhysicsSpec{
		RodPowerKg: 14.0102, LineStrengthKg: 10.5,
		LeaderStrengthKg: 4.2, ReelDragKg: 6.4, LineStretch: 0.85,
	}
	if got := gearBreakLimitKg(spec); math.Abs(got-4.2) > 1e-9 {
		t.Fatalf("最弱环=%f，期望前导 4.2kg", got)
	}
	if got := gearWeakestPart(spec); got != "leader" {
		t.Fatalf("最弱断件=%q，期望 leader", got)
	}
	can, part := canLandGear(spec, 20, 0.35)
	if can || part != "leader" {
		t.Fatalf("20kg 鱼应断 leader，得到 can=%v part=%q", can, part)
	}
	if can, part := canLandGear(spec, 10, 0.35); !can || part != "" {
		t.Fatalf("10kg 鱼应能上岸，得到 can=%v part=%q", can, part)
	}
}

func TestGearWeightCapUsesWeakestLinkAndLineBuffer(t *testing.T) {
	spec := gearPhysicsSpec{
		RodPowerKg: 14.0102, LineStrengthKg: 10.5,
		LeaderStrengthKg: 13.3, ReelDragKg: 6.4, LineStretch: 0.85,
	}
	buf := effectiveLineBuffer(spec.LineStretch)
	if math.Abs(buf-0.575) > 1e-9 {
		t.Fatalf("line buffer=%f，期望 0.575", buf)
	}
	// 摩擦 6.4kg 低于鱼线 10.5kg，但摩擦是打滑力，上限跟鱼线走。
	want := 10.5 * (1.0 / 0.35) / 0.575
	if got := gearWeightCapKg(spec); math.Abs(got-want) > 1e-9 {
		t.Fatalf("gear cap=%f，期望 %f", got, want)
	}
	if got := capWeightGrams(5000, 1234); got != 1234 {
		t.Fatalf("capWeightGrams=%d，期望1234", got)
	}
	if got := poolWeightCapG(0, 0.2); math.Abs(got-780) > 1e-9 {
		t.Fatalf("fight_tolerance 兜底上限=%f，期望780g", got)
	}
	if got := poolWeightCapG(0.9, 0.2); math.Abs(got-900) > 1e-9 {
		t.Fatalf("真实钓具上限应替换650g兜底，得到%fg", got)
	}
}

func TestRigGearIDsReadsCurrentRodFieldsAndComponents(t *testing.T) {
	db := connectTestDB(t)
	srv := NewServerForGearTest(t, db)
	ctx := context.Background()
	const uid = int64(1000001)

	insert := func(numeric int64, itemID string, cat int64, attrs string) {
		t.Helper()
		if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO item_def(numeric_id,item_id,category,kind,brand,attrs_json)
			VALUES(?,?,?,'test','',?)`, numeric, itemID, cat, attrs); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, "rod_test", gearCatRod, attrsJSON(t, map[string]any{gearAttrMaxLoad: f32Attr(14.0102)}))
	insert(2, "reel_test", gearCatReel, attrsJSON(t, map[string]any{
		gearAttrDrag: map[string]any{gearAttrDragMax: f32Attr(6.4)},
	}))
	insert(3, "line_test", gearCatLine, attrsJSON(t, map[string]any{
		gearAttrMaxLoad: f32Attr(10.5), gearAttrStretch: f32Attr(0.85),
	}))
	insert(4, "leader_test", gearCatLeader, attrsJSON(t, map[string]any{gearAttrMaxLoad: f32Attr(13.3)}))

	row := func(guid string, numeric int64, itemID string, cat int64) *store.Item {
		return &store.Item{GUID: guid, UID: uid, ItemID: itemID, NumericID: numeric,
			WireClass: "cnolibojkao", TypeID: 157, Container: "rig", FieldsJSON: "[]",
			Durability: 1, Workmanship: 1}
	}
	rodGUID := "11111111111111111111111111111111"
	reelGUID := "22222222222222222222222222222222"
	lineGUID := "33333333333333333333333333333333"
	leaderGUID := "44444444444444444444444444444444"
	if err := db.InsertItem(ctx, row(rodGUID, 1, "rod_test", gearCatRod)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row(reelGUID, 2, "reel_test", gearCatReel)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row(lineGUID, 3, "line_test", gearCatLine)); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertItem(ctx, row(leaderGUID, 4, "leader_test", gearCatLeader)); err != nil {
		t.Fatal(err)
	}
	for slot, guid := range map[int64]string{
		gearCatReel: reelGUID, gearCatLine: lineGUID, gearCatLeader: leaderGUID,
	} {
		if err := db.UpsertRigComponent(ctx, rodGUID, slot, guid, 0); err != nil {
			t.Fatal(err)
		}
	}
	ids := srv.rigGearIDs(ctx, uid, rodGUID)
	if ids.Rod != "rod_test" || ids.Reel != "reel_test" || ids.Line != "line_test" || ids.Leader != "leader_test" {
		t.Fatalf("rig gear IDs=%+v", ids)
	}
	capKg, _, weakest := srv.gearWeightCapForRig(ctx, uid, rodGUID)
	wantCap := 10.5 * (1.0 / 0.35) / 0.575
	if math.Abs(capKg-wantCap) > 1e-4 || weakest != "line" {
		t.Fatalf("当前钓组 cap=%f weakest=%q，期望 cap=%f weakest=line", capKg, weakest, wantCap)
	}

	// 实例耐久必须参与服务端最弱环计算：半耐久鱼线 10.5kg -> 5.25kg。
	if err := db.SetItemDurability(ctx, lineGUID, 0.5); err != nil {
		t.Fatal(err)
	}
	capKg, _, weakest = srv.gearWeightCapForRig(ctx, uid, rodGUID)
	wantCap = 5.25 * (1.0 / 0.35) / 0.575
	if math.Abs(capKg-wantCap) > 1e-4 || weakest != "line" {
		t.Fatalf("磨损后 cap=%f weakest=%q，期望 cap=%f weakest=line", capKg, weakest, wantCap)
	}
}

func NewServerForGearTest(t *testing.T, db *store.DB) *Server {
	t.Helper()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "gear"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv
}

func TestOfficialPushDelayUsesTraceSamples(t *testing.T) {
	eligible := map[float64]bool{}
	for _, v := range officialPushDelaySamples {
		if v >= 6.0 {
			eligible[v] = true
		}
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		got := officialPushDelayWith(rng)
		if got < 6.0 || !eligible[got] {
			t.Fatalf("采样=%f，不属于过滤后的官服 trace 样本", got)
		}
	}
}

func TestBiteDelayConfigPrecedenceAndSkill(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "bite"}}, logging.Discard(), nil)
	srv.tunCache = map[string]float64{
		"bite_delay_fixed":    0.3,
		"bite_delay_max":      7,
		"bite_delay_scale":    0.5,
		"bite_delay_lure_max": 6,
	}
	srv.tunAt = time.Now()
	st := &FishState{RigType: 12}
	if got := srv.biteDelay(st, fishingSkillBonus{BiteSpeed: 0.5}); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("fixed 优先级错误: %f", got)
	}
	srv.tunCache["bite_delay_fixed"] = 0
	got := srv.biteDelay(st, fishingSkillBonus{BiteSpeed: 1})
	if got < 0.05 || got > 3.5 {
		t.Fatalf("cap/scale 顺序错误: %f（应先 cap 7 再 scale 0.5）", got)
	}
	if got := standardBiteDelay(12, 0.5, 0, 1); math.Abs(got-6) > 1e-9 {
		t.Fatalf("技能咬速后=%f，期望下限6s", got)
	}
	if got := standardBiteDelay(12, 1, 7, 0.5); math.Abs(got-3.5) > 1e-9 {
		t.Fatalf("标准咬钩 cap/scale=%f，期望3.5", got)
	}
}

func TestTrophyLuckRaisesAtMostOneGrade(t *testing.T) {
	if got := applyTrophyLuck(3, 2.0); got != 4 {
		t.Fatalf("luck 必中时应升到4，得到%d", got)
	}
	if got := applyTrophyLuck(4, 2.0); got != 4 {
		t.Fatalf("grade4 不应继续升档，得到%d", got)
	}
	if got := applyTrophyLuck(1, 0); got != 1 {
		t.Fatalf("无技能不应改档，得到%d", got)
	}
}

func TestPoolRollHonorsDynamicGearCap(t *testing.T) {
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "poolcap"}}, logging.Discard(), nil)
	overOldMockCap := false
	for i := 0; i < 300; i++ {
		f := srv.rollFishPoolsWithRules("lvl2_01", 0.9, neutralFishingSkillBonus())
		if f == nil || f.WeightG > 900 {
			t.Fatalf("内置池体重=%v，超过动态物理上限900g", f)
		}
		if f.WeightG > 650 {
			overOldMockCap = true
		}
	}
	if !overOldMockCap {
		t.Fatal("动态上限=900g 时仍被旧650g上限钳住")
	}
}

func TestFishingBonusUsesActualRigSkillCategory(t *testing.T) {
	db := connectTestDB(t)
	ctx := context.Background()
	const uid = int64(1000001)
	if _, err := db.SQL.Exec(`INSERT INTO skill_def(cat_id,node_id,unlock_pct,max_level,effect_pct,group_id,enabled,ordinal)
		VALUES(3,301,0,5,0,0,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO skill(uid,skill_id,points) VALUES(?,3,10)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO skill_node(uid,node_id,level) VALUES(?,301,2)`, uid); err != nil {
		t.Fatal(err)
	}
	srv := NewServerForGearTest(t, db)

	bottom := srv.fishingBonus(ctx, uid, 12)
	// effect = 10 * 0.15 + 2 * 1.0 = 3.5
	if math.Abs(bottom.BiteSpeed-(1.0-3.5/10.0*0.03)) > 1e-9 {
		t.Fatalf("bottom bite_speed=%f", bottom.BiteSpeed)
	}
	if math.Abs(bottom.TrophyLuck-(3.5/10.0*0.012)) > 1e-9 {
		t.Fatalf("bottom trophy_luck=%f", bottom.TrophyLuck)
	}
	if math.Abs(bottom.FightTolerance-(3.5/10.0*0.06)) > 1e-9 {
		t.Fatalf("bottom fight_tolerance=%f", bottom.FightTolerance)
	}
	if lure := srv.fishingBonus(ctx, uid, 5); lure != neutralFishingSkillBonus() {
		t.Fatalf("路亚 cat=2 不应消费底钓 cat=3 技能: %+v", lure)
	}
}

func TestReelStrengthUsesGearRatioNotDrag(t *testing.T) {
	def := &store.ItemDef{
		Category: 6,
		AttrsJSON: attrsJSON(t, map[string]any{
			gearAttrDrag: map[string]any{gearAttrDragMax: f32Attr(35.4)},
			gearAttrGear: map[string]any{gearAttrRatio: f32Attr(4)},
		}),
	}
	got, ok := strengthBitsFor(def, 1)
	if !ok || math.Abs(got-4) > 1e-3 {
		t.Fatalf("渔轮强度=%v ok=%v，期望传动比 4，不能等于摩擦 35.4", got, ok)
	}
	if drag := reelDragMaxKg(def); math.Abs(drag-35.4) > 0.05 {
		t.Fatalf("摩擦制动力=%f，期望 35.4", drag)
	}
}

func TestDBRollHonorsDynamicGearCap(t *testing.T) {
	db := connectTestDB(t)
	if _, err := db.SQL.Exec(`INSERT INTO loc_alias(alias,loc) VALUES('level_001_lake','station_test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO loc_fish(loc,fish_key,name_zh,catches) VALUES('station_test','c.roach','roach',100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`INSERT INTO species_weight(fish_key,min_g,max_g,trophy_g,trophy_line_g,super_trophy_line_g,length_k,length_p)
		VALUES('c.roach',80,900,318,450,700,0.0355,0.34)`); err != nil {
		t.Fatal(err)
	}
	srv := NewServerForGearTest(t, db)
	for i := 0; i < 300; i++ {
		f := srv.rollFishFromDBWithRules("level_001_lake", "", 0.25, neutralFishingSkillBonus())
		if f == nil || f.WeightG > 250 {
			t.Fatalf("DB 体重=%v，超过物理上限250g", f)
		}
	}
}
