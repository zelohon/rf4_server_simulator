package gameserver

import (
	"context"
	"math"
	"sync"

	"rf4server/internal/proto"
)

// 无轮手竿和缺少装配数据时保留旧协议基线；带轮竿使用综合物理模型。
const (
	castBaseFloatM   float32 = 7.5461488
	castBaseLureM    float32 = 7.1598907
	castBaseDefaultM float32 = castBaseFloatM

	castNominalDefaultM = 60.0
	castNominalLureM    = 45.0
	castMinPoweredM     = 8.0
)

// CastDistanceContext 是抛投模型的一次只读输入。后续加入风力、抛投姿势、
// 轮型或特殊技能时，可在不改主计算流程的情况下扩展字段和修正器。
type CastDistanceContext struct {
	RigType              int64
	CastAction           uint32
	CastPower            float64
	RodLengthM           float64
	RodTaper             float64
	RodTestMinG          float64
	RodTestMaxG          float64
	RigWeightG           float64
	BaitWeightG          float64
	PayloadWeightG       float64
	ReelEfficiency       float64
	ReelType             int64
	SpoolSize            int64
	SpoolCapacityM       float64
	SpoolFill            float64
	LineDiameterMM       float64
	LineLengthM          float64
	LineStretch          float64
	LineMaterial         int64
	LineLinearDensityGPM float64
	LineRunningWeightG   float64
	PlayerPrimary        float64
	PlayerSecondary      float64
	SkillCategory        int64
	SkillMastery         float64
	SkillNodeEffect      float64
}

// CastDistanceModifier 是可扩展的抛投距离变量接口。Factor 返回乘数；
// 1 为中性，小于 1 缩短距离，大于 1 增加距离。
type CastDistanceModifier interface {
	Name() string
	Factor(CastDistanceContext) float64
}

type castDistanceModifierFunc struct {
	name string
	fn   func(CastDistanceContext) float64
}

func (m castDistanceModifierFunc) Name() string { return m.name }
func (m castDistanceModifierFunc) Factor(c CastDistanceContext) float64 {
	if m.fn == nil {
		return 1
	}
	return m.fn(c)
}

var (
	extraCastModifiersMu sync.RWMutex
	extraCastModifiers   []CastDistanceModifier
)

// RegisterCastDistanceModifier 注册后续新增变量。注册应在服务启动阶段完成；
// 计算时会读取一份副本，不会与在线抛投互相阻塞。
func RegisterCastDistanceModifier(m CastDistanceModifier) {
	if m == nil || m.Name() == "" {
		return
	}
	extraCastModifiersMu.Lock()
	extraCastModifiers = append(extraCastModifiers, m)
	extraCastModifiersMu.Unlock()
}

