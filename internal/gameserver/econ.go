package gameserver

import (
	"context"
	"math"
	"math/rand"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 经济结算、渔获价值和商店逻辑。

// valueRates 按鱼种和档位保存经验与银币速率。
type vgKey struct {
	species string
	grade   int64
}

var valueRates = map[vgKey][2]float64{
	{"crucian", 1}: {0.1200, 0}, {"crucian", 2}: {0.1710, 0.0563},
	{"c.roach", 1}: {0.1856, 0}, {"c.roach", 2}: {0.2990, 0.0990}, {"c.roach", 3}: {1.4996, 0.7495},
	{"perch", 1}: {0.1570, 0}, {"perch", 2}: {0.3569, 0.1176},
	{"s.bream", 1}: {0.3556, 0}, {"s.bream", 2}: {0.6247, 0.2065},
	{"c.carp", 2}:    {0.1770, 0.0574},
	{"a.sleeper", 1}: {0.7780, 0}, {"a.sleeper", 2}: {1.8710, 0.6190},
}

// catchValue 计算渔获经验和展示用银价。
// 银价只用于结算界面和 catch_log，不直接增加余额；银币在鱼市 20/2 卖鱼时入账。
func catchValue(species string, grade, weightG int64) (xp, silver int64) {
	rate, ok := valueRates[vgKey{species, grade}]
	xpRate, silverRate := 0.4, 0.0
	if ok {
		xpRate, silverRate = rate[0], rate[1]
	} else if grade >= 2 {
		silverRate = 0.1
	}
	w := weightG
	if w < 1 {
		w = 1
	}
	xp = int64(math.Max(1, math.Round(xpRate*float64(w))))
	silver = int64(math.Max(0, math.Round(silverRate*float64(w)/silverRateSeedDivisor)))
	return
}

// grantXp 增加经验并重算等级，返回新等级、累计经验和提升级数。
func (s *Server) grantXp(ctx context.Context, uid, xp int64) (int64, int64, int64) {
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return 1, 0, 0
	}
	oldLevel := p.Level
	newXp := p.XP + xp
	newLevel := levelForXp(newXp)
	_, _ = s.DB.SQL.ExecContext(ctx, `UPDATE profile SET xp=?, level=?, updated_ts=? WHERE uid=?`,
		newXp, newLevel, float64(time.Now().Unix()), uid)
	_ = s.DB.AddTxn(ctx, uid, "xp", "", "", 0, 0, "fishing")
	return newLevel, newXp, newLevel - oldLevel
}

// fishingEnd 14/4 收竿结算。
func (s *Server) fishingEnd(ctx *Ctx) ([]byte, bool) {
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
	rigType := st.RigType
	st.mu.Unlock()

	cctx := ctx.Sess.pushCtx()
	var stats813 *proto.Value
	var profileTree *proto.Value
	var skill312 *proto.Value
	hasFish := pending != nil && hooked

	levelsGained := int64(0)
	prevLevel := int64(1)
	if hasFish {
		// 14/4 只结算经验；银币由鱼市 20/2 卖鱼入账。
		xp, silver := catchValue(pending.Species, pending.Grade, pending.WeightG)
		bonusXp := s.tunableF("xp_gain", 1.0)
		xp = int64(math.Round(float64(xp) * bonusXp))
		prevBest, _ := s.DB.CatchLogMaxWeight(cctx, uid, pending.Species)
		newLevel, _, gained := s.grantXp(cctx, uid, xp)
		levelsGained = gained
		prevLevel = max64(1, newLevel-1)
		// 熟练度：bait 以 spinner 开头 → 2（纺车）否则 1（浮钓）；+0.2
		skillID := int64(1)
		if stBait := st.Bait; stBait != "" && len(stBait) >= 7 && stBait[:7] == "spinner" {
			skillID = 2
		}
		_ = s.DB.AddSkillPoints(cctx, uid, skillID, 0.2)
		// catch_log 只在 14/5 入护时写入；14/4 放生的鱼不进入统计和纪录。
		pending.XP = xp
		pending.Silver = silver
		// 对本次钓组组件施加钓获磨损。
		s.wearRigForCatch(cctx, st.GearGuid)
		st.mu.Lock()
		st.LastSkill = skillID
		st.mu.Unlock()

		stats813 = s.build813(cctx, uid, pending, prevBest)
		profileTree, _ = s.BuildProfile307(cctx, uid)
		skill312 = s.buildSingleSkill312(cctx, uid, skillID)
	}

	// 801（空竿也回完整 801，f0/f6/f7 = null）
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

	// 结算后主动推送：升级 → 2/6；然后 2/1
	if hasFish && levelsGained > 0 {
		ctx.Sess.Push2_6(prevLevel)
	}
	if hasFish {
		ctx.Sess.Push2_1()
	}
	return body, true
}

