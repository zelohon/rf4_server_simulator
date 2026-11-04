package gameserver

import (
	"context"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 欢乐时光：服务端权威判定，客户端只负责显示。
//
// 加成类型取值见 internal/wiregen/tables.go 的 eodbnfbiome 枚举：
//   4 = 会员 Premium    56 = 快乐时光 HappyHour
const (
	bonusTypePremium   = 4
	bonusTypeHappyHour = 56
)

// premiumXPBonus 会员自身的经验倍率。官方分档，这里先用固定值。
const premiumXPBonus = 1.10

// HappyHourConfig 欢乐时光配置（DB 单行，id=1）。
type HappyHourConfig struct {
	Enabled      bool
	NeedsPremium bool
	StartMinute  int
	DurationMin  int
	XPMult       float64
	MoneyMult    float64
}

// happyHourConfig 读取配置；读不到时回退到安全默认（关闭）。
func (s *Server) happyHourConfig(ctx context.Context) HappyHourConfig {
	cfg := HappyHourConfig{
		Enabled: false, NeedsPremium: true,
		StartMinute: 1140, DurationMin: 120,
		XPMult: 1.5, MoneyMult: 1.0,
	}
	var en, np int
	err := s.DB.SQL.QueryRowContext(ctx, `
		SELECT enabled, needs_premium, start_minute, duration_min, xp_mult, money_mult
		FROM happy_hour_config WHERE id=1`).
		Scan(&en, &np, &cfg.StartMinute, &cfg.DurationMin, &cfg.XPMult, &cfg.MoneyMult)
	if err != nil {
		return cfg // 表不存在或未配置 → 视为关闭
	}
	cfg.Enabled = en != 0
	cfg.NeedsPremium = np != 0
	return cfg
}

// IsPremium 会员是否在有效期内（唯一判定入口，别在别处重复写）。
func (s *Server) IsPremium(ctx context.Context, uid int64) bool {
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return false
	}
	return p.MembershipActive(time.Now())
}

// HappyHourState 当前是否处于欢乐时光窗口。
//
// active=false 但 remaining>0 表示「窗口开着但没资格」
// （客户端据此显示「需要高级会员」）。
func (s *Server) HappyHourState(ctx context.Context, uid int64) (active bool, remaining int, mult float64) {
	cfg := s.happyHourConfig(ctx)
	if !cfg.Enabled {
		return false, 0, 1
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := dayStart.Add(time.Duration(cfg.StartMinute) * time.Minute)
	end := start.Add(time.Duration(cfg.DurationMin) * time.Minute)
	if now.Before(start) || !now.Before(end) {
		return false, 0, 1
	}
	remain := int(end.Sub(now).Seconds())
	if cfg.NeedsPremium && !s.IsPremium(ctx, uid) {
		return false, remain, 1 // 窗口内但无会员：不给加成
	}
	return true, remain, cfg.XPMult
}

// buildBonusList 构造 310.f6 的加成行列表。
//
// 每行使用 kcgnjffmege (type_id=108)：
//
//	f0=倍率(F32Bits 编码成 U32，客户端显示 ×1.5)
//	f1=guid（固定值，用加成类型做末段区分）
//	f2=物品品类(0=非物品加成)
//	f3=加成类型 liampcgfchp（4=会员 56=欢乐时光）
//	f4=标志
func (s *Server) buildBonusList(ctx context.Context, uid int64) *proto.Value {
	list := proto.Args(proto.S("31"))
	isPrem := s.IsPremium(ctx, uid)
	hhActive, _, hhMult := s.HappyHourState(ctx, uid)
	if !isPrem && !hhActive {
		return list
	}
	if isPrem {
		row := wiregen.MustMakeObj("kcgnjffmege", map[int]*proto.Value{
			0: proto.U32(proto.F32Bits(float32(premiumXPBonus))),
			1: proto.Guid("00000000-0000-0000-0000-000000000004"),
			2: proto.U32(0),
			3: proto.U32(bonusTypePremium),
			4: proto.Null(),
		})
		list.Items = append(list.Items, row)
	}
	if hhActive {
		row := wiregen.MustMakeObj("kcgnjffmege", map[int]*proto.Value{
			0: proto.U32(proto.F32Bits(float32(hhMult))),
			1: proto.Guid("00000000-0000-0000-0000-000000000056"),
			2: proto.U32(0),
			3: proto.U32(bonusTypeHappyHour),
			4: proto.Null(),
		})
		list.Items = append(list.Items, row)
	}
	return list
}
