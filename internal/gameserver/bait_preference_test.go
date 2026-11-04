package gameserver

import (
	"context"
	"testing"
	"time"
)

func TestFishBaitPreferenceAlternateCombinationAndChange(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	if _, err := db.SQL.Exec(`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES(999804,'rule_fly',27,'{"fopbhlmkcpe":7}'); INSERT INTO item_catalog(item_id,name_zh) VALUES('rule_fly','苍蝇')`); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(worldDayEpochUnix+1500, 0)
	weight := func(baits []string, at time.Time) float64 {
		t.Helper()
		keys, w := s.filterFishBaitSet("rules-test", baits, []string{"c.roach"}, []float64{1}, at)
		if len(keys) == 0 {
			return 0
		}
		return w[0]
	}
	favorite := weight([]string{"rule_worm"}, now)
	other := weight([]string{"rule_other"}, now)
	if other <= 0 || other >= favorite || other > 0.12 || favorite < 1 {
		t.Fatalf("favorite=%f other=%f", favorite, other)
	}
	if weight([]string{"rule_worm", "rule_fly"}, now) <= favorite {
		t.Fatal("two preferred baits must participate together")
	}
	if weight([]string{"rule_worm", "rule_worm"}, now) != favorite || weight([]string{"rule_fly", "rule_worm"}, now) != weight([]string{"rule_worm", "rule_fly"}, now) {
		t.Fatal("duplicates or slot ordering changed bait preference")
	}
	if weight([]string{"rule_worm", "rule_lure"}, now) != 0 || weight([]string{"missing"}, now) != 0 || weight(nil, now) != 0 {
		t.Fatal("combination bypassed banned, invalid or absent bait")
	}
	if favorite == weight([]string{"rule_worm"}, now.Add(600*time.Second)) {
		t.Fatal("bait preference did not vary over game time")
	}
	setBaitRuleSwitch(t, s, "1")
	// 强制只选拟鲤，验证差异也传到了实际派鱼概率，而非仅改变鱼种比例。
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts) VALUES('rules-test','frog',0,0,'test',0),('rules-test','c.roach',1,1,'test',0)`); err != nil {
		t.Fatal(err)
	}
	preferredHits, otherHits := 0, 0
	for i := 0; i < 160; i++ {
		if s.rollFishForRig(context.Background(), 0, "", "level_900_rules", "rule_worm", neutralFishingSkillBonus()) != nil {
			preferredHits++
		}
		if s.rollFishForRig(context.Background(), 0, "", "level_900_rules", "rule_other", neutralFishingSkillBonus()) != nil {
			otherHits++
		}
	}
	if preferredHits < 65 || otherHits > 35 || preferredHits <= otherHits {
		t.Fatalf("preferred hits=%d alternate hits=%d", preferredHits, otherHits)
	}
}
