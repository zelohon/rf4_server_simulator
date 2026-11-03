package gameserver

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 进度与钓获结算：
//   - xp 使用累计值，等级由 levelForXp 重算；写点时保证等级不下降，
//     并把 xp 回填到当前等级门槛。
//   - 会员身份只影响档案展示，不额外增加钓获经验。
//   - 鱼档位由 species_weight 的达标、奖杯和蓝冠三条重量线决定。

// settleXpResult 保存一次钓获结算的经验分解和结算状态。
type settleXpResult struct {
	baseXP       int64 // 技能倍率后的裸经验（分解中的"基础经验"）
	totalXP      int64 // 310.f0 = 本条鱼总经验
	silver       int64 // 展示用银价（catch_silver_cents 口径，不入账）
	prevBest     int64 // 该鱼种已有纪录重量（813.f6）
	newLevel     int64 // grant 后等级
	totalXp      int64 // grant 后累计经验
	levelsGained int64 // 本次升级数（310.f5）
	isRecord     bool  // 是否破个人纪录（310.f1：1=是，0=否）
	skillID      int64 // 310.f2 钓法大类（1 浮钓 / 2 纺车）
	// 会员 + 欢乐时光加成（B 段）
	appliedBonuses []uint32 // 生效的加成类型（4=会员 56=欢乐时光），供 310.f6 展示
	bonusMult      float64  // 本次总倍率（1.0 = 无加成）
}

// grantXpMonotonic 增加累计经验并重算等级，保证等级不下降。
// 返回新等级、累计经验和本次提升的等级数。
func (s *Server) grantXpMonotonic(ctx context.Context, uid, xpGain int64) (int64, int64, int64) {
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return 1, 0, 0
	}
	oldLevel := p.Level
	newXp := p.XP + xpGain
	newLevel := levelForXp(newXp)
	// 重算等级只升不降，避免档案中的 xp 与 level 不一致时降级。
	if newLevel < oldLevel {
		newLevel = oldLevel
	}
	if newLevel > maxLevel {
		newLevel = maxLevel
	}
	// xp 不得低于当前等级门槛，否则后续按 xp 重算会再次降级。
	if b := baseXp(newLevel); newXp < b {
		newXp = b
	}
	_, _ = s.DB.SQL.ExecContext(ctx, `UPDATE profile SET xp=?, level=?, updated_ts=? WHERE uid=?`,
		newXp, newLevel, float64(time.Now().Unix()), uid)
	_ = s.DB.AddTxn(ctx, uid, "xp", "", "", 0, 0,
		fmt.Sprintf("xp+%d level %d->%d", xpGain, oldLevel, newLevel))
	return newLevel, newXp, max64(0, newLevel-oldLevel)
}

// settleCatchXP 计算并持久化一条钓获的经验、熟练度、纪录和装备磨损。
// 经验依次经过技能倍率和单调入账；prevBest 在写入 catch_log 前读取。
func (s *Server) settleCatchXP(ctx context.Context, uid int64, f *RolledFish, bait string) *settleXpResult {
	r := &settleXpResult{skillID: 1}
	xp0, silver := catchValue(f.Species, f.Grade, f.WeightG)
	// 技能与全局经验倍率。
	skillMult := s.tunableF("xp_gain", 1.0)
	r.baseXP = int64(math.Round(float64(xp0) * skillMult))
	if r.baseXP < 1 {
		r.baseXP = 1
	}
	r.totalXP = r.baseXP

	// —— 会员 + 欢乐时光倍率（B 段）——
	// 倍率只作用于经验，不改变银价；门控由 IsPremium / HappyHourState 决定。
	r.bonusMult = 1.0
	if s.IsPremium(ctx, uid) {
		r.bonusMult *= premiumXPBonus
		r.appliedBonuses = append(r.appliedBonuses, bonusTypePremium)
	}
	if active, _, hm := s.HappyHourState(ctx, uid); active {
		r.bonusMult *= hm
		r.appliedBonuses = append(r.appliedBonuses, bonusTypeHappyHour)
	}
	if r.bonusMult != 1.0 {
		r.totalXP = int64(math.Round(float64(r.baseXP) * r.bonusMult))
	}

	r.silver = silver
	// 写入 catch_log 前读取现有纪录。
	r.prevBest, _ = s.DB.CatchLogMaxWeight(ctx, uid, f.Species)
	r.isRecord = r.prevBest <= 0 || f.WeightG > r.prevBest
	// 经验入账（累计模型 + 单调）
	r.newLevel, r.totalXp, r.levelsGained = s.grantXpMonotonic(ctx, uid, r.totalXP)
	// spinner 饵提升纺车熟练度，其余提升浮钓熟练度，每次 +0.2。
	if len(bait) >= 7 && bait[:7] == "spinner" {
		r.skillID = 2
	}
	_ = s.DB.AddSkillPoints(ctx, uid, r.skillID, 0.2)
	// catch_log 在入护时写入一次；释放鱼不进入统计和纪录。
	f.XP = r.totalXP
	f.Silver = r.silver
	// 同步钓获磨损到列、fields_json 和版本戳。
	s.wearRigForCatchPersisted(ctx, stGearOf(s, uid))
	// 结算日志：鱼种、重量、档位、经验分解与等级。
	s.log.Info("14/4 结算",
		"鱼种", f.Species, "重量", f.WeightG, "grade", f.Grade,
		"基础经验", r.baseXP, "总经验", r.totalXP,
		"新纪录", r.isRecord, "累计", r.totalXp, "等级", r.newLevel)
	return r
}

