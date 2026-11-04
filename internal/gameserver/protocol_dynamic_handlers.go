package gameserver

import (
	"context"
	"math"
	"strings"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// registerDynamicProtocolHandlers 注册动态协议命令。
func registerDynamicProtocolHandlers(d *Dispatch) {
	d.Register(13, 5, hDynamicHanditemThrow)
	d.Register(13, 6, hDynamicRigSnapshot)
	d.Register(17, 9, hDynamicRodBreak)
	d.Register(17, 10, hDynamicRigSnapshot)
	d.Register(17, 11, hDynamicRigSnapshot)
	d.Register(3, 40, hDynamicRewardEntry)
	d.Register(3, 41, hDynamicEquippedItems)
	d.Register(1, 10, hDynamicSonar)

	objRule := func(typeID uint32, class string) ResponseContractRule {
		return ResponseContractRule{Kind: proto.KindObj, TypeIDs: []uint32{typeID}, Classes: []string{class}}
	}
	d.RegisterResponseContract(13, 5, objRule(805, "lmbomkliaad"))
	d.RegisterResponseContract(13, 6, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(17, 9, objRule(11028, "ikmdmfoojfg"))
	d.RegisterResponseContract(17, 10, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(17, 11, objRule(800, "dlbahppbdad"))
	d.RegisterResponseContract(3, 40, objRule(2015, "enikeammfen"))
	d.RegisterResponseContract(3, 41, ResponseContractRule{
		Kind: proto.KindArgs, ArgCode: "61", ArgItemsAreItems: true,
	})
	d.RegisterResponseContract(1, 10, objRule(15003, "efnipphkbce"))
}

// hDynamicHanditemThrow 13/5 handitem/throw。
// 请求 [手持物 Guid, olieodeoaof 投掷目标]，响应 805 lmbomkliaad。
// 距离从目标 f0 的 f32 位模式读取；实际抛出时消耗 1 份已装填物。
func hDynamicHanditemThrow(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	dist := throwDistance(ctx.Tree)
	if math.IsNaN(dist) || math.IsInf(dist, 0) {
		dist = 20
	}
	if dist <= 0 {
		dist = 0
	} else {
		dist = math.Max(1, math.Min(150, dist))
	}
	handGUID := ""
	for _, g := range deepGUIDs(ctx.Tree) {
		handGUID = normalizeDynamicGUID(g)
		if handGUID != "" && handGUID != zeroGUID {
			break
		}
	}
	var upserts []*proto.Value
	var removed []string
	if dist > 0 && handGUID != "" && handGUID != zeroGUID {
		got := srv.consumeHanditemLoadedForThrow(ctx.Sess.pushCtx(), handGUID)
		upserts = got.upserts
		removed = got.removed
	}
	obj, err := wiregen.MakeObj("lmbomkliaad", map[int]*proto.Value{
		0: proto.F32(float32(dist)),
		1: proto.F32(5.0),
		2: proto.Null(),
		3: proto.Args(proto.S("61"), upserts...),
		4: proto.Args(proto.S("61")),
		5: proto.GuidArr(removed),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicRigSnapshot 为 13/6、17/10、17/11 返回当前容器状态的 800 全量快照。
func hDynamicRigSnapshot(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	body := srv.rigFullSnapshotBody(ctx)
	if len(body) == 0 || body[0] != 0x01 {
		return nil, false
	}
	return body, true
}

// hDynamicRodBreak 17/9 rod/break：按断件 GUID 持久化断线/断竿并回 11028。
// 请求参数顺序为 [竿 GUID, 断件 GUID, 物理模型]；没有断件时回退到当前竿。
func hDynamicRodBreak(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	rodGUID := normalizeDynamicGUID(ctx.Tree.ArgItem(0).GuidHex())
	brokenGUID := normalizeDynamicGUID(ctx.Tree.ArgItem(1).GuidHex())
	if brokenGUID == zeroGUID {
		brokenGUID = ""
	}
	st := srv.stateFor(ctx.Sess.Userid)
	if rodGUID == "" || rodGUID == zeroGUID {
		st.mu.Lock()
		rodGUID = normalizeDynamicGUID(st.GearGuid)
		st.mu.Unlock()
	}
	if rodGUID == "" {
		rodGUID = zeroGUID
	}

	if srv.DB != nil && rodGUID != zeroGUID && ctx.DecodeOK {
		outcome, err := srv.breakLineOrRod(ctx.Sess.pushCtx(), ctx.Sess.Userid, rodGUID, brokenGUID)
		if err == nil {
			st.mu.Lock()
			if st.biteTimer != nil {
				st.biteTimer.Stop()
			}
			st.Pending = nil
			st.CastActive = false
			st.Hooked = false
			st.Resolved = true
			if !outcome.LineBroken && !outcome.ReelDamaged && !outcome.ComponentBroken {
				st.GearGuid = ""
			}
			st.mu.Unlock()
			if outcome.ReelDamaged {
				// 轮子只是降一个耐久阶段，仍留在竿上；本次鱼按超载跑鱼处理。
				srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
			} else if outcome.LineBroken {
				srv.bumpFishingCounter(ctx.Sess.Userid, "line_breaks")
			} else if outcome.ComponentBroken {
				// 钩/拟饵等终端件断裂：竿仍在手上，钓组摘除损坏件。
				srv.bumpFishingCounter(ctx.Sess.Userid, "component_breaks")
			} else {
				srv.bumpFishingCounter(ctx.Sess.Userid, "rod_breaks")
			}
			obj, berr := buildGearBreakResult(outcome)
			if berr != nil {
				return nil, false
			}
			body, berr := proto.EncodeValue(obj, ctx.Sess.Table())
			if berr != nil {
				return nil, false
			}
			ctx.Log.Info("17/9 渔具超载", "rod", shortGUID(rodGUID), "part", outcome.BreakPart,
				"broken", shortGUID(outcome.BrokenGUID), "line", outcome.LineBroken,
				"comp", outcome.ComponentBroken,
				"line_loss_m", outcome.LineLossM, "removed", len(outcome.RemovedGUIDs))
			return body, true
		}
		ctx.Log.Warn("17/9 断件处理失败", "rod", shortGUID(rodGUID), "err", err)
	}
	if srv.DB != nil {
		ctx.ReplyFailure("System.InvalidOperationException", "断件处理失败，请重新取竿后重试")
		return nil, true
	}

	// 无存储时保留原有兼容响应。
	fallbackGUID := brokenGUID
	if fallbackGUID == "" {
		fallbackGUID = rodGUID
	}
	obj, err := wiregen.MakeObj("ikmdmfoojfg", map[int]*proto.Value{
		0: proto.U32(0),
		1: proto.Guid(fallbackGUID),
		2: proto.Args(proto.S("61")),
		3: proto.Null(),
		4: proto.Null(),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicRewardEntry 处理 3/40，返回 2015 奖励条目并回显请求中的 guid。
// 没有奖励持久化表时保留空物品 ID，不生成不存在的 systemID。
func hDynamicRewardEntry(ctx *Ctx) ([]byte, bool) {
	guid := zeroGUID
	for _, g := range deepGUIDs(ctx.Tree) {
		if n := normalizeDynamicGUID(g); n != "" {
			guid = n
			break
		}
	}
	obj, err := wiregen.MakeObj("enikeammfen", map[int]*proto.Value{
		0:  proto.U32(0),
		1:  proto.U32(1),
		2:  proto.Guid(guid),
		3:  proto.Guid(""),
		4:  proto.U32(10),
		5:  proto.U32(0),
		6:  proto.U32(0),
		7:  proto.U32(0),
		8:  proto.U64(0),
		9:  proto.U64(0),
		10: proto.U32(0),
		11: proto.Null(),
		12: proto.Null(),
		13: proto.U32(0),
		14: proto.U32(0),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicEquippedItems 3/41：args("61")[container=rig 的 DB 物品树]。
func hDynamicEquippedItems(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	rows, err := srv.DB.ListItemsByContainer(cctx, ctx.Sess.Userid, "rig")
	if err != nil {
		return nil, false
	}
	out := proto.Args(proto.S("61"))
	for _, row := range rows {
		tree, terr := srv.ItemToTree(cctx, row)
		if terr != nil {
			continue
		}
		out.Items = append(out.Items, tree)
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hDynamicSonar 处理 1/10，返回 15003 声呐响应并以请求水域名作为 f0。
// 已配置水域生成 15004/15005 回声；未知或空水域返回空网格。
func hDynamicSonar(ctx *Ctx) ([]byte, bool) {
	key := argFirstStr(ctx.Tree)
	obj, err := ctx.Sess.Srv.sonarResponse(ctx.Sess.pushCtx(), key)
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

type handitemThrowConsume struct {
	upserts []*proto.Value
	removed []string
}

// consumeHanditemLoadedForThrow 消耗一份手持工具中的已装填物：
// 只扣 1 份，未用尽更新子实例；用尽删除子实例并清手持物装填引用，不退回来源堆。
func (s *Server) consumeHanditemLoadedForThrow(ctx context.Context, parentGUID string) handitemThrowConsume {
	var out handitemThrowConsume
	loaded := s.handitemLoadedGuid(ctx, parentGUID)
	if loaded == "" || loaded == zeroGUID {
		return out
	}
	row, err := s.DB.GetItemByGUID(ctx, loaded)
	if err != nil || row == nil {
		s.setHanditemLoadedRef(ctx, parentGUID, "")
		if parent, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && parent != nil {
			if tree, terr := s.itemTree(ctx, parent); terr == nil {
				out.upserts = append(out.upserts, tree)
			}
		}
		return out
	}
	if s.consumeItemQty(ctx, row, 1) < 1 {
		return out
	}
	left, err := s.DB.GetItemByGUID(ctx, loaded)
	if err == nil && left != nil {
		if tree, terr := s.itemTree(ctx, left); terr == nil {
			out.upserts = append(out.upserts, tree)
		}
		return out
	}
	// 用完：清引用，更新后的手持物进 f3，被消耗的子实例 guid 进 f5。
	s.setHanditemLoadedRef(ctx, parentGUID, "")
	if parent, perr := s.DB.GetItemByGUID(ctx, parentGUID); perr == nil && parent != nil {
		if tree, terr := s.itemTree(ctx, parent); terr == nil {
			out.upserts = append(out.upserts, tree)
		}
	}
	out.removed = append(out.removed, loaded)
	return out
}

// throwDistance 从 olieodeoaof.f0 读取投掷距离。
// 字段缺失时使用 20m；值不大于 0 时返回 0，客户端会跳过落点创建。
func throwDistance(root *proto.Value) float64 {
	var target *proto.Value
	var walk func(*proto.Value)
	walk = func(v *proto.Value) {
		if v == nil || target != nil {
			return
		}
		if v.Kind == proto.KindObj {
			if v.Class == "olieodeoaof" {
				target = v
				return
			}
			for i := range v.Fields {
				walk(v.Fields[i].Val)
			}
			return
		}
		for _, it := range v.Items {
			walk(it)
		}
	}
	walk(root)
	if target == nil || target.FieldCount() == 0 || target.Field(0).Num == nil {
		return 20
	}
	bits := uint32(u64Of(target.Field(0).Num))
	return float64(math.Float32frombits(bits))
}

func normalizeDynamicGUID(g string) string {
	g = strings.ToLower(strings.ReplaceAll(g, "-", ""))
	if len(g) == 32 && g != zeroGUID {
		return g
	}
	if g == zeroGUID {
		return zeroGUID
	}
	return ""
}
