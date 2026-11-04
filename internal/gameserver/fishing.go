package gameserver

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 钓鱼状态、判鱼、咬钩调度和 14/14 推送。

// FishState 保存单个玩家的钓鱼状态，重连后继续使用。
type FishState struct {
	mu           fishStateMutex
	GearGuid     string
	RigType      int64 // jppmjnahbpo 钓组种类（on_cast 记录；路亚族单独节奏）
	Loc          string
	Spot         uint64
	DepthCM      int64 // 玩家设钓深（17/12 记入；14/1 的 158.f2 透传用）
	CastActive   bool
	BiteGen      uint64 // 咬钩定时器代号：每次重排递增，过期定时器自动作废
	InWater      bool   // 14/7 动画位出现 0x400（钓组在水中）
	NeedBite     bool   // 到点时还没入水，挂起等 0x400 再派鱼
	CastTs       time.Time
	FallTs       time.Time
	FallSeen     bool
	RigSettled   bool
	TickSeen     bool
	TickSeq      uint64 // 14/7 f14 客户端 tick 序号（诊断/恢复）
	LastAnim     uint32 // 14/7 f5 上一次的动画位（仅在变更时打日志）
	PushSent     bool
	Hooked       bool
	Resolved     bool
	Pending      *RolledFish
	biteTimer    *time.Timer
	pendingTimer *time.Timer
	BiteAt       time.Time     // 原定咬钩绝对时刻；硬重启后据此恢复 timer
	Keepnet      []*RolledFish // 回退缓存（DB 权威）
	Bait         string
	PosX         float64 // 3/9 位置同步：世界坐标 X
	PosZ         float64 // 3/9 位置同步：世界坐标 Z
	HasPos       bool
	LastSkill    int64       // 本次钓鱼大类（1 浮钓 / 2 纺车 / 3 底钓 / 11 海钓）
	Armed        *RolledFish // GM 武装"下一杆必上"（唯一绕过重量上限的路径）
	Bonus        fishingSkillBonus
	persist      fishPersistMeta
}

// RolledFish 待结算/已结算的鱼。
type RolledFish struct {
	FishGUID string
	Species  string
	Grade    int64
	WeightG  int64
	LengthM  float64
	XP       int64
	Silver   int64
	LocLevel string
	Stamina  float64 // obj27#05 搏鱼体力池；0=按体重和已验证兜底比例计算
	Force    float64 // obj27#08 拉力；0=按体重和已验证比例计算
	NamedID  string  // 被命名鱼
	Name     string
}

// stateFor 按 uid 取/建状态。
func (s *Server) stateFor(uid int64) *FishState {
	s.fishMu.Lock()
	st, ok := s.fishStates[uid]
	if !ok {
		st = s.newFishState(uid)
		s.fishStates[uid] = st
	}
	s.fishMu.Unlock()
	s.resumeFishingState(st)
	return st
}

// ---- 钓组动画就位位族 ----
// 0x200 水层静止（浮标）、0x800 底部静止（沉底/Feeder）、0x1000 水面静止。
const rigAnimSettledMask = 0x200 | 0x800 | 0x1000

// rigAnimInWaterMask 是“钓组在水中”的位族。协议字段表确认
// 0x200=MotionlessInWaterColumn；0x400/0x800/0x1000 分别是不同水层/姿态的
// 静止状态。朝岸上抛竿不会出现这些位，因此仍能挡住“饵落在陆地”。
const rigAnimInWaterMask = 0x200 | 0x400 | 0x800 | 0x1000

// ---- 咬钩延迟 ----

// PUSH_DELAY_BANDS 四档混合分布（概率, 下限s, 上限s）。
var pushDelayBands = []struct {
	p      float64
	lo, hi float64
}{
	{0.40, 6.0, 10.0},
	{0.40, 10.0, 16.0},
	{0.15, 16.0, 24.0},
	{0.05, 24.0, 35.0},
}

// officialPushDelaySamples 是咬钩延迟样本。运行时过滤低于 6 秒的值，
// 与 14/3 的落水立漂保护一致。
var officialPushDelaySamples = []float64{
	0.52, 1.49, 1.93, 3.48, 3.52, 4.52,
	6.48, 6.7, 7.09, 7.53, 9.51, 9.77, 10.49, 11.53,
	12.1, 12.48, 13.43, 13.5, 17.7, 17.73, 17.79, 21.48,
	21.5, 23.5, 25.35, 25.62, 27.49, 34.47, 38.47, 38.49,
	38.49, 46.6,
}

var officialPushDelayEligible = func() []float64 {
	out := make([]float64, 0, len(officialPushDelaySamples))
	for _, v := range officialPushDelaySamples {
		if v >= 6.0 {
			out = append(out, v)
		}
	}
	return out
}()

func officialPushDelayWith(rng *rand.Rand) float64 {
	if len(officialPushDelayEligible) > 0 {
		if rng == nil {
			return officialPushDelayEligible[rand.Intn(len(officialPushDelayEligible))]
		}
		return officialPushDelayEligible[rng.Intn(len(officialPushDelayEligible))]
	}
	// 数据文件异常时的四档兜底；运行时正常不会走到这里。
	r := rand.Float64()
	if rng != nil {
		r = rng.Float64()
	}
	acc := 0.0
	for _, b := range pushDelayBands {
		acc += b.p
		if r <= acc {
			u := rand.Float64()
			if rng != nil {
				u = rng.Float64()
			}
			v := b.lo + u*(b.hi-b.lo)
			return math.Round(v*100) / 100
		}
	}
	return 10.0
}

func officialPushDelay() float64 { return officialPushDelayWith(nil) }