// 磨损槽位使用 item category，而不是客户端序号。鱼线与前导按承力线计算，
// 钩、拟饵和浮漂分别使用各自的磨损率。
var persistedSlotWearKey = map[int64]string{
	catLine:  "line",
	catLead:  "line",
	catHook:  "hook",
	catLure:  "lure",
	catBait:  "bait",
	catFloat: "float",
}

func (s *Server) wearRigForCatchPersisted(ctx context.Context, rigGUID string) {
	if rigGUID == "" || s.DB == nil {
		return
	}
	applyWear := func(row *store.Item, key string, amount float64, slot, ordinal int64) {
		if row == nil || amount <= 0 {
			return
		}
		before := row.Durability
		newD := math.Max(0, before-amount)
		if math.Abs(newD-before) < 1e-9 {
			return
		}
		if err := s.persistItemDurability(ctx, row, newD); err != nil {
			s.log.Warn("钓获磨损写库失败", "guid", shortGUID(row.GUID), "err", err)
			return
		}
		detail := damageDetailJSON(map[string]any{"slot": slot, "ordinal": ordinal, "category": key})
		if err := s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
			UID: row.UID, ItemGUID: row.GUID, ItemID: row.ItemID, Event: damageEventWear,
			BeforeDurability: ptrFloat64(before), AfterDurability: ptrFloat64(newD), DetailJSON: detail,
		}); err != nil {
			s.log.Warn("钓获磨损历史写库失败", "guid", shortGUID(row.GUID), "err", err)
		}
		s.log.Info("钓获磨损", "guid", shortGUID(row.GUID), "slot", slot,
			"ordinal", ordinal, "类别", key, "耐久", fmt.Sprintf("%.4f->%.4f", before, newD))
	}

	comps, err := s.DB.ListRigComponents(ctx, rigGUID)
	if err == nil {
		seen := map[string]bool{}
		for _, c := range comps {
			if c.GUID == "" || seen[c.GUID] {
				continue
			}
			seen[c.GUID] = true
			key, ok := persistedSlotWearKey[c.Slot]
			if !ok {
				key = "default"
			}
			row, rerr := s.DB.GetItemByGUID(ctx, c.GUID)
			if rerr != nil || row == nil {
				continue
			}
			applyWear(row, key, wearPerCatch[key], c.Slot, c.Ordinal)
		}
	}
	// 竿本体不在 rig_component 中，单独施加结构磨损。
	if rod, rerr := s.DB.GetItemByGUID(ctx, rigGUID); rerr == nil && rod != nil {
		applyWear(rod, "rod", wearPerCatch["rod"], gearCatRod, 0)
	}
}

