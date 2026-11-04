package gameserver

import (
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// skills_craft_test.go 测试技能学习（5/2、5/3、5/4）和制作流程（9/1、9/3、9/4、9/6）。
// 测试使用仓库自带的静态库，并按需导入 skill_def、skill_cat_def、recipe 和 item_def。

// importStaticForSkillsCraft 从静态库导入指定表；缺少表或库时跳过测试。
func importStaticForSkillsCraft(t *testing.T, db *store.DB, tables ...string) {
	t.Helper()
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, "../../server_data/static.sqlite3"); err != nil {
		t.Skip("静态库不可用: ", err)
	}
	defer func() { _, _ = db.SQL.Exec(`DETACH DATABASE src`) }()
	for _, tb := range tables {
		if _, err := db.SQL.Exec(`INSERT INTO main.` + tb + ` SELECT * FROM src.` + tb); err != nil {
			t.Skipf("%s 导入失败: %v", tb, err)
		}
	}
}

func newSkillsCraftServer(t *testing.T, db *store.DB) *Server {
	t.Helper()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	return srv
}

// decodeTreeValue 解码响应体；失败时终止测试。
func decodeTreeValue(t *testing.T, srv *Server, body []byte) *proto.Value {
	t.Helper()
	v, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatalf("响应解码失败: %v（body=%x…）", err, head(body, 16))
	}
	return v
}

// treeAvailPoints 返回 311 技能树的 f0 可用点数。
func treeAvailPoints(t *testing.T, v *proto.Value) uint64 {
	t.Helper()
	if !v.IsKind(proto.KindObj) || v.TypeID != 311 {
		t.Fatalf("应为 311 技能树，实得 %v/%d", v.Kind, v.TypeID)
	}
	return u64Of(v.Fields[0].Num)
}

// craftObj8 构造 9/x 制作请求的 obj8 载荷。
func craftObj8(recipe string, materials []craftSlot, qty uint64) *proto.Value {
	materialsArg := proto.Args(proto.S("11"))
	for _, sl := range materials {
		materialsArg.Items = append(materialsArg.Items, proto.Obj("bhfikaplhmh", 9,
			proto.FStr("STR", proto.S(sl.ItemID)),
			proto.FGuid(sl.GUID),
			proto.FStr("STR", proto.S("")),
		))
	}
	return proto.Obj("dhkphmjinmj", 8,
		proto.FStr("STR", proto.S(recipe)),
		proto.FVal("VAL", proto.Args(proto.S("11"))), // 工具槽（空）
		proto.FVal("VAL", materialsArg),
		proto.FOpt(0),
		proto.FStr("U16STR", nil),
		proto.FNum("U32", qty),
	)
}

