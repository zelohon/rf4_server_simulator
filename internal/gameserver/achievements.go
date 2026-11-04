package gameserver

import (
	"context"
	"sort"

	"rf4server/internal/proto"
	"rf4server/internal/store"
	"rf4server/internal/wiregen"
)

// 本文件把持久化的成就与鱼种纪录转换为客户端 DTO。

func unixToFiletime100ns(ts float64) uint64 {
	if ts <= 0 {
		return 0
	}
	return uint64(int64(ts*10_000_000 + 116444736000000000))
}

func (s *Server) buildAchievements305(ctx context.Context, uid int64) (*proto.Value, error) {
	defs, err := s.DB.AchievementDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.DB.PlayerAchievementStates(ctx, uid)
	if err != nil {
		return nil, err
	}
	items := make([]*proto.Value, 0, len(defs))
	for _, achID := range defs {
		st := states[achID]
		unlocked, claimed := uint32(0), uint32(0)
		if st.Unlocked {
			unlocked = 1
		}
		if st.Claimed {
			claimed = 1
		}
		items = append(items, wiregen.MustMakeObj("efnkohdcnld", map[int]*proto.Value{
			0: proto.U32(uint32(achID)),
			1: proto.U32(unlocked),
			2: proto.F32(float32(st.Progress)),
			3: proto.U32(claimed),
			4: proto.U64(unixToFiletime100ns(st.TS)),
		}))
	}
	return proto.Args(proto.S("114"), items...), nil
}

func (s *Server) buildFishTrophies323(ctx context.Context, uid int64) (*proto.Value, error) {
	rows, err := s.DB.PlayerCatchRecords(ctx, uid)
	if err != nil {
		return nil, err
	}

	// 客户端按水域分组，并在每组内按鱼种匹配唯一一张纪录卡。
	groups := make(map[string][]store.CatchRecord)
	for _, r := range rows {
		if r.FishKey == "" {
			continue
		}
		placeID := r.LocLevel
		if placeID == "" {
			placeID = r.Loc
		}
		if placeID == "" {
			continue
		}
		groups[placeID] = append(groups[placeID], r)
	}
	placeIDs := make([]string, 0, len(groups))
	for placeID := range groups {
		placeIDs = append(placeIDs, placeID)
	}
	sort.Strings(placeIDs)

	items := make([]*proto.Value, 0, len(placeIDs))
	for _, placeID := range placeIDs {
		bestByFish := make(map[string]store.CatchRecord)
		for _, r := range groups[placeID] {
			prev, exists := bestByFish[r.FishKey]
			if !exists || r.WeightG > prev.WeightG ||
				(r.WeightG == prev.WeightG && (r.TS < prev.TS || (r.TS == prev.TS && r.ID < prev.ID))) {
				bestByFish[r.FishKey] = r
			}
		}
		fishKeys := make([]string, 0, len(bestByFish))
		for fishKey := range bestByFish {
			fishKeys = append(fishKeys, fishKey)
		}
		sort.Strings(fishKeys)
		cards := make([]*proto.Value, 0, len(fishKeys))
		for _, fishKey := range fishKeys {
			cards = append(cards, recordCard320FromCatch(bestByFish[fishKey]))
		}
		items = append(items, wiregen.MustMakeObj("dochkomimic", map[int]*proto.Value{
			0: proto.StrVal(placeID),
			1: proto.Args(proto.S("129"), cards...),
		}))
	}
	return proto.Args(proto.S("132"), items...), nil
}

func recordCard320FromCatch(r store.CatchRecord) *proto.Value {
	grade := r.Grade
	if grade <= 0 {
		grade = 1
	}
	loc := r.LocLevel
	if loc == "" {
		loc = r.Loc
	}
	hook := uint32(27)
	if len(r.Bait) >= 7 && r.Bait[:7] == "spinner" {
		hook = 17
	}
	return wiregen.MustMakeObj("ippcfiphgfl", map[int]*proto.Value{
		0:  proto.StrVal(r.FishKey),
		1:  proto.U32(uint32(grade)),
		2:  proto.U32(uint32(r.WeightG)),
		3:  proto.F32(float32(r.LengthM)),
		4:  proto.U64(uint64(r.XP)),
		5:  proto.U32(hook),
		6:  proto.StrVal(r.Bait),
		7:  proto.StrVal(loc),
		8:  proto.U64(unixToFiletime100ns(r.TS)),
		9:  proto.U32(0),
		10: proto.U32(0),
		11: proto.U32(0),
		12: proto.U32(uint32(grade)),
		13: proto.Str(nil),
		14: proto.U32(0),
	})
}