var builtInCastModifiers = []CastDistanceModifier{
	castDistanceModifierFunc{name: "cast_action", fn: func(c CastDistanceContext) float64 {
		return castActionFactor(c.CastAction)
	}},
	castDistanceModifierFunc{name: "cast_power", fn: func(c CastDistanceContext) float64 {
		return castPowerFactor(c.CastPower)
	}},
	castDistanceModifierFunc{name: "rod_length", fn: func(c CastDistanceContext) float64 {
		if c.RodLengthM <= 0 {
			return 1
		}
		// 2.4m≈0.99，3.6m≈1.07；长竿有更大的有效加速行程。
		return clampFloat(0.82+0.07*c.RodLengthM, 0.85, 1.18)
	}},
	castDistanceModifierFunc{name: "rod_taper", fn: func(c CastDistanceContext) float64 {
		return castRodTaperFactor(c.RodTaper)
	}},
	castDistanceModifierFunc{name: "rod_test_payload", fn: func(c CastDistanceContext) float64 {
		return rodTestWeightFactor(c.PayloadWeightG, c.RodTestMinG, c.RodTestMaxG)
	}},
	castDistanceModifierFunc{name: "fishing_skill", fn: func(c CastDistanceContext) float64 {
		return castMasteryFactor(c.SkillMastery)
	}},
	castDistanceModifierFunc{name: "special_skill", fn: func(c CastDistanceContext) float64 {
		return castSpecialSkillFactor(c.SkillNodeEffect)
	}},
	castDistanceModifierFunc{name: "line_diameter", fn: func(c CastDistanceContext) float64 {
		return castLineDiameterFactor(c.LineDiameterMM)
	}},
	castDistanceModifierFunc{name: "line_stretch", fn: func(c CastDistanceContext) float64 {
		return castLineStretchFactor(c.LineStretch)
	}},
	castDistanceModifierFunc{name: "line_material", fn: func(c CastDistanceContext) float64 {
		return castLineMaterialFactor(c.LineMaterial)
	}},
	castDistanceModifierFunc{name: "line_mass", fn: func(c CastDistanceContext) float64 {
		return castLineMassFactor(c.LineRunningWeightG)
	}},
	castDistanceModifierFunc{name: "spool_size", fn: func(c CastDistanceContext) float64 {
		return castSpoolSizeFactor(c.SpoolSize)
	}},
	castDistanceModifierFunc{name: "spool_fill", fn: func(c CastDistanceContext) float64 {
		return castSpoolFillFactor(c.SpoolFill)
	}},
	castDistanceModifierFunc{name: "reel_type", fn: func(c CastDistanceContext) float64 {
		return castReelTypeFactor(c.ReelType)
	}},
	castDistanceModifierFunc{name: "player_state", fn: func(c CastDistanceContext) float64 {
		return castPlayerStateFactor(c.PlayerPrimary, c.PlayerSecondary)
	}},
	castDistanceModifierFunc{name: "reel_friction", fn: func(c CastDistanceContext) float64 {
		return clampFloat(c.ReelEfficiency, 0.25, 1)
	}},
}

type castDistanceProfile struct {
	DistanceM            float64
	BaseM                float64
	Friction             float64
	Efficiency           float64
	BearingQuality       float64
	OilQuality           float64
	ReelGUID             string
	LineLimitM           float64
	ClipLimitM           float64
	CastAction           uint32
	CastPower            float64
	ReelType             int64
	SpoolSize            int64
	SpoolCapacityM       float64
	SpoolFill            float64
	LineDiameterMM       float64
	LineLengthM          float64
	LineStretch          float64
	LineMaterial         int64
	LineLinearDensityGPM float64
	LineRunningWeightG   float64
	PlayerPrimary        float64
	PlayerSecondary      float64

	RodLengthM         float64
	RodTaper           float64
	RodTestMinG        float64
	RodTestMaxG        float64
	RigWeightG         float64
	BaitItemID         string
	BaitWeightG        float64
	PayloadWeightG     float64
	WeightFactor       float64
	SkillCategory      int64
	SkillMastery       float64
	SkillNodeEffect    float64
	SkillFactor        float64
	SpecialSkillFactor float64
	LengthFactor       float64
	Factors            map[string]float64
}

// 抛投动作来自 olieodeoaof.f8。1 是常规全挥杆；0 是短促动作；2 是
// 精准动作。动作只校验本次实际抛投，不会污染下一杆的最大距离。
func castActionFactor(action uint32) float64 {
	switch action {
	case 0:
		return 0.92
	case 2:
		return 0.86
	default:
		return 1
	}
}

func castPowerFactor(power float64) float64 {
	if power <= 0 {
		return 0.05
	}
	// 挥杆能量与距离不是线性关系，低蓄力仍能抛出一段距离。
	return clampFloat(0.18+0.82*math.Sqrt(clampFloat(power, 0, 1)), 0.05, 1)
}

func castLineDiameterFactor(diameterMM float64) float64 {
	if diameterMM <= 0 {
		return 1
	}
	return clampFloat(math.Sqrt(0.25/diameterMM), 0.84, 1.12)
}

// 快调/硬梢竿更容易把挥杆能量传给线组；这里只做小幅修正，避免覆盖
// 竿长与 Test 克重这两个主变量。
func castRodTaperFactor(taper float64) float64 {
	if taper <= 0 {
		return 1
	}
	return clampFloat(0.98+0.04*taper, 0.98, 1.022)
}

