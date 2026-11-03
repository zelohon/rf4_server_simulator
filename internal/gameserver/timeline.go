package gameserver

// timeline.go 实现登录和进图时间线中的辅助命令。
// 这些命令大多只需要返回结构合法的最小响应；响应类型错误会导致客户端解析失败。

import (
	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

func registerTimelineHandlers(d *Dispatch) { // fire-and-forget：客户端不等响应，绝不回包
	d.Register(1, 7, hNoReply)     // game/locale 区域上报
	d.Register(1, 15, hNoReply)    // game/enter_place_notify 进入地图单向通知
	d.Register(3, 10, hBoatUseLog) // boat_use 上报类：当前只记录请求体，便于分析上船协议
	d.Register(1, 18, hBoolTrue)   // game/telemetry 设备指纹 → bool true

	// 空集/无数据语义
	d.Register(6, 2, hNull)          // comp/load 无竞赛 → null
	d.Register(24, 3, hSocialCurrent) // social/current：返回当前公共聊天频道
	d.Register(24, 12, hU32ArrEmpty) // social/bl_get 黑名单 → 空 u32 数组
	d.Register(9, 5, hCraftTemplates)

	// 环境/视觉（28 族，纯构造最小合法结构）
	d.Register(28, 1, hEnvironment)
	d.Register(28, 2, hFog)

	// 金币商店货架（23/1 storereal/list）
	d.Register(23, 1, hStoreRealList)
}

// hNoReply 处理但不回包（fire-and-forget）。
func hNoReply(*Ctx) ([]byte, bool) { return nil, true }

// hBoolTrue 回 bool true（0x0e）。
func hBoolTrue(*Ctx) ([]byte, bool) { return []byte{0x0e}, true }

// hNull 回 null（0x00）。
func hNull(*Ctx) ([]byte, bool) { return []byte{0x00}, true }

// hU32ArrEmpty 回空 u32 数组（05 0000）。
func hU32ArrEmpty(*Ctx) ([]byte, bool) { return []byte{0x05, 0x00, 0x00}, true }

// hCraftTemplates 处理 9/5，返回空的 args(97) 配方模板。
// 必须通过编码器生成字节，避免手写长度前缀导致错位。
func hCraftTemplates(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("97")), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody(), true
	}
	return body, true
}

// nullVal 共享的 null 值。
func nullVal() *proto.Value { return &proto.Value{Kind: proto.KindNull} }

// hEnvironment 处理 28/1，返回 5011 环境配置对象和空 args(206) 参数表。
func hEnvironment(*Ctx) ([]byte, bool) {
	obj := proto.Obj("acjaheimjge", 5011,
		proto.Field{Op: "BYTE", Num: ptrU64(0)},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("206"))},
	)
	body, err := proto.EncodeValue(obj, wiregen.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hFog 处理 28/2，返回 5003 雾效与环境时段对象。
// 赛季块包含固定时段和空的环境参数容器。
func hFog(*Ctx) ([]byte, bool) {
	season := proto.Obj("nkakjeigjla", 5013,
		proto.Field{Op: "BYTE", Num: ptrU64(6)},
		proto.Field{Op: "U64", Num: ptrU64(134238816000000000)},
		proto.Field{Op: "U64", Num: ptrU64(134265924000000000)},
		proto.Field{Op: "OPT", Num: ptrU64(0)},
		proto.Field{Op: "U64", Num: ptrU64(0)},
		proto.Field{Op: "VAL", Val: nullVal()},
	)
	fog := proto.Obj("fopkeoppndn", 5003,
		proto.Field{Op: "VAL", Val: proto.Obj("fkcmcjlagdh", 5012,
			proto.Field{Op: "VAL", Val: proto.Args(proto.S("210"), season)}),
		},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("202"))},
		proto.Field{Op: "VAL", Val: proto.Args(proto.S("201"))},
	)
	body, err := proto.EncodeValue(fog, wiregen.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// hStoreRealList 23/1 金币商店：本项目不提供充值或付费功能，恒回空货架。
func hStoreRealList(ctx *Ctx) ([]byte, bool) {
	body, err := proto.EncodeValue(proto.Args(proto.S("147")), ctx.Sess.Table())
	if err != nil {
		return emptyArgsBody(), true
	}
	return body, true
}

// hBoatUseLog 处理 3/10 boat_use。
// 当前没有上船/载具实现，这里只记录请求体，便于分析客户端按 E 后发送的内容。
func hBoatUseLog(ctx *Ctx) ([]byte, bool) {
	if ctx.Log != nil {
		ctx.Log.Info("3/10 boat_use", "len", len(ctx.Body), "hex", traceHexHead(ctx.Body, 160))
	}
	return nil, true
}
