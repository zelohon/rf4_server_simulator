package gameserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// TestItemTreeSkeleton 打印 4/5 物品元素骨架，用于检查不同类别生成不同 wire 类。
func TestItemTreeSkeleton(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListItemsByContainer(ctx, 1000001, "backpack")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("播种后背包为空")
	}
	n := len(rows)
	if n > 8 {
		n = 8
	}
	for _, row := range rows[:n] {
		tree, err := srv.ItemToTree(ctx, row)
		if err != nil {
			t.Fatalf("%s: %v", row.ItemID, err)
		}
		b, _ := json.Marshal(skeletonOf(tree))
		t.Logf("%s wire=%s typeID=%d -> %s", row.ItemID, row.WireClass, row.TypeID, b)
	}
}

// skeletonOf 压缩树骨架：args/obj/vallist 摘要 + 各字段 op。
func skeletonOf(v *proto.Value) map[string]any {
	switch v.Kind {
	case proto.KindArgs:
		items := make([]any, 0, len(v.Items))
		for _, it := range v.Items {
			items = append(items, skeletonOf(it))
		}
		code := ""
		if v.Code != nil {
			code = *v.Code
		}
		return map[string]any{"t": "args", "code": code, "items": items}
	case proto.KindObj:
		ops := make([]string, 0, len(v.Fields))
		for _, f := range v.Fields {
			ops = append(ops, f.Op)
		}
		return map[string]any{"t": "obj", "typeID": v.TypeID, "ops": ops}
	case proto.KindValList:
		items := make([]any, 0, len(v.Items))
		for _, it := range v.Items {
			items = append(items, skeletonOf(it))
		}
		return map[string]any{"t": "vallist", "items": items}
	default:
		return map[string]any{"t": string(v.Kind)}
	}
}

// TestItemHandlersFullChain 全链路探测：4/1 → 4/4 → 4/5 经处理器 + 编码全链路，
// 打印响应体和解码骨架，用于检查回包形状。
func TestItemHandlersFullChain(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "probe2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := newProbeSess(srv)

	// 4/1
	b41, ok := hItemCount(&Ctx{Sess: sess, Main: 4, Sub: 1, Tree: probeArgsU32(0), DecodeOK: true})
	t.Logf("4/1 handled=%v body=%x", ok, b41)

	// 4/4
	b44, ok := hItemGUIDs(&Ctx{Sess: sess, Main: 4, Sub: 4, Tree: probeArgsU32(0), DecodeOK: true})
	t.Logf("4/4 handled=%v body len=%d head=%x", ok, len(b44), head(b44, 24))

	// 4/5：取全部 guid
	guids, _ := srv.DB.ItemGUIDsByContainers(ctx, 1000001, []string{"backpack", "rig"})
	tree45 := probeArgsGuids(guids)
	b45, ok := hItemDetails(&Ctx{Sess: sess, Main: 4, Sub: 5, Tree: tree45, DecodeOK: true})
	t.Logf("4/5 handled=%v body len=%d head=%x", ok, len(b45), head(b45, 24))
	if ok {
		dec, _, err := proto.DecodeValue(b45, 0, srv.Table())
		if err != nil {
			t.Fatalf("4/5 响应解码失败: %v", err)
		}
		b, _ := json.Marshal(skeletonOf(dec))
		t.Logf("4/5 骨架: %s", b)
	}
}

func head(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

func newProbeSess(srv *Server) *Session {
	return &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}
}

func probeArgsU32(v uint32) *proto.Value {
	return proto.Args524(proto.U32(v))
}

func probeArgsGuids(guids []string) *proto.Value {
	return proto.Args524(proto.U32(0), proto.GuidArr(guids))
}

