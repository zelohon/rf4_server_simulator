package gameserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 商店命令 22/x。

// hStoreList 处理 22/1，返回 args(140) 中的店内生效商品。
func hStoreList(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	srv := ctx.Sess.Srv
	storeID := ""
	if ctx.Tree != nil {
		for _, it := range ctx.Tree.Items {
			if it != nil && it.Kind == proto.KindStr && it.Str != nil && *it.Str != "" {
				storeID = *it.Str
				break
			}
		}
	}
	cctx := ctx.Sess.pushCtx()
	out := proto.Args(proto.S("140"))
	items, err := srv.DB.ShopItems(cctx, storeID)
	if err != nil {
		ctx.Log.Warn("读取商店货架失败", "store", storeID, "err", err)
		items = nil
	}
	for _, p := range items {
		out.Items = append(out.Items, wiregen.MustMakeObj("ifdefblccde", map[int]*proto.Value{
			0: proto.StrVal(p.ItemID),
			1: proto.U64(uint64(p.Silver)),
			2: proto.U64(uint64(p.Gold)),
			3: proto.U32(uint32(p.Stock)),
		}))
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// storePrice 只接受该店生效货架上的价格，避免使用全局价格绕过店内库存。
func (s *Server) storePrice(ctx context.Context, storeID, itemID string, clientOffer uint64, goldCol bool) (uint64, error) {
	_ = clientOffer // 客户端报价只用于诊断，绝不作为服务端成交价。
	pick := func(p *store.ShopPrice) (uint64, error) {
		if p == nil {
			return 0, fmt.Errorf("无价目: %s@%s", storeID, itemID)
		}
		price := p.Silver
		cur := "silver"
		if goldCol {
			price = p.Gold
			cur = "gold"
		}
		if price <= 0 {
			return 0, fmt.Errorf("商品没有 %s 价格: %s@%s", cur, storeID, itemID)
		}
		return uint64(price), nil
	}
	p, fellBack, err := s.DB.ShopItemGetWithFallback(ctx, storeID, itemID)
	if err != nil {
		return 0, fmt.Errorf("商品不在该店生效货架: %s@%s: %w", storeID, itemID, err)
	}
	if fellBack {
		// 客户端按全局价目渲染，这类商品不在店铺货架里；回退后才买得到。
		s.log.Warn("商品不在店铺货架，使用全局价目",
			"store", storeID, "item", itemID)
	}
	return pick(p)
}

// prepareStoreGoods 在事务前展开套盒并构造物品对象。
// 构造失败会在扣款前返回，避免产生已扣款但未发货的状态。
func (s *Server) prepareStoreGoods(ctx context.Context, uid int64, itemID string) (items []*store.Item, isBundle bool, err error) {
	def, err := s.DB.GetItemDefByItemID(ctx, itemID)
	if err != nil || def == nil {
		return nil, false, fmt.Errorf("无定义 %s", itemID)
	}
	if def.Category == bundleCategory {
		attrs := defAttrs(def)
		contents, _ := attrs["nbkhfhenbag"].([]any)
		for _, raw := range contents {
			entry, _ := raw.(map[string]any)
			inner, _ := entry["cemjmlpafmc"].(string)
			if inner == "" {
				return nil, true, fmt.Errorf("套盒内含物缺 item_id: %s", itemID)
			}
			qty := int64(1)
			switch q := entry["ljnnpokapig"].(type) {
			case float64:
				qty = int64(q)
			case int64:
				qty = q
			case int:
				qty = int64(q)
			}
			if qty < 1 {
				qty = 1
			}
			it, perr := s.prepareNewItem(ctx, uid, inner, "backpack", qty, "")
			if perr != nil {
				return nil, true, perr
			}
			items = append(items, it)
		}
		if len(items) == 0 {
			return nil, true, fmt.Errorf("套盒没有可发放内含物: %s", itemID)
		}
		return items, true, nil
	}
	it, err := s.prepareNewItem(ctx, uid, itemID, "backpack", 1, "")
	if err != nil {
		return nil, false, err
	}
	return []*store.Item{it}, false, nil
}

// hStoreBuy 处理 22/2 银币购买和 22/3 金币购买：
// 校验价格、构造物品，并在同一事务内扣款、记流水和入库。
func hStoreBuy(goldCol bool) Handler {
	return func(ctx *Ctx) ([]byte, bool) {
		srv := ctx.Sess.Srv
		if !srv.requireDB(ctx) {
			return nil, false
		}
		uid := ctx.Sess.Userid
		strs := treeStrs(ctx.Tree)
		ints := treeInts(ctx.Tree)
		if len(strs) < 2 {
			return []byte{0x0F}, true // Bool false
		}
		storeID, itemID := strs[0], strs[1]
		var offer uint64
		if len(ints) > 0 {
			offer = ints[0]
		}
		cctx := ctx.Sess.pushCtx()
		price, err := srv.storePrice(cctx, storeID, itemID, offer, goldCol)
		if err != nil {
			ctx.Log.Warn("商店拒绝购买", "store", storeID, "item", itemID, "gold", goldCol, "err", err)
			return []byte{0x0F}, true
		}
		prepared, isBundle, err := srv.prepareStoreGoods(cctx, uid, itemID)
		if err != nil {
			ctx.Log.Warn("商店商品不可构造", "item", itemID, "err", err)
			return []byte{0x0F}, true
		}

		tx, err := beginEconomyTx(cctx, srv.DB)
		if err != nil {
			ctx.Log.Warn("商店开启事务失败", "err", err)
			return []byte{0x0F}, true
		}
		defer func() { _ = tx.Rollback() }()

		currency := "silver"
		silverDelta, goldDelta := -int64(price), int64(0)
		if goldCol {
			currency = "gold"
			silverDelta, goldDelta = 0, -int64(price)
		}
		note := fmt.Sprintf("store=%s currency=%s", storeID, currency)
		if err := applyWalletTx(cctx, tx, uid, silverDelta, goldDelta,
			"buy", itemID, "", note); err != nil {
			ctx.Log.Warn("商店扣款失败", "item", itemID, "err", err)
			return []byte{0x0F}, true
		}

		granted := make([]*store.Item, 0, len(prepared))
		for _, it := range prepared {
			row, _, gerr := grantPreparedItemTx(cctx, tx, it, true)
			if gerr != nil {
				ctx.Log.Warn("商店入库失败", "item", it.ItemID, "err", gerr)
				return []byte{0x0F}, true
			}
			granted = append(granted, row)
		}
		// 购买纸质地图（category 40）时同时解锁对应地点，否则客户端旅行页
		// 仍显示未购买。地点 key 取自 item_def 的 dhaenhabecc 字段。
		// 必须走事务内查询：主库只有一条连接，事务未提交时再调用 store.DB
		// 会卡住，5 秒后上下文取消并把事务回滚，提交就报 already committed。
		if strings.HasPrefix(itemID, "papermap") {
			if def, derr := itemDefByItemIDTx(cctx, tx, itemID); derr == nil && def != nil {
				if place := itemDefStr(def, "dhaenhabecc"); place != "" {
					if _, e := tx.ExecContext(cctx, `
						INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?, 'buyout', ?)
						ON CONFLICT(uid, place, kind) DO NOTHING`,
						uid, place, float64(time.Now().Unix())); e != nil {
						ctx.Log.Warn("地图解锁失败", "item", itemID, "place", place, "err", e)
					} else {
						ctx.Log.Info("购买地图并解锁地点", "item", itemID, "place", place, "price", price)
					}
				}
			}
		}
		if err := tx.Commit(); err != nil {
			ctx.Log.Warn("商店事务提交失败", "item", itemID, "err", err)
			return []byte{0x0F}, true
		}

		trees := make([]*proto.Value, 0, len(granted))
		for _, row := range granted {
			tree, terr := srv.ItemToTree(cctx, row)
			if terr != nil {
				ctx.Log.Warn("商店物品树构造失败", "item", itemID, "err", terr)
				continue
			}
			trees = append(trees, tree)
		}
		level := spawnLevelKey
		srv.levelMu.Lock()
		if lv, ok := srv.levelMap[uid]; ok && lv != "" {
			level = lv
		}
		srv.levelMu.Unlock()
		receipt := proto.Obj("pcfflcdpjgk", 166,
			proto.Field{Op: "STR", Str: &storeID},
			proto.Field{Op: "U32", Num: ptrU64(uint64(uid))},
			proto.Field{Op: "U32", Num: ptrU64(0)},
			proto.Field{Op: "U64", Num: ptrU64(price)},
			proto.Field{Op: "OPT", Num: ptrU64(0)},
			proto.Field{Op: "STR", Str: &level},
		)
		if len(trees) > 0 {
			attachReceipt(trees[0], receipt)
		}
		f1 := proto.Null()
		f2 := proto.Null()
		if isBundle {
			if len(trees) > 0 {
				f2 = proto.Args(proto.S("61"), trees...)
			}
		} else if len(trees) > 0 {
			f1 = proto.Args(proto.S("61"), trees...)
		}
		obj804, err := wiregen.MakeObj("jfafgmcfjkc", map[int]*proto.Value{
			0: proto.StrVal(itemID),
			1: f1,
			2: f2,
			3: proto.Null(),
		})
		if err != nil {
			return nil, false
		}
		body, err := proto.EncodeValue(obj804, ctx.Sess.Table())
		if err != nil {
			return nil, false
		}
		ctx.Sess.Push2_1()
		return body, true
	}
}
func boolToI(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// hStoreReturn 处理 22/4，返回 800 背包全量快照。
func hStoreReturn(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	out := proto.Args(proto.S("61"))
	guids, _ := srv.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"backpack"})
	for _, g := range guids {
		if row, err := srv.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
				out.Items = append(out.Items, tree)
			}
		}
	}
	body, err := proto.EncodeValue(build800(out, proto.Null(), proto.Null()), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hStoreATMQuery 处理 22/6，固定返回 false 且不修改货币。
func hStoreATMQuery(ctx *Ctx) ([]byte, bool) {
	return []byte{0x0F}, true
}

// treeStrs、treeInts 分别读取顶层 args 中的字符串和整数序列。
func treeStrs(v *proto.Value) []string {
	var out []string
	if v == nil {
		return out
	}
	for _, it := range v.Items {
		if it != nil && it.Kind == proto.KindStr && it.Str != nil {
			out = append(out, *it.Str)
		}
	}
	return out
}

func treeInts(v *proto.Value) []uint64 {
	var out []uint64
	if v == nil {
		return out
	}
	for _, it := range v.Items {
		if it != nil && (it.Kind == proto.KindU32 || it.Kind == proto.KindU64) {
			out = append(out, it.Num)
		}
	}
	return out
}

func registerShopHandlers(d *Dispatch) {
	d.Register(22, 1, hStoreList)
	d.Register(22, 2, hStoreBuy(false))
	d.Register(22, 3, hStoreBuy(true))
	d.Register(22, 4, hStoreReturn)
	d.Register(22, 6, hStoreATMQuery)
}

// attachReceipt 将购买收据 166 写入物品树的对应字段；字段缺失时补 null。
func attachReceipt(tree *proto.Value, receipt *proto.Value) {
	if tree == nil || tree.Kind != proto.KindObj {
		return
	}
	idx := fieldIdxByCS(tree.Class, csOrigin)
	if idx < 0 || idx >= len(tree.Fields) {
		return
	}
	tree.Fields[idx].Op = "VAL"
	tree.Fields[idx].Val = receipt
}