// TestSkillLearnFlow 验证技能学习、学习点数扣减、重置和全量重置。
// 测试数据包含 cat1 的节点 502、504 和 514。
func TestSkillLearnFlow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def", "skill_def", "skill_cat_def")
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	// 升到 10 级 → skillPointsTotal(10) = 9（<20 级 1 点/级，L=2..10）
	if _, err := db.SQL.Exec(`UPDATE profile SET level=10 WHERE uid=1000001`); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}

	call := func(main, sub uint8, tree *proto.Value) ([]byte, bool) {
		return (&handlerBox{srv: srv, sess: sess, main: main, sub: sub, tree: tree}).invoke(t)
	}

	// 初始可用点 = 9
	body, ok := call(5, 1, proto.Args524())
	if !ok {
		t.Fatal("5/1 未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 9 {
		t.Fatalf("初始可用点应 9，实得 %d", got)
	}

	// 5/2 学 1/502：可用点 9→8，节点等级 0→1，树反映
	body, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(502)))
	if !ok {
		t.Fatal("5/2 未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 8 {
		t.Fatalf("学 1 次后可用点应 8，实得 %d", got)
	}
	if lv, _ := db.GetSkillNode(ctx, 1000001, 502); lv != 1 {
		t.Fatalf("节点 502 等级应 1，实得 %d", lv)
	}

	// 再学两次到满级（max=3）：8→7→6；第 4 次 max-level 拒绝，点数不动
	for i, want := range []uint64{7, 6} {
		body, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(502)))
		if !ok {
			t.Fatalf("5/2 第 %d 次未处理", i+2)
		}
		if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != want {
			t.Fatalf("学 %d 次后可用点应 %d，实得 %d", i+2, want, got)
		}
	}
	body, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(502)))
	if !ok {
		t.Fatal("5/2 满级请求未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 6 {
		t.Fatalf("满级后再学可用点应保持 6，实得 %d", got)
	}
	if lv, _ := db.GetSkillNode(ctx, 1000001, 502); lv != 3 {
		t.Fatalf("节点 502 等级应停在 3，实得 %d", lv)
	}

	// 熟练度不足（504 unlock=35，prof=0）→ locked：点数/等级不动
	body, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(504)))
	if !ok {
		t.Fatal("5/2 锁定请求未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 6 {
		t.Fatalf("锁定后可用点应保持 6，实得 %d", got)
	}
	if lv, _ := db.GetSkillNode(ctx, 1000001, 504); lv != 0 {
		t.Fatalf("锁定节点 504 等级应 0，实得 %d", lv)
	}

	// 技能点耗尽（level=1 → 0 点）→ no-points：树 f0 归 0（total 0 − 已投 3 → 钳 0），
	// 节点等级不动
	if _, err := db.SQL.Exec(`UPDATE profile SET level=1 WHERE uid=1000001`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.Exec(`DELETE FROM skill_node WHERE uid=1000001 AND node_id<>502`); err != nil {
		t.Fatal(err)
	}
	body, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(514)))
	if !ok {
		t.Fatal("5/2 无点请求未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 0 {
		t.Fatalf("无点时可用点应 0，实得 %d", got)
	}
	if lv, _ := db.GetSkillNode(ctx, 1000001, 514); lv != 0 {
		t.Fatalf("无点节点 514 等级应 0，实得 %d", lv)
	}
	if _, err := db.SQL.Exec(`UPDATE profile SET level=10 WHERE uid=1000001`); err != nil {
		t.Fatal(err)
	}
	// 恢复 10 级后树反映 6
	body, ok = call(5, 1, proto.Args524())
	if !ok {
		t.Fatal("5/1 未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 6 {
		t.Fatalf("恢复等级后可用点应 6，实得 %d", got)
	}

	// 未知节点：仍回树（handled），点数不动
	body, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(99999)))
	if !ok {
		t.Fatal("5/2 未知节点未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 6 {
		t.Fatalf("未知节点后可用点应保持 6，实得 %d", got)
	}

	// 参数缺失 → PASS（交给兜底链）
	if _, handled := call(5, 2, proto.Args524()); handled {
		t.Fatal("缺参 5/2 应 PASS")
	}

	// 5/3 重置单节点（返还技能点）：6→9，等级 0
	body, ok = call(5, 3, proto.Args524(proto.U32(1), proto.U32(502)))
	if !ok {
		t.Fatal("5/3 未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 9 {
		t.Fatalf("重置后可用点应 9，实得 %d", got)
	}
	if lv, _ := db.GetSkillNode(ctx, 1000001, 502); lv != 0 {
		t.Fatalf("重置后节点 502 等级应 0，实得 %d", lv)
	}

	// 再学两次（9→7），5/4 全部重置 → 9、无残留行
	for i := 0; i < 2; i++ {
		if _, ok = call(5, 2, proto.Args524(proto.U32(1), proto.U32(502))); !ok {
			t.Fatal("5/2 未处理")
		}
	}
	body, ok = call(5, 4, proto.Args524())
	if !ok {
		t.Fatal("5/4 未处理")
	}
	if got := treeAvailPoints(t, decodeTreeValue(t, srv, body)); got != 9 {
		t.Fatalf("全部重置后可用点应 9，实得 %d", got)
	}
	var n int64
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM skill_node WHERE uid=1000001`).Scan(&n)
	if n != 0 {
		t.Fatalf("全部重置后 skill_node 应无行，实得 %d", n)
	}
}

// TestCraftFlow 制作闭环：材料齐 → 产出+消耗；材料缺 → 803(false)。
// 附带 9/3 分解、9/4 询价、9/6 模板与路径 2（客户端材料槽）。
func TestCraftFlow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "craft.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def", "recipe")
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	// 独立 uid、不播种 starter（背包装料可控）
	uid := int64(2000002)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess, tree: nil}

	countOf := func(itemID string) int64 {
		rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, it := range rows {
			if it.ItemID == itemID {
				n += srv.stackCountOf(ctx, it)
			}
		}
		return n
	}

	// 材料：loaf_01 ×4（定义单位量 4 → CreateItem units=1 后对齐行数量）
	loaf, err := srv.CreateItem(ctx, uid, "loaf_01", "backpack", 1)
	if err != nil {
		t.Fatalf("loaf_01 创建失败: %v", err)
	}
	srv.alignItemRowQty(ctx, loaf)
	if got := countOf("loaf_01"); got != 4 {
		t.Fatalf("loaf_01 应有 4 份，实得 %d", got)
	}

	// 9/1 craft_bait_bread（loaf_01×4 → bread）：803 成功 + 消耗 + 产出
	box.main, box.sub, box.tree = 9, 1, proto.Args524(craftObj8("craft_bait_bread", nil, 1))
	body, ok := box.invoke(t)
	if !ok {
		t.Fatal("9/1 未处理")
	}
	v := decodeTreeValue(t, srv, body)
	if !v.IsKind(proto.KindObj) || v.TypeID != 803 {
		t.Fatalf("9/1 响应应为 803，实得 %v/%d", v.Kind, v.TypeID)
	}
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("803.f0 成功标志应 1，实得 %d", got)
	}
	f1 := v.Fields[1].Val
	if f1 == nil || f1.Kind != proto.KindArgs || len(f1.Items) != 1 {
		t.Fatalf("803.f1 应为 1 件产出，实得 %+v", f1)
	}
	if f8 := v.Fields[8].Val; f8 == nil || f8.Kind != proto.KindGuidArr || len(f8.Items) != 1 {
		t.Fatalf("803.f8 应移除 1 个材料 guid，实得 %+v", f8)
	}
	if got := countOf("loaf_01"); got != 0 {
		t.Fatalf("loaf_01 应被扣空，实得 %d", got)
	}
	if got := countOf("bread"); got != 30 { // bread 定义单位量 30
		t.Fatalf("bread 应入库 30 份，实得 %d", got)
	}
	if f2 := v.Fields[2].Val; f2 == nil || f2.Kind != proto.KindObj || f2.TypeID != 310 {
		t.Fatalf("制作鱼饵 803.f2 应带 310 熟练度块，实得 %+v", f2)
	}
	if pts, err := db.GetSkillPoints(ctx, uid, captureBaitSkillID); err != nil || pts != 0.1 {
		t.Fatalf("制作后捕获鱼饵熟练度应 0.1，实得 %.3f err=%v", pts, err)
	}
	var txnNote string
	if err := db.SQL.QueryRow(
		`SELECT note FROM txn WHERE uid=? AND kind='craft' ORDER BY id DESC LIMIT 1`, uid).Scan(&txnNote); err != nil {
		t.Fatalf("craft 流水缺失: %v", err)
	}
	if txnNote != "recipe=craft_bait_bread qty=1" {
		t.Fatalf("流水 note 不符: %q", txnNote)
	}

	// 材料缺 → 803(false)，不产出
	box.tree = proto.Args524(craftObj8("craft_bait_bread", nil, 1))
	body, ok = box.invoke(t)
	if !ok {
		t.Fatal("9/1 缺料请求未处理")
	}
	v = decodeTreeValue(t, srv, body)
	if got := u64Of(v.Fields[0].Num); got != 0 {
		t.Fatalf("缺料 803.f0 应 0，实得 %d", got)
	}
	if got := countOf("bread"); got != 30 {
		t.Fatalf("缺料不应再产出 bread，实得 %d", got)
	}

	// 未知配方 → 803(false)
	box.tree = proto.Args524(craftObj8("no_such_recipe", nil, 1))
	body, ok = box.invoke(t)
	if !ok {
		t.Fatal("9/1 未知配方未处理")
	}
	v = decodeTreeValue(t, srv, body)
	if got := u64Of(v.Fields[0].Num); got != 0 {
		t.Fatalf("未知配方 803.f0 应 0，实得 %d", got)
	}

	// 9/4 询价：501{配方,0,0,qty}
	box.main, box.sub = 9, 4
	box.tree = proto.Args524(craftObj8("craft_bait_bread", nil, 3))
	body, ok = box.invoke(t)
	if !ok {
		t.Fatal("9/4 未处理")
	}
	v = decodeTreeValue(t, srv, body)
	if !v.IsKind(proto.KindObj) || v.TypeID != 501 {
		t.Fatalf("9/4 响应应为 501，实得 %v/%d", v.Kind, v.TypeID)
	}
	if s := v.Fields[0].StrOr(); s != "craft_bait_bread" {
		t.Fatalf("501.f0 应回请求配方 id，实得 %q", s)
	}
	if u64Of(v.Fields[1].Num) != 0 || u64Of(v.Fields[2].Num) != 0 {
		t.Fatalf("501 价格应 0/0，实得 %d/%d", u64Of(v.Fields[1].Num), u64Of(v.Fields[2].Num))
	}
	if u64Of(v.Fields[3].Num) != 3 {
		t.Fatalf("501.f3 数量应 3，实得 %d", u64Of(v.Fields[3].Num))
	}

	// 9/6 模板：GUID 必须确定；输入为 17 对象和 loaf_01×4。
	box.main, box.sub = 9, 6
	box.tree = proto.Args524(proto.StrVal("craft_bait_bread"))
	body, ok = box.invoke(t)
	if !ok {
		t.Fatal("9/6 未处理")
	}
	v = decodeTreeValue(t, srv, body)
	if !v.IsKind(proto.KindObj) || v.TypeID != 171 {
		t.Fatalf("9/6 响应应为 171，实得 %v/%d", v.Kind, v.TypeID)
	}
	// 9/6 的 f0 由配方名按 UUIDv5 生成。
	if g := v.Fields[0].GuidHex(); g != "506cdc49a763535fa8106a703773990c" {
		t.Fatalf("9/6 guid 不符: %s", g)
	}
	if s := v.Fields[1].StrOr(); s != "craft_bait_bread" {
		t.Fatalf("9/6 名称不符: %q", s)
	}
	f3 := v.Fields[3].Val
	if f3 == nil || f3.Kind != proto.KindArgs || f3.ArgCodeStr() != "18" || len(f3.Items) != 1 {
		t.Fatalf("9/6.f3 应 args(18)[1]，实得 %+v", f3)
	}
	demand := f3.Items[0]
	if s := demand.Fields[0].StrOr(); s != "craft_bait_bread_req" {
		t.Fatalf("需求 key 不符: %q", s)
	}
	acc := demand.Fields[4].Val
	if acc == nil || acc.Kind != proto.KindArray || len(acc.Items) != 1 ||
		acc.Items[0].Hex != hexOfStr("loaf_01") {
		t.Fatalf("需求 f4 应 array[loaf_01]，实得 %+v", acc)
	}
	if u64Of(demand.Fields[5].Num) != 4 {
		t.Fatalf("需求数量应 4，实得 %d", u64Of(demand.Fields[5].Num))
	}
	if f4 := v.Fields[4].Val; f4 == nil || f4.Kind != proto.KindArgs || f4.ArgCodeStr() != "61" || len(f4.Items) != 0 {
		t.Fatalf("9/6.f4 应 args(61)[]，实得 %+v", f4)
	}

	// 客户端材料槽路径：craft_feed_universal 只约束类别 18/20 与标签。
	// 槽位带客户端生成的 guid → 按需求类目在背包定位 mix_combi 扣 1 份
	mix, err := srv.CreateItem(ctx, uid, "mix_combi", "backpack", 1)
	if err != nil {
		t.Fatalf("mix_combi 创建失败: %v", err)
	}
	srv.alignItemRowQty(ctx, mix)
	before := countOf("mix_combi")
	box.main, box.sub = 9, 1
	box.tree = proto.Args524(craftObj8("craft_feed_universal", []craftSlot{
		{ItemID: "craft_feed_generic_mix_1", GUID: "00112233445566778899aabbccddeeff"}, // 客户端生成的 guid
	}, 1))
	body, ok = box.invoke(t)
	if !ok {
		t.Fatal("9/1 路径2 未处理")
	}
	v = decodeTreeValue(t, srv, body)
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("路径2 803.f0 应 1，实得 %d（body=%x…）", got, head(body, 16))
	}
	if got := countOf("feedcompleted"); got != 1 {
		t.Fatalf("feedcompleted 应入库 1 份，实得 %d", got)
	}
	after := countOf("mix_combi")
	if after != before-1 {
		t.Fatalf("mix_combi 应被扣 1 份（%d → %d）", before, after)
	}

	// 9/3 分解：扣 2 件 → 803(true)
	g1, err := srv.CreateItem(ctx, uid, "bread", "backpack", 1)
	if err != nil {
		t.Fatalf("bread 创建失败: %v", err)
	}
	srv.alignItemRowQty(ctx, g1)
	g2, err := srv.CreateItem(ctx, uid, "bread", "backpack", 1)
	if err != nil {
		t.Fatalf("bread 创建失败: %v", err)
	}
	srv.alignItemRowQty(ctx, g2)
	breadsBefore := countOf("bread") // 30（首个）+ 30 + 30 = 90 份
	box.main, box.sub = 9, 3
	box.tree = proto.Args524(proto.Guid(g1.GUID), proto.Guid(g2.GUID))
	body, ok = box.invoke(t)
	if !ok {
		t.Fatal("9/3 未处理")
	}
	v = decodeTreeValue(t, srv, body)
	if got := u64Of(v.Fields[0].Num); got != 1 {
		t.Fatalf("9/3 803.f0 应 1，实得 %d", got)
	}
	// 整件移除 2 行 bread = 60 份
	if got := countOf("bread"); got != breadsBefore-60 {
		t.Fatalf("9/3 应整件移除 2 件 bread（30×2 份），%d → %d", breadsBefore, got)
	}
}

// TestBaitCraftStacksAndMastery 验证连续制作鱼饵会合并到同一个背包堆，
// 每次制作固定产出一包 30 个并增加 0.1% 捕获鱼饵熟练度。
func TestBaitCraftStacksAndMastery(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "bait-craft-stack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def", "recipe")
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2000004)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess, main: 9, sub: 1}
	for i := 0; i < 2; i++ {
		loaf, err := srv.CreateItem(ctx, uid, "loaf_01", "backpack", 1)
		if err != nil {
			t.Fatalf("第 %d 次创建材料失败: %v", i+1, err)
		}
		srv.alignItemRowQty(ctx, loaf)
		box.tree = proto.Args524(craftObj8("craft_bait_bread", nil, 1))
		body, ok := box.invoke(t)
		if !ok || u64Of(decodeTreeValue(t, srv, body).Fields[0].Num) != 1 {
			t.Fatalf("第 %d 次制作失败", i+1)
		}
	}
	var rows, qty int64
	if err := db.SQL.QueryRow(`SELECT COUNT(*), COALESCE(SUM(qty),0) FROM item WHERE uid=? AND item_id='bread' AND container='backpack'`,
		uid).Scan(&rows, &qty); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || qty != 60 {
		t.Fatalf("两次制作应合并成 1 堆 60 个，实得 rows=%d qty=%d", rows, qty)
	}
	if pts, err := db.GetSkillPoints(ctx, uid, captureBaitSkillID); err != nil || pts != 0.2 {
		t.Fatalf("两次制作后熟练度应 0.2，实得 %.3f err=%v", pts, err)
	}
}

// TestBaitDigFlow 验证 9/3 使用铲子会产出鱼饵、合并背包堆并增加捕获鱼饵熟练度。
func TestBaitDigFlow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "baitdig.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def")
	ctx := context.Background()
	srv := newSkillsCraftServer(t, db)
	uid := int64(2000003)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: uid, Srv: srv, log: logging.Discard()}
	box := &handlerBox{srv: srv, sess: sess}
	tool, err := srv.CreateItem(ctx, uid, baitShovelItemID, "backpack", 1)
	if err != nil {
		t.Fatalf("创建铲子失败: %v", err)
	}
	baitCount := func() int64 {
		rows, err := db.ListItemsByContainers(ctx, uid, []string{"backpack", "rig"})
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, it := range rows {
			switch it.ItemID {
			case "worm_01", "maggot", "naiad", "caddisfly", "unibeetle_larva", "stonefly_nymph", "scolytinae":
				n += srv.stackCountOf(ctx, it)
			}
		}
		return n
	}
	box.main, box.sub = 9, 3
	box.tree = proto.Args524(proto.Guid(tool.GUID))
	body, ok := box.invoke(t)
	if !ok {
		t.Fatal("9/3 挖饵未处理")
	}
	v := decodeTreeValue(t, srv, body)
	if !v.IsKind(proto.KindObj) || v.TypeID != 803 || u64Of(v.Fields[0].Num) != 1 {
		t.Fatalf("挖饵应返回成功的 803，实得 %+v", v)
	}
	if got := baitCount(); got < 1 {
		t.Fatalf("挖饵后应至少新增 1 个鱼饵，实得 %d", got)
	}
	if pts, err := db.GetSkillPoints(ctx, uid, captureBaitSkillID); err != nil || pts != 0.1 {
		t.Fatalf("挖饵后捕获鱼饵熟练度应 0.1，实得 %.3f err=%v", pts, err)
	}
	f2 := v.Fields[2].Val
	if f2 == nil || f2.Kind != proto.KindObj || f2.TypeID != 310 {
		t.Fatalf("挖饵 803.f2 应带 310 熟练度块，实得 %+v", f2)
	}

	before := baitCount()
	box.tree = proto.Args524(proto.Guid(tool.GUID))
	body, ok = box.invoke(t)
	if !ok || u64Of(decodeTreeValue(t, srv, body).Fields[0].Num) != 1 {
		t.Fatal("第二次挖饵失败")
	}
	if got := baitCount(); got <= before {
		t.Fatalf("第二次挖饵后鱼饵数量应增加，前=%d 后=%d", before, got)
	}
	if pts, err := db.GetSkillPoints(ctx, uid, captureBaitSkillID); err != nil || pts != 0.2 {
		t.Fatalf("第二次挖饵后熟练度应 0.2，实得 %.3f err=%v", pts, err)
	}
}

// hexOfStr 字符串的 hex（kdpbhdffjaf.f4 的字节数组元素）。
func hexOfStr(s string) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		out = append(out, hexDigits[s[i]>>4], hexDigits[s[i]&0x0f])
	}
	return string(out)
}

// handlerBox 直调处理器，使用真实 Ctx 但不经过信封。
type handlerBox struct {
	srv       *Server
	sess      *Session
	main, sub uint8
	tree      *proto.Value
}

func (b *handlerBox) invoke(t *testing.T) ([]byte, bool) {
	t.Helper()
	h := b.srv.dispatch.lookup(b.main, b.sub)
	if h == nil {
		t.Fatalf("%d/%d 未注册", b.main, b.sub)
	}
	return h(&Ctx{
		Sess: b.sess, Main: int(b.main), Sub: int(b.sub),
		CallID: 1, Tree: b.tree, DecodeOK: b.tree != nil,
		Log: logging.Discard(),
	})
}
