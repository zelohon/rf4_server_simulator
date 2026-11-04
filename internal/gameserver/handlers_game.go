package gameserver

import (
	"context"
	"fmt"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 登录时间线、容器、技能、快捷栏、地图和钓鱼处理器。

// requireDB 有 DB 才处理，否则交给兜底链。
func (s *Server) requireDB(ctx *Ctx) bool { return s.DB != nil }

// ---- 3/1 profile ----

func hProfile(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	p, err := ctx.Sess.Srv.BuildProfile307(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("3/1 构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(p, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// 容器枚举（Go 只建模 0 背包 / 5 鱼护）。
const (
	containerBackpack = 0
	containerKeepnet  = 5
)

// ---- 4/1 items/count ----

func hItemCount(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	container, _ := ctx.ArgInt(0)
	var used, cap, capMember int64
	cctx := ctx.Sess.pushCtx()
	switch container {
	case containerBackpack:
		used, _ = srv.DB.CountItemsByContainers(cctx, ctx.Sess.Userid, []string{"backpack", "rig"})
	case containerKeepnet:
		n, err := srv.DB.KeepnetList(cctx, ctx.Sess.Userid)
		if err == nil {
			used = int64(len(n))
		}
		cap, capMember = 100, 150
	}
	obj := wiregen.MustMakeObj("pbndmldbfai", map[int]*proto.Value{
		0: proto.U32(uint32(used)),
		1: proto.U32(uint32(cap)),
		2: proto.U32(uint32(capMember)),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 4/4 items/guids ----

func hItemGUIDs(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	container, _ := ctx.ArgInt(0)
	cctx := ctx.Sess.pushCtx()
	var guids []string
	switch container {
	case containerBackpack:
		guids, _ = srv.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"backpack", "rig"})
	case containerKeepnet:
		list, err := srv.DB.KeepnetList(cctx, ctx.Sess.Userid)
		if err == nil {
			for _, e := range list {
				guids = append(guids, e.EchoGUID)
			}
		}
	}
	body, err := proto.EncodeValue(proto.GuidArr(guids), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 4/5 items/details：args("61")[物品树...] ----

func hItemDetails(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	container, _ := ctx.ArgInt(0)
	guids := deepGUIDs(ctx.Tree)
	cctx := ctx.Sess.pushCtx()
	out := proto.Args(proto.S("61"))
	if container == containerKeepnet {
		for _, g := range guids {
			e, err := srv.DB.KeepnetGet(cctx, ctx.Sess.Userid, g)
			if err != nil || e == nil {
				continue
			}
			out.Items = append(out.Items, buildKeepnet25(e, srv.currentLevel(ctx.Sess.Userid)))
		}
	} else if container != containerBackpack {
		// 非背包容器必须返回空。把背包物品下发到其他容器会让同一 GUID
		// 被客户端登记两次，导致容器索引冲突。鱼护内容单独处理。
		ctx.Log.Info("4/5 容器不在建模范围 -> 回空", "container", container)
	} else {
		for _, g := range guids {
			row, err := srv.DB.GetItemByGUID(cctx, g)
			if err != nil || row == nil || row.UID != ctx.Sess.Userid {
				continue
			}
			tree, err := srv.ItemToTree(cctx, row)
			if err != nil {
				continue
			}
			out.Items = append(out.Items, tree)
		}
	}
	body, err := proto.EncodeValue(out, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// deepGUIDs 深扫 args 里全部 guid 值。
func deepGUIDs(v *proto.Value) []string {
	var out []string
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindGuid:
			out = append(out, x.Hex)
		case proto.KindArgs, proto.KindValList, proto.KindArray, proto.KindU32Arr, proto.KindGuidArr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				if x.Fields[i].Op == "GUID16" && x.Fields[i].Hex != "" {
					out = append(out, x.Fields[i].Hex)
				}
				if x.Fields[i].Val != nil {
					walk(x.Fields[i].Val)
				}
			}
		}
	}
	walk(v)
	return out
}

// ---- 5/1 skills/get ----

func hSkillsGet(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	tree, err := srv.BuildSkillTree311(ctx.Sess.pushCtx(), ctx.Sess.Userid, ^uint64(0))
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(tree, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 11/1 快捷栏快照、11/2 绑定、11/3 解绑 ----

func hShortcutSnapshot(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	body, err := proto.EncodeValue(srv.BuildShortcutList(ctx.Sess.pushCtx(), ctx.Sess.Userid), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func hShortcutBind(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	slot, validSlot := ctx.ArgInt(0)
	guids := deepGUIDs(ctx.Tree)
	if !validSlot || slot > 2147483647 || len(guids) != 1 {
		ctx.ReplyFailure("System.ArgumentException", "快捷栏绑定参数无效")
		return nil, true
	}
	g := guids[0]
	if err := srv.DB.BindShortcut(ctx.Sess.pushCtx(), ctx.Sess.Userid, int64(slot), g); err != nil {
		ctx.ReplyFailure("System.ArgumentException", "快捷栏物品不可用")
		return nil, true
	}
	body, err := proto.EncodeValue(build136(int64(slot), g), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func hShortcutUnbind(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	slot, validSlot := ctx.ArgInt(0)
	if !validSlot || slot > 2147483647 {
		ctx.ReplyFailure("System.ArgumentException", "快捷栏解绑参数无效")
		return nil, true
	}
	if err := srv.DB.BindShortcut(ctx.Sess.pushCtx(), ctx.Sess.Userid, int64(slot), ""); err != nil {
		ctx.ReplyFailure("System.InvalidOperationException", "快捷栏解绑未保存")
		return nil, true
	}
	body, err := proto.EncodeValue(build136(int64(slot), ""), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 3/30 / 3/31 钓组物品同步 ----

func build800(f0, f1, f2 *proto.Value) *proto.Value {
	return wiregen.MustMakeObj("dlbahppbdad", map[int]*proto.Value{
		0: f0,
		1: f1,
		2: f2,
	})
}

func hRigSyncBackpack(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	bp := proto.Args(proto.S("61"))
	rg := proto.Args(proto.S("61"))
	guids, _ := srv.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"backpack"})
	for _, g := range guids {
		if row, err := srv.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
				bp.Items = append(bp.Items, tree)
			}
		}
	}
	rguids, _ := srv.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"rig"})
	for _, g := range rguids {
		if row, err := srv.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
				rg.Items = append(rg.Items, tree)
			}
		}
	}
	body, err := proto.EncodeValue(build800(bp, rg, proto.Null()), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func hRigSyncRig(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	rg := proto.Args(proto.S("61"))
	rguids, _ := srv.DB.ItemGUIDsByContainers(cctx, ctx.Sess.Userid, []string{"rig"})
	for _, g := range rguids {
		if row, err := srv.DB.GetItemByGUID(cctx, g); err == nil {
			if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
				rg.Items = append(rg.Items, tree)
			}
		}
	}
	ga := proto.GuidArr(rguids)
	body, err := proto.EncodeValue(build800(proto.Null(), rg, ga), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---- 3/37 金币买断地图 ----

// hPlaceBuyout 处理 3/37 地图买断。
// 请求参数为 [place, packetId, price]，响应为 307 玩家档案。
//
// 价格必须等于静态买断价才扣金币，防止客户端改价。成功后写入 place_access
// 和交易流水；档案 307 的字段 22 用于向客户端表示已买断。
func hPlaceBuyout(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	place, packet, price := parsePlaceBuyoutArgs(ctx.Tree)
	cctx := ctx.Sess.pushCtx()
	uid := ctx.Sess.Userid
	if tier := placeBuyoutPrice(place, packet); tier > 0 && price == tier {
		// 金币买断（百分位）：扣款 → 授予 → 流水
		if p, err := srv.DB.EnsureProfile(cctx, uid); err == nil && p != nil {
			_ = srv.DB.UpdateBalance(cctx, uid, p.Silver, p.Gold-tier, p.Bank)
		}
		if err := srv.DB.PlaceAccessInsert(cctx, uid, place, "buyout"); err != nil {
			ctx.Log.Warn("3/37 授予地点失败", "place", place, "err", err)
		}
		_ = srv.DB.AddTxn(cctx, uid, "buy", place, "", 0, -tier, "place_buyout")
		ctx.Log.Info("3/37 金币买断地图", "place", place, "tier", packet,
			"gold", -tier, "gold_yuan", float64(tier)/100.0)
	} else {
		tiers, _ := placeBuyoutTiers[place]
		ctx.Log.Info("3/37 价格与静态表不符，不扣款", "place", place,
			"packet", packet, "price", price, "tiers", tiers)
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

// parsePlaceBuyoutArgs 顶层 args 位置取参：[0]=place 字符串、[1]=packetId、
// [2]=price（long）。缺参按零值处理（调用方走"价格不符"分支）。
func parsePlaceBuyoutArgs(tree *proto.Value) (place string, packet, price int64) {
	if tree == nil || tree.Kind != proto.KindArgs {
		return "", 0, 0
	}
	ints := 0
	for _, it := range tree.Items {
		if it == nil {
			continue
		}
		switch it.Kind {
		case proto.KindStr:
			if it.Str != nil && *it.Str != "" && place == "" {
				place = *it.Str
			}
		case proto.KindU32, proto.KindU64:
			v := int64(it.Num)
			switch ints {
			case 0:
				packet = v
			case 1:
				price = v
			}
			ints++
		}
	}
	return place, packet, price
}

// ---- 1/12 place / 1/14 travel（切图）----

// hPlace 处理 1/12 地图读取，返回 205 dob。
// 请求为 [地图 key]；未知地图回退出生地图，避免客户端读取失败。
func hPlace(ctx *Ctx) ([]byte, bool) {
	key := spawnLevelKey
	if ctx.Tree != nil {
		if v := firstNonEmptyStr(ctx.Tree); v != "" {
			key = v
		}
	}
	key = travelLevelKey(key)
	body, err := ctx.Sess.Srv.buildMapDobBody(key, time.Now().Unix())
	if err != nil {
		ctx.Log.Warn("1/12 dob 构造失败", "level", key, "err", err)
		return nil, false
	}
	return body, true
}

// hTravel 处理 1/14 切图，返回 [dob(205), 护网快照(800), 玩家档案(307), null]。
// 地图解锁由客户端的档案字段决定；本处理器负责切换当前地图和返回场景数据。
func hTravel(ctx *Ctx) ([]byte, bool) {
	if !ctx.Sess.Srv.requireDB(ctx) {
		return nil, false
	}
	key := spawnLevelKey
	if ctx.Tree != nil {
		if v := firstNonEmptyStr(ctx.Tree); v != "" {
			key = v
		}
	}
	key = travelLevelKey(key)
	sess := ctx.Sess
	body, err := sess.Srv.buildTravelBody(sess, key)
	if err != nil {
		ctx.Log.Warn("1/14 travel 构造失败", "level", key, "err", err)
		return nil, false
	}
	// 响应体成功构造后才提交位置，避免构造失败时把玩家记录已切图。
	scene := sceneForLevel(key)
	var spot int64
	if ints := treeIntsDeep(ctx.Tree); len(ints) > 0 {
		spot = int64(ints[0])
	}
	if err := sess.Srv.DB.SetPlayerLocation(sess.pushCtx(), sess.Userid, key, scene, spot); err != nil {
		ctx.Log.Warn("1/14 位置落库失败", "level", key, "err", err)
		return nil, false
	}
	sess.Srv.setLevel(sess.Userid, key)
	ctx.Log.Info("1/14 travel", "level", key, "scene", scene, "spot", spot)
	return body, true
}

// ---- 1/13 enter map（登录定位：优先恢复上次所在地图）----

func hEnterMap(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	sess := ctx.Sess
	uid := sess.Userid
	requested := ""
	if ctx.Tree != nil {
		requested = firstNonEmptyStr(ctx.Tree)
	}

	level, scene, spot := spawnLevelKey, sceneForLevel(spawnLevelKey), int64(0)
	saved, err := srv.DB.GetPlayerLocation(sess.pushCtx(), uid)
	if err != nil {
		ctx.Log.Warn("1/13 读取上次位置失败，回出生地图", "err", err)
	} else if saved != nil && validLevelKey(saved.LevelKey) {
		level, scene, spot = saved.LevelKey, saved.Scene, saved.Spot
	}

	// 1/13 若带地图 key，则以本次进入作为一次成功切图；空请求是登录恢复。
	if requested != "" {
		level = travelLevelKey(requested)
		scene = sceneForLevel(level)
	}
	level = travelLevelKey(level)
	if scene == "" {
		scene = sceneForLevel(level)
	}

	body, err := srv.buildSpawnLocatorBody(level, scene, time.Now().Unix())
	if err != nil {
		ctx.Log.Warn("1/13 出生点定位构造失败", "level", level, "err", err)
		return nil, false
	}
	if err := srv.DB.SetPlayerLocation(sess.pushCtx(), uid, level, scene, spot); err != nil {
		ctx.Log.Warn("1/13 位置落库失败", "level", level, "err", err)
		return nil, false
	}
	srv.setLevel(uid, level)
	ctx.Log.Info("1/13 下发出生地图", "level", level, "scene", scene, "spot", spot, "bytes", len(body))
	return body, true
}

func firstNonEmptyStr(v *proto.Value) string {
	if v == nil {
		return ""
	}
	switch v.Kind {
	case proto.KindArgs, proto.KindValList:
		for _, it := range v.Items {
			if it != nil && it.Kind == proto.KindStr && it.Str != nil && *it.Str != "" {
				return *it.Str
			}
			if s := firstNonEmptyStr(it); s != "" {
				return s
			}
		}
	case proto.KindObj:
		for i := range v.Fields {
			if v.Fields[i].Str != nil && *v.Fields[i].Str != "" {
				return *v.Fields[i].Str
			}
		}
	}
	return ""
}

// hSetQuivertip 处理 17/5 竿稍设置。
// 请求为 [容器, 竿 GUID, 型号键]。竿实例的 f8 是抛竿校验读取的权威值，
// 因此同时更新 rod_setting 和实例字段；响应 800.f1 返回更新后的竿树。
func hSetQuivertip(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	gs := deepGUIDs(ctx.Tree)
	strs := treeStrsDeep(ctx.Tree)
	rodGUID, tip := "", ""
	if len(gs) > 0 {
		rodGUID = gs[0]
	}
	if len(strs) > 0 {
		tip = strs[0]
	}
	if rodGUID == "" || tip == "" {
		return nil, false // 交给兜底
	}
	cctx := ctx.Sess.pushCtx()
	_ = srv.DB.UpsertRodSetting(cctx, rodGUID, "quivertip", tip)
	if row, err := srv.DB.GetItemByGUID(cctx, rodGUID); err == nil && row != nil {
		if fields, ferr := FieldsFromJSON(row.FieldsJSON); ferr == nil {
			tidx := fieldIdxByCS(row.WireClass, "kjphlnnlflh")
			if tidx >= 0 {
				for len(fields) <= tidx {
					zf, zerr := proto.ZeroField(fieldOpAt(row.WireClass, len(fields)))
					if zerr != nil {
						break
					}
					fields = append(fields, zf)
				}
				fields[tidx].Str = &tip
				// 落库时将 U32N 的字符串值写成 STR 标签；
				// fields_json 读取侧会丢弃 u32n 标签下的字符串。
				fields[tidx].Op = "STR"
				if fj, jerr := FieldsToJSON(fields); jerr == nil {
					_ = srv.DB.UpdateItemFields(cctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID)
				}
			}
		}
		// 响应 800：Field 1（更新段）= 带新尖梢的竿实例
		if tree, terr := srv.ItemToTree(cctx, row); terr == nil {
			body, encErr := proto.EncodeValue(build800(proto.Null(), proto.Args(proto.S("61"), tree), proto.Null()), ctx.Sess.Table())
			if encErr == nil {
				return body, true
			}
		}
	}
	return nil, false
}

// treeStrsDeep 深扫字符串。
func treeStrsDeep(v *proto.Value) []string {
	var out []string
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindStr:
			if x.Str != nil {
				out = append(out, *x.Str)
			}
		case proto.KindArgs, proto.KindValList, proto.KindArray, proto.KindGuidArr, proto.KindU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				if x.Fields[i].Str != nil {
					out = append(out, *x.Fields[i].Str)
				}
			}
		}
	}
	walk(v)
	return out
}

// ---- 14/x 钓鱼 ----

func hCastPrepare(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	gear := ""
	if len(guids) > 0 {
		gear = guids[0]
	}
	st := srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	st.GearGuid = gear
	depthCM := st.DepthCM
	rigType := st.RigType
	st.mu.Unlock()
	// 收敛钓组数据：补齐 f5、rig 行、组件和尖梢。
	// 尖梢有更新时推送 4/22 全量竿树，因为客户端抛竿前会校验实例 f8。
	if srv.ensureRig(ctx.Sess.pushCtx(), ctx.Sess.Userid, gear) {
		ctx.Sess.Push4_22Full()
	}
	if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), gear); err == nil && row != nil {
		rigType = srv.rigTypeOfItem(ctx.Sess.pushCtx(), row)
		st.mu.Lock()
		st.RigType = rigType
		st.mu.Unlock()
	}

	body := castReady158(ctx, gear, depthCM, rigType)
	if body == nil {
		return nil, false
	}
	return body, true
}

func hCast(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	// loc/spot：顶层 obj olieodeoaof 第一个非空 str = loc，第二个 u32 = spot
	guids := deepGUIDs(ctx.Tree)
	gear := ""
	if len(guids) > 0 {
		gear = guids[len(guids)-1]
	}
	st := srv.stateFor(ctx.Sess.Userid)
	if ctx.Tree != nil {
		if loc := findObjStr(ctx.Tree, "olieodeoaof"); loc != "" {
			st.Loc = loc
		}
		if spot := findObjNthU32(ctx.Tree, "olieodeoaof", 1); spot != 0 {
			st.Spot = spot
		}
	}
	st.mu.Lock()
	st.GearGuid = gear
	st.mu.Unlock()
	srv.ensureRig(ctx.Sess.pushCtx(), ctx.Sess.Userid, gear)
	// 记录钓组类型并读取当前钩上饵；路亚使用独立的咬钩节奏。
	rigType := int64(0)
	if row, err := srv.DB.GetItemByGUID(ctx.Sess.pushCtx(), gear); err == nil && row != nil {
		rt := srv.rigTypeOfItem(ctx.Sess.pushCtx(), row)
		rigType = rt
		st.mu.Lock()
		st.RigType = rt
		st.mu.Unlock()
	}
	// 14/1 给出的是满力度上限；14/2 才携带本次实际落点、动作和出手力度。
	// 这里只做真实请求诊断，不能把本杆力度写回下一杆的上限。
	requestedM := throwDistance(ctx.Tree)
	action := uint32(findObjNthByte(ctx.Tree, "olieodeoaof", 0))
	profile := srv.castDistanceForRig(ctx.Sess.pushCtx(), ctx.Sess.Userid, gear, rigType)
	actionMaxM := profile.DistanceM * castActionFactor(action)
	powerRatio := 0.0
	if actionMaxM > 0 {
		powerRatio = requestedM / actionMaxM
	}
	ctx.Log.Info("实际抛投请求", "rod", shortGUID(gear), "requested_m", math.Round(requestedM*100)/100,
		"max_m", math.Round(profile.DistanceM*100)/100, "action", action,
		"action_factor", math.Round(castActionFactor(action)*1000)/1000,
		"power_ratio", math.Round(powerRatio*1000)/1000)
	// 主线版本会无条件记录当前饵，供路亚咬钩规则和持久化状态使用。
	bait := srv.currentBait(ctx.Sess.pushCtx(), gear)
	st.mu.Lock()
	st.Bait = bait
	st.mu.Unlock()
	// 水体距离闸门：玩家离水体太远（抛投够不到）时不派鱼。
	// 只有该地点有水体矩形数据时才判定；没有数据一律放行，避免误伤。
	st.mu.Lock()
	hasPos, px, pz, locNow := st.HasPos, st.PosX, st.PosZ, st.Loc
	st.mu.Unlock()
	if hasPos {
		const waterReachM = 15.0
		nearWater, hasData := waterWithin(locNow, px, pz, waterReachM)
		ctx.Log.Info("抛竿水体诊断", "loc", locNow, "x", px, "z", pz, "hasData", hasData, "near", nearWater)
		if hasData && !nearWater {
			ctx.Log.Info("距水体过远，本杆不派鱼", "loc", locNow, "x", px, "z", pz, "reach", waterReachM)
		} else {
			srv.scheduleBite(ctx.Sess, st)
		}
	} else {
		srv.scheduleBite(ctx.Sess, st)
	}

	// 响应 800.f0 返回当前钓组 157 对象，包含完整竿树和重建后的 f5。
	cctx := ctx.Sess.pushCtx()
	out := proto.Args(proto.S("61"))
	if tree := srv.buildRigObject(cctx, ctx.Sess.Userid, gear); tree != nil {
		out.Items = append(out.Items, tree)
	}
	body, err := proto.EncodeValue(build800(out, proto.Null(), proto.Null()), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

func findObjStr(root *proto.Value, class string) string {
	if o := findObj(root, class); o != nil {
		for i := range o.Fields {
			if o.Fields[i].Str != nil && *o.Fields[i].Str != "" {
				return *o.Fields[i].Str
			}
		}
	}
	return ""
}

func findObjU32(root *proto.Value, class string) uint64 {
	return findObjNthU32(root, class, 0)
}

// findObjNthByte 取对象第 n 个 BYTE 字段。14/2 的 olieodeoaof.f8 是本次
// 抛投动作枚举，不能和距离、钓点这些 U32 混读。
func findObjNthByte(root *proto.Value, class string, n int) uint64 {
	if o := findObj(root, class); o != nil {
		seen := 0
		for i := range o.Fields {
			if o.Fields[i].Op != "BYTE" || o.Fields[i].Num == nil {
				continue
			}
			if seen == n {
				return u64Of(o.Fields[i].Num)
			}
			seen++
		}
	}
	return 0
}

// findObjNthU32 取对象第 n 个 U32 字段。14/2 中 loc 是首个非空字符串，
// spot 是第二个 U32，不能改用第一个数字字段。
func findObjNthU32(root *proto.Value, class string, n int) uint64 {
	if o := findObj(root, class); o != nil {
		seen := 0
		for i := range o.Fields {
			if o.Fields[i].Op != "U32" || o.Fields[i].Num == nil {
				continue
			}
			if seen == n {
				return u64Of(o.Fields[i].Num)
			}
			seen++
		}
	}
	return 0
}

func findObj(root *proto.Value, class string) *proto.Value {
	if root == nil {
		return nil
	}
	switch root.Kind {
	case proto.KindObj:
		if root.Class == class {
			return root
		}
		for i := range root.Fields {
			if root.Fields[i].Val != nil {
				if o := findObj(root.Fields[i].Val, class); o != nil {
					return o
				}
			}
		}
	case proto.KindArgs, proto.KindValList:
		for _, it := range root.Items {
			if o := findObj(it, class); o != nil {
				return o
			}
		}
	}
	return nil
}

// fireAndForget14 客户端上报族：只记录不回包。
func hFishingStep(ctx *Ctx) ([]byte, bool) {
	// 14/7：obj lobemkloehp f5=anim 位掩码；0x200=MotionlessInWaterColumn（水中静止）
	if ctx.Tree != nil {
		if o := findObj(ctx.Tree, "lobemkloehp"); o != nil && o.FieldCount() > 5 {
			anim := u32Of(o.Field(5).Num)
			settled := anim&rigAnimSettledMask != 0
			var seq uint64
			if o.FieldCount() > 14 && o.Field(14).Num != nil {
				seq = u64Of(o.Field(14).Num)
			}
			strs := treeStrsDeep(o)
			st := ctx.Sess.Srv.stateFor(ctx.Sess.Userid)
			st.mu.Lock()
			st.TickSeen = true
			st.TickSeq = seq
			// 每帧按当前动画位重算，不保留上一帧的就位状态。
			st.RigSettled = settled
			// 从 lobemkloehp 的第一个非空 STR 更新当前水域，避免跨图后沿用上一地点。
			for _, s := range strs {
				if s != "" {
					st.Loc = s
					break
				}
			}
			changed := anim != st.LastAnim
			st.LastAnim = anim
			// 0x200/0x400/0x800/0x1000 是不同水层与姿态的“在水中”位族；岸上不会出现。
			enterWater := false
			needBite := false
			if st.CastActive && !st.InWater && anim&rigAnimInWaterMask != 0 {
				st.InWater = true
				enterWater = true
				if st.NeedBite {
					st.NeedBite = false
					needBite = true
				}
			}
			st.mu.Unlock()
			if needBite {
				ctx.Log.Info("钓组在水中，补排咬钩", "f5", fmt.Sprintf("0x%X", anim))
				ctx.Sess.Srv.scheduleBiteForHooked(ctx.Sess, st)
			} else if enterWater {
				ctx.Log.Info("钓组在水中", "f5", fmt.Sprintf("0x%X", anim))
			}
			// 14/7 每帧都会发送，仅在动画位变化时记录日志。
			if changed {
				ctx.Log.Info("14/7 钓组动画位", "f5", fmt.Sprintf("0x%X", anim))
			}
		}
	}
	return nil, true // handled + nil body = 不回包
}

func hFishingFall(ctx *Ctx) ([]byte, bool) {
	st := ctx.Sess.Srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	st.FallSeen = true
	st.FallTs = time.Now()
	st.mu.Unlock()
	return nil, true
}

func hFishingPull(ctx *Ctx) ([]byte, bool) {
	// 14/9 fishing/pull：纺车杆的"抬杆"（右键）与"小刺"（Ctrl+右键）。
	// 客户端在本地播放杆梢动作，这条命令是 fire-and-forget 通知，不需要回包。
	// 先原样记录请求体，便于确认两种手势的参数差异。
	ctx.Log.Info("14/9 抬杆/小刺", "len", len(ctx.Body), "hex", traceHexHead(ctx.Body, 96))
	return nil, true
}

func hFishingHooked(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	st := srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	st.Hooked = true
	pending := st.Pending
	castActive := st.CastActive
	resolved := st.Resolved
	st.mu.Unlock()

	// 登录恢复/提前起竿：复用完整判鱼链（概率、GM、装备限重、mounted hook），
	// 保留本次 Hooked=true；不能直接 roll 后把竿 guid 当 f23。
	if pending == nil && !resolved && castActive {
		srv.scheduleBiteForHooked(ctx.Sess, st)
	}
	ctx.Sess.Push4_22Hook()
	return nil, true
}

func hFishingLeft(ctx *Ctx) ([]byte, bool) {
	// 14/12 的 f0 是客户端鱼实体状态变化：
	//   f0=3：判鱼推送未被接收，6 秒后重推；
	//   f0=5：鱼弃咬离开，不重推也不作废；
	//   f0=6：新鱼已替换旧鱼，不重推；
	//   其他：本轮没有鱼，作废本次等待。
	srv := ctx.Sess.Srv
	st := srv.stateFor(ctx.Sess.Userid)
	f0 := uint32(0)
	if ctx.Tree != nil {
		if o := findObj(ctx.Tree, "lhicjpmglbo"); o != nil && o.FieldCount() > 0 {
			f0 = u32Of(o.Field(0).Num)
		}
	}
	st.mu.Lock()
	ctx.Log.Info("14/12 鱼状态", "f0", f0)
	if f0 == 3 {
		st.PushSent = false
		pending := st.Pending
		castActive := st.CastActive
		st.mu.Unlock()
		if castActive && pending != nil {
			uid := ctx.Sess.Userid
			time.AfterFunc(6*time.Second, func() { srv.repushPending14_14(uid) })
		}
		return nil, true
	}
	if f0 == 5 {
		// 鱼已脱钩离开（含断线跑鱼）：本轮作废，后续 14/4 不能再结算成渔获。
		// 否则会出现"鱼线已断、鱼却进了护网"的错账。
		ctx.Log.Info("14/12 鱼已脱钩离开", "f0", f0)
		if st.biteTimer != nil {
			st.biteTimer.Stop()
		}
		st.Resolved = true
		st.Pending = nil
		st.CastActive = false
		st.Hooked = false
		st.mu.Unlock()
		srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
		return nil, true
	}
	if f0 == 6 {
		// 新鱼已替换旧鱼：不重推，保持当前等待状态。
		ctx.Log.Info("14/12 鱼状态通知", "f0", f0)
		st.mu.Unlock()
		return nil, true
	}
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.Resolved = true
	st.Pending = nil
	st.CastActive = false
	st.mu.Unlock()
	srv.bumpFishingCounter(ctx.Sess.Userid, "escapes")
	return nil, true
}

func hFishingGen(ctx *Ctx) ([]byte, bool) { return nil, true } // fire-and-forget
func hFishingLoad(ctx *Ctx) ([]byte, bool) {
	st := ctx.Sess.Srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	st.Hooked = true
	st.mu.Unlock()
	ctx.Sess.Push4_22Full()
	return nil, true
}

// hFishingEnd 处理 14/4 收竿结算。
// 结算由 progress_ops.go 的 fishingEndSettle 完成，包括经验、Premium 加成和等级更新。
func hFishingEnd(ctx *Ctx) ([]byte, bool) {
	// 收竿：钓组离水，清掉“在水中/挂起”状态。
	st := ctx.Sess.Srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	st.InWater = false
	st.NeedBite = false
	st.mu.Unlock()
	body, ok := ctx.Sess.Srv.fishingEndSettle(ctx)
	ctx.Log.Info("14/4 收竿", "handled", ok, "bytes", len(body))
	return body, ok
}

// hFishingKeep 14/5 入护。
func hFishingKeep(ctx *Ctx) ([]byte, bool) {
	return ctx.Sess.Srv.fishingKeep(ctx)
}

// hFishingRelease 14/6 放生。
func hFishingRelease(ctx *Ctx) ([]byte, bool) {
	return ctx.Sess.Srv.fishingRelease(ctx)
}

// hFishStats 处理 3/2 钓鱼统计，321 根据 catch_log 实时构造。
func hFishStats(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	tree, err := srv.buildStats321(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("3/2 统计构造失败", "err", err)
		return nil, false
	}
	f0, f1, f4 := tree.Field(0), tree.Field(1), tree.Field(4)
	ctx.Log.Info("3/2 统计快照", "total", f0.U32(), "rare", f1.U32(), "daily_count", f4.U32())
	body, err := proto.EncodeValue(tree, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// registerSessionHandlers 注册登录时间线 + 钓鱼族。
func registerSessionHandlers(d *Dispatch) {
	d.Register(3, 1, hProfile)
	d.Register(3, 2, hFishStats)
	d.Register(4, 1, hItemCount)
	d.Register(4, 4, hItemGUIDs)
	d.Register(4, 5, hItemDetails)
	d.Register(5, 1, hSkillsGet)
	d.Register(11, 1, hShortcutSnapshot)
	d.Register(11, 2, hShortcutBind)
	d.Register(11, 3, hShortcutUnbind)
	d.Register(3, 9, hPositionReport) // 位置同步：记录玩家世界坐标
	d.Register(3, 30, hRigSyncBackpack)
	d.Register(3, 31, hRigSyncRig)
	d.Register(3, 37, hPlaceBuyout)
	d.Register(1, 12, hPlace)
	d.Register(1, 13, hEnterMap)
	d.Register(1, 14, hTravel)
	d.Register(17, 5, hSetQuivertip)
	// 14/x
	d.Register(14, 1, hCastPrepare)
	d.Register(14, 2, hCast)
	d.Register(14, 3, hFishingFall)
	d.Register(14, 4, hFishingEnd)
	d.Register(14, 5, hFishingKeep)
	d.Register(14, 6, hFishingRelease)
	d.Register(14, 7, hFishingStep)
	d.Register(14, 8, hFishingLoad)
	d.Register(14, 9, hFishingPull)
	d.Register(14, 10, hFishingGen)
	d.Register(14, 11, hFishingHooked)
	d.Register(14, 12, hFishingLeft)
}

// ---- 25 护网条目 ----

func buildKeepnet25(e *store.KeepnetEntry, levelKey string) *proto.Value {
	if e.KeepLevel != "" {
		levelKey = e.KeepLevel
	}
	lengthBits := proto.F32Bits(float32(e.LengthM))
	grade := int(e.Cat)
	var opt byte
	if grade >= 2 {
		opt = 1
	}
	// 游戏时刻 600：gameSec=(u-1438387200)*24
	day, hour, minute := gameClockOf(e.TS)
	var state uint32
	if grade >= 4 && e.NameZh == "" {
		state |= 4194304 // 蓝冠未命名可命名位
	}
	var name *string
	if e.NameZh != "" {
		name = &e.NameZh
	}
	var bait *string
	if e.KeepBait != "" {
		bait = &e.KeepBait
	}
	var species *string
	if e.Species != "" {
		species = &e.Species
	}
	var loc *string
	if levelKey != "" {
		loc = &levelKey
	}
	created := uint64(netTicksNow()) // f22 是版本戳，每次序列化必须严格变大
	updated := uint64(filetimeFromUnixFloat(e.TS))
	displayXP := uint32(float64(e.WeightG) * map[bool]float64{true: 1.3, false: 0.85}[grade == 1])

	return wiregen.MustMakeObj("ecknbkpeeji", map[int]*proto.Value{
		0: proto.Str(species),
		1: proto.U32(uint32(e.WeightG)),
		2: proto.U32(lengthBits),
		3: proto.U32(uint32(grade)),
		5: proto.U64(updated),
		6: wiregen.MustMakeObj("bncmdhbgahe", map[int]*proto.Value{
			0: proto.U32(uint32(day)),
			1: proto.U32(uint32(hour)),
			2: proto.U32(uint32(minute)),
		}),
		7:  proto.U32(uint32(opt)),
		8:  proto.Str(bait),
		9:  proto.U32(27), // 钩位：浮钓
		10: proto.Str(loc),
		11: proto.Str(name),
		14: proto.U32(displayXP),
		16: proto.Guid(e.EchoGUID),
		18: proto.U32(5), // 鱼护容器 id
		22: proto.U64(created),
		23: proto.U64(updated),
		24: proto.U32(state),
	})
}

// gameClockOf 游戏时刻 (day,hour,min)：现实 1h = 游戏 24h，基线 2015-08-01。
func gameClockOf(unixSec float64) (uint32, uint32, uint32) {
	gameSec := (unixSec - 1438387200) * 24
	totalMin := int64(gameSec) / 60
	if totalMin < 0 {
		totalMin = 0
	}
	day := totalMin / 1440
	hour := (totalMin / 60) % 24
	min := totalMin % 60
	return uint32(day), uint32(hour), uint32(min)
}

// filetimeFromUnixFloat 将 Unix 时间转换为 FILETIME，并保留亚秒精度。
func filetimeFromUnixFloat(ts float64) int64 {
	return int64(ts*10_000_000) + 116444736000000000
}

// netTicksFromUnix .NET Ticks。
func netTicksFromUnix(unixSec float64) int64 {
	return int64(unixSec)*10_000_000 + 621_355_968_000_000_000
}

var _ = context.Background
