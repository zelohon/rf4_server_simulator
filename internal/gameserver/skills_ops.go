package gameserver

// 技能学习与重置命令（5/2、5/3、5/4）。
// 三个命令成功后都返回 311 全量技能树，供客户端刷新面板。

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// fishingSkillBonus 保存钓鱼判定使用的三类技能增益。
type fishingSkillBonus struct {
	BiteSpeed      float64 // 咬钩等待倍率，<1 更快
	TrophyLuck     float64 // 单次升档概率
	FightTolerance float64 // 默认鱼池体重上限的放宽比例
}

func neutralFishingSkillBonus() fishingSkillBonus {
	return fishingSkillBonus{BiteSpeed: 1.0}
}

// fishingSkillCatForRig 根据钓组类型返回对应技能大类；未知类型归入浮钓。
func fishingSkillCatForRig(rigType int64) int64 {
	if bottomRigTypes[rigType] {
		return 3
	}
	if rigTypeLureFamily[rigType] {
		return 2
	}
	if rigType >= 50 && rigType <= 59 {
		return 11
	}
	return 1
}

const (
	// 熟练度增长概率按 p = 0.9 * 25 / (25 + 当前熟练度) 计算。
	// 熟练度越高，命中增长的概率越低；100% 时直接返回 0。
	fishingProficiencyChanceScale = 25.0
	fishingProficiencyMaxChance   = 0.9
)

// fishingProficiencyGainChance 返回一次钓获触发熟练度增长的概率。
func fishingProficiencyGainChance(current float64) float64 {
	if current < 0 {
		current = 0
	}
	if current >= profCap {
		return 0
	}
	return fishingProficiencyMaxChance * fishingProficiencyChanceScale /
		(fishingProficiencyChanceScale + current)
}

// fishingProficiencyLargeGainChance 返回增长命中后获得 0.2%（而非 0.1%）的概率。
// 低熟练度更容易获得较大增量，高熟练度更多获得 0.1%。
func fishingProficiencyLargeGainChance(current float64) float64 {
	if current < 0 {
		current = 0
	}
	if current > profCap {
		current = profCap
	}
	return 0.8 - 0.6*(current/profCap)
}

// fishingProficiencyGainForRoll 是可测试的纯函数。gainRoll 决定本次是否增长，
// amountRoll 决定命中后增加 0.2 还是 0.1。
func fishingProficiencyGainForRoll(current, gainRoll, amountRoll float64) float64 {
	if current >= profCap || gainRoll >= fishingProficiencyGainChance(current) {
		return 0
	}
	if amountRoll < fishingProficiencyLargeGainChance(current) {
		return 0.2
	}
	return 0.1
}

// awardFishingProficiency 按实际钓组大类增加熟练度。
func (s *Server) awardFishingProficiency(ctx context.Context, uid, rigType int64) (int64, float64) {
	cat := fishingSkillCatForRig(rigType)
	return cat, s.awardSkillProficiency(ctx, uid, cat)
}

// awardSkillProficiency 按指定技能大类增加熟练度。满熟练度不写库；
// 接近上限时只增加剩余可容纳的数值。
func (s *Server) awardSkillProficiency(ctx context.Context, uid, cat int64) float64 {
	return s.awardSkillProficiencyWithRolls(ctx, uid, cat, rand.Float64(), rand.Float64())
}

// awardSkillProficiencyWithRolls 允许测试注入随机数，实际写库仍复用同一概率公式。
func (s *Server) awardSkillProficiencyWithRolls(ctx context.Context, uid, cat int64, gainRoll, amountRoll float64) float64 {
	if s == nil || s.DB == nil {
		return 0
	}
	current, err := s.DB.GetSkillPoints(ctx, uid, cat)
	if err != nil {
		return 0
	}
	gain := fishingProficiencyGainForRoll(current, gainRoll, amountRoll)
	if gain <= 0 {
		return 0
	}
	if current+gain > profCap {
		gain = profCap - current
	}
	if gain <= 1e-9 {
		return 0
	}
	if err := s.DB.AddSkillPoints(ctx, uid, cat, gain); err != nil {
		return 0
	}
	return gain
}

// skillNodeEffectPoints 将每级效果百分比换算为效果点。
// 每 5% 计 1 点；effect_pct 未配置时使用默认值 1.0。
func skillNodeEffectPoints(def skillNodeDef) float64 {
	if def.EffectPct > 0 {
		return float64(def.EffectPct) / 5.0
	}
	return 1.0
}