// biteDelay 依次应用采样、技能加成、范围约束和倍率。
// 路亚使用 1.5~6 秒的短窗，避免 14/14 晚于收线窗口。
func (s *Server) biteDelay(st *FishState, bonuses ...fishingSkillBonus) float64 {
	bonus := neutralFishingSkillBonus()
	if len(bonuses) > 0 {
		bonus = bonuses[0]
	}
	fixed := s.tunableF("bite_delay_fixed", 0)
	scale := s.tunableF("bite_delay_scale", 1.0)
	maxDelay := s.tunableF("bite_delay_max", 20.0)
	lureMax := s.tunableF("bite_delay_lure_max", 6.0)
	if st != nil && st.Loc != "" && s.DB != nil {
		biteLoc := st.Loc
		if resolved := s.DB.ResolveStatLoc(s.dbCtx(), biteLoc); resolved != "" {
			biteLoc = resolved
		}
		if setting, err := s.DB.GetFishBiteSetting(s.dbCtx(), biteLoc); err == nil && setting.HasOverride {
			fixed = setting.BiteDelayFixed
			scale = setting.BiteDelayScale
			maxDelay = setting.BiteDelayMax
			lureMax = setting.BiteDelayLureMax
		}
	}
	if fixed > 0 {
		return math.Max(0.05, fixed)
	}
	if st != nil && rigTypeLureFamily[st.RigType] {
		d := 1.5 + rand.Float64()*(6.0-1.5)
		d *= bonus.BiteSpeed
		if d < 0.8 {
			d = 0.8
		}
		if capS := lureMax; capS > 0 && d > capS {
			d = capS
		}
		if scale != 1.0 {
			d *= scale
		}
		return math.Max(0.8, d)
	}
	d := officialPushDelay()
	return standardBiteDelay(d, bonus.BiteSpeed, maxDelay, scale)
}

func standardBiteDelay(sample, biteSpeed, capS, scale float64) float64 {
	if biteSpeed <= 0 {
		biteSpeed = 1
	}
	d := sample * biteSpeed
	if d < 6.0 {
		d = 6.0
	}
	if capS > 0 && d > capS {
		d = capS
	}
	if scale != 1.0 {
		d *= scale
	}
	return math.Max(0.05, d)
}

// biteProbabilityForLoc 上鱼概率（默认 1.0 每杆必出）。后台鱼口覆盖优先。
func (s *Server) biteProbabilityForLoc(loc string) float64 {
	p := s.tunableF("bite_probability", 1.0)
	if s.DB != nil && loc != "" {
		biteLoc := loc
		if resolved := s.DB.ResolveStatLoc(s.dbCtx(), biteLoc); resolved != "" {
			biteLoc = resolved
		}
		if setting, err := s.DB.GetFishBiteSetting(s.dbCtx(), biteLoc); err == nil && setting.HasOverride {
			p = setting.BiteProbability
		}
	}
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// tunableF 运行参数读取（PG tunables 优先，缓存 30s；缺省回退）。
func (s *Server) tunableF(key string, def float64) float64 {
	s.tunMu.Lock()
	defer s.tunMu.Unlock()
	if s.tunCache == nil || time.Since(s.tunAt) > 30*time.Second {
		m := map[string]float64{}
		if s.DB != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			rows, err := s.DB.SQL.QueryContext(ctx, `SELECT key, value FROM tunables`)
			if err == nil {
				for rows.Next() {
					var k, v string
					if rows.Scan(&k, &v) == nil {
						var f float64
						if jsonFloat(v, &f) == nil {
							m[k] = f
						}
					}
				}
				rows.Close()
			}
			cancel()
		}
		s.tunCache = m
		s.tunAt = time.Now()
	}
	if v, ok := s.tunCache[key]; ok {
		return v
	}
	return def
}

func jsonFloat(s string, out *float64) error {
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%g", out)
	return err
}

// ---- 判鱼 ----

type fishPoolEntry struct {
	species       string
	minG, maxG    int64
	valuableLineG int64
}

// 内置鱼池按地点保存鱼种、重量范围和达标线。
var fishPools = map[string][]fishPoolEntry{
	"loc1_02": {
		{"crucian", 150, 900, 400}, {"c.roach", 80, 600, 318},
		{"freshwater_mussel", 30, 150, 80}, {"perch", 100, 800, 340}, {"ruffe", 50, 300, 84},
	},
	"lvl2_01": {
		{"c.roach", 80, 700, 318}, {"perch", 100, 1200, 340}, {"ruffe", 50, 300, 84},
		{"ruffe_n", 50, 300, 84}, {"crucian", 150, 900, 400},
	},
	"level_019_american_pond": {
		{"perch", 100, 1500, 340}, {"c.roach", 80, 600, 318}, {"crucian", 150, 900, 400},
	},
}

var defaultFishPool = []fishPoolEntry{
	{"crucian", 150, 900, 400}, {"c.roach", 80, 600, 318},
	{"perch", 100, 1200, 340}, {"freshwater_mussel", 30, 150, 80},
}

// maxMockWeight 是缺少可信装备物理数据时使用的重量上限。
const maxMockWeight = 650

// 以下三项来自旧版对真实服务器 14/14 帧的逐字节校准：
// 53g -> 体力池 126.5661239624、拉力 0.5197524514；
// 63g -> 拉力 0.6178189516。旧版两条样本的拉力都严格满足每克 0.00980665。
const (
	verifiedStaminaPerGram      = 2.3880400747623083
	verifiedInitialStaminaRatio = 0.5628187656402588
	verifiedForcePerGram        = 0.009806650025503976
	// 实机反馈鱼体力见底过快：默认把体力池放大 4 倍，并让鱼以 85% 体力开局。
	// 两项都可通过 tunables 的 fish_stamina_scale / fish_initial_stamina_ratio 调整。
	defaultFishStaminaScale        = 4.0
	defaultFishInitialStaminaRatio = 0.85
)

// lengthSeed 保存体长公式 k*w^p 的参数。
var lengthSeed = map[string][2]float64{
	"perch":   {0.021948, 0.40868},
	"c.roach": {0.033498, 0.34010},
	"s.bream": {0.066818, 0.20866},
	"crucian": {0.035129, 1.0 / 3.0},
}

// scoreRate 是默认积分率。
func scoreRate(grade int64) float64 {
	switch grade {
	case 1:
		return 2.0
	case 2:
		return 2.6
	case 3:
		return 4.0
	case 4:
		return 6.0
	}
	return 2.0
}

