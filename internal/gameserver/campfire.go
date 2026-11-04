package gameserver

import (
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// defaultCampfireDurationMin 是游戏内分钟；24 倍速下 60 游戏分钟等于
// 150 秒现实时间。
const defaultCampfireDurationMin int64 = 60

// hCampfireStart 处理 3/11 player/campfire_start。
// 响应 802 的真实布局是：绝对结束游戏分钟、两个 onobdmohahp[] 物品数组、Guid[]。
// 服务端另用 FILETIME 持久化真实到期点；把 FILETIME 塞进 f1 会让客户端
// 按物品数组解析并断线。
// 燃料消耗规则暂不猜测，先把真实点火状态闭环。
func hCampfireStart(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	cctx := ctx.Sess.pushCtx()
	now := time.Now()
	nowFT := int64(proto.WindowsFiletimeNow())
	st := srv.stateFor(ctx.Sess.Userid)
	st.mu.Lock()
	loc, spot := st.Loc, int64(st.Spot)
	st.mu.Unlock()

	duration := defaultCampfireDurationMin
	maxRealDuration := gameMinutesToRealDuration(duration)
	expiry := nowFT + maxRealDuration.Nanoseconds()/100
	if existing, err := srv.DB.GetCampfireState(cctx, ctx.Sess.Userid); err == nil &&
		existing != nil && existing.ExpiresFT > nowFT &&
		existing.ExpiresFT-nowFT <= maxRealDuration.Nanoseconds()/100+5*10_000_000 {
		duration = existing.DurationMin
		expiry = existing.ExpiresFT
	} else if err != nil {
		ctx.Log.Warn("读取篝火状态失败", "err", err)
	}

	state := store.CampfireState{
		UID: ctx.Sess.Userid, Loc: loc, Spot: spot,
		DurationMin: duration, ExpiresFT: expiry,
	}
	if err := srv.DB.UpsertCampfireState(cctx, state); err != nil {
		ctx.Log.Warn("保存篝火状态失败", "err", err)
	}

	remainingGameMinutes := float64(expiry-nowFT) / 10_000_000 / 60 * gameHoursPerRealHr
	clientNowMinute := sessionGameMinuteAt(ctx.Sess, now)
	clientExpiryMinute := uint32(math.Ceil(clientNowMinute + remainingGameMinutes))

	obj, err := wiregen.MakeObj("ihaaofcmopp", map[int]*proto.Value{
		0: proto.U32(clientExpiryMinute),
		1: proto.Args(proto.S("61")),
		2: proto.Args(proto.S("61")),
		3: proto.GuidArr(nil),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}

	items := make([]string, 0, 2)
	for _, guid := range deepGUIDs(ctx.Tree) {
		if row, ierr := srv.DB.GetItemByGUID(cctx, guid); ierr == nil && row != nil {
			items = append(items, row.ItemID)
		}
	}
	ctx.Log.Info("篝火点火", "loc", loc, "spot", spot, "duration_game_min", duration,
		"client_now_min", math.Floor(clientNowMinute*10)/10, "client_expires_min", clientExpiryMinute,
		"expires_ft", expiry, "request_items", items, "expect_response", ctx.ExpectResponse)
	return body, true
}
