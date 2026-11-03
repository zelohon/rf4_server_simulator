package gameserver

// 补充基础注册之外的一组协议命令：物品状态位与格位、玩家档案、公聊和容器枚举。
//
// 这些处理器通过 init 和 RegisterExtra 注册。扩展注册先执行，内建注册随后覆盖
// 相同命令，因此这里只负责补齐缺失的处理。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func init() {
	RegisterExtra(func(d *Dispatch) {
		d.Register(4, 9, hItemSetFlag)
		d.Register(4, 10, hItemSetSlot)
		d.Register(3, 20, hChangeName)
		d.Register(3, 21, hChangeRegion)
		d.Register(3, 23, hVerifyEmail)
		d.Register(24, 10, hSocialPublic)
		d.Register(24, 2, hSocialCurrent)
		d.Register(24, 3, hSocialCurrent)
	})
}

// ---------------------------------------------------------------------------
// 4/9 items/suflag
// ---------------------------------------------------------------------------

// hItemSetFlag 处理 4/9 items/suflag，对一批物品置位或清位状态标记。
//
// 请求中的 GUID 和整数都从整棵树收集。至少两个整数时，
// flag=ints[0]、op=ints[1]；只有一个整数时执行置位。op 非 0 使用 cur|flag，
// op 为 0 使用 cur&^flag。未知 GUID 或没有状态字段的物品会被跳过。
// 响应为容器快照 800，与 4/6、4/7 使用相同的 rigSnapshot800 构造。
func hItemSetFlag(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	guids := deepGUIDs(ctx.Tree)
	ints := treeIntsDeep(ctx.Tree)
	var flag, op uint64
	switch {
	case len(ints) >= 2:
		flag, op = ints[0], ints[1]
	case len(ints) == 1:
		flag, op = ints[0], 1
	}
	cctx := ctx.Sess.pushCtx()
	for _, g := range guids {
		row, err := srv.DB.GetItemByGUID(cctx, g)
		if err != nil || row == nil || row.UID != ctx.Sess.Userid {
			continue
		}
		fields, ferr := FieldsFromJSON(row.FieldsJSON)
		if ferr != nil {
			continue
		}
		idx := fieldIdxByCS(row.WireClass, csStateEnum)
		if idx < 0 || idx >= len(fields) {
			continue // 该类没有状态位字段
		}
		cur := u64Of(fields[idx].Num) & 0xFFFFFFFF
		var nw uint64
		if op != 0 {
			nw = cur | flag
		} else {
			nw = cur &^ flag
		}
		nw &= 0xFFFFFFFF
		fields[idx].Num = ptrU64(nw)
		if fj, jerr := FieldsToJSON(fields); jerr == nil {
			_ = srv.DB.UpdateItemFields(cctx, row.GUID, fj, row.Qty, row.Durability, row.Workmanship, row.RigGUID)
		}
	}
	ctx.Log.Info("4/9 标记", "count", len(guids), "flag", fmt.Sprintf("0x%x", flag), "op", op)
	return srv.rigSnapshot800(ctx), true
}

// ---------------------------------------------------------------------------
// 4/10 items/set_slot
// ---------------------------------------------------------------------------