// fishingBonus 汇总玩家在指定钓组大类上的效果。熟练度每 1% -> 0.15 效果点，
// 节点每级按 skillNodeEffectPoints 累加。
func (s *Server) fishingBonus(ctx context.Context, uid, rigType int64) fishingSkillBonus {
	b := neutralFishingSkillBonus()
	if s == nil || s.DB == nil {
		return b
	}
	cat := fishingSkillCatForRig(rigType)
	points, _ := s.DB.ListSkills(ctx, uid)
	nodes, _ := s.DB.ListSkillNodes(ctx, uid)
	effect := points[cat] * 0.15
	for _, def := range s.skillDefsOfCat(cat) {
		if lv := nodes[def.NodeID]; lv > 0 {
			effect += float64(lv) * skillNodeEffectPoints(def)
		}
	}
	b.BiteSpeed = 1.0 - effect/10.0*0.03
	if b.BiteSpeed < 0.5 {
		b.BiteSpeed = 0.5
	}
	b.TrophyLuck = effect / 10.0 * 0.012
	b.FightTolerance = effect / 10.0 * 0.06
	return b
}

func init() {
	RegisterExtra(func(d *Dispatch) {
		d.Register(5, 2, hSkillLearn)
		d.Register(5, 3, hSkillReset)
		d.Register(5, 4, hSkillResetAll)
	})
}

// hSkillLearn 处理 5/2 技能学习或升级。
// 请求需包含大类 ID 和节点 ID；缺少参数时交给兜底链。
// 无论成功与否都返回 311 全量技能树，失败原因只写日志。
func hSkillLearn(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := argsIntsDeep(ctx.Tree)
	if len(ints) < 2 {
		return nil, false // cat_id / node_id 缺失 → PASS
	}
	catID, nodeID := int64(ints[0]), int64(ints[1])
	ok, reason := srv.skillLearn(ctx.Sess.pushCtx(), ctx.Sess.Userid, catID, nodeID)
	if ok {
		ctx.Log.Info("5/2 学习 OK", "cat", catID, "node", nodeID)
	} else {
		ctx.Log.Info("5/2 学习拒绝", "cat", catID, "node", nodeID, "reason", reason)
	}
	return srv.replySkillTree(ctx)
}

// hSkillReset 处理 5/3 单节点重置并返还技能点。
// 有两个以上整数时取第二个作为节点 ID，否则取第一个。
func hSkillReset(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	ints := argsIntsDeep(ctx.Tree)
	if len(ints) > 0 {
		nodeID := int64(ints[0])
		if len(ints) >= 2 {
			nodeID = int64(ints[1])
		}
		// skills.reset_node：重置一个技能节点（返还技能点）
		if err := srv.DB.SetSkillNode(ctx.Sess.pushCtx(), ctx.Sess.Userid, nodeID, 0); err != nil {
			ctx.Log.Warn("5/3 重置写库失败", "node", nodeID, "err", err)
		} else {
			ctx.Log.Info("5/3 重置节点", "node", nodeID)
		}
	}
	return srv.replySkillTree(ctx)
}

// hSkillResetAll 处理 5/4 全部重置，清空 skill_node 后返回全量技能树。
func hSkillResetAll(ctx *Ctx) ([]byte, bool) {
	srv := ctx.Sess.Srv
	if !srv.requireDB(ctx) {
		return nil, false
	}
	if err := srv.DB.DeleteSkillNodes(ctx.Sess.pushCtx(), ctx.Sess.Userid); err != nil {
		ctx.Log.Warn("5/4 全部重置写库失败", "err", err)
	} else {
		ctx.Log.Info("5/4 已重置全部技能")
	}
	return srv.replySkillTree(ctx)
}

// replySkillTree 构造并编码 311 全量技能树；f0 是当前自然可用技能点。
func (s *Server) replySkillTree(ctx *Ctx) ([]byte, bool) {
	tree, err := s.BuildSkillTree311(ctx.Sess.pushCtx(), ctx.Sess.Userid, ^uint64(0))
	if err != nil {
		ctx.Log.Warn("5/x 技能树构造失败", "err", err)
		return nil, false // 树构造失败，交给兜底链
	}
	body, err := proto.EncodeValue(tree, ctx.Sess.Table())
	if err != nil {
		ctx.Log.Warn("5/x 技能树编码失败", "err", err)
		return nil, false
	}
	return body, true
}