// TestItemDetailsEncodeError 定位 4/5 编码失败的具体物品与字段。
func TestItemDetailsEncodeError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "probe3.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	guids, _ := srv.DB.ItemGUIDsByContainers(ctx, 1000001, []string{"backpack", "rig"})
	for i, g := range guids {
		row, err := srv.DB.GetItemByGUID(ctx, g)
		if err != nil || row == nil {
			t.Logf("[%d] %s 取行失败: %v", i, g, err)
			continue
		}
		tree, err := srv.ItemToTree(ctx, row)
		if err != nil {
			t.Logf("[%d] %s ItemToTree 失败: %v", i, row.ItemID, err)
			continue
		}
		if _, err := proto.EncodeValue(tree, srv.Table()); err != nil {
			t.Errorf("[%d] %s (%s/%d) 编码失败: %v", i, row.ItemID, row.WireClass, row.TypeID, err)
			continue
		}
		_ = tree
	}
	// 整体 args 编码
	out := proto.Args(proto.S("61"))
	for _, g := range guids {
		row, _ := srv.DB.GetItemByGUID(ctx, g)
		if row == nil || row.UID != 1000001 {
			continue
		}
		tree, err := srv.ItemToTree(ctx, row)
		if err != nil {
			continue
		}
		out.Items = append(out.Items, tree)
	}
	if _, err := proto.EncodeValue(out, srv.Table()); err != nil {
		t.Errorf("整体 args(61) 编码失败（%d 件）: %v", len(out.Items), err)
	} else {
		t.Logf("整体编码 OK（%d 件）", len(out.Items))
	}
}

// TestGjdmdcpoifhMismatch 探查 gjdmdcpoifh(101) 字段数不匹配的物品明细。
func TestGjdmdcpoifhMismatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "probe4.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	guids, _ := srv.DB.ItemGUIDsByContainers(ctx, 1000001, []string{"backpack", "rig"})
	for i, g := range guids {
		row, _ := srv.DB.GetItemByGUID(ctx, g)
		if row == nil || row.WireClass != "gjdmdcpoifh" {
			continue
		}
		var nj []any
		_ = json.Unmarshal([]byte(row.FieldsJSON), &nj)
		t.Logf("#%d item=%q numeric=%d jsonfields=%d ops=%d",
			i, row.ItemID, row.NumericID, len(nj), len(wiregen.ClassOps["gjdmdcpoifh"]))
	}
}

// TestGjdmdcpoifhOps 打印 Go ops 表中 gjdmdcpoifh 的字段 ops 序列。
func TestGjdmdcpoifhOps(t *testing.T) {
	ops := wiregen.ClassOps["gjdmdcpoifh"]
	t.Logf("gjdmdcpoifh ops(%d): %v", len(ops), ops)
	ops2 := wiregen.ClassOps["blpddffbgjg"]
	t.Logf("blpddffbgjg ops(%d): %v", len(ops2), ops2)
}

// TestGjdmdcpoifhCSMap 打印该类 cs 名→索引映射（重建 13 字段用）。
func TestGjdmdcpoifhCSMap(t *testing.T) {
	ops := wiregen.ClassOps["gjdmdcpoifh"]
	for i, op := range ops {
		cs := ""
		meta := wiregen.ClassMeta["gjdmdcpoifh"]
		for _, fi := range meta.Fields {
			if fi.Idx == i {
				cs = fi.Name
			}
		}
		t.Logf("[%d] %-8s %s", i, op, cs)
	}
}

// TestMapDefsClassesInOps 校验 dob 树 5 个类的 ops 数与 map_defs.json 一致。
func TestMapDefsClassesInOps(t *testing.T) {
	want := map[string]int{"bleinjfhebp": 5, "dobfogbcggd": 6, "kcpjalejcog": 1, "odnlikfnhnl": 10, "paboeegkcmj": 1}
	for class, n := range want {
		ops := wiregen.ClassOps[class]
		if len(ops) != n {
			t.Errorf("类 %s ops=%d, map_defs 期望 %d", class, len(ops), n)
		} else {
			t.Logf("类 %s ops=%d ✓", class, len(ops))
		}
	}
}

// TestSpawnClasses 打印 206 定位器与 205 dob 的 ops。
func TestSpawnClasses(t *testing.T) {
	t.Logf("cgbaffjpbkg ops: %v", wiregen.ClassOps["cgbaffjpbkg"])
	t.Logf("dobfogbcggd ops: %v", wiregen.ClassOps["dobfogbcggd"])
	t.Logf("bleinjfhebp ops: %v", wiregen.ClassOps["bleinjfhebp"])
	t.Logf("odnlikfnhnl ops: %v", wiregen.ClassOps["odnlikfnhnl"])
}