// hItemSetSlot 处理 4/10 items/set_slot，将物品绑定到指定容器格位。
// 请求参数顺序为 [容器号, 物品 GUID, 槽位]。
//
// 同一槽位的原占用物品会被解绑回背包未指派格。目标物品同时更新
// item.container、item.slot 和 container_slot 绑定。该命令不返回响应。
func hItemSetSlot(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := treeIntsDeep(ctx.Tree)
	guids := deepGUIDs(ctx.Tree)
	container, slot := int64(0), int64(0)
	if len(ints) > 0 {
		container = int64(ints[0])
	}
	if len(ints) > 1 {
		slot = int64(ints[1])
	}
	if len(guids) == 0 {
		return nil, true // GUID 为空时直接忽略
	}
	guid := guids[0]
	cctx := ctx.Sess.pushCtx()
	row, err := srv.DB.GetItemByGUID(cctx, guid)
	if err != nil || row == nil || row.UID != ctx.Sess.Userid {
		ctx.Log.Info("4/10 物品不存在，忽略", "guid", guidShort(guid))
		return nil, true
	}
	cname := containerEnumName(uint64(container))
	// 格位绑定、原占用者解绑和物品位置必须同事务。container_slot.guid
	// 有 UNIQUE 约束，因此先删除目标物品的旧绑定再插入新绑定。
	tx, err := srv.DB.SQL.BeginTx(cctx, nil)
	if err != nil {
		ctx.Log.Warn("4/10 开启事务失败", "err", err)
		return nil, true
	}
	defer func() { _ = tx.Rollback() }()
	var prev string
	if err := tx.QueryRowContext(cctx,
		`SELECT COALESCE(guid,'') FROM container_slot WHERE uid=? AND container=? AND slot=?`,
		ctx.Sess.Userid, container, slot).Scan(&prev); err == nil && prev != "" && prev != guid {
		if _, err := tx.ExecContext(cctx,
			`UPDATE item SET container='backpack', slot=0 WHERE uid=? AND guid=?`,
			ctx.Sess.Userid, prev); err != nil {
			ctx.Log.Warn("4/10 解绑旧占用者失败", "err", err)
			return nil, true
		}
	}
	if _, err := tx.ExecContext(cctx,
		`DELETE FROM container_slot WHERE uid=? AND guid=?`, ctx.Sess.Userid, guid); err != nil {
		ctx.Log.Warn("4/10 清理物品旧格位失败", "err", err)
		return nil, true
	}
	if _, err := tx.ExecContext(cctx, `
		INSERT INTO container_slot(uid, container, slot, guid) VALUES(?,?,?,?)
		ON CONFLICT (uid, container, slot) DO UPDATE SET guid=?`,
		ctx.Sess.Userid, container, slot, guid, guid); err != nil {
		ctx.Log.Warn("4/10 绑定落库失败", "err", err)
		return nil, true
	}
	if _, err := tx.ExecContext(cctx,
		`UPDATE item SET container=?, slot=? WHERE uid=? AND guid=?`, cname, slot, ctx.Sess.Userid, guid); err != nil {
		ctx.Log.Warn("4/10 更新物品位置失败", "err", err)
		return nil, true
	}
	if err := tx.Commit(); err != nil {
		ctx.Log.Warn("4/10 提交事务失败", "err", err)
		return nil, true
	}
	ctx.Log.Info("4/10 绑定", "guid", guidShort(guid), "container", container, "slot", slot)
	return nil, true // fire-and-forget，不返回响应
}

func guidShort(g string) string {
	if len(g) > 8 {
		return g[:8]
	}
	return g
}

// ---------------------------------------------------------------------------
// 3/20 change_name / 3/21 change_region / 3/23 邮箱验证码
// ---------------------------------------------------------------------------

// hChangeName 处理 3/20 改名：读取首个非空字符串，写入 profile，
// 然后返回数据库中的完整档案 307。空名称交给兜底链。
func hChangeName(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	name := argFirstStr(ctx.Tree)
	if name == "" {
		return nil, false // 交给兜底链
	}
	cctx := ctx.Sess.pushCtx()
	if _, err := srv.DB.SQL.ExecContext(cctx,
		`UPDATE profile SET name=?, updated_ts=? WHERE uid=?`,
		name, float64(time.Now().Unix()), ctx.Sess.Userid); err != nil {
		ctx.Log.Warn("3/20 改名落库失败", "err", err)
		return nil, false
	}
	ctx.Log.Info("3/20 改名", "name", name)
	return replyProfile307(ctx)
}

// hChangeRegion 处理 3/21 换区：读取首个非空字符串或整数作为区域，
// 以字符串形式写入 profile，然后返回完整档案 307。无参数时仅返回当前档案。
func hChangeRegion(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	if region, has := argFirstStrOrInt(ctx.Tree); has {
		if _, err := srv.DB.SQL.ExecContext(ctx.Sess.pushCtx(),
			`UPDATE profile SET region=?, updated_ts=? WHERE uid=?`,
			region, float64(time.Now().Unix()), ctx.Sess.Userid); err != nil {
			ctx.Log.Warn("3/21 换区落库失败", "err", err)
		} else {
			ctx.Log.Info("3/21 换区", "region", region)
		}
	}
	return replyProfile307(ctx)
}