// speciesPushCalib 按鱼种和档位提供 14/14 的积分率与质量系数。
// 未配置时使用通用值。
var speciesPushCalib = map[string]map[int64][2]float64{
	"ruffe":     {2: {0.8625, 0.4}},
	"c.roach":   {1: {1.6825, 0.5}, 2: {2.3515, 0.5}},
	"perch":     {1: {2.8268, 0.7}, 2: {2.4336, 0.8}},
	"c.carp":    {1: {2.6033, 0.4}, 2: {2.7555, 0.4}},
	"crucian":   {1: {0, 0.55}, 2: {1.9721, 0.6}},
	"a.sleeper": {1: {3.241, 0.3}, 2: {4.3602, 0.3}},
	"vjun":      {1: {23.0181, 0.1}},
	"e.chub":    {1: {0, 0.7}, 2: {0, 0.75}},
	"bream":     {2: {0, 0.3}},
	"s.bream":   {2: {0, 0.3}},
}

func pushScoreRate(species string, grade int64) float64 {
	if byGrade := speciesPushCalib[species]; byGrade != nil {
		if v := byGrade[grade]; v[0] > 0 {
			return v[0]
		}
	}
	return scoreRate(grade)
}

func fishQuality(species string, grade int64) float64 {
	if byGrade := speciesPushCalib[species]; byGrade != nil {
		if v := byGrade[grade]; v[1] > 0 {
			return v[1]
		}
	}
	return 0.5
}

// rollFish 优先从静态鱼库判鱼，地点无数据时回退内置鱼池。
// bait 是 14/2 记录的钩上饵 systemID，可影响鱼种权重。
func (s *Server) rollFish(loc string, bait string) *RolledFish {
	return s.rollFishWithRules(loc, bait, 0, fishingSkillBonus{BiteSpeed: 1})
}

// rollFishForRig 是判鱼入口：先计算当前钓组的最弱环体重上限，
// 再对静态鱼库和内置鱼池应用该上限及奖杯幸运。
func (s *Server) rollFishForRig(ctx context.Context, uid int64, gearGUID, loc, bait string, bonus fishingSkillBonus) *RolledFish {
	capKg, ids, weakest := s.gearWeightCapForRig(ctx, uid, gearGUID)
	if capKg > 0 {
		s.log.Info("钓鱼物理上限", "rod", ids.Rod, "reel", ids.Reel,
			"line", ids.Line, "leader", ids.Leader, "weakest", weakest, "cap_kg", capKg)
	}
	return s.rollFishWithRules(loc, bait, capKg, bonus)
}

func (s *Server) rollFishWithRules(loc, bait string, gearCapKg float64, bonus fishingSkillBonus) *RolledFish {
	if s.tunableF("fish_bait_rules_enabled", 0) == 1 {
		if s.DB == nil {
			return nil
		}
		return s.rollFishFromDBWithRules(loc, bait, gearCapKg, bonus)
	}
	if s.DB != nil {
		if f := s.rollFishFromDBWithRules(loc, bait, gearCapKg, bonus); f != nil {
			return f
		}
	}
	return s.rollFishPoolsWithRules(loc, gearCapKg, bonus)
}

// rollFishFromDB 根据静态统计判鱼：地点名册提供鱼种，捕获计数提供权重，
// 饵效调整权重，体重按鱼种分布采样，档位由重量决定，体长由鱼种参数计算。
// 地点无数据时返回 nil，由调用方回退内置鱼池。
func (s *Server) rollFishFromDB(loc, bait string) *RolledFish {
	return s.rollFishFromDBWithRules(loc, bait, 0, fishingSkillBonus{BiteSpeed: 1})
}

func (s *Server) rollFishFromDBWithRules(loc, bait string, gearCapKg float64, bonus fishingSkillBonus) *RolledFish {
	strict := s.tunableF("fish_bait_rules_enabled", 0) == 1
	cctx := s.dbCtx()
	statLoc := s.DB.ResolveStatLoc(cctx, loc)
	if statLoc == "" {
		return nil
	}
	roster, err := s.DB.LocRoster(cctx, statLoc)
	if err != nil || len(roster) == 0 {
		return nil
	}
	keys := make([]string, 0, len(roster))
	wts := make([]float64, 0, len(roster))
	for _, r := range roster {
		keys = append(keys, r.FishKey)
		wts = append(wts, r.Catches)
	}
	// 管理后台鱼口覆盖：静态名册保持不变，覆盖值和额外鱼种在运行时叠加。
	// 若所有有效权重都被关成 0，则忽略整组覆盖，避免 weightedChoice 落到
	// 最后一个已禁用的鱼种。
	if s.DB != nil {
		if ovs, oerr := s.DB.FishWeightOverrides(cctx, statLoc); oerr == nil && len(ovs) > 0 {
			seen := make(map[string]int, len(keys))
			for i, key := range keys {
				seen[key] = i
			}
			base := append([]float64(nil), wts...)
			hasPositive := false
			for key, ov := range ovs {
				i, ok := seen[key]
				if !ok {
					keys = append(keys, key)
					wts = append(wts, 0)
					i = len(wts) - 1
					seen[key] = i
				}
				if !ov.Enabled || ov.Weight <= 0 {
					wts[i] = 0
					continue
				}
				wts[i] = ov.Weight
				hasPositive = true
			}
			if !hasPositive && !strict {
				copy(wts, base)
				keys = keys[:len(roster)]
				wts = wts[:len(roster)]
			}
		}
	}
	if strict {
		keys, wts = s.filterFishBaits(statLoc, bait, keys, wts)
		if len(keys) == 0 {
			return nil
		}
	}
	// companion 周纪录饵效（config bait_pref_scale / bait_pref_weight_shift；
	// 缺表=空，运行中性）。tier：top5=3 / top20=2 / 在列=1 → ×8/×4/×2；
	// 未在列 ×(0.9 - 0.6×覆盖度)（覆盖度 = total_evidence/200，封顶 1）。
	prefScale := s.tunableF("bait_pref_scale", 1.0)
	prefShift := s.tunableF("bait_pref_weight_shift", 1.0)
	var tiers map[string]int64
	var meta map[string]int64
	if bait != "" && prefScale > 0 {
		tiers = s.DB.BaitTiers(cctx, bait)
		if tiers != nil {
			meta = s.DB.CompanionEvidence(cctx)
		}
	}
	baitHit := false
	if tiers != nil {
		for i, key := range keys {
			t := tiers[key]
			if t > 0 {
				wts[i] *= companionTierMult(t) * prefScale
				baitHit = true
				continue
			}
			ev := meta[key]
			cov := float64(ev) / 200.0
			if cov > 1.0 {
				cov = 1.0
			}
			wts[i] *= (0.9 - 0.6*cov) * prefScale
		}
	}
	key := weightedChoice(keys, wts)
	t := tiers[key] // tiers 为 nil 时取零值 0
	modeMult := 1.0
	if t >= 2 {
		modeMult = 1.0 + 0.12*float64(t)*prefShift
	}
	w := s.sampleWeightDB(cctx, key, modeMult)
	// fish_weight_scale：整体缩放鱼的实际体重（在判档与装备上限之前）。
	if k := s.tunableF("fish_weight_scale", 1.0); k > 0 && k != 1.0 {
		w = int64(math.Round(float64(w) * k))
		if w < 1 {
			w = 1
		}
	}
	// 有可信装备数据时，将重量限制在钓组上限内；gearCapKg<=0 时不设额外上限。
	if gearCapKg > 0 {
		w = capWeightGrams(w, int64(gearCapKg*1000))
	}
	grade := s.rollGradeDB(cctx, key, w)
	grade = applyTrophyLuck(grade, bonus.TrophyLuck)
	lk, lp := s.DB.SpeciesLengthKP(cctx, key)
	length := lk * math.Pow(float64(w), lp)
	length = math.Round(length*100000) / 100000
	f := &RolledFish{
		FishGUID: newFishGUID(),
		Species:  key,
		Grade:    grade,
		WeightG:  w,
		LengthM:  length,
	}
	if baitHit {
		s.log.Info("roll_from_db 命中饵效", "鱼种", key, "重量", w, "tier", t)
	}
	return f
}