// 拉伸率越高，蓄力时的能量损失越明显。数值来自主线静态定义。
func castLineStretchFactor(stretch float64) float64 {
	if stretch < 0 {
		return 1
	}
	return clampFloat(1.03-0.05*stretch, 0.97, 1.03)
}

// 主线材质来自静态枚举：1=尼龙，2=碳线，3=编织线。
func castLineMaterialFactor(material int64) float64 {
	switch material {
	case 2:
		return 0.99
	case 3:
		return 1.035
	default:
		return 1
	}
}

// 参与飞行的线段质量会让启动和制动更吃能量；按 g 做温和的非线性衰减。
func castLineMassFactor(weightG float64) float64 {
	if weightG <= 0 {
		return 1
	}
	return clampFloat(1/math.Sqrt(1+weightG/60), 0.80, 1)
}

func castSpoolSizeFactor(size int64) float64 {
	// 线杯规格 1～7 对应常见纺车轮 1000～7000。小杯转动惯量低，
	// 同竿同线同饵时抛得更远；规格 5（6000）作为当前大杯基准。
	switch size {
	case 1:
		return 0.90
	case 2:
		return 0.84
	case 3:
		return 0.79
	case 4:
		return 0.75
	case 5:
		return 0.72
	case 6:
		return 0.68
	case 7:
		return 0.65
	case 8:
		return 0.84 // 低轮廓线杯
	case 9, 10, 11, 12, 13, 14:
		return 0.90 - 0.020*float64(size-9)
	case 15, 16, 17, 18, 19, 20:
		return 0.72 - 0.012*float64(size-15)
	case 21, 22:
		return 0.82
	case 23:
		return 0.56
	case 24:
		return 0.52
	default:
		return 1
	}
}

func castSpoolFillFactor(fill float64) float64 {
	if fill <= 0 {
		return 0.72
	}
	if fill <= 0.80 {
		return clampFloat(0.72+0.28*math.Sqrt(fill/0.80), 0.72, 1)
	}
	if fill <= 1 {
		return 1
	}
	return clampFloat(1-0.35*(fill-1), 0.80, 1)
}

func castReelTypeFactor(reelType int64) float64 {
	switch reelType {
	case 1: // 纺车轮
		return 1
	case 2: // 低轮廓水滴轮
		return 0.98
	case 3: // 鼓轮/水滴轮
		return 0.96
	case 4: // 船轮
		return 0.90
	case 5: // 飞钓轮
		return 0.92
	default:
		return 1
	}
}

