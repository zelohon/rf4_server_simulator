package gameserver

import (
	"errors"
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
	"time"
)

// world 命令族（25/27/29）和背包变动命令（4/6-4/8）。
// world 族通常返回 800 容器快照。

// rigSnapshot800 构造背包和装备容器的 800 快照。
func (s *Server) rigSnapshot800(ctx *Ctx) []byte {
	cctx := ctx.Sess.pushCtx()
	bp := proto.Args(proto.S("61"))
	rg := proto.Args(proto.S("61"))
	guids, _ := s.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"backpack"})
	for _, g := range guids {
		if row, err := s.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := s.ItemToTree(cctx, row); terr == nil {
				bp.Items = append(bp.Items, tree)
			}
		}
	}
	rguids, _ := s.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"rig"})
	for _, g := range rguids {
		if row, err := s.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := s.ItemToTree(cctx, row); terr == nil {
				rg.Items = append(rg.Items, tree)
			}
		}
	}
	body, err := proto.EncodeValue(build800(bp, rg, proto.Null()), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody()
	}
	return body
}

func registerWorldHandlers(d *Dispatch) {
	// 27/x RemoteSettings：返回容器快照，27/4 返回空集合。
	d.Register(27, 1, hWorldSnapshot)
	d.Register(27, 2, hWorldSnapshot)
	d.Register(27, 3, hWorldSnapshot)
	d.Register(27, 4, hWorldRemoteSettingsEmpty)
	// 25/x 船只与燃料：返回容器快照。
	d.Register(25, 1, hWorldSnapshot)
	d.Register(25, 2, hWorldSnapshot)
	d.Register(25, 4, hWorldSnapshot)
	d.Register(25, 5, hWorldSnapshot)
	d.Register(25, 6, hU64Zero) // 船只/加油配置 → long 0（回 800 快照是自造形状错误）
	// 29/x 赛事结果：返回 null。
	for _, sub := range []uint8{1, 2, 3, 4, 5} {
		d.Register(29, sub, hWorldNull)
	}
	// 背包变动
	d.Register(4, 6, hItemMove)
	d.Register(4, 7, hItemRemove)
	d.Register(4, 8, hItemUse)
	d.Register(3, 37, hPlaceBuyoutTx)
}

func hWorldSnapshot(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	return ctx.Sess.Srv.rigSnapshot800(ctx), true
}

func hWorldRemoteSettingsEmpty(ctx *Ctx) ([]byte, bool) {
	obj := wiregen.MustMakeObj("gglppphenoi", map[int]*proto.Value{
		0: proto.Null(),
		1: proto.Null(),
		2: proto.Null(),
		3: proto.Null(),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func hWorldNull(ctx *Ctx) ([]byte, bool) {
	return []byte{0x00}, true
}

// hItemMove 4/6：guids + [源容器, 目标容器] → 移动 → 回快照。
func hItemMove(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	ints := treeIntsDeep(ctx.Tree)
	if len(guids) > 0 {
		dst := "backpack"
		if len(ints) > 1 {
			dst = containerName(ints[1])
		}
		for _, g := range guids {
			if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), g); err == nil && row.UID == ctx.Sess.Userid {
				row.Container = dst
				_, _ = srv.DB.SQL.ExecContext(ctx.Sess.pushCtx(),
					`UPDATE item SET container=? WHERE guid=?`, dst, g)
			}
		}
	}
	return srv.rigSnapshot800(ctx), true
}

// hItemRemove 4/7：删除物品 → 回快照。
func hItemRemove(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	owned := make([]string, 0, len(guids))
	for _, g := range guids {
		row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), g)
		if err == nil && row != nil && row.UID == ctx.Sess.Userid {
			owned = append(owned, g)
		}
	}
	if len(owned) > 0 {
		if err := srv.DB.DeleteItems(ctx.Sess.pushCtx(), owned); err == nil {
			// 4/21 = “这些物品已移除”推送；商店/邮件删物品也用它。
			// 不推的话库里删干净了，客户端背包还留着，再点丢弃就是删不存在的 GUID。
			ctx.Sess.Push4_21(owned)
		}
	}
	return srv.rigSnapshot800(ctx), true
}

