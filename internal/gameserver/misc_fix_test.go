package gameserver

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/tunnel"
)

// TestMiscHandlerContracts 验证杂项命令的响应可解码、形状正确且持久化结果符合预期。
func TestMiscHandlerContracts(t *testing.T) {
	srv, sess, ctx := newMiscProbe(t)
	guids, _ := srv.DB.ItemGUIDsByContainers(ctx, 1000001, []string{"backpack"})
	if len(guids) < 2 {
		t.Fatalf("播种物品不足: %d", len(guids))
	}

	// ---- 4/9 suflag：状态位置位和清位 ----
	t.Run("4_9_suflag", func(t *testing.T) {
		// 取一件带状态位字段的物品（竿/轮/钩等装备类）
		g := ""
		for _, cand := range guids {
			row, err := srv.DB.GetItemByGUID(ctx, cand)
			if err == nil && row != nil && fieldIdxByCS(row.WireClass, csStateEnum) >= 0 {
				g = cand
				break
			}
		}
		if g == "" {
			t.Skip("播种物品均无状态位字段")
		}
		before := miscStateFlags(t, srv, g)
		// 整数参数依次为 flag 和 op。
		body, ok := hItemSetFlag(miscCtx(sess, 4, 9,
			proto.Args524(proto.GuidArr([]string{g}), proto.U32(1), proto.U32(1))))
		if !ok || len(body) == 0 {
			t.Fatal("4/9 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("4/9 响应不可解码: %v", err)
		}
		if dec.Kind != proto.KindObj || dec.Class != "dlbahppbdad" {
			t.Fatalf("4/9 响应应为 800 dlbahppbdad，实得 %s", dec.Class)
		}
		if after := miscStateFlags(t, srv, g); after != (before|1)&0xFFFFFFFF {
			t.Fatalf("置位失败: before=%d after=%d", before, after)
		}
		// op=0 清位（cur &^ flag）
		if _, ok := hItemSetFlag(miscCtx(sess, 4, 9,
			proto.Args524(proto.GuidArr([]string{g}), proto.U32(1), proto.U32(0)))); !ok {
			t.Fatal("4/9 清位未处理")
		}
		if after := miscStateFlags(t, srv, g); after != before&^1&0xFFFFFFFF {
			t.Fatalf("清位失败: after=%d", after)
		}
		// 单参形态默认执行置位。
		if _, ok := hItemSetFlag(miscCtx(sess, 4, 9,
			proto.Args524(proto.GuidArr([]string{g}), proto.U32(2)))); !ok {
			t.Fatal("4/9 单参未处理")
		}
		if after := miscStateFlags(t, srv, g); after&2 == 0 {
			t.Fatalf("单参缺省 op=1 置位失败: after=%d", after)
		}
	})

	// ---- 4/10 set_slot：格位绑定持久化 ----
	t.Run("4_10_set_slot", func(t *testing.T) {
		a, b := guids[0], guids[1]
		// 请求参数为 [容器号, GUID, 槽位]；命令不返回响应。
		if body, ok := hItemSetSlot(miscCtx(sess, 4, 10,
			proto.Args524(proto.U32(0), proto.Guid(a), proto.U32(77)))); !ok || body != nil {
			t.Fatalf("4/10 应 fire-and-forget（nil body），ok=%v body=%x", ok, body)
		}
		rowA, err := srv.DB.GetItemByGUID(ctx, a)
		if err != nil || rowA == nil || rowA.Container != "backpack" || rowA.Slot != 77 {
			t.Fatalf("A 绑定失败: %+v err=%v", rowA, err)
		}
		// 绑 B → 同格：A 被解绑回背包未指派格（container='backpack', slot=0）
		if _, ok := hItemSetSlot(miscCtx(sess, 4, 10,
			proto.Args524(proto.U32(0), proto.Guid(b), proto.U32(77)))); !ok {
			t.Fatal("4/10 B 未处理")
		}
		rowA2, _ := srv.DB.GetItemByGUID(ctx, a)
		if rowA2 == nil || rowA2.Slot != 0 {
			t.Fatalf("A 未被解绑回未指派格: slot=%d", rowA2.Slot)
		}
		// container_slot 落绑定（uid, container, slot -> guid）
		var g string
		if err := srv.DB.SQL.QueryRow(
			`SELECT guid FROM container_slot WHERE uid=1000001 AND container=0 AND slot=77`).Scan(&g); err != nil || g != b {
			t.Fatalf("container_slot 绑定错误: %q err=%v", g, err)
		}
	})

	// ---- 3/20 change_name ----
	t.Run("3_20_change_name", func(t *testing.T) {
		body, ok := hChangeName(miscCtx(sess, 3, 20, proto.Args524(proto.StrVal("钓鱼佬 Pro"))))
		if !ok || len(body) == 0 {
			t.Fatal("3/20 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("3/20 响应不可解码: %v", err)
		}
		if dec.Kind != proto.KindObj || dec.Class != "ghfbofgdflm" {
			t.Fatalf("3/20 响应应为 307 ghfbofgdflm，实得 %s", dec.Class)
		}
		f1 := dec.Field(1)
		if got := f1.StrOr(); got != "钓鱼佬 Pro" {
			t.Fatalf("307.f1 应为新昵称，实得 %q", got)
		}
		p, err := srv.DB.GetProfile(ctx, 1000001)
		if err != nil || p.Name != "钓鱼佬 Pro" {
			t.Fatalf("DB 昵称未更新: %q err=%v", p.Name, err)
		}
		// 空名称交给兜底链。
		if b, ok := hChangeName(miscCtx(sess, 3, 20, proto.Args524())); ok || b != nil {
			t.Fatal("空名应 PASS（nil,false）")
		}
	})

	// ---- 3/21 change_region ----
	t.Run("3_21_change_region", func(t *testing.T) {
		body, ok := hChangeRegion(miscCtx(sess, 3, 21, proto.Args524(proto.StrVal("RU"))))
		if !ok || len(body) == 0 {
			t.Fatal("3/21 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("3/21 响应不可解码: %v", err)
		}
		if dec.Kind != proto.KindObj || dec.Class != "ghfbofgdflm" {
			t.Fatalf("3/21 响应应为 307，实得 %s", dec.Class)
		}
		p, _ := srv.DB.GetProfile(ctx, 1000001)
		if p.Region != "RU" {
			t.Fatalf("DB region 未更新: %q", p.Region)
		}
		// 整数形态也接受，包括 0。
		if _, ok := hChangeRegion(miscCtx(sess, 3, 21, proto.Args524(proto.U32(3)))); !ok {
			t.Fatal("3/21 u32 形态未处理")
		}
		p2, _ := srv.DB.GetProfile(ctx, 1000001)
		if p2.Region != "3" {
			t.Fatalf("u32 region 未按 str(3) 落库: %q", p2.Region)
		}
	})

	// ---- 3/23 邮箱验证码 ----
	t.Run("3_23_verify_email", func(t *testing.T) {
		sess.SetState("email_pending", map[string]any{"email": "a@b.c"})
		body, ok := hVerifyEmail(miscCtx(sess, 3, 23,
			proto.Args524(proto.StrVal("tok123"), proto.StrVal("654321"))))
		if !ok || len(body) == 0 {
			t.Fatal("3/23 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("3/23 响应不可解码: %v", err)
		}
		if dec.Kind != proto.KindObj || dec.Class != "ghfbofgdflm" {
			t.Fatalf("3/23 响应应为 307，实得 %s", dec.Class)
		}
		p, _ := srv.DB.GetProfile(ctx, 1000001)
		if p.Email != "a@b.c" {
			t.Fatalf("pending 邮箱未落库: %q", p.Email)
		}
		if v, _ := sess.GetState("email_verified"); v != true {
			t.Fatal("email_verified 未置位")
		}
		if v, _ := sess.GetState("email_pending"); v != nil {
			t.Fatal("email_pending 未被清除")
		}
	})

	// ---- 24/10 公聊回显：载荷形状 ----
	t.Run("24_10_chat_echo_shape", func(t *testing.T) {
		payload := buildChatEchoPush("nav1_03[CN]", "hello", 1000001, 227)
		if reason := checkPushArgs(24, 27, payload); reason != "" {
			t.Fatalf("24/27 形状与合同不符: %s", reason)
		}
		body, err := proto.EncodeValue(payload, srv.Table())
		if err != nil {
			t.Fatalf("编码失败: %v", err)
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("回环解码失败: %v", err)
		}
		if dec.Kind != proto.KindArgs || len(dec.Items) != 3 {
			t.Fatalf("24/27 应 args[3]，实得 %d 参", len(dec.Items))
		}
		m := dec.Items[0]
		if !m.IsKind(proto.KindObj) || m.TypeID != 401 || m.Class != "lfbdbaffeai" {
			t.Fatalf("args[0] 应为 401 lfbdbaffeai，实得 %s/%d", m.Class, m.TypeID)
		}
		mf1 := m.Field(1)
		if got := mf1.StrOr(); got != "hello" {
			t.Fatalf("401.f1 应为文本，实得 %q", got)
		}
		mf2 := m.Field(2)
		if mf2.U32() != 1000001 {
			t.Fatalf("401.f2 应为发送者 uid，实得 %d", mf2.U32())
		}
		mf0 := m.Field(0)
		if mf0.U32() != 0 {
			t.Fatalf("401.f0 应为 CHAT_KIND_SAY=0，实得 %d", mf0.U32())
		}
		if dec.Items[1].StrOr() != "nav1_03[CN]" || dec.Items[2].U32Val() != 227 {
			t.Fatalf("args[1]=locationId / args[2]=usersCount 形状错误")
		}
	})

	// ---- 24/10 公聊回显：端到端走 24/27 推送 ----
	t.Run("24_10_chat_echo_push", func(t *testing.T) {
		srv2, sess2, _ := newMiscProbe(t)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		type hsRes struct {
			c   *tunnel.Conn
			err error
		}
		scCh := make(chan hsRes, 1)
		go func() {
			nc, aerr := ln.Accept()
			if aerr != nil {
				scCh <- hsRes{err: aerr}
				return
			}
			c, herr := tunnel.HandshakeServer(nc, "misc")
			scCh <- hsRes{c: c, err: herr}
		}()
		nc, derr := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if derr != nil {
			t.Fatal(derr)
		}
		gc, cerr := tunnel.HandshakeClient(nc, "misc")
		if cerr != nil {
			t.Fatal(cerr)
		}
		sr := <-scCh
		if sr.err != nil {
			t.Fatal(sr.err)
		}
		sess2.tunnel = sr.c             // 游戏服持隧道主服侧
		srv2.sessions[sess2.ID] = sess2 // 公共聊天向已注册会话广播。
		defer func() { _ = gc.Close(); _ = sr.c.Close() }()

		type pushed struct {
			rec tunnel.RecordType
			env *proto.Envelope
			err error
		}
		recCh := make(chan pushed, 1)
		go func() {
			rec, _, payload, rerr := gc.ReadRecord()
			if rerr != nil {
				recCh <- pushed{err: rerr}
				return
			}
			env, ok := proto.ParseEnvelope(payload)
			if !ok {
				recCh <- pushed{err: fmt.Errorf("信封解析失败")}
				return
			}
			recCh <- pushed{rec: rec, env: env}
		}()

		body, ok := hSocialPublic(miscCtx(sess2, 24, 10, proto.Args524(proto.StrVal("区域聊天测试"))))
		if !ok || body != nil {
			t.Fatalf("24/10 应 fire-and-forget（nil body），ok=%v body=%x", ok, body)
		}
		select {
		case p := <-recCh:
			if p.err != nil {
				t.Fatalf("读推送失败: %v", p.err)
			}
			if p.rec != tunnel.TypeData {
				t.Fatalf("应 DATA 记录，实得 %v", p.rec)
			}
			// 回显必须使用 24/27。
			if p.env.Main != 24 || p.env.Sub != 27 {
				t.Fatalf("回显必须走 24/27，实得 %d/%d", p.env.Main, p.env.Sub)
			}
			dec, _, derr := proto.DecodeValue(p.env.Body, 0, srv2.Table())
			if derr != nil {
				t.Fatalf("推送 body 不可解码: %v", derr)
			}
			if dec.Kind != proto.KindArgs || len(dec.Items) != 3 {
				t.Fatalf("24/27 body 应 args[3]")
			}
			mf1 := dec.Items[0].Field(1)
			if got := mf1.StrOr(); got != "区域聊天测试" {
				t.Fatalf("回显文本不符: %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("未收到 24/27 推送")
		}
		// 空消息 / GM 指令前缀：不回包、不推送（无第二条记录即通过——超时前收不到）
		if b, ok := hSocialPublic(miscCtx(sess2, 24, 10, proto.Args524())); !ok || b != nil {
			t.Fatal("空消息应 handled+nil")
		}
		if b, ok := hSocialPublic(miscCtx(sess2, 24, 10, proto.Args524(proto.StrVal("/help")))); !ok || b != nil {
			t.Fatal("GM 指令应 handled+nil（不回显）")
		}
		select {
		case p := <-recCh:
			t.Fatalf("空消息/GM 指令不应产生推送: %v", p.env)
		case <-time.After(300 * time.Millisecond):
		}
	})

	// ---- 容器枚举全表 ----
	t.Run("container_enum", func(t *testing.T) {
		cases := map[uint64]string{
			0: "backpack", 1: "tent", 2: "car", 3: "boat", 4: "scooter",
			5: "keepnet", 6: "tent", 7: "car", 8: "boat", 9: "scooter",
			10: "house", 11: "house", 12: "backpack", 99: "backpack",
		}
		for num, want := range cases {
			if got := containerEnumName(num); got != want {
				t.Errorf("容器 %d -> %q, 期望 %q", num, got, want)
			}
		}
		// 4/6 使用同一张容器映射表。
		if got := containerName(1); got != "tent" {
			t.Fatalf("containerName(1)=%q, 期望 tent", got)
		}
		if got := containerName(5); got != "keepnet" {
			t.Fatalf("containerName(5)=%q, 期望 keepnet", got)
		}
	})

	// ---- 4/5 非建模容器返回空 ----
	t.Run("4_5_nonmodeled_container_empty", func(t *testing.T) {
		body, ok := hItemDetails(miscCtx(sess, 4, 5,
			proto.Args524(proto.U32(1), proto.GuidArr(guids))))
		if !ok || len(body) == 0 {
			t.Fatal("4/5 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatalf("4/5 响应不可解码: %v", err)
		}
		if dec.Kind != proto.KindArgs || len(dec.Items) != 0 {
			t.Fatalf("非建模容器 4/5 应回空 args(61)，实得 %d 件", len(dec.Items))
		}
		// 背包容器仍按 GUID 返回物品。
		body, ok = hItemDetails(miscCtx(sess, 4, 5,
			proto.Args524(proto.U32(0), proto.GuidArr(guids[:1]))))
		if !ok {
			t.Fatal("4/5 背包未处理")
		}
		dec, _, _ = proto.DecodeValue(body, 0, srv.Table())
		if dec.Kind != proto.KindArgs || len(dec.Items) != 1 {
			t.Fatalf("背包容器 4/5 应回 1 件，实得 %d", len(dec.Items))
		}
	})
}

// newMiscProbe 创建带探针数据库和服务器的测试会话。
func newMiscProbe(t *testing.T) (*Server, *Session, context.Context) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "misc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForProbe(t, db)
	ctx := context.Background()
	srv := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "misc"}}, logging.Discard(), nil)
	srv.SetDB(db)
	if err := srv.EnsurePlayer(ctx, 1000001); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: 1, Userid: 1000001, Srv: srv, log: logging.Discard()}
	sess.state = map[string]any{}
	return srv, sess, ctx
}

// miscCtx 探针请求上下文。
func miscCtx(sess *Session, main, sub int, tree *proto.Value) *Ctx {
	return &Ctx{Sess: sess, Main: main, Sub: sub, CallID: 1, Tree: tree, DecodeOK: true, Log: logging.Discard()}
}

// miscStateFlags 读 DB fields_json 里的状态位（hgaijieklek）。
func miscStateFlags(t *testing.T, srv *Server, guid string) uint64 {
	t.Helper()
	row, err := srv.DB.GetItemByGUID(context.Background(), guid)
	if err != nil || row == nil {
		t.Fatalf("取物品失败: %v", err)
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		t.Fatalf("字段解析失败: %v", err)
	}
	idx := fieldIdxByCS(row.WireClass, csStateEnum)
	if idx < 0 || idx >= len(fields) {
		t.Fatalf("类 %s 无状态位字段", row.WireClass)
	}
	return u64Of(fields[idx].Num)
}