// build813 搏鱼统计块（813 内嵌 310 + 25）。
func (s *Server) build813(ctx context.Context, uid int64, f *RolledFish, prevBest int64) *proto.Value {
	// SPECIES_FIGHT_STATICS 鱼种静态组（未知鱼种按 species_weight 推导）
	st := s.settleStatics(ctx, f.Species)
	f0, f1, f2, f3, f5 := float64(st[0]), float64(st[1]), float64(st[2]), float64(st[3]), float64(st[5])
	f4 := f3 * (0.9 + rand.Float64()*0.2)
	// 银价（分）= round(silverRate × max(1,w))，用未除数速率
	_, silverRate := 0.4, 0.0
	if rate, ok := valueRates[vgKey{f.Species, f.Grade}]; ok {
		silverRate = rate[1]
	} else if f.Grade >= 2 {
		silverRate = 0.1
	}
	silverCents := int64(math.Round(silverRate * math.Max(1, float64(f.WeightG))))

	obj310 := wiregen.MustMakeObj("kegeppimjbm", map[int]*proto.Value{
		0: proto.U64(uint64(f.XP)), // 单鱼经验（不是累计）
		1: proto.U32(personalRecordFlag(prevBest <= 0 || f.WeightG > prevBest)),
		2: proto.U32(uint32(stLastSkill(s, uid))),
		3: proto.F32(0.2),
		4: proto.Null(),
		5: proto.U32(0), // levelsGained 由 2/6 承担
		6: proto.Args(proto.S("31")),
	})
	keep25 := s.buildPending25(f, s.stateFor(uid), s.currentLevel(uid))
	// ndiejnnioni.f0..f6 是客户端按 U32 克重读取的整数，不能写 proto.F32；
	// 2000.0f 的位型 1157234688 会在鱼卡尺显示成“1157235 公斤”。
	return wiregen.MustMakeObj("ndiejnnioni", map[int]*proto.Value{
		0: proto.U32(uint32(f0)),
		1: proto.U32(uint32(f1)),
		2: proto.U32(uint32(f2)),
		3: proto.U32(uint32(f3)),
		4: proto.U32(uint32(f4)),
		5: proto.U32(uint32(f5)),
		6: proto.U32(uint32(prevBest)),
		7: proto.U64(uint64(silverCents)),
		8: obj310,
		9: keep25,
	})
}

func stLastSkill(s *Server, uid int64) int64 {
	st := s.stateFor(uid)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.LastSkill == 0 {
		return 1
	}
	return st.LastSkill
}