// companionTierMult 将饵效档位转换为权重倍率。
func companionTierMult(tier int64) float64 {
	switch tier {
	case 3:
		return 8.0
	case 2:
		return 4.0
	case 1:
		return 2.0
	}
	return 1.0
}

// weightSkewK 右偏钟形的右尾拉伸系数。
// 过大会把绝大多数体重压到平均值附近；1.6 能让小、中、大个体分布更明显。
const weightSkewK = 1.6

// sampleWeightDB 按鱼种自然体重分布采样。
// 峰值取达标带中点，上限取蓝冠线的 1.05 倍，并使用对数正态分布和拒绝采样。
func (s *Server) sampleWeightDB(ctx context.Context, key string, modeMult float64) int64 {
	val, tro, sup := s.DB.GradeLines(ctx, key)
	var vf, trof, supf float64
	if val != nil {
		vf = float64(*val)
	}
	if tro != nil {
		trof = float64(*tro)
	}
	if sup != nil {
		supf = float64(*sup)
	}
	// 峰值 M：达标带中点（缺 val→tro*0.6→sup*0.3→250）
	M := 250.0
	switch {
	case val != nil && tro != nil && trof > vf:
		M = (vf + trof) / 2.0
	case tro != nil:
		M = trof * 0.6
	case sup != nil:
		M = supf * 0.3
	}
	if modeMult != 1.0 {
		M *= modeMult // 饵效命中：峰值锚点向奖杯线右移
	}
	// 整体峰值向右上抬 15%，让日常鱼获里更容易出现中大个体。
	M *= 1.15
	// 上限 hi：蓝冠线×1.25（缺 sup→tro*1.45→M*6.0）
	var hi float64
	switch {
	case sup != nil:
		hi = supf * 1.25
	case tro != nil:
		hi = trof * 1.45
	default:
		hi = M * 6.0
	}
	// 下限 lo：少量"太小/不达标"个体
	lo := M * 0.12
	if val != nil {
		lo = math.Min(lo, vf*0.2)
	}
	lo = math.Max(1.0, math.Min(lo, M*0.6))
	if hi <= M {
		hi = M * 1.6
	}
	hi = math.Max(hi, lo+1.0)
	sd := math.Max(0.20, (math.Log(hi)-math.Log(M))/weightSkewK)
	mu := math.Log(M) + sd*sd
	for i := 0; i < 32; i++ {
		w := math.Exp(rand.NormFloat64()*sd + mu)
		if lo <= w && w <= hi {
			return int64(math.Max(1, math.Round(w)))
		}
	}
	return int64(math.Max(1, math.Round(math.Min(math.Max(M, lo), hi))))
}

// rollGradeDB 根据重量和 species_weight 的三个阈值确定鱼档位。
// trophy_g 是达标线，trophy_line_g 是奖杯线，super_trophy_line_g 是蓝冠线；
// 缺少某个阈值时只使用已有的分界。
func (s *Server) rollGradeDB(ctx context.Context, key string, weightG int64) int64 {
	val, tro, sup := s.DB.GradeLines(ctx, key)
	return gradeFromLines(val, tro, sup, weightG)
}

// gradeFromLines 根据重量和档位线判定档位。
// trophy 与 super 重合时不存在 3 档区间。
func gradeFromLines(val, tro, sup *int64, weightG int64) int64 {
	if tro != nil && sup != nil && *sup <= *tro {
		sup = nil
	}
	if val != nil && weightG < *val {
		return 1
	}
	if tro != nil && weightG < *tro {
		return 2
	}
	if sup != nil && weightG < *sup {
		return 3
	}
	if val == nil && tro == nil && sup == nil {
		return 1
	}
	if tro == nil {
		return 2
	}
	if sup == nil {
		return 3
	}
	return 4
}

// weightedChoice 按权重选择鱼种。
func weightedChoice(keys []string, wts []float64) string {
	total := 0.0
	for _, w := range wts {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return keys[rand.Intn(len(keys))]
	}
	r := rand.Float64() * total
	var acc float64
	for i, w := range wts {
		if w <= 0 {
			continue
		}
		acc += w
		if r <= acc {
			return keys[i]
		}
	}
	return keys[len(keys)-1]
}

