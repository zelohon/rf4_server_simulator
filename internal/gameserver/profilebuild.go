package gameserver

import (
	"context"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 档案、技能树与推送树构造。

// 通行证与赛季使用的固定时间常量。
const (
	pass745F2     = uint64(131765184000000000)
	season5013F1  = uint64(134238816000000000)
	season5013F2  = uint64(134265924000000000)
	f32Const801F2 = uint32(1092571843) // 801.f2 位型
	castDepthBits = uint32(1091032928) // 158.f2 = f32 8.4900818
	profileFlags  = uint32(17 | 0x100) // 0x100：跳过绑定邮箱提示
)

// BuildPose308 玩家姿态块（2/2 与 307.f12 共用；f7=世界时钟 FILETIME）。
func BuildPose308() *proto.Value {
	return wiregen.MustMakeObj("pnbphpdmppe", map[int]*proto.Value{
		0: proto.F32(0.85), 1: proto.F32(0.9946), 2: proto.F32(0.0),
		3: proto.F32(1.0), 4: proto.F32(0.0), 5: proto.F32(11.375), 6: proto.F32(0.0),
		7: proto.U64(uint64(proto.WindowsFiletimeNow())),
		8: proto.U32(0),
	})
}

// build315 构造 322.f4 使用的统计子块。
func build315() *proto.Value {
	return wiregen.MustMakeObj("ifoifbocoom", map[int]*proto.Value{
		0: proto.F32(0.0),
		1: proto.U32(4),
		2: proto.StrVal(""),
		3: proto.U32(2),
		4: proto.StrVal(""),
	})
}

// BuildStats322 钓鱼统计块（f0=最大渔获 g）。
func BuildStats322(ctx context.Context, db *store.DB, uid int64) *proto.Value {
	maxW := int64(0)
	if db != nil {
		// 简化：MAX(catch_log.weight_g)
		_ = db.SQL.QueryRowContext(ctx, `SELECT COALESCE(MAX(weight_g),0) FROM catch_log WHERE uid=?`, uid).Scan(&maxW)
	}
	return wiregen.MustMakeObj("kkmhfbeldpg", map[int]*proto.Value{
		0: proto.U32(uint32(maxW)),
		4: build315(),
	})
}

// build745 构造通行证状态块。
func build745() *proto.Value {
	return wiregen.MustMakeObj("dnijhkbdfan", map[int]*proto.Value{
		0: proto.U32(0),
		1: proto.U32(0),
		2: proto.U64(pass745F2),
	})
}

// build5013 构造赛季起止时段。
func build5013() *proto.Value {
	return wiregen.MustMakeObj("nkakjeigjla", map[int]*proto.Value{
		0: proto.U32(6),
		1: proto.U64(season5013F1),
		2: proto.U64(season5013F2),
		3: proto.U32(0),
		4: proto.U64(0),
		5: proto.Null(),
	})
}

// build5012 构造包含 5013 的赛季容器。
func build5012() *proto.Value {
	return wiregen.MustMakeObj("fkcmcjlagdh", map[int]*proto.Value{
		0: proto.Args(proto.S("210"), build5013()),
	})
}

// BuildProfile307 全量本人档案（3/1 响应、801.f7、2/1 推送共用）。
func (s *Server) BuildProfile307(ctx context.Context, uid int64) (*proto.Value, error) {
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return nil, err
	}
	places, _ := s.DB.PlaceAccessList(ctx, uid)
	placeItems := make([]*proto.Value, 0, len(places))
	for _, pl := range places {
		placeItems = append(placeItems, proto.BytesOf([]byte(pl)))
	}

	// 会员：以「到期时间」为唯一真值，避免过期后仍显示已激活。
	nowUnix := time.Now().Unix()
	memActive := p.Membership == 1 && p.MembershipExpireAt > nowUnix
	var membershipExp uint64
	memFlag := uint32(0)
	if memActive {
		memFlag = 1
		membershipExp = uint64(proto.UnixToFiletime(p.MembershipExpireAt))
	}
	totalPts := skillPointsTotal(p.Level)

	// 322 里的等级经验拆分
	base := baseXp(p.Level)
	var inLevel uint64
	if p.XP > base {
		inLevel = uint64(p.XP - base)
	}
	// 5012 赛季块
	season := wiregen.MustMakeObj("fkcmcjlagdh", map[int]*proto.Value{
		0: proto.Args(proto.S("210"), build5013()),
	})

	return wiregen.MakeObj("ghfbofgdflm", map[int]*proto.Value{
		0:  proto.U32(uint32(p.UID)),
		1:  proto.StrVal(p.Name),
		2:  proto.U32(uint32(p.Level)),
		3:  proto.U64(uint64(p.XP)),
		4:  proto.U64(inLevel),
		5:  proto.U64(uint64(nextLevelReq(p.Level))),
		6:  proto.U32(uint32(totalPts)),
		7:  proto.U64(uint64(p.Silver)),
		8:  proto.U64(uint64(p.Gold)),
		9:  proto.StrVal(""),
		10: proto.U32(memFlag),
		11: proto.U64(membershipExp),
		12: BuildPose308(),
		13: BuildStats322(ctx, s.DB, p.UID),
		14: proto.U32(0),
		15: proto.U32(profileFlags),
		16: proto.U32(1),
		17: proto.StrVal(p.Email),
		18: proto.U32(uint32(p.Role)),
		19: proto.U64(uint64(p.Bank)),
		20: proto.ArrayOf(nil),
		21: proto.Str(nil),
		22: proto.ArrayOf(hexOf(placeItems)),
		23: proto.Args(proto.S("181")),
		24: build745(),
		25: season,
	})
}