// persistItemDurability 同步 item.durability、fields_json 与版本戳；
// 只更新列会在后续物品快照中被字段值覆盖。
func (s *Server) persistItemDurability(ctx context.Context, row *store.Item, durability float64) error {
	if durability < 0 {
		durability = 0
	}
	if durability > 1 {
		durability = 1
	}
	fields, err := FieldsFromJSON(row.FieldsJSON)
	if err != nil {
		return err
	}
	idx := fieldIdxByCS(row.WireClass, csDurability)
	if idx >= 0 {
		if !setWireFloat(fields, row.WireClass, idx, durability) {
			return fmt.Errorf("耐久字段写入失败: class=%s idx=%d", row.WireClass, idx)
		}
	}
	bumpCreatedTicks(fields, row.WireClass)
	fj, err := FieldsToJSON(fields)
	if err != nil {
		return err
	}
	if err := s.DB.UpdateItemFields(ctx, row.GUID, fj, row.Qty, durability, row.Workmanship, row.RigGUID); err != nil {
		return err
	}
	row.FieldsJSON = fj
	row.Durability = durability
	return nil
}

// stLocOf、stSpotOf、stGearOf 分别读取玩家位置、钓点和手持钓组。
func stLocOf(s *Server, uid int64) string {
	st := s.stateFor(uid)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.Loc
}

func stSpotOf(s *Server, uid int64) int64 {
	st := s.stateFor(uid)
	st.mu.Lock()
	defer st.mu.Unlock()
	return int64(st.Spot)
}

func stGearOf(s *Server, uid int64) string {
	st := s.stateFor(uid)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.GearGuid
}

// speciesFightStatics 保存 813 结算使用的鱼种搏斗静态值；
// 第五项单独作为随机缩放的基础值。
var speciesFightStatics = map[string][6]int64{
	"crucian": {300, 1900, 3200, 3998, 3996, 3954},
	"c.roach": {220, 1200, 2000, 2498, 2100, 2473},
	"perch":   {280, 1600, 2700, 3477, 3000, 3312},
	"s.bream": {140, 800, 1200, 1396, 1296, 1205},
}

var speciesFightStaticsDefault = [6]int64{280, 1600, 2700, 3477, 3000, 3312}

// settleStatics 返回 813 结算块使用的鱼种静态组。
//
// 已知鱼种沿用内置值；其余鱼种按 species_weight 的奖杯线/蓝冠线推导，
// 避免回落到默认组（鲈鱼）导致结算界面把别的鱼的重量标准显示出来。
func (s *Server) settleStatics(ctx context.Context, species string) [6]int64 {
	// 调试开关：settle_scale_debug>=1 时对所有鱼种发固定假值，
	// 用于判断客户端结算比例尺是否读取服务端 813 的 f0..f3。
	if s.tunableF("settle_scale_debug", 0) >= 1 {
		return [6]int64{111, 222, 333, 444, 555, 666}
	}
	if st, ok := speciesFightStatics[species]; ok {
		return st
	}
	f1, f2 := int64(1600), int64(2700)
	if s.DB != nil {
		_, tro, sup := s.DB.GradeLines(ctx, species)
		if tro != nil && *tro > 0 {
			f1 = *tro
		}
		if sup != nil && *sup > 0 {
			f2 = *sup
		}
	}
	if f2 <= f1 {
		f2 = f1 * 5 / 3
	}
	// 达标线优先用 Wiki 的 Record（绿旗）实测值；没有数据时回退到内置表，
	// 最后才按奖杯线比例估算（仅用于缺少公开数据的鱼种）。
	f0 := int64(0)
	if v := fishRecordWeight(species); v > 0 {
		f0 = v
	} else if st, ok := speciesFightStatics[species]; ok {
		f0 = st[0]
	} else {
		f0 = int64(math.Round(float64(f1) * 0.173))
	}
	f3 := int64(math.Round(float64(f2) * 1.25))
	f4 := int64(math.Round(float64(f2) * 1.1))
	f5 := int64(math.Round(float64(f2) * 1.2))
	return [6]int64{f0, f1, f2, f3, f4, f5}
}