// rollFishPools 在静态鱼库无数据时使用内置鱼池。
func (s *Server) rollFishPools(loc string) *RolledFish {
	return s.rollFishPoolsWithRules(loc, 0, fishingSkillBonus{BiteSpeed: 1})
}

func (s *Server) rollFishPoolsWithRules(loc string, gearCapKg float64, bonus fishingSkillBonus) *RolledFish {
	pool := fishPools[loc]
	if len(pool) == 0 {
		pool = defaultFishPool
	}
	e := pool[rand.Intn(len(pool))]
	// 三角分布（偏向 min + 30% 区间）
	mode := float64(e.minG) + float64(e.maxG-e.minG)*0.3
	w := triangular(float64(e.minG), float64(e.maxG), mode)
	if k := s.tunableF("fish_weight_scale", 1.0); k > 0 && k != 1.0 {
		w *= k
	}
	// 有装备物理数据时使用动态上限；缺少可信数据时使用内置上限，
	// 并按 fight_tolerance 放宽。
	cap := poolWeightCapG(gearCapKg, bonus.FightTolerance)
	if cap > 0 && w > cap {
		w = cap
	}
	weightG := int64(math.Round(w))
	if weightG < 1 {
		weightG = 1
	}
	// 有 species_weight 档位线时按重量判档；缺表时使用内置 valuable 线。
	grade := applyTrophyLuck(s.poolGrade(e, weightG), bonus.TrophyLuck)
	// 体长
	lp, ok := lengthSeed[e.species]
	if !ok || lp[0] == 0 {
		lp = [2]float64{0.0355, 1.0 / 3.0}
	}
	length := lp[0] * math.Pow(float64(weightG), lp[1])
	length = math.Round(length*100000) / 100000

	return &RolledFish{
		FishGUID: newFishGUID(),
		Species:  e.species,
		Grade:    grade,
		WeightG:  weightG,
		LengthM:  length,
	}
}

func poolWeightCapG(gearCapKg, fightTolerance float64) float64 {
	if gearCapKg > 0 {
		return gearCapKg * 1000
	}
	return float64(maxMockWeight) * (1.0 + math.Max(0, fightTolerance))
}

// applyTrophyLuck 按技能概率将鱼提升一档，最高为 4 档；无加成时保持原档位。
func applyTrophyLuck(grade int64, luck float64) int64 {
	if luck <= 0 || grade >= 4 {
		return grade
	}
	if rand.Float64() < luck {
		return grade + 1
	}
	return grade
}

func triangular(min, max, mode float64) float64 {
	r := rand.Float64()
	if r < (mode-min)/(max-min) {
		return min + math.Sqrt(r*(max-min)*(mode-min))
	}
	return max - math.Sqrt((1-r)*(max-min)*(max-mode))
}

// poolGrade 优先使用 species_weight 档位线；缺表时使用内置池的 valuable 线。
func (s *Server) poolGrade(e fishPoolEntry, weightG int64) int64 {
	if s.DB != nil {
		val, tro, sup := s.DB.GradeLines(s.dbCtx(), e.species)
		if val != nil || tro != nil || sup != nil {
			return gradeFromLines(val, tro, sup, weightG)
		}
	}
	if weightG >= e.valuableLineG {
		return 2
	}
	return 1
}

// newFishGUID 随机 16B hex，第 13 个 nibble 固定 '9'。
func newFishGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := fmt.Sprintf("%x", b[:])
	h = h[:12] + "9" + h[13:]
	return h
}

// ---- 14/14 判鱼推送 ----

// officialSpeciesStatics 保存 f9 到 f20 的鱼种静态参数，重量单位为克。
type fishStatics struct {
	f13 map[int64]float64
	f14 map[int64]float64
	f15 map[int64]float64
	f17 map[int64]float64
	f20 float64
	f9  [2]float64 // A, p
	f10 [2]float64
	f11 [2]float64
	f19 [2]float64
}

var officialSpeciesStatics = map[string]*fishStatics{
	"bream": {
		f13: map[int64]float64{2: 0.35}, f14: map[int64]float64{2: 2.0}, f15: map[int64]float64{2: 50.0},
		f17: map[int64]float64{2: 0.8406126}, f20: 0.9978698,
		f9: [2]float64{0.159245, 0.2656}, f10: [2]float64{0.153964, 0.2191},
		f11: [2]float64{0.0991065, 0.2604}, f19: [2]float64{1.60587e-05, 0.7986},
	},
	"c.carp": {
		f13: map[int64]float64{1: 0.25, 2: 0.35}, f14: map[int64]float64{1: 0.5, 2: 0.5},
		f15: map[int64]float64{1: 4.0, 2: 4.0}, f17: map[int64]float64{1: 0.8406168, 2: 0.8406168},
		f20: 0.9978698,
		f9:  [2]float64{0.485972, 0.1722}, f10: [2]float64{0.215219, 0.2493},
		f11: [2]float64{0.227746, 0.1307}, f19: [2]float64{3.18019e-05, 0.7000},
	},
	"c.roach": {
		f13: map[int64]float64{1: 0.30, 2: 0.15}, f14: map[int64]float64{1: 0.6, 2: 0.7},
		f15: map[int64]float64{1: 3.0, 2: 15.0}, f17: map[int64]float64{1: 0.8406168, 2: 0.8406168},
		f20: 0.9978698,
		f9:  [2]float64{0.414243, 0.2656}, f10: [2]float64{0.369253, 0.2191},
		f11: [2]float64{0.139494, 0.2604}, f19: [2]float64{1.78540e-05, 0.7986},
	},
	"crucian": {
		f13: map[int64]float64{1: 0.25, 2: 0.35}, f14: map[int64]float64{1: 0.5, 2: 0.5},
		f15: map[int64]float64{1: 4.0, 2: 4.0}, f17: map[int64]float64{1: 0.8406168, 2: 0.8406168},
		f20: 0.9978698,
		f9:  [2]float64{0.452706, 0.1941}, f10: [2]float64{0.233332, 0.2355},
		f11: [2]float64{0.206706, 0.1482}, f19: [2]float64{3.18023e-05, 0.7000},
	},
	"e.chub": {
		f13: map[int64]float64{1: 0.35, 2: 0.45}, f14: map[int64]float64{1: 0.6, 2: 0.6},
		f15: map[int64]float64{1: 6.0, 2: 6.0}, f17: map[int64]float64{1: 0.8406126, 2: 0.7030094},
		f20: 0.9978698,
		f9:  [2]float64{2.77068, 0.0700}, f10: [2]float64{0.725204, 0.1019},
		f11: [2]float64{0.27588, 0.1533}, f19: [2]float64{1.01401e-04, 0.5211},
	},
	"perch": {
		f13: map[int64]float64{1: 0.15, 2: 0.24}, f14: map[int64]float64{1: 0.4, 2: 0.5},
		f15: map[int64]float64{1: 40.0, 2: 40.0}, f17: map[int64]float64{1: 0.8995978, 2: 0.8995978},
		f20: 0.9979957,
		f9:  [2]float64{0.589176, 0.2371}, f10: [2]float64{0.182802, 0.2936},
		f11: [2]float64{0.202101, 0.2187}, f19: [2]float64{3.18016e-05, 0.7000},
	},
	"s.bream": {
		f13: map[int64]float64{2: 0.30}, f14: map[int64]float64{2: 1.2}, f15: map[int64]float64{2: 13.0},
		f17: map[int64]float64{2: 0.8406126}, f20: 0.9978698,
		f9: [2]float64{0.160663, 0.2656}, f10: [2]float64{0.119378, 0.2191},
		f11: [2]float64{0.0792359, 0.2604}, f19: [2]float64{1.77994e-05, 0.7986},
	},
}

