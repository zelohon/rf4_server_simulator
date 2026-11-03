package gameserver

import (
	"context"
	"sort"
	"strings"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// 321 统计快照（3/2）。所有值都从玩家 catch_log 生成，不使用静态模板。

type catchStatRow struct {
	TS       float64
	Loc      string
	Spot     int64
	FishKey  string
	WeightG  int64
	LengthM  float64
	Source   string
	Bait     string
	Grade    int64
	XP       int64
	Silver   int64
	LocLevel string
}

func (s *Server) loadCatchStatRows(ctx context.Context, uid int64) ([]catchStatRow, error) {
	rows, err := s.DB.SQL.QueryContext(ctx, `
		SELECT COALESCE(ts,0), COALESCE(loc,''), COALESCE(spot,0), COALESCE(fish_key,''),
		       COALESCE(weight_g,0), COALESCE(length_m,0), COALESCE(source,''), COALESCE(bait,''),
		       COALESCE(grade,0), COALESCE(xp,0), COALESCE(silver,0), COALESCE(loc_level,'')
		FROM catch_log WHERE uid=? ORDER BY id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catchStatRow
	for rows.Next() {
		var r catchStatRow
		if err := rows.Scan(&r.TS, &r.Loc, &r.Spot, &r.FishKey, &r.WeightG, &r.LengthM,
			&r.Source, &r.Bait, &r.Grade, &r.XP, &r.Silver, &r.LocLevel); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Server) playerCounter(ctx context.Context, uid int64, key string) int64 {
	var v int64
	_ = s.DB.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(value,0) FROM player_counters WHERE uid=? AND key=?`, uid, key).Scan(&v)
	return v
}

// buildStats321 构造 321 jahbacemfof。
func (s *Server) buildStats321(ctx context.Context, uid int64) (*proto.Value, error) {
	rows, err := s.loadCatchStatRows(ctx, uid)
	if err != nil {
		return nil, err
	}

	total := int64(len(rows))
	var rare, superRare int64
	dailyCount := map[string]int64{}
	dailyWeight := map[string]float64{}
	for _, r := range rows {
		if r.Grade >= 3 {
			rare++
		}
		if r.Grade >= 4 {
			superRare++
		}
		d := time.Unix(int64(r.TS), 0).In(time.Local).Format("2006-01-02")
		dailyCount[d]++
		dailyWeight[d] += float64(r.WeightG)
	}
	maxDailyCount, maxDailyWeightG := int64(0), 0.0
	for d, c := range dailyCount {
		if c > maxDailyCount {
			maxDailyCount = c
		}
		if dailyWeight[d] > maxDailyWeightG {
			maxDailyWeightG = dailyWeight[d]
		}
	}

	// 按诱饵和钓法聚合：路亚归入纺车 1/5/17，其余归入浮钓 3/1/27。
	baitCount := map[string]int64{}
	baitRare := map[string]int64{}
	baitOrder := make([]string, 0)
	rodCount := map[int64]int64{}
	rodRare := map[int64]int64{}
	rigCount := map[int64]int64{}
	rigRare := map[int64]int64{}
	for _, r := range rows {
		if r.Bait == "" {
			continue
		}
		if baitCount[r.Bait] == 0 {
			baitOrder = append(baitOrder, r.Bait)
		}
		baitCount[r.Bait]++
		rod, rig, _ := tackleOfBait(r.Bait)
		if r.Grade >= 3 {
			baitRare[r.Bait]++
			rodRare[rod]++
			rigRare[rig]++
		}
		rodCount[rod]++
		rigCount[rig]++
	}

	cards := buildRecordCards(rows)
	card := func(key string) *proto.Value {
		r, ok := cards[key]
		return recordCard320(r, ok)
	}
	biggest := card("max_float")
	if biggest == nil {
		biggest = card("max_spin")
	}

	baitKeys := append([]string(nil), baitOrder...)
	sort.SliceStable(baitKeys, func(i, j int) bool {
		return baitCount[baitKeys[i]] > baitCount[baitKeys[j]]
	})
	baitArgs := proto.Args(proto.S("128"))
	for _, b := range baitKeys {
		_, _, hook := tackleOfBait(b)
		baitArgs.Items = append(baitArgs.Items, wiregen.MustMakeObj("keoejejkenf", map[int]*proto.Value{
			0: proto.U32(uint32(hook)),
			1: proto.StrVal(b),
			2: proto.StrVal(""),
			3: proto.U32(uint32(baitCount[b])),
		}))
		_ = baitRare[b]
	}

	rodArgs := proto.Args(proto.S("125"))
	for _, cat := range sortedCountKeys(rodCount) {
		rodArgs.Items = append(rodArgs.Items, wiregen.MustMakeObj("gjndofnpibn", map[int]*proto.Value{
			0: proto.U32(uint32(cat)), 1: proto.U32(uint32(rodCount[cat])),
			2: proto.U32(uint32(rodRare[cat])), 3: proto.U32(0),
		}))
	}
	rigArgs := proto.Args(proto.S("126"))
	for _, cat := range sortedCountKeys(rigCount) {
		rigArgs.Items = append(rigArgs.Items, wiregen.MustMakeObj("emehdoalanl", map[int]*proto.Value{
			0: proto.U32(uint32(cat)), 1: proto.U32(uint32(rigCount[cat])),
			2: proto.U32(uint32(rigRare[cat])), 3: proto.U32(0),
		}))
	}

	return wiregen.MustMakeObj("jahbacemfof", map[int]*proto.Value{
		0:  proto.U32(uint32(total)),
		1:  proto.U32(uint32(rare)),
		2:  proto.U32(uint32(superRare)), // 蓝冠（grade>=4）
		3:  proto.U32(uint32(s.playerCounter(ctx, uid, "escapes"))),
		4:  proto.U32(uint32(maxDailyCount)),
		5:  proto.F32(float32(maxDailyWeightG / 1000.0)),
		6:  proto.Args(proto.S("129")),
		7:  biggest,
		8:  card("strongest"),
		9:  proto.Null(),
		10: card("max_silver"),
		11: proto.U64(0),
		12: card("max_xp"),
		13: proto.U64(0),
		14: card("max_float"),
		15: card("max_spin"),
		16: proto.Null(),
		17: rodArgs,
		18: rigArgs,
		19: proto.Null(),
		20: proto.Null(),
		21: baitArgs,
		22: proto.U32(0),
	}), nil
}

func tackleOfBait(bait string) (rod, rig, hook int64) {
	if strings.HasPrefix(bait, "spinner") {
		return 1, 5, 17
	}
	return 3, 1, 27
}

func sortedCountKeys(m map[int64]int64) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// buildRecordCards 只为 bait 非空的日志生成纪录卡。
func buildRecordCards(rows []catchStatRow) map[string]catchStatRow {
	out := map[string]catchStatRow{}
	var usable []catchStatRow
	for _, r := range rows {
		if r.Bait != "" {
			usable = append(usable, r)
		}
	}
	if len(usable) == 0 {
		return out
	}
	bySpecies := map[string][]catchStatRow{}
	speciesOrder := make([]string, 0)
	for _, r := range usable {
		if _, ok := bySpecies[r.FishKey]; !ok {
			speciesOrder = append(speciesOrder, r.FishKey)
		}
		bySpecies[r.FishKey] = append(bySpecies[r.FishKey], r)
	}
	var topSpecies []catchStatRow
	for _, key := range speciesOrder {
		rs := bySpecies[key]
		if len(rs) > len(topSpecies) {
			topSpecies = rs
		}
	}
	sort.SliceStable(topSpecies, func(i, j int) bool { return topSpecies[i].WeightG > topSpecies[j].WeightG })
	byWeight := append([]catchStatRow(nil), usable...)
	sort.SliceStable(byWeight, func(i, j int) bool { return byWeight[i].WeightG > byWeight[j].WeightG })
	byXP := append([]catchStatRow(nil), usable...)
	sort.SliceStable(byXP, func(i, j int) bool { return byXP[i].XP > byXP[j].XP })
	bySilver := append([]catchStatRow(nil), usable...)
	sort.SliceStable(bySilver, func(i, j int) bool { return bySilver[i].Silver > bySilver[j].Silver })
	var spinner []catchStatRow
	for _, r := range usable {
		if strings.HasPrefix(r.Bait, "spinner") {
			spinner = append(spinner, r)
		}
	}
	sort.SliceStable(spinner, func(i, j int) bool { return spinner[i].WeightG > spinner[j].WeightG })

	out["catch_count"] = topSpecies[0]
	out["max_float"] = byWeight[0]
	out["strongest"] = byWeight[0]
	out["max_xp"] = byXP[0]
	out["max_silver"] = bySilver[0]
	if len(spinner) > 0 {
		out["max_spin"] = spinner[0]
	}
	return out
}

func recordCard320(r catchStatRow, ok bool) *proto.Value {
	if !ok {
		return nil
	}
	grade := r.Grade
	if grade == 0 {
		grade = 1
	}
	hook := int64(27)
	if strings.HasPrefix(r.Bait, "spinner") {
		hook = 17
	}
	loc := r.LocLevel
	if loc == "" {
		loc = r.Loc
	}
	return wiregen.MustMakeObj("ippcfiphgfl", map[int]*proto.Value{
		0:  proto.StrVal(r.FishKey),
		1:  proto.U32(uint32(grade)),
		2:  proto.U32(uint32(r.WeightG)),
		3:  proto.F32(float32(r.LengthM)),
		4:  proto.U64(uint64(r.XP)),
		5:  proto.U32(uint32(hook)),
		6:  proto.StrVal(r.Bait),
		7:  proto.StrVal(loc),
		8:  proto.U64(uint64(int64(r.TS*10_000_000) + 116444736000000000)),
		9:  proto.U32(0),
		10: proto.U32(0),
		11: proto.U32(0),
		12: proto.U32(uint32(grade)),
		13: proto.Str(nil),
		14: proto.U32(0),
	})
}