// buildSettle813 构造 813 搏斗统计块：f8 是 310 经验块，f9 是 25 鱼条目。
// 310.f1/f5 保存本次升级数，310.f6 保存加成分解，813.f4 由第五项静态值随机缩放。
func (s *Server) buildSettle813(ctx context.Context, uid int64, f *RolledFish, r *settleXpResult) *proto.Value {
	statics := s.settleStatics(ctx, f.Species)
	f4 := int64(float64(statics[4]) * (0.9 + rand.Float64()*0.2)) // 向下取整
	// 813.f7 是该鱼银价（分），由单位重量价格计算。
	_, silverRate := 0.4, 0.0
	if rate, ok := valueRates[vgKey{f.Species, f.Grade}]; ok {
		silverRate = rate[1]
	} else if f.Grade >= 2 {
		silverRate = 0.1
	}
	silverCents := int64(math.Round(silverRate * math.Max(1, float64(f.WeightG))))

	// 310 经验块：f0=单条鱼经验，f2=钓法大类，f3=熟练度增量，
	// f1=本次升级数，f5=同义字段（历史实现也写升级数），f6=加成分解列表。
	//
	// f1 绝不能再塞"个人纪录"标志：客户端把 f1 当升级数，
	// 只要非 0 就播一次「新级别」并连带弹出新水域解锁。
	// 入护纪录只走 321.f2（蓝冠统计）与 catch_log，不进 310。
	obj310 := wiregen.MustMakeObj("kegeppimjbm", map[int]*proto.Value{
		0: proto.U64(uint64(r.totalXP)),
		1: proto.U32(uint32(max64(0, r.levelsGained))),
		2: proto.U32(uint32(r.skillID)),
		3: proto.F32(0.2),
		4: proto.Null(),
		5: proto.U32(uint32(max64(0, r.levelsGained))),
		6: s.buildBonusList(ctx, uid),
	})
	keep25 := s.buildPending25(f, s.stateFor(uid), s.currentLevel(uid))
	// ndiejnnioni.f0..f6 必须按 U32 克重编码；写成 F32 位型会导致客户端显示错误。
	return wiregen.MustMakeObj("ndiejnnioni", map[int]*proto.Value{
		0: proto.U32(uint32(statics[0])),
		1: proto.U32(uint32(statics[1])),
		2: proto.U32(uint32(statics[2])),
		3: proto.U32(uint32(statics[3])),
		4: proto.U32(uint32(f4)),
		5: proto.U32(uint32(statics[5])),
		6: proto.U32(uint32(r.prevBest)),
		7: proto.U64(uint64(silverCents)),
		8: obj310,
		9: keep25,
	})
}

// fishingEndSettle 处理 14/4 收竿结算；空竿或未刺中时返回空结算 801。
func (s *Server) fishingEndSettle(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	st := s.stateFor(uid)
	st.mu.Lock()
	if st.biteTimer != nil {
		st.biteTimer.Stop()
	}
	st.CastActive = false
	pending := st.Pending
	hooked := st.Hooked
	bait := st.Bait
	rigType := st.RigType
	st.mu.Unlock()

	cctx := ctx.Sess.pushCtx()
	var stats813 *proto.Value
	var profileTree *proto.Value
	var skill312 *proto.Value
	hasFish := pending != nil && hooked

	if pending != nil && !hooked {
		// 鱼已咬钩但玩家未刺中，记录一次逃脱。
		s.bumpFishingCounter(uid, "escapes")
	}
	var res *settleXpResult
	if hasFish {
		res = s.settleCatchXP(cctx, uid, pending, bait)
		stats813 = s.buildSettle813(cctx, uid, pending, res)
		profileTree, _ = s.BuildProfile307(cctx, uid)
		skill312 = s.buildSingleSkill312(cctx, uid, res.skillID)
		st.mu.Lock()
		st.LastSkill = res.skillID
		st.mu.Unlock()
	}

	// 801 即使空竿也返回完整对象，f0/f6/f7 为 null。
	f0 := proto.Null()
	if hasFish {
		f0 = proto.Args(proto.S("174"), stats813)
	}
	f6 := proto.Null()
	if hasFish && skill312 != nil {
		f6 = proto.Args(proto.S("121"), skill312)
	}
	f7 := proto.Null()
	if hasFish && profileTree != nil {
		f7 = profileTree
	}
	obj801, err := wiregen.MakeObj("caagocpofin", map[int]*proto.Value{
		0:  f0,
		1:  proto.U32(castBaseDistanceBits(rigType)),
		2:  proto.U32(f32Const801F2),
		3:  build5003(),
		4:  proto.Null(),
		5:  proto.Null(),
		6:  f6,
		7:  f7,
		8:  proto.Null(),
		9:  proto.Args(proto.S("61")),
		10: proto.Null(),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj801, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}

	// 结算后推送升级和最新档案；多级连升只推送一次。
	if hasFish && res != nil && res.levelsGained > 0 {
		ctx.Sess.Push2_6(max64(1, res.newLevel-1))
	}
	if hasFish {
		ctx.Sess.Push2_1()
	}
	return body, true
}