// hexOf 把 bytes 树转 hex 列表（ArrayOf 用）。
func hexOf(vals []*proto.Value) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, v.Hex)
	}
	return out
}

// ---- 技能树 ----

// skillCatOrder 是技能树大类的显示顺序。
var skillCatOrder = []int64{1, 2, 3, 10, 11, 4, 5, 6, 7, 8, 9}

// profCap 熟练度合成上限。
const profCap = 100.0

// nodeUnlocked 计算技能节点在 313.f6 中的解锁状态。
// 客户端直接显示该值，因此服务端必须按当前熟练度判断；显式 enabled 的节点始终可用。
func nodeUnlocked(nd skillNodeDef, prof, gear float64) bool {
	if nd.Enabled {
		return true
	}
	total := gear + prof
	if total > profCap {
		total = profCap
	}
	return total+1e-6 >= float64(nd.UnlockPct)
}

// BuildSkillTree311 技能树（5/1 响应；2/6 推送复用，f0 由调用方覆写）。
func (s *Server) BuildSkillTree311(ctx context.Context, uid int64, availablePointsOverride uint64) (*proto.Value, error) {
	p, err := s.DB.EnsureProfile(ctx, uid)
	if err != nil {
		return nil, err
	}
	points, _ := s.DB.ListSkills(ctx, uid)
	nodes, _ := s.DB.ListSkillNodes(ctx, uid)

	total := skillPointsTotal(p.Level) + s.skillBonusPoints(ctx, uid)
	var used int64
	for _, lv := range nodes {
		used += lv
	}
	avail := uint64(0)
	if total > used {
		avail = uint64(total - used)
	}
	if availablePointsOverride != ^uint64(0) {
		avail = availablePointsOverride
	}

	cats := proto.Args(proto.S("121"))
	for _, catID := range skillCatOrder {
		mastery := float32(points[catID]) // f32 位由 F32 承载
		prof := points[catID]
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
					6: proto.U32(boolU32(nodeUnlocked(nd, prof, 0))),
				}))
		}
		cats.Items = append(cats.Items,
			wiregen.MustMakeObj("kmepameiibi", map[int]*proto.Value{
				0: proto.U32(uint32(catID)),
				1: proto.F32(mastery),
				2: proto.F32(0.0),
				3: catNodes,
			}))
	}
	return wiregen.MakeObj("djndmghegld", map[int]*proto.Value{
		0: proto.U32(uint32(avail)),
		1: cats,
		2: proto.U64(500),
		3: proto.Null(),
	})
}

// skillNodeDef 技能节点定义（PG skill_def）。
type skillNodeDef struct {
	NodeID    int64
	CatID     int64
	UnlockPct float32
	MaxLevel  int64
	EffectPct float32
	GroupID   int64
	Enabled   bool
}