// TestSpawnLocatorBuild 验证 1/13 响应可编码、顶层为 206 且 dob 重锚生效。
func TestSpawnLocatorBuild(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "probe5.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "probe"}}, logging.Discard(), nil)
	srv.SetDB(db)
	body, err := srv.buildSpawnLocatorBody(spawnLevelKey, spawnScene, time.Now().Unix())
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if body[0] != 0x01 {
		t.Fatalf("顶层应为 obj(0x01)，实得 %x", body[0])
	}
	tid := uint32(body[1]) | uint32(body[2])<<8 | uint32(body[3])<<16 | uint32(body[4])<<24
	if tid != 206 {
		t.Fatalf("顶层 type_id 应 206，实得 %d", tid)
	}
	t.Logf("1/13 响应 %dB，顶层 206 ✓", len(body))
	// 重锚校验：预报最小日号 == 昨天
	day, _ := worldDayAndHour(time.Now().Unix())
	maps, _ := loadMapDefs()
	var dob irNode
	_ = json.Unmarshal(maps[spawnLevelKey], &dob)
	_ = materializeIR(&dob)
	reanchorWeather(&dob, time.Now().Unix())
	minD := int64(1 << 62)
	irWalkObj(&dob, func(n *irNode) {
		if n.Class == forecastClass {
			if v, ok := irFieldInt(n, 0); ok && v < minD {
				minD = v
			}
		}
	})
	if minD != day-1 {
		t.Fatalf("预报首条应=今天-1(%d)，实得 %d", day-1, minD)
	}
	t.Logf("预报重锚 ✓ 首条=%d", minD)
}

// TestTimelineClassesOps 打印时间线新命令涉及的类 ops。
func TestTimelineClassesOps(t *testing.T) {
	for _, c := range []string{"acjaheimjge", "fopkeoppndn", "fkcmcjlagdh", "nkakjeigjla", "gbnhfllijoh", "godhiicbgng"} {
		t.Logf("%s ops: %v", c, wiregen.ClassOps[c])
	}
}

// TestTimelineHandlersSmoke 时间线新处理器冒烟：全部可编码且形状正确。
func TestTimelineHandlersSmoke(t *testing.T) {
	sess := &Session{ID: 1, Userid: 1000001, Srv: NewServer(config.Game{Tunnel: config.Tunnel{PSK: "t"}}, logging.Discard(), nil), log: logging.Discard()}
	cases := []struct {
		name string
		h    Handler
		want []byte // nil = 只要求 handled
	}{
		{"9/5", hCraftTemplates, []byte{0x03, 0x02, '9', '7', 0x00, 0x00}},
		{"6/2", hNull, []byte{0x00}},
		{"24/3", hNull, []byte{0x00}},
		{"24/12", hU32ArrEmpty, []byte{0x05, 0x00, 0x00}},
		{"1/18", hBoolTrue, []byte{0x0e}},
	}
	for _, c := range cases {
		body, ok := c.h(&Ctx{Sess: sess, Log: logging.Discard()})
		if !ok {
			t.Errorf("%s 未处理", c.name)
			continue
		}
		if c.want != nil && string(body) != string(c.want) {
			t.Errorf("%s 形状不符: got %x want %x", c.name, body, c.want)
		}
	}
	// 23/1 金币商店
	b, ok := hStoreRealList(&Ctx{Sess: sess, Log: logging.Discard()})
	if !ok || len(b) < 6 {
		t.Fatal("23/1 未处理")
	}
	dec, _, err := proto.DecodeValue(b, 0, sess.Table())
	if err != nil {
		t.Fatalf("23/1 解码失败: %v", err)
	}
	if dec.Kind != proto.KindArgs || len(dec.Items) != 0 {
		t.Fatalf("23/1 应为 args(147)[0 件]，实得 %d 件", len(dec.Items))
	}
	// 28/1、28/2
	for name, h := range map[string]Handler{"28/1": hEnvironment, "28/2": hFog} {
		b, ok := h(&Ctx{Sess: sess, Log: logging.Discard()})
		if !ok {
			t.Fatalf("%s 未处理", name)
		}
		if _, _, err := proto.DecodeValue(b, 0, sess.Table()); err != nil {
			t.Fatalf("%s 编码不可解码: %v", name, err)
		}
	}
	// fire-and-forget：nil body
	if b, _ := hNoReply(&Ctx{Sess: sess}); b != nil {
		t.Fatal("fire-and-forget 应回 nil")
	}
}