func castPlayerStateFactor(primary, secondary float64) float64 {
	if primary <= 0 {
		primary = defaultPlayerConditionPrimary
	}
	if secondary <= 0 {
		secondary = defaultPlayerConditionSecondary
	}
	state := 0.55*(primary/defaultPlayerConditionPrimary) +
		0.45*(secondary/defaultPlayerConditionSecondary)
	return clampFloat(state, 0.70, 1.03)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func castBaseDistanceM(rigType int64) float32 {
	if rigTypeLureFamily[rigType] {
		return castBaseLureM
	}
	return castBaseDefaultM
}

func castBaseDistanceBits(rigType int64) uint32 {
	return proto.F32Bits(castBaseDistanceM(rigType))
}

func castNominalDistanceM(rigType int64) float64 {
	if rigTypeLureFamily[rigType] {
		return castNominalLureM
	}
	return castNominalDefaultM
}

// rodTestWeightFactor 把线组载荷换算为无封顶的单调抛投增幅。
// 同一套装备、其他参数不变时，载荷越重系数越高；采用对数增长，
// 重量可以持续增加距离，但不会因为几克变化而出现夸张跳变。
// 该系数只影响抛投，不参与断线或炸杆；炸杆仍由搏鱼张力决定。
func rodTestWeightFactor(payloadG, testMinG, testMaxG float64) float64 {
	if testMaxG <= 0 {
		return 1
	}
	if testMinG < 0 {
		testMinG = 0
	}
	if testMinG > testMaxG {
		testMinG, testMaxG = testMaxG, testMinG
	}
	if payloadG <= 0 {
		return 0.80
	}
	effectiveMin := testMinG
	if effectiveMin <= 0 {
		effectiveMin = math.Max(0.1, testMaxG*0.10)
	}
	referenceG := (effectiveMin + testMaxG) * 0.5
	if referenceG <= 0 {
		return 1
	}
	return 0.80 + 0.12*math.Log1p(payloadG/referenceG)
}

func castMasteryFactor(mastery float64) float64 {
	mastery = clampFloat(mastery, 0, profCap)
	return 0.82 + 0.18*(mastery/profCap)
}

// 已学习技能节点作为独立的特殊技能变量，避免与大类熟练度重复计算。
func castSpecialSkillFactor(nodeEffect float64) float64 {
	return 1 + clampFloat(nodeEffect*0.002, 0, 0.08)
}

func castSkillFactor(mastery, nodeEffect float64) float64 {
	return castMasteryFactor(mastery) * castSpecialSkillFactor(nodeEffect)
}

func castModifiersSnapshot() []CastDistanceModifier {
	extraCastModifiersMu.RLock()
	extras := append([]CastDistanceModifier(nil), extraCastModifiers...)
	extraCastModifiersMu.RUnlock()
	mods := make([]CastDistanceModifier, 0, len(builtInCastModifiers)+len(extras))
	mods = append(mods, builtInCastModifiers...)
	return append(mods, extras...)
}

func calculateCastDistance(c CastDistanceContext) (float64, map[string]float64) {
	distance := castNominalDistanceM(c.RigType)
	factors := make(map[string]float64)
	for _, modifier := range castModifiersSnapshot() {
		if modifier == nil {
			continue
		}
		factor := modifier.Factor(c)
		if math.IsNaN(factor) || math.IsInf(factor, 0) || factor <= 0 {
			factor = 1
		}
		factor = clampFloat(factor, 0.05, 2)
		factors[modifier.Name()] = factor
		distance *= factor
	}
	if distance < castMinPoweredM {
		distance = castMinPoweredM
	}
	return distance, factors
}

// castDistanceForRig 从真实装配、主线实例、轮子性能和技能表构造抛投上下文，
// 再经修正器管线计算。没有安装轮子的手竿继续使用旧协议距离。
func (s *Server) castDistanceForRig(ctx context.Context, uid int64, rodGUID string, rigType int64) castDistanceProfile {
	legacy := float64(castBaseDistanceM(rigType))
	p := castDistanceProfile{DistanceM: legacy, BaseM: legacy, Efficiency: 1,
		CastAction: 1, CastPower: 1,
		PlayerPrimary:   defaultPlayerConditionPrimary,
		PlayerSecondary: defaultPlayerConditionSecondary}
	if s == nil || s.DB == nil || rodGUID == "" {
		return p
	}

	ids := s.rigGearIDs(ctx, uid, rodGUID)
	if ids.ReelGUID == "" {
		return p
	}
	reel, err := s.DB.GetItemByGUID(ctx, ids.ReelGUID)
	if err != nil || reel == nil {
		return p
	}
	perf, err := s.syncReelPerformance(ctx, reel)
	if err != nil || perf == nil {
		return p
	}

	if rod, rerr := s.DB.GetItemByGUID(ctx, rodGUID); rerr == nil && rod != nil {
		if def, derr := s.itemDefForRow(ctx, rod); derr == nil {
			p.RodLengthM = rodLengthM(def)
			p.RodTaper = rodTaper(def)
			p.RodTestMinG, p.RodTestMaxG = rodTestG(def)
		}
	}

	line := s.castLineStats(ctx, rodGUID, ids.Line)
	p.LineDiameterMM = line.DiameterMM
	p.LineLengthM = line.LengthM
	p.LineStretch = line.Stretch
	p.LineMaterial = line.Material
	p.LineLinearDensityGPM = line.LinearDensityGPM

	terminalRigWeight, baitWeight := s.castPayloadWeights(ctx, rodGUID)
	// 真正随挥杆启动的只有竿梢到终端之间那段线；线杯上的存线不全部算作
	// 飞行载荷。这里以实际剩余线和竿长中的较小值估算运行线段。
	lineRunningLength := math.Min(p.LineLengthM, math.Max(p.RodLengthM, 0))
	p.LineRunningWeightG = p.LineLinearDensityGPM * lineRunningLength
	p.RigWeightG = terminalRigWeight + p.LineRunningWeightG
	p.BaitWeightG = baitWeight
	p.PayloadWeightG = p.RigWeightG + p.BaitWeightG
	// 同一次抛投同时记录咬钩逻辑使用的当前饵 ID，便于审计“饵效饵”和
	// “重量饵”是否来自同一个 f5 装配。饵 ID 本身不做距离乘数，真正
	// 进入物理计算的是上方汇总出的真实重量。
	p.BaitItemID = s.currentBait(ctx, rodGUID)
	p.SkillCategory, p.SkillMastery, p.SkillNodeEffect = s.castSkillStats(ctx, uid, rigType)
	p.PlayerPrimary, p.PlayerSecondary = s.castPlayerCondition(ctx, uid)
	p.ReelType, p.SpoolSize, p.SpoolCapacityM = s.castReelSpoolStats(ctx, reel, ids.Line)
	if p.SpoolCapacityM > 0 && p.LineLengthM > 0 {
		p.SpoolFill = p.LineLengthM / p.SpoolCapacityM
	}

	c := CastDistanceContext{
		RigType: rigType, CastAction: p.CastAction, CastPower: p.CastPower,
		RodLengthM: p.RodLengthM, RodTaper: p.RodTaper,
		RodTestMinG: p.RodTestMinG, RodTestMaxG: p.RodTestMaxG,
		RigWeightG: p.RigWeightG, BaitWeightG: p.BaitWeightG, PayloadWeightG: p.PayloadWeightG,
		ReelEfficiency: perf.CastEfficiency,
		ReelType:       p.ReelType, SpoolSize: p.SpoolSize, SpoolCapacityM: p.SpoolCapacityM,
		SpoolFill: p.SpoolFill, LineDiameterMM: p.LineDiameterMM, LineLengthM: p.LineLengthM,
		LineStretch: p.LineStretch, LineMaterial: p.LineMaterial,
		LineLinearDensityGPM: p.LineLinearDensityGPM, LineRunningWeightG: p.LineRunningWeightG,
		PlayerPrimary: p.PlayerPrimary, PlayerSecondary: p.PlayerSecondary,
		SkillCategory: p.SkillCategory, SkillMastery: p.SkillMastery, SkillNodeEffect: p.SkillNodeEffect,
	}
	p.BaseM = castNominalDistanceM(rigType)
	p.Friction = perf.CastFriction
	p.Efficiency = perf.CastEfficiency
	p.BearingQuality = perf.BearingQuality
	p.OilQuality = perf.OilQuality
	p.ReelGUID = ids.ReelGUID
	p.DistanceM, p.Factors = calculateCastDistance(c)
	p.LengthFactor = p.Factors["rod_length"]
	p.WeightFactor = p.Factors["rod_test_payload"]
	p.SkillFactor = p.Factors["fishing_skill"]
	p.SpecialSkillFactor = p.Factors["special_skill"]

	if p.LineLengthM > 0 {
		p.LineLimitM = p.LineLengthM
		p.DistanceM = math.Min(p.DistanceM, p.LineLimitM)
	}
	if clip, ok := s.savedReelClipM(ctx, rodGUID); ok && clip > 0 {
		p.ClipLimitM = float64(clip)
		p.DistanceM = math.Min(p.DistanceM, p.ClipLimitM)
	}
	if p.DistanceM < 0.5 {
		p.DistanceM = 0.5
	}
	return p
}

func (s *Server) castDistanceBitsForRig(ctx context.Context, uid int64, rodGUID string, rigType int64) uint32 {
	return proto.F32Bits(float32(s.castDistanceForRig(ctx, uid, rodGUID, rigType).DistanceM))
}