// skillDefsOfCat 从 skill_def 读取大类节点并缓存。
func (s *Server) skillDefsOfCat(catID int64) []skillNodeDef {
	s.skillDefsOnce.Do(func() {
		rows, err := s.DB.SQL.QueryContext(context.Background(), `
			SELECT cat_id, node_id, unlock_pct, max_level, effect_pct, group_id, enabled
		FROM skill_def ORDER BY cat_id, ordinal, node_id`)
		if err != nil {
			return
		}
		defer rows.Close()
		m := map[int64][]skillNodeDef{}
		for rows.Next() {
			var d skillNodeDef
			var enabled int64
			if err := rows.Scan(&d.CatID, &d.NodeID, &d.UnlockPct, &d.MaxLevel, &d.EffectPct, &d.GroupID, &enabled); err != nil {
				continue
			}
			d.Enabled = enabled != 0
			m[d.CatID] = append(m[d.CatID], d)
		}
		s.skillDefs = m
	})
	return s.skillDefs[catID]
}

func f32bitsOf(v float32) uint32 { return proto.F32Bits(v) }

func boolU32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// ---- 快捷栏 136 ----

func build136(slot int64, guid string) *proto.Value {
	return wiregen.MustMakeObj("iiemhjlblcc", map[int]*proto.Value{
		0: proto.U32(uint32(slot)),
		1: proto.Guid(guid),
	})
}

// BuildShortcutList 11/1 快照：args("65")[136...]。
func (s *Server) BuildShortcutList(ctx context.Context, uid int64) *proto.Value {
	out := proto.Args(proto.S("65"))
	if slots, err := s.DB.ListShortcuts(ctx, uid); err == nil {
		for _, sc := range slots {
			out.Items = append(out.Items, build136(sc.Slot, sc.GUID))
		}
	}
	return out
}

// ---- 推送 ----

// Push2_1 推送当前档案，用于刷新货币与等级。
func (s *Session) Push2_1() {
	p, err := s.Srv.BuildProfile307(s.pushCtx(), s.Userid)
	if err != nil {
		s.log.Warn("2/1 构造失败", "err", err)
		return
	}
	s.Push(2, 1, proto.Args524(p))
}

// Push2_6 推送升级后的技能树，外层必须是 args(524)[311]。
// f0 表示当前可用技能点；客户端用前后差值显示新增点数。
// 多级连升只发送一次，fromLevel 仅用于日志。
func (s *Session) Push2_6(fromLevel int64) {
	tree, err := s.Srv.BuildSkillTree311(s.pushCtx(), s.Userid, ^uint64(0)) // f0=自然可用点
	if err != nil {
		s.log.Warn("2/6 构造失败", "err", err)
		return
	}
	s.Push(2, 6, proto.Args524(tree))
}

// Push2_9 通知推送（306；type=100 放生额外经验）。
func (s *Session) Push2_9(notifyType byte, extraXP uint64) {
	obj := wiregen.MustMakeObj("nkoicoaemen", map[int]*proto.Value{
		0: proto.U32(uint32(notifyType)),
		1: proto.U32(0),
		2: proto.U64(extraXP),
		3: proto.U32(0),
		4: proto.Str(nil),
	})
	s.Push(2, 9, proto.Args524(obj))
}

// Push4_21 物品移除推送：args("524")[guidarr]。
func (s *Session) Push4_21(guids []string) {
	s.Push(4, 21, proto.Args524(proto.GuidArr(guids)))
}

// Push4_22Full 搏鱼全量同步：args("61")[157竿, 140线]。
func (s *Session) Push4_22Full() {
	items, err := s.Srv.buildFightItems(s.pushCtx(), s.Userid, true)
	if err != nil || len(items) == 0 {
		return
	}
	s.Push(4, 22, proto.Args(proto.S("61"), items...))
}

// Push4_22Hook 刺中后单钩同步：args("61")[131钩]。
func (s *Session) Push4_22Hook() {
	items, err := s.Srv.buildFightItems(s.pushCtx(), s.Userid, false)
	if err != nil || len(items) == 0 {
		return
	}
	s.Push(4, 22, proto.Args(proto.S("61"), items...))
}

// pushCtx 推送上下文（超时保护）。
func (s *Session) pushCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ctx
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