// TestAllHandlersDecode 在探针会话上调用每个已注册处理器，
// 验证返回的响应体都能被协议层完整解码。
func TestAllHandlersDecode(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "gate"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}
	sess.state = map[string]any{}

	n := 0
	for key, h := range srv.dispatch.exact {
		body, handled := func() (body []byte, handled bool) {
			defer func() { _ = recover() }() // 无隧道的推送类处理器在探针会话中会跳过
			return h(&Ctx{
				Sess: sess, Main: int(key.main), Sub: int(key.sub),
				CallID: 1, Tree: proto.Args524(), DecodeOK: true,
				Log: logging.Discard(),
			})
		}()
		if !handled || body == nil {
			continue // 跳过依赖请求上下文或无需响应的处理器
		}
		n++
		if _, _, err := proto.DecodeValue(body, 0, srv.Table()); err != nil {
			t.Errorf("%d/%d 响应不可解码: %v（body=%x…）", key.main, key.sub, err, head(body, 16))
		}
	}
	t.Logf("解码门通过处理器数: %d / 注册 %d", n, len(srv.dispatch.exact))
}

// TestReceiptClassOps 打印 166 收据类 ops。
func TestReceiptClassOps(t *testing.T) {
	t.Logf("pcfflcdpjgk ops: %v tid=%d", wiregen.ClassOps["pcfflcdpjgk"], wiregen.ClassTypeID["pcfflcdpjgk"])
}

// TestNumericIDFieldMapping 核对物品数字 id 字段；iofbjlaadfg 是实际字段名。
func TestNumericIDFieldMapping(t *testing.T) {
	for _, cls := range []string{"gjdmdcpoifh", "blpddffbgjg", "kbnbcppjkaf", "aafdnnmleob", "cnolibojkao", "ohciikgjghe"} {
		t.Logf("%-14s iofbjlaadfg@%d  dbjnbhhhhcn@%d", cls,
			fieldIdxByCS(cls, "iofbjlaadfg"), fieldIdxByCS(cls, "dbjnbhhhhcn"))
	}
}

// importStaticForProbe 从仓库自带静态库导入 item_def（探针用，仅此一张表）。
func importStaticForProbe(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.SQL.Exec(`ATTACH DATABASE ? AS src`, "../../server_data/static.sqlite3"); err != nil {
		t.Skip("静态库不可用: ", err)
	}
	defer func() { _, _ = db.SQL.Exec(`DETACH DATABASE src`) }()
	if _, err := db.SQL.Exec(`INSERT OR IGNORE INTO main.item_def SELECT * FROM src.item_def`); err != nil {
		t.Skip("item_def 导入失败: ", err)
	}
}

// TestCSNamesCanonical 验证常量表中的字段名都存在于对应物品类。
func TestCSNamesCanonical(t *testing.T) {
	for cls, want := range map[string][]string{
		"gjdmdcpoifh": {csGUID, csNumericID, csWorkmanship, csDurability, csCreated, csUpdated, csStateEnum, csOrigin},
		"blpddffbgjg": {csGUID, csNumericID, csOrigin},
		"cnolibojkao": {csGUID, csNumericID, csParent, csStrength},
		"ohciikgjghe": {csGUID, csNumericID, csWorkmanship, csDurability},
	} {
		for _, name := range want {
			if fieldIdxByCS(cls, name) < 0 {
				t.Errorf("类 %s 缺权威字段 %s", cls, name)
			}
		}
	}
}

// TestCreateItemWellFormed 验证创建物品的 GUID、数字 id 和可编码结构。
func TestCreateItemWellFormed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wellform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "wf"}}, logging.Discard(), nil)
	srv.SetDB(db)
	importStaticForProbe(t, db)
	// 从商店货架取几种不同类别的商品
	shopItems := []string{"bob_25018", "shirt_35002", "worm_01", "spinner_2713"}
	for _, itemID := range shopItems {
		it, err := srv.CreateItem(ctx, 1000001, itemID, "backpack", 1)
		if err != nil {
			t.Logf("%s 创建跳过: %v", itemID, err)
			continue
		}
		fields, err := FieldsFromJSON(it.FieldsJSON)
		if err != nil {
			t.Fatalf("%s: %v", itemID, err)
		}
		guidIdx := fieldIdxByCS(it.WireClass, csGUID)
		idIdx := fieldIdxByCS(it.WireClass, csNumericID)
		if guidIdx < 0 || idIdx < 0 {
			t.Fatalf("%s 类 %s 缺 guid/数字id 字段", itemID, it.WireClass)
		}
		if fields[guidIdx].Hex == "" || fields[guidIdx].Hex == "00000000000000000000000000000000" {
			t.Errorf("%s GUID 为零", itemID)
		}
		if got := u64Of(fields[idIdx].Num); got != uint64(it.NumericID) {
			t.Errorf("%s iofbjlaadfg=%d 与 numeric_id=%d 不一致", itemID, got, it.NumericID)
		}
		if _, err := srv.ItemToTree(ctx, it); err != nil {
			t.Errorf("%s ItemToTree 失败: %v", itemID, err)
		}
	}
}