// hItemUse 处理 4/8，返回被寻址物品的单件 800 快照。
func hItemUse(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	out := proto.Args(proto.S("61"))
	if len(guids) > 0 {
		if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), guids[0]); err == nil {
			if tree, terr := srv.ItemToTree(ctx.Sess.pushCtx(), row); terr == nil {
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

// containerName 将容器枚举转换为存储 key。
// 已知的 0 到 11 容器按各自类型处理；未知枚举回退到背包。
func containerName(v uint64) string {
	return containerEnumName(v)
}

// treeIntsDeep 深扫整数。
func treeIntsDeep(v *proto.Value) []uint64 {
	var out []uint64
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindU32, proto.KindU64:
			out = append(out, x.Num)
		case proto.KindArgs, proto.KindValList, proto.KindArray, proto.KindGuidArr, proto.KindU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				if x.Fields[i].Num != nil {
					out = append(out, u64Of(x.Fields[i].Num))
				}
			}
		}
	}
	walk(v)
	return out
}

// hPlaceBuyoutTx 处理 3/37 地点买断：
// 已拥有检查、余额条件扣减、授权和流水在同一事务内完成。
func hPlaceBuyoutTx(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	cctx := ctx.Sess.pushCtx()
	place, packet, price := parsePlaceBuyoutArgs(ctx.Tree)
	tier := placeBuyoutPrice(place, packet)

	tx, err := beginEconomyTx(cctx, srv.DB)
	if err != nil {
		ctx.Log.Warn("3/37 开启事务失败", "err", err)
		return nil, false
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(cctx, `SELECT COUNT(*) FROM place_access WHERE uid=? AND place=? AND kind='buyout'`,
		uid, place).Scan(&owned); err != nil {
		ctx.Log.Warn("3/37 查询已拥有地点失败", "place", place, "err", err)
		return nil, false
	}
	validPrice := tier > 0 && price == tier
	if owned == 0 && validPrice {
		walletErr := applyWalletTx(cctx, tx, uid, 0, -tier, "buy", place, "", "place_buyout")
		if walletErr != nil {
			if !errors.Is(walletErr, errInsufficientFunds) {
				ctx.Log.Warn("3/37 金币扣款失败", "place", place, "tier", packet, "err", walletErr)
				return nil, false
			}
			ctx.Log.Info("3/37 金币余额不足，不扣款", "place", place, "tier", packet)
		} else {
			if _, err := tx.ExecContext(cctx, `
				INSERT INTO place_access(uid, place, kind, ts) VALUES(?,?, 'buyout', ?)
				ON CONFLICT(uid, place, kind) DO NOTHING`, uid, place, float64(time.Now().Unix())); err != nil {
				ctx.Log.Warn("3/37 授予地点失败", "place", place, "err", err)
				return nil, false
			}
			ctx.Log.Info("3/37 金币买断地图", "place", place, "tier", packet,
				"gold", -tier, "gold_yuan", float64(tier)/100.0)
		}
	} else if owned == 0 {
		tiers, _ := placeBuyoutTiers[place]
		ctx.Log.Info("3/37 价格与静态表不符或未授权，不扣款", "place", place,
			"packet", packet, "price", price, "tiers", tiers)
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("3/37 事务提交失败", "err", err)
		return nil, false
	}
	p, err := srv.BuildProfile307(cctx, uid)
	if err != nil {
		ctx.Log.Warn("3/37 档案构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(p, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hU64Zero 返回 u64 零值，供需要 long 语义的命令使用。
func hU64Zero(*Ctx) ([]byte, bool) { return append([]byte{0x11}, proto.PackU64(0)...), true }
