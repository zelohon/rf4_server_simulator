package gameserver

import (
	"math"
	"time"
)

const (
	gameMinutesPerDay        = 24 * 60
	gameMinutesPerRealSecond = float64(gameHoursPerRealHr*60) / 3600
)

// gameMinuteOfDayAt 返回客户端世界时钟采用的游戏日内分钟。
// 游戏时间以 24 倍速运行，因此 150 秒现实时间正好是一游戏小时。
func gameMinuteOfDayAt(now time.Time) uint32 {
	gameMinutes := float64(now.Unix()-worldDayEpochUnix) * gameMinutesPerRealSecond
	minute := int64(math.Floor(gameMinutes)) % gameMinutesPerDay
	if minute < 0 {
		minute += gameMinutesPerDay
	}
	return uint32(minute)
}

// sessionGameMinuteAt 返回本次会话中客户端当前采用的连续游戏分钟。
// 307 档案先下发日内分钟，客户端随后以 24 倍速继续推进；这里保留
// 越过午夜后的天数，避免篝火在 23:xx 点燃时被立即判为过期。
func sessionGameMinuteAt(sess *Session, now time.Time) float64 {
	if sess == nil || sess.startedAt.IsZero() {
		return float64(gameMinuteOfDayAt(now))
	}
	elapsed := now.Sub(sess.startedAt).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return float64(gameMinuteOfDayAt(sess.startedAt)) + elapsed*gameMinutesPerRealSecond
}

func gameMinutesToRealDuration(minutes int64) time.Duration {
	if minutes <= 0 {
		return 0
	}
	return time.Duration(float64(minutes) / gameMinutesPerRealSecond * float64(time.Second))
}