// staticPow 计算 A*w^p，重量单位为克。
func staticPow(ap [2]float64, w float64) float64 {
	return ap[0] * math.Pow(w, ap[1])
}

// 未知鱼种使用的搏鱼静态参数和体重校准表。
var fallbackFightStatic = map[int]float64{
	9: 1.536152, 10: 1.028922, 11: 0.514461, 12: 0.0,
	13: 0.35, 14: 0.5, 15: 4.0, 16: 0.833907,
	17: 0.830302, 18: 1.0, 19: 0.002332, 20: 0.997804,
}

var fallbackCalWeights = []float64{94, 189, 212, 255, 331, 405}
var fallbackCalFields = map[int][]float64{
	9:  {1.3414, 1.6466, 1.7205, 1.8586, 2.0138, 2.0467},
	10: {0.9795, 1.1512, 1.1928, 1.2705, 1.3586, 1.3792},
	11: {0.4442, 0.5396, 0.5627, 0.6058, 0.6552, 0.6675},
	17: {0.6936, 0.8406, 0.8406, 0.8406, 0.8406, 0.8406},
	19: {0.0006, 0.0012, 0.0014, 0.0015, 0.0018, 0.0021},
}

func fallbackFightStatics(weightG float64) map[int]float64 {
	out := make(map[int]float64, len(fallbackFightStatic))
	for k, v := range fallbackFightStatic {
		out[k] = v
	}
	for idx, ys := range fallbackCalFields {
		if weightG <= fallbackCalWeights[0] {
			out[idx] = ys[0]
			continue
		}
		if weightG >= fallbackCalWeights[len(fallbackCalWeights)-1] {
			out[idx] = ys[len(ys)-1]
			continue
		}
		for j := 1; j < len(fallbackCalWeights); j++ {
			if weightG <= fallbackCalWeights[j] {
				x0, x1 := fallbackCalWeights[j-1], fallbackCalWeights[j]
				y0, y1 := ys[j-1], ys[j]
				out[idx] = y0 + (y1-y0)*(weightG-x0)/(x1-x0)
				break
			}
		}
	}
	return out
}

// gradeMapVal 读取指定档位的值，缺失时使用表中已有档位。
func gradeMapVal(m map[int64]float64, grade int64) float64 {
	if v, ok := m[grade]; ok {
		return v
	}
	for _, g := range []int64{1, 2, 3, 4} {
		if v, ok := m[g]; ok {
			return v
		}
	}
	return 0
}

// buildFish27 构造 27 判鱼对象。
func (s *Server) fishStaminaFor(f *RolledFish) float64 {
	if f == nil {
		return 0
	}
	stamina := f.Stamina
	if stamina <= 0 {
		stamina = verifiedStaminaPerGram * float64(f.WeightG)
	}
	scale := s.tunableF("fish_stamina_scale", defaultFishStaminaScale)
	if scale > 0 {
		stamina *= scale
	}
	return stamina
}

func (s *Server) fishInitialStaminaRatio() float64 {
	ratio := s.tunableF("fish_initial_stamina_ratio", defaultFishInitialStaminaRatio)
	if ratio <= 0 {
		return defaultFishInitialStaminaRatio
	}
	return math.Max(0.05, math.Min(1.0, ratio))
}