// hVerifyEmail 处理 3/23 邮箱验证。请求为 [token, code]。
//
// 当前没有验证码校验依据，因此接受任意提交。处理器把 3/22 保存在
// email_pending 中的邮箱写入 profile，清除挂起状态，设置 email_verified，
// 并返回数据库中的完整档案 307。
func hVerifyEmail(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	strs := argStringsTop(ctx.Tree)
	token, code := "", ""
	if len(strs) > 0 {
		token = strs[0]
	}
	if len(strs) > 1 {
		code = strs[1]
	}
	// 邮箱来自 3/22 写入的 email_pending，支持 map 和裸字符串两种形态。
	// 没有挂起邮箱时仍返回当前档案，保持重复请求幂等。
	email := ""
	if v, ok := ctx.Sess.GetState("email_pending"); ok && v != nil {
		switch t := v.(type) {
		case map[string]any:
			if s, isStr := t["email"].(string); isStr {
				email = s
			}
		case string:
			email = t
		}
	}
	cctx := ctx.Sess.pushCtx()
	if email != "" {
		if _, err := srv.DB.SQL.ExecContext(cctx,
			`UPDATE profile SET email=?, updated_ts=? WHERE uid=?`,
			email, float64(time.Now().Unix()), ctx.Sess.Userid); err != nil {
			ctx.Log.Warn("3/23 邮箱落库失败", "err", err)
		}
	}
	ctx.Sess.SetState("email_pending", nil) // 清除挂起邮箱
	ctx.Sess.SetState("email_verified", true)
	ctx.Log.Info("3/23 邮箱验证 -> 回档案", "token", token, "hasCode", code != "")
	return replyProfile307(ctx)
}