// TestProbeCreatedFields 打印 CreateItem 产出的字段 JSON，用于检查 GUID 和字段结构。
func TestProbeCreatedFields(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "probe6.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "p"}}, logging.Discard(), nil)
	srv.SetDB(db)
	importStaticForProbe(t, db)
	it, err := srv.CreateItem(context.Background(), 1000001, "bob_25018", "backpack", 1)
	if err != nil {
		t.Skip(err)
	}
	t.Logf("wire=%s guid=%s", it.WireClass, it.GUID)
	var arr []map[string]any
	_ = json.Unmarshal([]byte(it.FieldsJSON), &arr)
	for i := 0; i < 6 && i < len(arr); i++ {
		b, _ := json.Marshal(arr[i])
		t.Logf("[%d] %s", i, b)
	}
}

// TestWorkshopToolsAreNotFishingLine 工坊工具和材料不能再落成钓线类。
// 已经按钓线类买进背包的钳子，启动修复后改写成工具类并保留 guid。
func TestWorkshopToolsAreNotFishingLine(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "workshop"}}, logging.Discard(), nil)
	srv.SetDB(db)

	pliers, err := srv.CreateItem(ctx, 1000001, "fishing_pliers", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if pliers.WireClass != "jceogpbfpjh" || pliers.TypeID != 163 {
		t.Fatalf("钓鱼工具 wire=%s type=%d，期望 jceogpbfpjh/163", pliers.WireClass, pliers.TypeID)
	}
	fields, err := FieldsFromJSON(pliers.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	idIdx := fieldIdxByCS(pliers.WireClass, csNumericID)
	guidIdx := fieldIdxByCS(pliers.WireClass, csGUID)
	if idIdx < 0 || guidIdx < 0 {
		t.Fatal("工具类缺少数字 id 或 guid")
	}
	if got := u64Of(fields[idIdx].Num); got != 6347 {
		t.Fatalf("钓鱼工具数字 id=%d，期望 6347", got)
	}
	if fields[guidIdx].Hex == "" {
		t.Fatal("钓鱼工具 guid 为空")
	}

	mat, err := srv.CreateItem(ctx, 1000001, "charcoal_bag", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if mat.WireClass != "nacllfpgomk" || mat.TypeID != 142 {
		t.Fatalf("木炭 wire=%s type=%d，期望 nacllfpgomk/142", mat.WireClass, mat.TypeID)
	}
	mf, err := FieldsFromJSON(mat.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	qi := fieldIdxByCS(mat.WireClass, csQty)
	if qi < 0 || u64Of(mf[qi].Num) == 0 {
		t.Fatalf("材料数量字段=%d 值=%d", qi, u64Of(mf[qi].Num))
	}

	line, err := srv.CreateItem(ctx, 1000001, "mono_15097", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if line.WireClass != fallbackWireClass || line.TypeID != fallbackTypeID {
		t.Fatalf("钓线被改类: %s/%d", line.WireClass, line.TypeID)
	}

	def, err := db.GetItemDefByItemID(ctx, "fishing_pliers")
	if err != nil || def == nil {
		t.Fatal(err)
	}
	const badGUID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bad := &store.Item{
		GUID: badGUID, UID: 1000001, ItemID: "fishing_pliers", NumericID: def.NumericID,
		WireClass: fallbackWireClass, TypeID: fallbackTypeID, Container: "backpack",
		Qty: 1, Durability: 0.8, Workmanship: 1, FieldsJSON: "[]",
	}
	if err := db.InsertItem(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := srv.RepairItemPhysics(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetItemByGUID(ctx, badGUID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.WireClass != "jceogpbfpjh" || got.TypeID != 163 {
		t.Fatalf("已购钳子修复后 wire=%s type=%d", got.WireClass, got.TypeID)
	}
	if got.Durability != 0.8 {
		t.Fatalf("修复改写了耐久 %f", got.Durability)
	}
	gf, err := FieldsFromJSON(got.FieldsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if hex := gf[fieldIdxByCS(got.WireClass, csGUID)].Hex; hex != badGUID {
		t.Fatalf("修复后 guid=%s，期望保持 %s", hex, badGUID)
	}
}
