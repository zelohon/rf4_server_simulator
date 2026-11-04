package gameserver

import (
	"context"
	"math"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func dynamicTestCtx(srv *Server, tree *proto.Value) *Ctx {
	return &Ctx{
		Sess: newProbeSess(srv), Main: 0, Sub: 0, Tree: tree,
		DecodeOK: true, ExpectResponse: true, Log: logging.Discard(),
	}
}

func TestDynamicProtocolHandlersShapeAndEcho(t *testing.T) {
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	for _, k := range []key2{{13, 5}, {13, 6}, {17, 9}, {17, 10}, {17, 11}, {3, 40}, {3, 41}, {1, 10}} {
		if srv.dispatch.lookup(k.main, k.sub) == nil {
			t.Fatalf("%d/%d 未注册动态 handler", k.main, k.sub)
		}
	}

	t.Run("1/10 sonar echoes water", func(t *testing.T) {
		tree := rigRequestTree(t, proto.StrVal("loc12_01"))
		body, ok := hDynamicSonar(dynamicTestCtx(srv, tree))
		if !ok {
			t.Fatal("1/10 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "efnipphkbce" || dec.TypeID != 15003 {
			t.Fatalf("1/10 响应类型=%s/%d", dec.Class, dec.TypeID)
		}
		f0 := dec.Field(0)
		if got := f0.StrOr(); got != "loc12_01" {
			t.Fatalf("1/10 f0=%q, 期望 loc12_01", got)
		}
		if grid := dec.Field(1).Val; grid == nil || grid.Kind != proto.KindArgs || grid.ArgCodeStr() != "15004" {
			t.Fatalf("1/10 f1 应为 args(15004)，实得 %#v", grid)
		}
		if reason := srv.dispatch.checkRegisteredResponse(1, 10, body, srv.Table()); reason != "" {
			t.Fatalf("1/10 合同守卫误拒: %s", reason)
		}
	})

	t.Run("3/40 echoes reward guid", func(t *testing.T) {
		guid := "0123456789abcdef0123456789abcdef"
		tree := rigRequestTree(t, proto.Guid(guid), proto.Bool(true))
		body, ok := hDynamicRewardEntry(dynamicTestCtx(srv, tree))
		if !ok {
			t.Fatal("3/40 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "enikeammfen" || dec.TypeID != 2015 {
			t.Fatalf("3/40 响应类型=%s/%d", dec.Class, dec.TypeID)
		}
		f2 := dec.Field(2)
		if got := f2.GuidHex(); got != guid {
			t.Fatalf("3/40 f2=%s, 期望回显 %s", got, guid)
		}
		if reason := srv.dispatch.checkRegisteredResponse(3, 40, body, srv.Table()); reason != "" {
			t.Fatalf("3/40 合同守卫误拒: %s", reason)
		}
	})

	t.Run("17/9 echoes broken component guid without DB", func(t *testing.T) {
		srv := NewServer(config.Game{}, logging.Discard(), nil)
		rodGUID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		brokenGUID := "fedcba9876543210fedcba9876543210"
		tree := rigRequestTree(t, proto.Guid(rodGUID), proto.Guid(brokenGUID), proto.U32(1), proto.Null())
		body, ok := hDynamicRodBreak(dynamicTestCtx(srv, tree))
		if !ok {
			t.Fatal("17/9 未处理")
		}
		dec, _, err := proto.DecodeValue(body, 0, srv.Table())
		if err != nil {
			t.Fatal(err)
		}
		if dec.Class != "ikmdmfoojfg" || dec.TypeID != 11028 {
			t.Fatalf("17/9 响应类型=%s/%d", dec.Class, dec.TypeID)
		}
		if u64Of(dec.Fields[0].Num) != 0 {
			t.Fatal("无数据库时兼容响应 f0 应为 false")
		}
		f1 := dec.Field(1)
		if got := f1.GuidHex(); got != brokenGUID {
			t.Fatalf("17/9 f1=%s, 期望回显 %s", got, brokenGUID)
		}
		if reason := srv.dispatch.checkRegisteredResponse(17, 9, body, srv.Table()); reason != "" {
			t.Fatalf("17/9 合同守卫误拒: %s", reason)
		}
	})

	t.Run("snapshot handlers return dynamic 800", func(t *testing.T) {
		for _, k := range []key2{{13, 6}, {17, 10}, {17, 11}} {
			body, ok := hDynamicRigSnapshot(dynamicTestCtx(srv, rigRequestTree(t)))
			if !ok {
				t.Fatalf("%d/%d 未处理", k.main, k.sub)
			}
			dec, _, err := proto.DecodeValue(body, 0, srv.Table())
			if err != nil {
				t.Fatal(err)
			}
			if dec.Class != "dlbahppbdad" || dec.TypeID != 800 {
				t.Fatalf("%d/%d 响应=%s/%d", k.main, k.sub, dec.Class, dec.TypeID)
			}
			if reason := srv.dispatch.checkRegisteredResponse(k.main, k.sub, body, srv.Table()); reason != "" {
				t.Fatalf("%d/%d 合同守卫误拒: %s", k.main, k.sub, reason)
			}
		}
	})
}

func TestDynamicHanditemThrowConsumesLoadedStack(t *testing.T) {
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	ctx := context.Background()
	const uid = int64(1000001)
	hand, err := srv.CreateItem(ctx, uid, "wh_pc_gb", "backpack", 1)
	if err != nil {
		t.Fatalf("创建手持物失败: %v", err)
	}
	bait, err := srv.CreateItem(ctx, uid, "worm_01", "rig", 1)
	if err != nil {
		t.Fatalf("创建装填物失败: %v", err)
	}
	srv.setStackCount(ctx, bait, 2)
	srv.setHanditemLoadedRef(ctx, hand.GUID, bait.GUID)

	target := wiregen.MustMakeObj("olieodeoaof", map[int]*proto.Value{0: proto.F32(17.5)})
	req := rigRequestTree(t, proto.Guid(hand.GUID), target)
	body, ok := hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 第一次未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Class != "lmbomkliaad" || dec.TypeID != 805 {
		t.Fatalf("13/5 响应=%s/%d", dec.Class, dec.TypeID)
	}
	f0 := dec.Field(0)
	gotDist := float64(proto.F32FromBits(f0.U32()))
	if math.Abs(gotDist-17.5) > 0.001 {
		t.Fatalf("13/5 f0=%f, 期望 17.5", gotDist)
	}
	upserts := dec.Field(3).Val
	if upserts == nil || upserts.ArgCount() != 1 {
		t.Fatalf("13/5 f3 应更新装填物，实得 %#v", upserts)
	}
	fresh, err := srv.DB.GetItemByGUID(ctx, bait.GUID)
	if err != nil || fresh == nil {
		t.Fatalf("第一次抛投后装填物应剩 1 份: %v", err)
	}
	if left := srv.stackCountOf(ctx, fresh); left != 1 {
		t.Fatalf("第一次抛投后剩 %d 份，期望 1", left)
	}

	body, ok = hDynamicHanditemThrow(dynamicTestCtx(srv, req))
	if !ok {
		t.Fatal("13/5 第二次未处理")
	}
	dec, _, err = proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if removed := dec.Field(5).Val; removed == nil || removed.Kind != proto.KindGuidArr ||
		removed.ArgCount() != 1 || removed.ArgItem(0).GuidHex() != bait.GUID {
		t.Fatalf("第二次抛投 f5 应移除装填物 %s，实得 %#v", bait.GUID, removed)
	}
	if left, _ := srv.DB.GetItemByGUID(ctx, bait.GUID); left != nil {
		t.Fatal("装填物用尽后仍存在（被错误退回/未删除）")
	}
	if loaded := srv.handitemLoadedGuid(ctx, hand.GUID); loaded != "" {
		t.Fatalf("装填物用尽后引用未清空: %s", loaded)
	}
	if reason := srv.dispatch.checkRegisteredResponse(13, 5, body, srv.Table()); reason != "" {
		t.Fatalf("13/5 合同守卫误拒: %s", reason)
	}
}

func TestDynamicEquippedItemsUsesRigRows(t *testing.T) {
	srv, _ := newRigTestServer(t)
	registerDynamicProtocolHandlers(srv.dispatch)
	ctx := context.Background()
	item, err := srv.CreateItem(ctx, 1000001, "mono_15097", "rig", 1)
	if err != nil {
		t.Fatalf("创建钓线失败: %v", err)
	}
	body, ok := hDynamicEquippedItems(dynamicTestCtx(srv, rigRequestTree(t)))
	if !ok {
		t.Fatal("3/41 未处理")
	}
	dec, _, err := proto.DecodeValue(body, 0, srv.Table())
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != proto.KindArgs || dec.ArgCodeStr() != "61" || dec.ArgCount() != 1 {
		t.Fatalf("3/41 应回 args(61)[1 件]，实得 %#v", dec)
	}
	got := dec.ArgItem(0)
	if got == nil || got.Kind != proto.KindObj || got.TypeID != uint32(item.TypeID) {
		t.Fatalf("3/41 物品树不符: %#v", got)
	}
	if reason := srv.dispatch.checkRegisteredResponse(3, 41, body, srv.Table()); reason != "" {
		t.Fatalf("3/41 合同守卫误拒: %s", reason)
	}
}