func (s *Server) buildFish27(f *RolledFish, hookGUID, gearGuid string) *proto.Value {
	w := float64(f.WeightG)
	st := officialSpeciesStatics[f.Species]
	var f9, f10, f11, f12v, f13v, f14v, f15v, f16v, f17v, f18v, f19v, f20v float64
	if st != nil {
		f9 = staticPow(st.f9, w)
		f10 = staticPow(st.f10, w)
		f11 = staticPow(st.f11, w)
		f13v = gradeMapVal(st.f13, f.Grade)
		f14v = gradeMapVal(st.f14, f.Grade)
		f15v = gradeMapVal(st.f15, f.Grade)
		f12v = 0
		f16v = 1
		f17v = gradeMapVal(st.f17, f.Grade)
		f18v = 1
		f19v = staticPow(st.f19, w)
		f20v = st.f20
	} else {
		fb := fallbackFightStatics(w)
		f9, f10, f11 = fb[9], fb[10], fb[11]
		f12v, f13v, f14v, f15v = fb[12], fb[13], fb[14], fb[15]
		f16v, f17v, f18v = fb[16], fb[17], fb[18]
		f19v, f20v = fb[19], fb[20]
	}
	stamina := s.fishStaminaFor(f)
	initialStamina := s.fishInitialStaminaRatio()
	pull := f.Force
	if pull <= 0 {
		pull = verifiedForcePerGram * w
	}
	// fight_power_scale：整体缩放鱼的拉力（同时影响 26 条张力曲线）。
	// 调低可显著减少断线与收杆脱钩，调高则更硬核；默认 1.0 保持真实样本比例。
	if k := s.tunableF("fight_power_scale", 1.0); k > 0 && k != 1.0 {
		pull = math.Max(0.5, math.Round(pull*k*10000)/10000)
	}

	var named *string
	name := f.Name
	if name != "" {
		named = &name
	}
	if named == nil && f.NamedID != "" {
		if nf, err := s.DB.NamedFishGet(s.dbCtx(), f.NamedID); err == nil && nf != nil {
			named = &nf.Name
		}
	}
	hook := hookGUID
	if hook == "" {
		// 线末端钩缺失时，27.f23 回退到鱼自身 GUID；
		// 客户端用 f23 查找鱼实体所属部件。
		hook = f.FishGUID
	}

	return wiregen.MustMakeObj("fgogccpglhb", map[int]*proto.Value{
		0:  proto.Guid(f.FishGUID),
		1:  proto.StrVal(f.Species),
		2:  proto.U32(uint32(f.Grade)),
		3:  proto.F32(float32(f.LengthM)),
		4:  proto.U32(uint32(f.WeightG)),
		5:  proto.F32(float32(stamina)),
		6:  proto.F32(float32(initialStamina)),
		8:  proto.F32(float32(pull)),
		9:  proto.F32(float32(f9)),
		10: proto.F32(float32(f10)),
		11: proto.F32(float32(f11)),
		12: proto.F32(float32(f12v)),
		13: proto.F32(float32(f13v)),
		14: proto.F32(float32(f14v)),
		15: proto.F32(float32(f15v)),
		16: proto.F32(float32(f16v)),
		17: proto.F32(float32(f17v)),
		18: proto.F32(float32(f18v)),
		19: proto.F32(float32(f19v)),
		20: proto.F32(float32(f20v)),
		21: s.buildFightCurves(f.Species, pull),
		22: proto.Str(named),
		23: proto.Guid(hook),
		24: proto.Null(),
	})
}

// ---- 搏鱼曲线（fight_curves_sets.json）----

// Push14_14 发判鱼推送：args("524")[guid(钓组), 27]。
func (s *Session) Push14_14(f *RolledFish, hookGUID, gearGuid string) {
	if gearGuid == "" {
		gearGuid = "00000000000000000000000000000000"
	}
	stamina := s.Srv.fishStaminaFor(f)
	initialStamina := s.Srv.fishInitialStaminaRatio()
	force := f.Force
	if force <= 0 {
		force = verifiedForcePerGram * float64(f.WeightG)
	}
	if k := s.Srv.tunableF("fight_power_scale", 1.0); k > 0 && k != 1.0 {
		force = math.Max(0.5, math.Round(force*k*10000)/10000)
	}
	s.log.Info("14/14 发鱼",
		"鱼种", f.Species, "重量_g", f.WeightG, "体力池", stamina,
		"初始体力", initialStamina, "拉力", force)
	s.Push(14, 14, proto.Args524(
		proto.Guid(gearGuid),
		s.Srv.buildFish27(f, hookGUID, gearGuid),
	))
}

// scheduleBite 排咬钩定时器（重抛作废上一杆）。
func (s *Server) scheduleBite(sess *Session, st *FishState) {
	s.scheduleBiteState(sess, st, false, true)
}

// scheduleBiteForHooked 在 14/14 尚未到达时补排判鱼，保留 Hooked 和原 CastTs，
// 不按新抛竿重置落水保护。
func (s *Server) scheduleBiteForHooked(sess *Session, st *FishState) {
	s.scheduleBiteState(sess, st, true, false)
}

func (s *Server) scheduleBiteState(sess *Session, st *FishState, preserveHooked, resetCast bool) {
	now := time.Now()
	bonus := s.fishingBonus(sess.pushCtx(), sess.Userid, st.RigType)
	st.mu.Lock()
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.CastActive = true
	st.Resolved = false
	// 已经推给客户端、且玩家刺中正在溜的鱼不能被新排程清掉：
	// 否则客户端还在溜旧鱼，服务端却按新鱼结算（青蛙→其他鱼就是这么来的）。
	fighting := st.PushSent && st.Hooked && st.Pending != nil
	if !fighting {
		st.Pending = nil
		if !preserveHooked {
			st.Hooked = false
		}
	}
	st.PushSent = false
	st.FallSeen = false
	st.RigSettled = false
	st.TickSeen = false
	if resetCast || st.CastTs.IsZero() {
		st.CastTs = now
	}
	if resetCast {
		// 新的一杆：还没确认在水中，等 14/7 的 0x400。
		st.InWater = false
		st.NeedBite = false
	}
	st.Bonus = bonus
	st.BiteGen++
	gen := st.BiteGen
	delay := s.biteDelay(st, bonus)
	st.BiteAt = now.Add(time.Duration(delay * float64(time.Second)))
	uid := sess.Userid
	timer := time.AfterFunc(time.Until(st.BiteAt), func() {
		s.onBiteTimer(uid, gen)
	})
	st.biteTimer = timer
	st.mu.Unlock()
}