// replyProfile307 构造并返回数据库中的完整档案 307，供 3/20、3/21、3/23 共用。
func replyProfile307(ctx *Ctx) ([]byte, bool) {
	tree, err := ctx.Sess.Srv.BuildProfile307(ctx.Sess.pushCtx(), ctx.Sess.Userid)
	if err != nil {
		ctx.Log.Warn("307 构造失败", "err", err)
		return nil, false
	}
	body, err := proto.EncodeValue(tree, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// ---------------------------------------------------------------------------
// 24/10 social/public 公聊回显
// ---------------------------------------------------------------------------

// chatChannel 表示聊天频道。
type chatChannel struct {
	Name    string
	Region  string
	Users   int64
	Private bool
}

// sessChannel 读取会话当前频道；没有记录时返回默认频道 nav1_03[CN]。
// 状态值支持 map 形态，便于后续切换频道功能复用。
func sessChannel(sess *Session) chatChannel {
	def := chatChannel{Name: "nav1_03", Region: "CN", Users: 1}
	if v, ok := sess.GetState("channel"); ok {
		if m, isMap := v.(map[string]any); isMap {
			ch := def
			if s, isStr := m["name"].(string); isStr && s != "" {
				ch.Name = s
			}
			if s, isStr := m["region"].(string); isStr && s != "" {
				ch.Region = s
			}
			if n, isNum := m["users"].(int64); isNum && n > 0 {
				ch.Users = n
			}
			if b, isBool := m["private"].(bool); isBool {
				ch.Private = b
			}
			return ch
		}
	}
	return def
}

// buildChatEchoPush 构造 24/27 公聊回显推送，合同形状为 [obj:401, str, u32]：
//
//	args[0] = 401 lfbdbaffeai{f0 消息类型(0=玩家发言 CHAT_KIND_SAY), f1 文本,
//	          f2 发送者 uid, f3 FILETIME, f4 KV 参数(null)}
//	args[1] = locationId（频道显示名形态 name[region]，与 402 f0 同源）
//	args[2] = 频道在线人数
func buildChatEchoPush(loc, msg string, uid uint64, users uint32) *proto.Value {
	msgObj := wiregen.MustMakeObj("lfbdbaffeai", map[int]*proto.Value{
		0: proto.U32(0), // CHAT_KIND_SAY
		1: proto.StrVal(msg),
		2: proto.U32(uint32(uid)),
		3: proto.U64(uint64(proto.WindowsFiletimeNow())),
		4: proto.Null(),
	})
	return proto.Args524(msgObj, proto.StrVal(loc), proto.U32(users))
}

// hSocialCurrent 处理 24/3 social/current / 24/2 social/auto：
// 返回当前公共聊天频道。客户端没有频道时聊天输入框不会发送 24/10。
func hSocialCurrent(ctx *Ctx) ([]byte, bool) {
	ch := sessChannel(ctx.Sess)
	srv := ctx.Sess.Srv
	srv.mu.Lock()
	users := int64(0)
	for _, sess := range srv.sessions {
		if sess != nil && !sess.Closed() {
			users++
		}
	}
	srv.mu.Unlock()
	if users < 1 {
		users = 1
	}
	obj := wiregen.MustMakeObj("godhiicbgng", map[int]*proto.Value{
		0: proto.StrVal(ch.Name),
		1: proto.StrVal("公共频道"),
		2: proto.StrVal(ch.Region),
		3: proto.U32(uint32(users)),
		4: proto.U32(0), // 0=Public
		5: proto.Bool(false),
		6: proto.Null(), // 成员列表懒加载
		7: proto.U32(1),
		8: proto.Bool(false),
	})
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hSocialPublic 处理 24/10 公聊，命令本身不返回响应。
//
// 普通发言通过 24/27 回显，客户端只接受 27..31 的聊天推送，不能直接推送 24/10。
// 推送由 buildChatEpochPush 构造并经过形状合同校验。空消息不广播；
// GM 指令不回显原文也不广播。
func hSocialPublic(ctx *Ctx) ([]byte, bool) {
	msg := argFirstStr(ctx.Tree)
	if msg == "" {
		return nil, true // 空消息不返回响应也不广播
	}
	if strings.HasPrefix(msg, "/") {
		ctx.Log.Info("24/10 GM 指令（Go 未实现，不回显）", "text", msg)
		return nil, true
	}
	ch := sessChannel(ctx.Sess)
	// 24/27 args[1] 必须与 24/3 godhiicbgng.f0 完全一致；带 [CN] 会被客户端当作别的频道丢弃。
	loc := ch.Name
	users := ch.Users
	if users < 1 {
		users = 1
	}
	// 公聊广播给所有在线会话；只回显发送者会导致多人频道互相看不到。
	srv := ctx.Sess.Srv
	srv.mu.Lock()
	peers := make([]*Session, 0, len(srv.sessions))
	for _, sess := range srv.sessions {
		if sess != nil && !sess.Closed() {
			peers = append(peers, sess)
		}
	}
	srv.mu.Unlock()
	if int64(len(peers)) > users {
		users = int64(len(peers))
	}
	for _, sess := range peers {
		sess.Push(24, 27, buildChatEchoPush(loc, msg, uint64(ctx.Sess.Userid), uint32(users)))
	}
	return nil, true // fire-and-forget，不返回响应
}

// ---------------------------------------------------------------------------
// 容器枚举全表
// ---------------------------------------------------------------------------

// containerEnumName 将容器枚举转换为 item.container 使用的存储名。
//
//	0 背包    1/6 帐篷    2/7 汽车    3/8 船
//	4/9 摩托车   5 鱼护    10/11 房屋
//
// 未知枚举回退到背包。4/1、4/4、4/5 只有背包和鱼护包含物品；其他容器必须
// 返回空，避免同一 GUID 被客户端登记到多个容器后发生索引冲突。
func containerEnumName(v uint64) string {
	switch v {
	case 0:
		return "backpack"
	case 1, 6:
		return "tent"
	case 2, 7:
		return "car"
	case 3, 8:
		return "boat"
	case 4, 9:
		return "scooter"
	case 5:
		return "keepnet"
	case 10, 11:
		return "house"
	default:
		return "backpack"
	}
}

// ---------------------------------------------------------------------------
// 顶层参数读取辅助
// ---------------------------------------------------------------------------

// argFirstStr 返回顶层首个非空字符串，同时接受裸字符串树。
func argFirstStr(v *proto.Value) string {
	for _, s := range argStringsTop(v) {
		if s != "" {
			return s
		}
	}
	return ""
}

// argStringsTop 返回顶层字符串参数，只扫描 args 或裸节点，不递归子对象。
func argStringsTop(v *proto.Value) []string {
	var out []string
	if v == nil {
		return out
	}
	if v.Kind == proto.KindStr {
		if v.Str != nil {
			out = append(out, *v.Str)
		}
		return out
	}
	if v.Kind == proto.KindArgs || v.Kind == proto.KindValList {
		for _, it := range v.Items {
			if it != nil && it.Kind == proto.KindStr && it.Str != nil {
				out = append(out, *it.Str)
			}
		}
	}
	return out
}

// argFirstStrOrInt 按顺序返回首个非空字符串或整数，整数包括 0。
// 返回值和是否命中的标志。
func argFirstStrOrInt(v *proto.Value) (string, bool) {
	if v == nil {
		return "", false
	}
	if v.Kind != proto.KindArgs && v.Kind != proto.KindValList {
		return "", false
	}
	for _, it := range v.Items {
		if it == nil {
			continue
		}
		switch it.Kind {
		case proto.KindStr:
			if it.Str != nil && *it.Str != "" {
				return *it.Str, true
			}
		case proto.KindU32, proto.KindU64:
			return strconv.FormatUint(it.Num, 10), true
		}
	}
	return "", false
}
