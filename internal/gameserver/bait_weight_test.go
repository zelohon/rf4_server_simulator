package gameserver

import (
	"math"
	"testing"

	"rf4server/internal/store"
)

func seedBaitWeightTest(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	s, db := seedBaitRuleTest(t)
	for _, query := range []string{
		`CREATE INDEX bait_weight_test_item_id ON item_def(item_id)`,
		`INSERT INTO loc_fish(loc,fish_key,name_zh,catches) VALUES('rules-test','c.roach','常见拟鲤',1)`,
		`INSERT INTO species_weight(fish_key,trophy_g,trophy_line_g,super_trophy_line_g) VALUES('c.roach',200,500,1000)`,
		`UPDATE fish_grade_lines SET qualified_g=200,trophy_g=500,blue_trophy_g=1000 WHERE fish_key='c.roach'`,
		`INSERT INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES('rule_worm','c.roach',3,10),('rule_other','c.roach',1,1)`,
		`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES(999805,'rule_neutral',27,'{"fopbhlmkcpe":6}')`,
		`INSERT INTO item_catalog(item_id,name_zh) VALUES('rule_neutral','蚯蚓')`,
	} {
		if _, err := db.SQL.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return s, db
}

func sampleBaitWeightMean(t *testing.T, s *Server, count int, maxWeight int64, roll func() *RolledFish) float64 {
	t.Helper()
	var total int64
	weights := map[int64]bool{}
	matched, atCap := 0, 0
	for attempts := 0; matched < count && attempts < count*20; attempts++ {
		fish := roll()
		if fish == nil || fish.Species != "c.roach" {
			continue
		}
		if fish.WeightG < 1 || fish.WeightG > maxWeight || fish.Grade != s.rollGradeDB(t.Context(), fish.Species, fish.WeightG) {
			t.Fatalf("weight or grade outside the sampled model: %+v", fish)
		}
		total += fish.WeightG
		weights[fish.WeightG] = true
		matched++
		if fish.WeightG == maxWeight {
			atCap++
		}
	}
	if matched != count || len(weights) < 100 || atCap > count/30 {
		t.Fatalf("sampling collapsed: count=%d distinct=%d atCap=%d", matched, len(weights), atCap)
	}
	return float64(total) / float64(matched)
}

func TestBaitWeightDistributionDBAndFallback(t *testing.T) {
	for _, strict := range []string{"0", "1"} {
		for _, fallback := range []bool{false, true} {
			name := "database"
			if fallback {
				name = "fallback"
			}
			t.Run(name+"/rules="+strict, func(t *testing.T) {
				s, db := seedBaitWeightTest(t)
				if err := db.TunableSet(t.Context(), "bait_pref_weight_shift", "2", "test"); err != nil {
					t.Fatal(err)
				}
				setBaitRuleSwitch(t, s, strict)
				mean := func(bait string) float64 {
					location := "level_900_rules"
					limit := int64(800)
					if fallback {
						location, limit = "weight-fallback", 600
					}
					return sampleBaitWeightMean(t, s, 600, limit, func() *RolledFish {
						return s.rollFishWithRules(location, bait, 0.8, neutralFishingSkillBonus())
					})
				}
				neutral := mean("rule_neutral")
				preferred := mean("rule_worm")
				if preferred < neutral*1.04 {
					t.Fatalf("same-species weight did not shift: neutral=%.2f preferred=%.2f", neutral, preferred)
				}
				if err := db.TunableSet(t.Context(), "bait_pref_weight_shift", "0", "test"); err != nil {
					t.Fatal(err)
				}
				s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
				disabled := mean("rule_worm")
				if math.Abs(disabled-neutral) > neutral*0.12 {
					t.Fatalf("zero shift changed distribution: neutral=%.2f disabled=%.2f", neutral, disabled)
				}
				t.Logf("same-species means: neutral=%.2f preferred=%.2f zero-shift=%.2f", neutral, preferred, disabled)
			})
		}
	}
}

func TestBaitWeightCombinationUsesEvidenceAndExistingParameters(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	for _, baits := range [][]string{{"rule_worm"}, {"rule_other", "rule_worm"}, {"rule_worm", "rule_other"}, {"rule_worm", "rule_worm"}} {
		if got := s.baitWeightMode("c.roach", baits); math.Abs(got-1.36) > 1e-12 {
			t.Fatalf("combination changed existing efficacy: baits=%v mode=%g", baits, got)
		}
	}
	for _, baits := range [][]string{nil, {}, {"missing"}, {"rule_neutral"}, {"rule_other"}} {
		if got := s.baitWeightMode("c.roach", baits); got != 1 {
			t.Fatalf("missing or low-tier evidence changed weight: baits=%v mode=%g", baits, got)
		}
	}
	for _, key := range []string{"bait_pref_scale", "bait_pref_weight_shift"} {
		if err := db.TunableSet(t.Context(), key, "0", "test"); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
		if got := s.baitWeightMode("c.roach", []string{"rule_worm"}); got != 1 {
			t.Fatalf("%s=0 did not disable weight shift: %g", key, got)
		}
		if err := db.TunableSet(t.Context(), key, "1", "test"); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	}
}

func TestBaitWeightReadsLivePrimaryAndSecondaryBaits(t *testing.T) {
	for _, strict := range []string{"0", "1"} {
		t.Run("rules="+strict, func(t *testing.T) {
			s, db := seedBaitWeightTest(t)
			if err := db.TunableSet(t.Context(), "bait_pref_weight_shift", "2", "test"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SQL.Exec(`INSERT OR REPLACE INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES('worm_01','c.roach',1,1),('muckworm','c.roach',3,10),('fly','c.roach',1,1)`); err != nil {
				t.Fatal(err)
			}
			setBaitRuleSwitch(t, s, strict)
			rod, err := s.CreateItem(t.Context(), testUID, "feeder_10126_FD390", "backpack", 1)
			if err != nil {
				t.Fatal(err)
			}
			mount := func(bait string, index int64) string {
				t.Helper()
				item, err := s.CreateItem(t.Context(), testUID, bait, "backpack", 5)
				if err != nil {
					t.Fatal(err)
				}
				result := s.rigSetComponent(t.Context(), testUID, rod.GUID, catBait, item.GUID, index, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
				if result == nil || result.Rejected || result.MountedGUID == "" {
					t.Fatalf("mount %s rejected: %+v", bait, result)
				}
				return result.MountedGUID
			}
			mean := func() float64 {
				return sampleBaitWeightMean(t, s, 600, 1250, func() *RolledFish {
					// 客户端缓存仍报高档饵，体重应读取当前实际饵位。
					return s.rollFishForRig(t.Context(), testUID, rod.GUID, "level_900_rules", "muckworm", neutralFishingSkillBonus())
				})
			}
			mount("worm_01", 350)
			neutral := mean()
			secondary := mount("muckworm", 351)
			combined := mean()
			mount("fly", 350)
			if result := s.rigRemoveItemComponent(t.Context(), testUID, rod.GUID, secondary); result == nil {
				t.Fatal("secondary bait detach failed")
			}
			changed := mean()
			if combined < neutral*1.08 || math.Abs(changed-neutral) > neutral*0.12 {
				t.Fatalf("live bait slots ignored: original=%.2f combined=%.2f changed=%.2f", neutral, combined, changed)
			}
			mount("muckworm", 350)
			if primary := mean(); primary < changed*1.08 {
				t.Fatalf("primary replacement did not update weight: before=%.2f after=%.2f", changed, primary)
			}
			t.Logf("live bait means: primary=%.2f combination=%.2f replacement=%.2f", neutral, combined, changed)
		})
	}
}

func TestWeightSamplingRespectsCapSwitchAndScale(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "database"
		if fallback {
			name = "fallback"
		}
		t.Run(name, func(t *testing.T) {
			s, db := seedBaitWeightTest(t)
			setBaitRuleSwitch(t, s, "0")
			if err := db.TunableSet(t.Context(), "fish_weight_scale", "2.5", "test"); err != nil {
				t.Fatal(err)
			}
			s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
			roll := func() *RolledFish {
				if fallback {
					return s.rollFishPoolsWithRules("weight-fallback", 0.301, neutralFishingSkillBonus())
				}
				return s.rollFishFromDBWithRules("level_900_rules", "rule_neutral", 0.301, neutralFishingSkillBonus())
			}
			weights := map[int64]bool{}
			atCap, count := 0, 0
			for attempts := 0; count < 600 && attempts < 12000; attempts++ {
				f := roll()
				if f == nil || f.Species != "c.roach" {
					continue
				}
				if f.WeightG > 301 || f.WeightG < 1 {
					t.Fatalf("scaled sample escaped enabled cap: %+v", f)
				}
				count++
				weights[f.WeightG] = true
				if f.WeightG == 301 {
					atCap++
				}
			}
			if count != 600 || len(weights) < 80 || atCap > count/20 {
				t.Fatalf("cap created a weight spike: count=%d distinct=%d atCap=%d", count, len(weights), atCap)
			}
			if err := db.TunableSet(t.Context(), "fish_weight_cap", "0", "test"); err != nil {
				t.Fatal(err)
			}
			s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
			aboveCap := 0
			for range 200 {
				if f := roll(); f != nil && f.WeightG > 301 {
					aboveCap++
				}
			}
			if aboveCap == 0 {
				t.Fatal("cap off still limited the scaled distribution")
			}
		})
	}
}

func TestWeightSamplingRetriesOnlyKnownRoster(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	setBaitRuleSwitch(t, s, "0")
	for _, query := range []string{
		`DELETE FROM loc_wiki_fish WHERE loc='rules-test' AND fish_key='frog'`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('rules-test','weight_heavy','测试大鱼')`,
		`INSERT INTO loc_fish(loc,fish_key,name_zh,catches) VALUES('rules-test','weight_heavy','测试大鱼',1000000000000)`,
		`INSERT INTO species_weight(fish_key,trophy_g,trophy_line_g,super_trophy_line_g) VALUES('weight_heavy',10000,20000,40000)`,
	} {
		if _, err := db.SQL.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for range 25 {
		f := s.rollFishWithRules("level_900_rules", "rule_neutral", 0.5, neutralFishingSkillBonus())
		if f == nil || f.Species != "c.roach" || f.WeightG > 500 {
			t.Fatalf("infeasible species prevented same-water retry: %+v", f)
		}
	}
	if f := s.rollFishWithRules("level_900_rules", "rule_neutral", 0.001, neutralFishingSkillBonus()); f != nil {
		t.Fatalf("infeasible known roster fell back to another pool: %+v", f)
	}
	if f := s.rollFishPoolsWithRules("weight-fallback", 0.001, neutralFishingSkillBonus()); f != nil {
		t.Fatalf("infeasible fallback pool produced a fish: %+v", f)
	}
}

func TestTriangularCapConditionsOriginalMode(t *testing.T) {
	const count = 20000
	var below, atCap int
	for range count {
		// 原三角分布的峰值为 80，限制到 50 后，P(X<=25|X<=50)=25²/50²。
		w := triangular(0, 100, 80, 50)
		if w < 0 || w > 50 {
			t.Fatalf("conditional triangle escaped support: %g", w)
		}
		if w <= 25 {
			below++
		}
		if w == 50 {
			atCap++
		}
	}
	fraction := float64(below) / count
	if math.Abs(fraction-0.25) > 0.02 || atCap != 0 {
		t.Fatalf("cap moved the natural mode or added a point mass: fraction=%g atCap=%d", fraction, atCap)
	}
}

func TestWeightSamplingRejectsUnrepresentableScaledWeights(t *testing.T) {
	s, db := seedBaitWeightTest(t)
	setBaitRuleSwitch(t, s, "0")
	if err := db.TunableSet(t.Context(), "fish_weight_cap", "0", "test"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"NaN", "+Inf", "1e300"} {
		if err := db.TunableSet(t.Context(), "fish_weight_scale", value, "test"); err != nil {
			t.Fatal(err)
		}
		s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
		if f := s.rollFishWithRules("level_900_rules", "rule_neutral", 0, neutralFishingSkillBonus()); f != nil {
			t.Fatalf("unrepresentable database scaling %s produced a fish: %+v", value, f)
		}
		if f := s.rollFishPoolsWithRules("weight-fallback", 0, neutralFishingSkillBonus()); f != nil {
			t.Fatalf("unrepresentable fallback scaling %s produced a fish: %+v", value, f)
		}
	}
}
