package gameserver

// 竿组装配测试：
// TestRigAttachFlow 验证装配、派生索引、物品归属和拆卸回退。
// TestRigSetTypeStaleCorrection 验证不兼容的钓组类型会按竿型矫正，
// 同时保留玩家显式选择的合法类型。

import (
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// newRigTestServer 创建测试服务器并导入 item_def 静态数据。
func newRigTestServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rigflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "rig"}}, logging.Discard(), nil)
	srv.SetDB(db)
	importStaticForProbe(t, db)
	return srv, db
}

// rigRequestTree 构造请求树，并走与 dispatch 相同的编码解码路径。
func rigRequestTree(t *testing.T, items ...*proto.Value) *proto.Value {
	t.Helper()
	b, err := proto.EncodeValue(proto.Args524(items...), wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	tree, _, err := proto.DecodeValue(b, 0, wiregen.Table())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// rigSeg 返回 800 快照第 idx 段的值树。
func rigSeg(dec *proto.Value, idx int) *proto.Value {
	return dec.Field(idx).Val
}

// rigStoredF5 读取竿实例字段中的 153 钓组对象。
func rigStoredF5(t *testing.T, row *store.Item) *proto.Value {
	t.Helper()
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csRigObject)
	if idx < 0 || idx >= len(fields) {
		t.Fatalf("%s 类 %s 无钓组字段", row.ItemID, row.WireClass)
	}
	return fields[idx].Val
}

// rigStoredGUID 读取指定字段名对应的 GUID。
func rigStoredGUID(t *testing.T, row *store.Item, csName string) string {
	t.Helper()
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(row.WireClass, csName)
	if idx < 0 || idx >= len(fields) {
		return ""
	}
	return fields[idx].Hex
}

func TestRigAttachFlow(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1) // 竿（Feeder，竿型 10）
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	reel, err := srv.CreateItem(ctx, uid, "bclp_5004", "backpack", 1) // 渔轮（品类 6）
	if err != nil {
		t.Fatalf("造轮失败: %v", err)
	}
	hook, err := srv.CreateItem(ctx, uid, "c_hook_20712", "backpack", 1) // 鱼钩（品类 12）
	if err != nil {
		t.Fatalf("造钩失败: %v", err)
	}
	sess := newProbeSess(srv)

	// ---- 17/2 装渔轮：[容器0, 竿guid, 物品容器0, 物品guid, extra]（无槽位）----
	tree := rigRequestTree(t, proto.U32(0), proto.Guid(rod.GUID), proto.U32(0),
		proto.Guid(reel.GUID), proto.U32(0))
	body, ok := hRodSetComp(&Ctx{Sess: sess, Main: 17, Sub: 2, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok || len(body) == 0 {
		t.Fatal("17/2 装渔轮未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("17/2 响应不可解码: %v", err)
	}
	if dec.Class != "dlbahppbdad" {
		t.Fatalf("17/2 应回 800 dlbahppbdad，实得 %s", dec.Class)
	}
	// 渔轮直接挂载：f0 和 f2 为 null，f1 包含轮和竿。
	if seg := rigSeg(dec, 0); seg == nil || seg.Kind != proto.KindNull {
		t.Fatalf("渔轮非拆分挂载 f0 应为 null，实得 %v", seg)
	}
	upd := rigSeg(dec, 1)
	if upd == nil || upd.Kind != proto.KindArgs || upd.ArgCount() != 2 {
		t.Fatalf("f1 应为 args(61)[轮, 竿]，实得 %v", upd)
	}
	if cls := upd.ArgItem(0).Class; cls != "dgjinoebdop" {
		t.Fatalf("f1[0] 应为渔轮 dgjinoebdop，实得 %s", cls)
	}
	if cls := upd.ArgItem(1).Class; cls != "cnolibojkao" {
		t.Fatalf("f1[1] 应为竿 cnolibojkao，实得 %s", cls)
	}
	if seg := rigSeg(dec, 2); seg == nil || seg.Kind != proto.KindNull {
		t.Fatalf("无替换时 f2 应为 null，实得 %v", seg)
	}

	// 派生索引：rig_component 行（槽位=渔轮品类 6）
	if g, gerr := db.GetItemGUIDByRigSlot(ctx, rod.GUID, 6); gerr != nil || g != reel.GUID {
		t.Fatalf("rig_component 槽6 应= 渔轮 guid，实得 %q err=%v", g, gerr)
	}
	// 物品侧：container='rig'、rig_guid=竿、wire 父引用=竿
	reelRow, err := db.GetItemByGUID(ctx, reel.GUID)
	if err != nil || reelRow == nil {
		t.Fatalf("渔轮行丢失: %v", err)
	}
	if reelRow.Container != "rig" || reelRow.RigGUID != rod.GUID {
		t.Fatalf("渔轮 container/rig_guid = %s/%s，应 rig/%s", reelRow.Container, reelRow.RigGUID, rod.GUID)
	}
	if got := rigStoredGUID(t, reelRow, csParent); got != rod.GUID {
		t.Fatalf("渔轮 wire 父引用 %q 应= 竿 guid", got)
	}
	// 渔轮写入 f4 专用字段，不进入 f5 组件表。
	rodRow, _ := db.GetItemByGUID(ctx, rod.GUID)
	if got := rigStoredGUID(t, rodRow, csReel); got != reel.GUID {
		t.Fatalf("竿 f4(neangpifoka) %q 应= 渔轮 guid", got)
	}
	if comps := rigCompsOf(rigStoredF5(t, rodRow)); len(rigCompKeysOfCategory(comps, gearCatReel)) != 0 {
		t.Fatalf("渔轮不应进入 152 组件表，实得 %v", comps)
	}

	// ---- 17/2 装鱼钩：走 f5 的 152 组件表（品类 12，规范索引 250）----
	tree = rigRequestTree(t, proto.U32(0), proto.Guid(rod.GUID), proto.U32(0),
		proto.Guid(hook.GUID), proto.U32(0))
	body, ok = hRodSetComp(&Ctx{Sess: sess, Main: 17, Sub: 2, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok {
		t.Fatal("17/2 装鱼钩未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("17/2(钩) 响应不可解码: %v", err)
	}
	upd = rigSeg(dec, 1)
	if upd == nil || upd.ArgCount() != 2 || upd.ArgItem(0).Class != "agedjipcnpe" {
		t.Fatalf("f1 应为 [钩, 竿]，实得 %v", upd)
	}
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	f5 := rigStoredF5(t, rodRow)
	comps := rigCompsOf(f5)
	hc, has := comps[12]
	if !has || hc.GUID != hook.GUID {
		t.Fatalf("竿 f5 的 152 表应含槽12=钩，实得 %v", comps)
	}
	if hc.Extra != 250 {
		t.Fatalf("钩规范索引应 250（CANONICAL_RIG_INDEX），实得 %d", hc.Extra)
	}
	// 钓组类型按竿型派生；Feeder 使用 rig_BottomSimple/12。
	if got := int64(u64Of(f5.Field(1).Num)); got != 12 {
		t.Fatalf("f5.f1 应为 12（Feeder 派生），实得 %d", got)
	}
	if name := f5.Fields[0].StrOr(); name != "rig_BottomSimple" {
		t.Fatalf("f5.f0 应 rig_BottomSimple，实得 %q", name)
	}
	// 钩挂上后 wire 父引用 / container
	hookRow, _ := db.GetItemByGUID(ctx, hook.GUID)
	if hookRow.Container != "rig" || hookRow.RigGUID != rod.GUID {
		t.Fatalf("钩 container/rig_guid = %s/%s，应 rig/%s", hookRow.Container, hookRow.RigGUID, rod.GUID)
	}
	if got := rigStoredGUID(t, hookRow, csParent); got != rod.GUID {
		t.Fatalf("钩 wire 父引用 %q 应= 竿 guid", got)
	}

	// ---- 17/3 拆鱼钩：[容器0, 竿guid, 槽位品类12, 钩guid, extra250] ----
	tree = rigRequestTree(t, proto.U32(0), proto.Guid(rod.GUID), proto.U32(12),
		proto.Guid(hook.GUID), proto.U32(250))
	body, ok = hRodRemoveComp(&Ctx{Sess: sess, Main: 17, Sub: 3, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok {
		t.Fatal("17/3 拆卸未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("17/3 响应不可解码: %v", err)
	}
	if seg := rigSeg(dec, 0); seg == nil || seg.Kind != proto.KindNull {
		t.Fatalf("拆卸 f0 应为 null，实得 %v", seg)
	}
	// 客户端按 f0、f1、f2 顺序处理快照，f2 会从物品索引删除 GUID。
	// 因此退回背包的物品树放入 f1，f2 只放整条删除的拆分子实例。
	upd = rigSeg(dec, 1)
	if upd == nil || upd.ArgCount() != 2 ||
		upd.ArgItem(0).Class != "agedjipcnpe" || upd.ArgItem(1).Class != "cnolibojkao" {
		t.Fatalf("拆卸 f1 应为 [退回钩, 竿]，实得 %v", upd)
	}
	rem := rigSeg(dec, 2)
	if rem == nil || rem.Kind != proto.KindNull {
		t.Fatalf("普通退回 f2 应为 null（guid 进 f2 会被客户端删索引），实得 %v", rem)
	}
	// 回退断言：rig_component 行删除、物品退回背包、wire 父引用清空、f5 组件表移除
	if g, gerr := db.GetItemGUIDByRigSlot(ctx, rod.GUID, 12); gerr == nil && g != "" {
		t.Fatalf("拆卸后槽12 应无组件行，实得 %q", g)
	}
	hookRow, _ = db.GetItemByGUID(ctx, hook.GUID)
	if hookRow == nil {
		t.Fatal("钩行不应被删除（普通物品退回背包）")
	}
	if hookRow.Container != "backpack" || hookRow.RigGUID != "" {
		t.Fatalf("钩退回后 container/rig_guid = %s/%q，应 backpack/空", hookRow.Container, hookRow.RigGUID)
	}
	if got := rigStoredGUID(t, hookRow, csParent); got != "" {
		t.Fatalf("钩 wire 父引用应清空，实得 %q", got)
	}
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	if comps := rigCompsOf(rigStoredF5(t, rodRow)); len(rigCompKeysOfCategory(comps, gearCatReel)) != 0 {
		t.Fatalf("拆卸后 f5 组件表应为空，实得 %v", comps)
	}
	// 渔轮不受拆卸影响
	if got := rigStoredGUID(t, rodRow, csReel); got != reel.GUID {
		t.Fatalf("渔轮 f4 应保持挂载，实得 %q", got)
	}
}

func TestRigSetTypeStaleCorrection(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	bob, err := srv.CreateItem(ctx, uid, "bob_25001", "backpack", 1) // 浮漂（沉底族要被剔除）
	if err != nil {
		t.Fatalf("造浮漂失败: %v", err)
	}

	// 构造不兼容的钓组类型：
	// rig 行 rig_type=1、实例 f5 = rig_FloatSimple/1 + 浮漂组件
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(rod.WireClass, csRigObject)
	stale := map[int64]rigComp{28: {GUID: bob.GUID, Extra: 125}}
	fields[idx].Op = "VAL"
	fields[idx].Val = buildRig153("rig_FloatSimple", 1, stale, 100)
	fj, jerr := FieldsToJSON(fields)
	if jerr != nil {
		t.Fatal(jerr)
	}
	if err := db.UpdateItemFields(ctx, rod.GUID, fj, rod.Qty, rod.Durability, rod.Workmanship, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRig(ctx, rod.GUID, uid, 1, "", "{}"); err != nil {
		t.Fatal(err)
	}

	// Feeder 竿只允许 12、13 等钓组，默认矫正为 12 rig_BottomSimple。
	srv.correctStaleRigType(ctx, rod.GUID)

	rt, found, gerr := db.GetRigType(ctx, rod.GUID)
	if gerr != nil || !found || rt != 12 {
		t.Fatalf("rig 行矫正后应 12，实得 %d (found=%v err=%v)", rt, found, gerr)
	}
	rodRow, _ := db.GetItemByGUID(ctx, rod.GUID)
	f5 := rigStoredF5(t, rodRow)
	if got := int64(u64Of(f5.Field(1).Num)); got != 12 {
		t.Fatalf("实例 f5.f1 矫正后应 12，实得 %d", got)
	}
	if name := f5.Fields[0].StrOr(); name != "rig_BottomSimple" {
		t.Fatalf("实例 f5.f0 应同步 rig_BottomSimple，实得 %q", name)
	}
	if _, has := rigCompsOf(f5)[28]; has {
		t.Fatal("换到沉底族应剔除浮漂组件（槽 28）")
	}

	// 玩家显式选择的合法类型不受矫正影响。
	sess := newProbeSess(srv)
	tree := rigRequestTree(t, proto.U32(0), proto.Guid(rod.GUID), proto.U32(13))
	body, ok := hRigSetType(&Ctx{Sess: sess, Main: 17, Sub: 1, Tree: tree,
		DecodeOK: true, Log: logging.Discard()})
	if !ok {
		t.Fatal("17/1 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("17/1 响应不可解码: %v", err)
	}
	upd := rigSeg(dec, 1)
	if upd == nil || upd.ArgCount() != 1 || upd.ArgItem(0).Class != "cnolibojkao" {
		t.Fatalf("17/1 f1 应为 [竿]，实得 %v", upd)
	}
	rt2, _, _ := db.GetRigType(ctx, rod.GUID)
	if rt2 != 13 {
		t.Fatalf("17/1 后 rig 行应 13，实得 %d", rt2)
	}
	rodRow, _ = db.GetItemByGUID(ctx, rod.GUID)
	f5 = rigStoredF5(t, rodRow)
	if name := f5.Fields[0].StrOr(); name != "rig_BottomPaternoster" {
		t.Fatalf("17/1 后 f5.f0 应 rig_BottomPaternoster（f0 与 f1 同步），实得 %q", name)
	}

	// 已矫正的竿在 ensureRig 中保持幂等。
	srv.ensureRig(ctx, uid, rod.GUID)
	rt3, _, _ := db.GetRigType(ctx, rod.GUID)
	if rt3 != 13 {
		t.Fatalf("ensureRig 应保持显式类型 13，实得 %d", rt3)
	}
}

// TestRigExplicitTypeWinsOverComponents 验证玩家显式选择的合法钓组类型
// 不会因已装组件不同而被覆盖。
func TestRigExplicitTypeWinsOverComponents(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatalf("造竿失败: %v", err)
	}
	fields, err := FieldsFromJSON(rod.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idx := fieldIdxByCS(rod.WireClass, csRigObject)
	comps := map[int64]rigComp{
		47: {GUID: "00000000000000000000000000000047", Extra: 100},
		12: {GUID: "00000000000000000000000000000012", Extra: 250},
		31: {GUID: "00000000000000000000000000000031", Extra: 200},
	}
	fields[idx].Op = "VAL"
	fields[idx].Val = buildRig153("rig_BottomSimple", 12, comps, 100)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateItemFields(ctx, rod.GUID, fj, rod.Qty, rod.Durability, rod.Workmanship, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRig(ctx, rod.GUID, uid, 12, "", "{}"); err != nil {
		t.Fatal(err)
	}

	srv.correctStaleRigType(ctx, rod.GUID)
	rt, found, err := db.GetRigType(ctx, rod.GUID)
	if err != nil || !found || rt != 12 {
		t.Fatalf("显式 BottomSimple 不应被组件推断覆盖，实得 %d (found=%v err=%v)", rt, found, err)
	}
	row, _ := db.GetItemByGUID(ctx, rod.GUID)
	f5 := rigStoredF5(t, row)
	if name := f5.Fields[0].StrOr(); name != "rig_BottomSimple" {
		t.Fatalf("f5.f0 应保持 rig_BottomSimple，实得 %q", name)
	}
	if _, ok := rigCompsOf(f5)[31]; !ok {
		t.Fatal("切换钓组类型不能擅自丢掉已装食笼31")
	}

	// 玩家显式选择 13 时，矫正必须保留该选择。
	comps = map[int64]rigComp{
		47: {GUID: "00000000000000000000000000000047", Extra: 100},
		12: {GUID: "00000000000000000000000000000012", Extra: 250},
		27: {GUID: "00000000000000000000000000000027", Extra: 350},
	}
	fields, _ = FieldsFromJSON(row.FieldsJSON)
	fields[idx].Op = "VAL"
	fields[idx].Val = buildRig153("rig_BottomPaternoster", 13, comps, 100)
	fj, _ = FieldsToJSON(fields)
	if err := db.UpdateItemFields(ctx, rod.GUID, fj, row.Qty, row.Durability, row.Workmanship, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRig(ctx, rod.GUID, uid, 13, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRigType(ctx, rod.GUID, 13); err != nil {
		t.Fatal(err)
	}
	srv.correctStaleRigType(ctx, rod.GUID)
	rt, found, err = db.GetRigType(ctx, rod.GUID)
	if err != nil || !found || rt != 13 {
		t.Fatalf("兼容的显式类型13应保持，实得 %d (found=%v err=%v)", rt, found, err)
	}
}

// TestRigMountKeepsTemplateCompatible 验证装配可自动选择兼容钓组，
// 且后续玩家显式选择仍具有最高优先级。
func TestRigMountKeepsTemplateCompatible(t *testing.T) {
	srv, db := newRigTestServer(t)
	ctx := context.Background()
	const uid = int64(1000001)

	rod, err := srv.CreateItem(ctx, uid, "feeder_10125_FD360", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	cage, err := srv.CreateItem(ctx, uid, "feeder_30037", "backpack", 1)
	if err != nil {
		t.Fatalf("创建食笼: %v", err)
	}
	sess := newProbeSess(srv)
	runRodSet(t, srv, sess, rod.GUID, cage.GUID, 0)

	rt, found, err := db.GetRigType(ctx, rod.GUID)
	if err != nil || !found || rt != 16 {
		t.Fatalf("装食笼后 rig_type=%d found=%v err=%v，期望 16", rt, found, err)
	}
	row, _ := db.GetItemByGUID(ctx, rod.GUID)
	f5 := rigStoredF5(t, row)
	if name := f5.Fields[0].StrOr(); name != "rig_BottomMethod" {
		t.Fatalf("装食笼后 f5.f0=%q，期望 rig_BottomMethod", name)
	}
	if _, ok := rigCompsOf(f5)[31]; !ok {
		t.Fatal("f5 缺少刚安装的食笼31")
	}

	if !srv.rigSetType(ctx, uid, rod.GUID, 13) {
		t.Fatal("玩家显式选 BottomPaternoster(13) 必须成功")
	}
	rt, found, err = db.GetRigType(ctx, rod.GUID)
	if err != nil || !found || rt != 13 {
		t.Fatalf("显式切换后 rig_type=%d found=%v err=%v，期望13", rt, found, err)
	}
	row, _ = db.GetItemByGUID(ctx, rod.GUID)
	f5 = rigStoredF5(t, row)
	if name := f5.Fields[0].StrOr(); name != "rig_BottomPaternoster" {
		t.Fatalf("显式切换后 f5.f0=%q，期望 rig_BottomPaternoster", name)
	}
}