// onBiteTimer 到点：顺延保护检查 → 判鱼 → 推 14/14。
func (s *Server) onBiteTimer(uid int64, gen uint64) {
	sess := s.liveSessionByUID(uid)
	st := s.stateFor(uid)
	st.mu.Lock()
	if gen != st.BiteGen {
		// 被重排替换掉的旧定时器：既不能清指针，也不能判鱼。否则会把新
		// 定时器的指针清空，它就成了“没人管但仍在跑”的第二条鱼。
		st.mu.Unlock()
		return
	}
	st.biteTimer = nil
	if sess == nil {
		// 进程仍活着但玩家断开：保留 CastActive/Pending/BiteAt，下一次
		// stateFor（重连首个业务请求）会按过期时刻立即恢复 timer。
		st.mu.Unlock()
		return
	}
	if !st.CastActive || st.Resolved || st.PushSent {
		st.mu.Unlock()
		return
	}
	if st.Pending != nil {
		// 钩上已经有鱼（或刚要结算），不能再判一条：否则会把 Pending 覆盖掉，
		// 客户端还在溜旧鱼，结算却按新鱼算。
		st.mu.Unlock()
		return
	}
	if !st.InWater {
		// 还没等到 14/7 的“在水中”动画位：先挂起，等它出现再派鱼。
		st.NeedBite = true
		st.mu.Unlock()
		return
	}
	base := st.CastTs
	if st.FallSeen && !st.FallTs.IsZero() {
		base = st.FallTs
	}
	lure := rigTypeLureFamily[st.RigType]
	// 路亚族 floor 降 1.2s：拟饵落水即生效，没有"浮标立稳"这回事。
	floor := 6 * time.Second
	minWait := time.Second
	if lure {
		floor = 1200 * time.Millisecond
		minWait = 500 * time.Millisecond
	}
	now := time.Now()
	if !base.IsZero() && now.Sub(base) < floor {
		// 落水保护：顺延
		wait := time.Until(base.Add(floor))
		if wait < minWait {
			wait = minWait
		}
		st.BiteAt = now.Add(wait)
		t := time.AfterFunc(wait, func() { s.onBiteTimer(uid, gen) })
		st.biteTimer = t
		st.mu.Unlock()
		return
	}
	// 钓组就位守门（Motionless 位族）——**路亚跳过**：收线动画族
	// StreamMoving/Twitch/StopAndGo 永远没有 Motionless 位，守门会把每杆
	// 路亚都顺延到 +12s（14/14 全部错过收线窗口）。
	if !lure && now.Sub(st.CastTs) < 12*time.Second && st.TickSeen && !st.RigSettled {
		// 顺延 1s 轮询（12s 兜底）
		st.BiteAt = now.Add(time.Second)
		t := time.AfterFunc(time.Second, func() { s.onBiteTimer(uid, gen) })
		st.biteTimer = t
		st.mu.Unlock()
		return
	}
	st.PushSent = true
	if st.Armed != nil {
		// GM 武装的"下一杆必上"优先，且绕过两道重量上限（arm_next_fish）
		f := st.Armed
		st.Armed = nil
		f.LocLevel = s.currentLevel(uid)
		st.Pending = f
		gear := st.GearGuid
		st.mu.Unlock()
		hookGUID := s.mountedHookGuid(s.dbCtx(), gear)
		sess.Push14_14(f, hookGUID, gear)
		return
	}
	if rand.Float64() >= s.biteProbabilityForLoc(st.Loc) {
		// 空竿
		st.Pending = nil
		st.Resolved = true
		st.mu.Unlock()
		return
	}
	f := s.rollFishForRig(s.dbCtx(), uid, st.GearGuid, st.Loc, st.Bait, st.Bonus)
	if f == nil {
		st.PushSent = false
		st.Pending = nil
		st.Resolved = true
		st.mu.Unlock()
		return
	}
	f.LocLevel = s.currentLevel(uid)
	st.Pending = f
	gear := st.GearGuid
	st.mu.Unlock()

	// f23 = 线末端钩 guid（槽 12 钩 → 路亚拟饵出厂钩回退；送错=鱼秒弃咬 f0=5）
	hookGUID := s.mountedHookGuid(s.dbCtx(), gear)
	sess.Push14_14(f, hookGUID, gear)
}

// repushPending14_14 在 14/12 f0=3 时于 6 秒后重推同一尾 pending 鱼，
// 不重新判定，以保持 GUID、重量和曲线不变。
func (s *Server) repushPending14_14(uid int64) {
	st := s.stateFor(uid)
	sess := s.liveSessionByUID(uid)
	st.mu.Lock()
	if st.pendingTimer != nil {
		st.pendingTimer = nil
	}
	if sess == nil {
		st.mu.Unlock()
		return
	}
	if !st.CastActive || st.Resolved || st.Pending == nil {
		st.mu.Unlock()
		return
	}
	f := st.Pending
	gear := st.GearGuid
	st.PushSent = true
	st.persist.recoverPending = false
	st.mu.Unlock()
	sess.Push14_14(f, s.mountedHookGuid(s.dbCtx(), gear), gear)
}

// bumpFishingCounter 增加玩家钓鱼计数器（escapes 等），幂等累加。
func (s *Server) bumpFishingCounter(uid int64, key string) {
	if s.DB == nil {
		return
	}
	_, _ = s.DB.SQL.ExecContext(s.dbCtx(), `
		INSERT INTO player_counters(uid, key, value) VALUES(?,?,1)
		ON CONFLICT(uid,key) DO UPDATE SET value=value+1`, uid, key)
}

// liveSessionByUID 取 uid 的存活会话（重连后新连接）。
func (s *Server) liveSessionByUID(uid int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.Userid == uid && !sess.Closed() {
			return sess
		}
	}
	return nil
}

// currentLevel 玩家当前地图。内存只作热缓存；缺失时从 profile 恢复，
// 进程重启后也不会默认回到 1 图。无效地图统一回退出生图。
func (s *Server) currentLevel(uid int64) string {
	if v, ok := s.levelByUID(uid); ok {
		v = travelLevelKey(v)
		s.setLevel(uid, v)
		return v
	}
	if s.DB != nil {
		if loc, err := s.DB.GetPlayerLocation(s.dbCtx(), uid); err == nil && loc != nil {
			v := travelLevelKey(loc.LevelKey)
			s.setLevel(uid, v)
			return v
		}
	}
	return spawnLevelKey
}

func (s *Server) levelByUID(uid int64) (string, bool) {
	s.levelMu.Lock()
	defer s.levelMu.Unlock()
	v, ok := s.levelMap[uid]
	return v, ok
}

func (s *Server) setLevel(uid int64, level string) {
	s.levelMu.Lock()
	s.levelMap[uid] = level
	s.levelMu.Unlock()
}

var _ = store.KeepnetEntry{}
var _ = wiregen.MakeObj
