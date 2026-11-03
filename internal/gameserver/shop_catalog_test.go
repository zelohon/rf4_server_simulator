package gameserver

import (
	"context"
	"testing"

	"rf4server/internal/proto"
)

func TestStoreListUsesCatalogTemplateAndUnknownStoreStaysEmpty(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	ctx := economyTestCtx(srv, 2601, proto.Args524(proto.StrVal("atm_006")))
	body, ok := hStoreList(ctx)
	if !ok {
		t.Fatal("22/1 应返回响应")
	}
	decoded, _, err := proto.DecodeValue(body, 0, ctx.Sess.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != proto.KindArgs || decoded.ArgCodeStr() != "140" || decoded.ArgCount() != 5 {
		t.Fatalf("22/1 模板货架不符: kind=%v code=%q count=%d", decoded.Kind, decoded.ArgCodeStr(), decoded.ArgCount())
	}
	first := decoded.ArgItem(0)
	if first == nil {
		t.Fatal("22/1 ATM 首项为空")
	}
	itemID := first.Field(0)
	silver := first.Field(1)
	gold := first.Field(2)
	if itemID.StrOr() != "wallet_50" || silver.U64() != 0 || gold.U64() != 49 {
		t.Fatalf("22/1 ATM 首项不符: %+v", first)
	}
	price, err := srv.storePrice(context.Background(), "atm_006", "wallet_50", 0, true)
	if err != nil || price != 49 {
		t.Fatalf("22/3 模板定价不符: price=%d err=%v", price, err)
	}

	if _, err := db.SQL.Exec(`INSERT INTO item_price(item_id,silver,gold,stock)
		VALUES('global_only',100,1,99)`); err != nil {
		t.Fatal(err)
	}
	unknownCtx := economyTestCtx(srv, 2601, proto.Args524(proto.StrVal("not_a_catalog_store")))
	body, ok = hStoreList(unknownCtx)
	if !ok {
		t.Fatal("未知店 22/1 仍应回合法空响应")
	}
	decoded, _, err = proto.DecodeValue(body, 0, unknownCtx.Sess.Table())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ArgCodeStr() != "140" || decoded.ArgCount() != 0 {
		t.Fatalf("未知店不得回全局价目兜底: code=%q count=%d", decoded.ArgCodeStr(), decoded.ArgCount())
	}
}

// 客户端按全局价目渲染商店，有约 16% 的商品不在任何店铺货架里。
// 购买这类商品必须能回退到全局价目，否则客户端只会收到通用失败
// （表现为"发生内部错误"）。但回退只对已知店铺开放，避免伪造店铺 ID
// 购买任意商品。
func TestStorePriceGlobalFallbackOnlyForKnownStore(t *testing.T) {
	srv, db := newEconomyTxTestServer(t)
	if _, err := db.SQL.Exec(`INSERT INTO item_price(item_id,silver,gold,stock)
		VALUES('global_only_rod',0,9952,1)`); err != nil {
		t.Fatal(err)
	}

	// 已知店铺 + 商品不在货架 → 回退全局价目
	price, err := srv.storePrice(context.Background(), "atm_006", "global_only_rod", 0, true)
	if err != nil {
		t.Fatalf("已知店铺应回退全局价目: %v", err)
	}
	if price != 9952 {
		t.Fatalf("回退价格不符: %d", price)
	}

	// 未知店铺 → 一律拒绝
	if _, err := srv.storePrice(context.Background(), "not_a_catalog_store", "global_only_rod", 0, true); err == nil {
		t.Fatal("未知店铺不得回退全局价目")
	}

	// 店内已有货架的商品仍走店内价，不受影响
	if price, err := srv.storePrice(context.Background(), "atm_006", "wallet_50", 0, true); err != nil || price != 49 {
		t.Fatalf("店内商品价格被影响: price=%d err=%v", price, err)
	}
}