// buildSingleSkill312 单大类 312（801.f6 用）。
func (s *Server) buildSingleSkill312(ctx context.Context, uid, catID int64) *proto.Value {
	points, _ := s.DB.ListSkills(ctx, uid)
	nodes, _ := s.DB.ListSkillNodes(ctx, uid)
	catNodes := proto.Args(proto.S("122"))
	for _, nd := range s.skillDefsOfCat(catID) {
		catNodes.Items = append(catNodes.Items,
			wiregen.MustMakeObj("bmdafhbpfci", map[int]*proto.Value{
				0: proto.U32(uint32(nd.NodeID)),
				1: proto.U32(f32bitsOf(nd.UnlockPct)),
				2: proto.U32(uint32(nd.MaxLevel)),
				3: proto.U32(uint32(nodes[nd.NodeID])),
				4: proto.U32(f32bitsOf(nd.EffectPct)),
				5: proto.U32(uint32(nd.GroupID)),
				6: proto.U32(boolU32(nodeUnlocked(nd, points[catID], 0))),
			}))
	}
	return wiregen.MustMakeObj("kmepameiibi", map[int]*proto.Value{
		0: proto.U32(uint32(catID)),
		1: proto.F32(float32(points[catID])),
		2: proto.F32(0.0),
		3: catNodes,
	})
}

// build5003 5012 赛季块（801.f3）。
func build5003() *proto.Value {
	return wiregen.MustMakeObj("fopkeoppndn", map[int]*proto.Value{
		0: build5012(),
		1: proto.Null(),
		2: proto.Null(),
	})
}

// buildPending25 由 RolledFish 构造 25 条目（14/5 回包与 813.f9 共用）。
func (s *Server) buildPending25(f *RolledFish, st *FishState, levelKey string) *proto.Value {
	if f.LocLevel != "" {
		levelKey = f.LocLevel
	}
	e := &store.KeepnetEntry{
		EchoGUID: f.FishGUID,
		Species:  f.Species,
		NameZh:   f.Name,
		WeightG:  f.WeightG,
		LengthM:  f.LengthM,
		Cat:      f.Grade,
		KeepBait: st.Bait,
		TS:       float64(time.Now().UnixNano()) / 1e9,
	}
	return buildKeepnet25(e, levelKey)
}