// skillBonusPoints 返回管理员通过后台追加/扣减的未分配技能点（kv 偏移量）。
func (s *Server) skillBonusPoints(ctx context.Context, uid int64) int64 {
	if s.DB == nil {
		return 0
	}
	v, ok, err := s.DB.KVGet(ctx, fmt.Sprintf("skill_bonus:%d", uid))
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// skillPointsUnlimited 返回该玩家是否开启了"无限技能点"。
// 管理端"一键技能满"会同时打开这个开关，之后玩家在游戏里继续加点不再被点数卡住。
func (s *Server) skillPointsUnlimited(ctx context.Context, uid int64) bool {
	return s != nil && s.DB != nil && s.DB.SkillUnlimited(ctx, uid)
}

// skillAvailablePoints 返回等级累计技能点减去已投入点数后的余额（含管理员偏移）。
// 开启无限技能点时直接返回面板固定值。
func (s *Server) skillAvailablePoints(ctx context.Context, uid int64) int64 {
	if s.skillPointsUnlimited(ctx, uid) {
		return store.UnlimitedSkillPoints
	}
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return 0
	}
	total := skillPointsTotal(p.Level) + s.skillBonusPoints(ctx, uid)
	spent, err := s.DB.SkillNodeLevelSum(ctx, uid)
	if err != nil {
		return 0
	}
	avail := total - spent
	if avail < 0 {
		return 0
	}
	return avail
}

// skillLearn 学习或升级一个技能节点，返回是否成功及原因。
// 校验顺序（与客户端预期一致）：
//  1. 节点存在（skill_def）
//  2. 同互斥组内已选其它节点时拒绝（group_id 语义）
//  3. 可学（max_level > 0）
//  4. 未达最大等级
//  5. 大类熟练度 ≥ 节点解锁门槛
//  6. 有可用技能点
//
// 通过则 skill_node.level += 1。
func (s *Server) skillLearn(ctx context.Context, uid, catID, nodeID int64) (bool, string) {
	def, err := s.DB.GetSkillDef(ctx, catID, nodeID)
	if err != nil || def == nil {
		return false, "unknown-node"
	}
	// 前置节点：同互斥组内已选其它节点时拒绝
	if grp := def.GroupID; grp != 0 {
		for _, other := range s.skillDefsOfCat(catID) {
			if other.NodeID != nodeID && other.GroupID == grp {
				if lv, _ := s.DB.GetSkillNode(ctx, uid, other.NodeID); lv > 0 {
					return false, "group-conflict"
				}
			}
		}
	}
	if def.MaxLevel <= 0 {
		return false, "not-learnable"
	}
	cur, _ := s.DB.GetSkillNode(ctx, uid, nodeID)
	if cur >= def.MaxLevel {
		return false, "max-level"
	}
	prof, _ := s.DB.GetSkillPoints(ctx, uid, catID)
	if prof+1e-6 < float64(def.UnlockPct) {
		return false, "locked"
	}
	if s.skillAvailablePoints(ctx, uid) <= 0 {
		return false, "no-points"
	}
	if err := s.DB.SetSkillNode(ctx, uid, nodeID, cur+1); err != nil {
		s.log.Warn("5/2 节点写库失败", "uid", uid, "node", nodeID, "err", err)
		return false, "db-error"
	}
	return true, "ok"
}

// argsIntsDeep 按深度优先顺序收集整数。
// 只匹配 U32、U64、DATETIME 线型；对象字段只读取 U32/U64，VAL 内嵌树不递归。
func argsIntsDeep(v *proto.Value) []uint64 {
	var out []uint64
	var walk func(x *proto.Value)
	walk = func(x *proto.Value) {
		if x == nil {
			return
		}
		switch x.Kind {
		case proto.KindU32, proto.KindU64, proto.KindDateTime:
			out = append(out, x.Num)
		case proto.KindArgs, proto.KindValList, proto.KindArray,
			proto.KindU32Arr, proto.KindGuidArr, proto.KindTypedU32Arr:
			for _, it := range x.Items {
				walk(it)
			}
		case proto.KindObj:
			for i := range x.Fields {
				switch x.Fields[i].Op {
				case "U32", "U64":
					if x.Fields[i].Num != nil {
						out = append(out, u64Of(x.Fields[i].Num))
					}
				}
			}
		}
	}
	walk(v)
	return out
}