// fishingKeep 14/5 入护。
func (s *Server) fishingKeep(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	guids := deepGUIDs(ctx.Tree)
	want := ""
	if len(guids) >= 2 {
		want = guids[1]
	} else if len(guids) == 1 {
		want = guids[0]
	}
	cctx := ctx.Sess.pushCtx()
	st := s.stateFor(uid)

	st.mu.Lock()
	pending := st.Pending
	var echo string
	if pending != nil {
		echo = want
		if echo == "" {
			echo = pending.FishGUID
		}
		kept := *pending
		kept.FishGUID = echo
		if kept.LocLevel == "" {
			kept.LocLevel = s.currentLevel(uid)
		}
		keepLevel := kept.LocLevel
		// 落库
		err := s.DB.KeepnetInsert(cctx, &store.KeepnetEntry{
			EchoGUID: echo, UID: uid, Species: kept.Species, NameZh: kept.Name,
			WeightG: kept.WeightG, LengthM: kept.LengthM, Cat: kept.Grade,
			KeepLoc: st.Loc, KeepSpot: int64(st.Spot), KeepBait: st.Bait,
			KeepLevel: keepLevel, TS: float64(time.Now().UnixNano()) / 1e9,
		})
		if err == nil {
			_ = s.DB.CatchLogInsert(cctx, uid, st.Loc, int64(st.Spot), kept.Species, "",
				kept.WeightG, kept.LengthM, "keep", st.Bait, kept.Grade, kept.XP, kept.Silver, kept.LocLevel)
			st.Pending = nil
			st.Keepnet = append(st.Keepnet, &kept)
			pending = &kept
		}
		st.mu.Unlock()
		f25 := s.buildPending25(&kept, st, keepLevel)
		body, err2 := proto.EncodeValue(build800(proto.Args(proto.S("61"), f25), proto.Null(), proto.Null()), ctx.Sess.Table())
		if err2 != nil {
			return nil, false
		}
		return body, true
	}
	st.mu.Unlock()

	// 无 Pending：查已入护（重复入护）→ 回该鱼快照；否则护网全量
	if want != "" {
		if e, err := s.DB.KeepnetGet(cctx, uid, want); err == nil && e != nil {
			level := e.KeepLevel
			if level == "" {
				level = s.currentLevel(uid)
			}
			f25 := buildKeepnet25(e, level)
			body, err2 := proto.EncodeValue(build800(proto.Args(proto.S("61"), f25), proto.Null(), proto.Null()), ctx.Sess.Table())
			if err2 != nil {
				return nil, false
			}
			return body, true
		}
	}
	// 护网全量快照
	out := proto.Args(proto.S("61"))
	if list, err := s.DB.KeepnetList(cctx, uid); err == nil {
		for _, e := range list {
			level := e.KeepLevel
			if level == "" {
				level = s.currentLevel(uid)
			}
			out.Items = append(out.Items, buildKeepnet25(e, level))
		}
	}
	body, err := proto.EncodeValue(build800(out, proto.Null(), proto.Null()), ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}

// fishingRelease 处理 14/6 放生，同时处理蓝冠鱼命名。
// 请求形状为 args=[guid, guid, str]，最后一个字符串是鱼名。
func (s *Server) fishingRelease(ctx *Ctx) ([]byte, bool) {
	if !s.requireDB(ctx) {
		return nil, false
	}
	uid := ctx.Sess.Userid
	st := s.stateFor(uid)
	st.mu.Lock()
	pending := st.Pending
	st.Pending = nil
	st.mu.Unlock()

	if pending != nil {
		cctx := ctx.Sess.pushCtx()
		// 只有蓝冠鱼可以命名，已命名的鱼不能改名。
		// 命名条件不满足时按普通放生处理，并记录日志。
		if name := releaseFishName(ctx.Tree); name != "" {
			fid := pending.NamedID
			if fid == "" {
				fid = pending.FishGUID
			}
			if ok, why := canNameFish(cctx, s.DB, pending.Grade, fid); ok {
				if ok2, why2 := s.insertNamedFish(cctx, uid, pending, fid, name, st.Loc); ok2 {
					pending.Name = strings.TrimSpace(name)
					pending.NamedID = fid
					ctx.Log.Info("蓝冠命名成功", "鱼种", pending.Species, "重量", pending.WeightG, "名字", pending.Name)
				} else {
					ctx.Log.Warn("蓝冠命名被拒", "原因", why2, "鱼种", pending.Species, "名字", name)
				}
			} else {
				ctx.Log.Warn("蓝冠命名被拒", "原因", why, "鱼种", pending.Species, "名字", name)
			}
		}
		// 被命名鱼又被钓到/放生一次 → 计数 +1（note_recatch；含刚命名的这条）
		if pending.NamedID != "" {
			s.DB.NamedFishNoteRecatch(cctx, pending.NamedID)
		}
		// 放生奖励：base × release_xp_ratio（默认 0.5）
		base := pending.XP
		if base == 0 {
			b, _ := catchValue(pending.Species, pending.Grade, pending.WeightG)
			base = b
		}
		ratio := s.tunableF("release_xp_ratio", 0.5)
		bonus := int64(math.Round(float64(base) * ratio))
		if bonus > 0 {
			newLevel, _, gained := s.grantXp(cctx, uid, bonus)
			ctx.Sess.Push2_9(100, uint64(bonus))
			ctx.Sess.Push2_1()
			if gained > 0 {
				ctx.Sess.Push2_6(max64(1, newLevel-1))
			}
		}
	}
	// 响应 807：{null, null, 307, null, null, null}
	profileTree, err := s.BuildProfile307(ctx.Sess.pushCtx(), uid)
	if err != nil {
		profileTree = proto.Null()
	}
	obj807, err := wiregen.MakeObj("gnilpafigmn", map[int]*proto.Value{
		0: proto.Null(),
		1: proto.Null(),
		2: profileTree,
		3: proto.Null(),
		4: proto.Null(),
		5: proto.Null(),
	})
	if err != nil {
		return nil, false
	}
	body, err := proto.EncodeValue(obj807, ctx.Sess.Table())
	if err != nil {
		return nil, false
	}
	return body, true
}
