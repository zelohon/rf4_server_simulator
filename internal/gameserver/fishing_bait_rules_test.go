package gameserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func TestFishBaitRuleParsingAndTypes(t *testing.T) {
	if len(fishBaitRules) != 252 {
		t.Fatalf("rules=%d", len(fishBaitRules))
	}
	if ruleName("真柱白鮭\ufeff") != ruleName("真柱白鲑") {
		t.Fatal("fish alias mismatch")
	}
	roach := fishBaitRules[ruleName("常见拟鲤")]
	if len(roach.deny) != 2 || roach.deny[0] != "拟饵" {
		t.Fatalf("exclusions=%v", roach.deny)
	}
	for _, tt := range []struct {
		name string
		bait ruleBait
		term string
		want bool
	}{
		{"natural", ruleBait{name: "蚯蚓", category: 27, baitType: 6}, "蠕虫类", true},
		{"live fish", ruleBait{name: "欧鲌", id: "c_bleak_lf", category: 27, baitType: 4}, "饵鱼类", true},
		{"dead fish is not live bait", ruleBait{name: "死鲭鱼", category: 27, baitType: 15}, "活饵类", false},
		{"fillet is not live fish", ruleBait{name: "鱼肉", id: "fish_flesh", category: 27, baitType: 1}, "饵鱼类", false},
		{"lure exclusion", ruleBait{name: "Hunter 1-001", category: 17, lureType: 4}, "拟饵", true},
		{"model", ruleBait{name: "Hunter 1-001", category: 17, lureType: 4}, "Hunter 1系列", true},
		{"different model", ruleBait{name: "Hunter 1000-001", category: 17, lureType: 4}, "Hunter 1系列", false},
		{"excluded family", ruleBait{name: "Gummi Makk 10-001", category: 17, lureType: 3}, "Gummi Makk", true},
		{"alias", ruleBait{name: "红蚯蚓", id: "muckworm", category: 27, baitType: 6}, "粪便蠕虫", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bait.matches(tt.term); got != tt.want {
				t.Fatalf("match=%v want=%v", got, tt.want)
			}
		})
	}
}

func seedBaitRuleTest(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	s, db := newDetachTestServer(t)
	for _, q := range []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_900_rules','rules-test')`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('rules-test','c.roach','常见拟鲤'),('rules-test','frog','青蛙')`,
		`INSERT INTO item_def(numeric_id,item_id,category,attrs_json) VALUES(999801,'rule_worm',27,'{"fopbhlmkcpe":6}'),(999802,'rule_lure',17,'{"mbemakijdeh":4}'),(999803,'rule_other',27,'{}')`,
		`INSERT INTO item_catalog(item_id,name_zh) VALUES('rule_worm','蚯蚓'),('rule_lure','Hunter 1-001'),('rule_other','补充饵')`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return s, db
}

func setBaitRuleSwitch(t *testing.T, s *Server, value string) {
	t.Helper()
	if err := s.DB.TunableSet(context.Background(), "fish_bait_rules_enabled", value, "test"); err != nil {
		t.Fatal(err)
	}
	s.tunMu.Lock()
	s.tunAt = time.Time{}
	s.tunMu.Unlock()
}

func TestFishBaitSwitchFiltersAndRestoresLegacy(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	if err := db.TunableSet(context.Background(), "bait_pref_scale", "0", "test"); err != nil {
		t.Fatal(err)
	}
	// 默认及关闭后仍允许旧流程回退。
	if s.rollFish("unknown-map", "") == nil {
		t.Fatal("legacy fallback changed")
	}
	setBaitRuleSwitch(t, s, "1")
	for _, bait := range []string{"", "missing", "rule_lure"} {
		if f := s.rollFish("level_900_rules", bait); f != nil {
			t.Fatalf("unsupported bait %q produced %s", bait, f.Species)
		}
	}
	for range 32 {
		if f := s.rollFish("level_900_rules", "rule_worm"); f == nil || (f.Species != "c.roach" && f.Species != "frog") {
			t.Fatalf("wrong fish: %+v", f)
		}
	}
	if s.rollFish("unknown-map", "rule_worm") == nil {
		t.Fatal("bait-only rules must retain water-wide fallback")
	}
	// 实钓证据可补充未列出的搭配，但不能绕过明确不吃拟饵的规则。
	for _, bait := range []string{"rule_lure", "rule_other"} {
		if _, err := db.SQL.Exec(`INSERT INTO companion_bait_fish(bait_id,fish_key,tier,evidence) VALUES(?,'c.roach',1,1)`, bait); err != nil {
			t.Fatal(err)
		}
	}
	if s.rollFish("level_900_rules", "rule_lure") != nil {
		t.Fatal("evidence bypassed explicit exclusion")
	}
	if f := s.rollFish("level_900_rules", "rule_other"); f == nil {
		t.Fatalf("evidence ignored: %+v", f)
	}
	for _, q := range []string{
		`INSERT INTO loc_alias(alias,loc) VALUES('level_901_rules','rules-added')`,
		`INSERT INTO loc_wiki_fish(loc,fish_key,fish_zh) VALUES('rules-added','frog','青蛙')`,
		`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts) VALUES('rules-added','c.roach',1,1,'test',0)`,
	} {
		if _, err := db.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if s.rollFish("level_901_rules", "rule_lure") != nil {
		t.Fatal("added fish bypassed global exclusions")
	}
	if _, err := db.SQL.Exec(`INSERT INTO fish_weight_override(loc,fish_key,weight,enabled,updated_by,updated_ts) VALUES('rules-test','c.roach',0,0,'test',0),('rules-test','frog',0,0,'test',0)`); err != nil {
		t.Fatal(err)
	}
	if s.rollFish("level_900_rules", "rule_worm") != nil {
		t.Fatal("all-disabled fish were restored")
	}
	setBaitRuleSwitch(t, s, "0")
	if s.rollFish("unknown-map", "") == nil {
		t.Fatal("switch did not restore legacy")
	}
}

func TestFishBaitNoCandidateTimerAndHotReload(t *testing.T) {
	s, db := seedBaitRuleTest(t)
	// 先读入旧缓存，再走管理指令刷新，验证真实的热更新路径。
	if s.tunableF("fish_bait_rules_enabled", 0) != 0 {
		t.Fatal("unexpected default")
	}
	if err := db.TunableSet(context.Background(), "fish_bait_rules_enabled", "1", "test"); err != nil {
		t.Fatal(err)
	}
	s.execAdminCmd(&AdminCommand{Op: "reload_tunables"})
	if s.tunableF("fish_bait_rules_enabled", 0) != 1 {
		t.Fatal("hot reload failed")
	}
	addFishingRecoverySession(s, 1000001, 901)
	sess := s.liveSessionByUID(1000001)
	st := s.stateFor(sess.Userid)
	st.mu.Lock()
	st.CastActive, st.InWater = true, true
	st.Loc, st.Bait = "level_900_rules", "rule_lure"
	st.CastTs = time.Now().Add(-time.Minute)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	st.biteTimer = timer
	gen := st.BiteGen
	st.mu.Unlock()
	s.onBiteTimer(sess.Userid, gen)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.Resolved || st.Pending != nil || st.PushSent {
		t.Fatalf("no-candidate state: resolved=%v pending=%v pushed=%v", st.Resolved, st.Pending, st.PushSent)
	}
}

func TestFishBaitRulesWithReadOnlyStaticAndExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	legacy, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := legacy.TunableSet(context.Background(), "xp_gain", "1.25", "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		db, err := store.OpenWithStatic(path, filepath.Join("..", "..", "server_data", "static.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			db.Close()
			t.Fatal(err)
		}
		s := NewServer(config.Game{Tunnel: config.Tunnel{PSK: "rules-runtime"}}, logging.Discard(), nil)
		s.SetDB(db)
		if attempt == 0 {
			setBaitRuleSwitch(t, s, "1")
		}
		if s.tunableF("fish_bait_rules_enabled", 0) != 1 || s.tunableF("xp_gain", 0) != 1.25 {
			db.Close()
			t.Fatal("restart lost tunables")
		}
		// 本地已有 1275 条证据的搭配，附件未列出也可生效。
		keys, _ := s.filterFishBaits("惟有诺克河", "muckworm", []string{"c_nase"}, []float64{1})
		if len(keys) != 1 {
			db.Close()
			t.Fatal("read-only evidence not applied")
		}
		keys, _ = s.filterFishBaits("克马羚诺也湖", "spinnerbaits_1244", []string{"c.roach"}, []float64{1})
		if len(keys) != 0 {
			db.Close()
			t.Fatal("read-only rule exclusion ignored")
		}
		if f := s.rollFish("level_009_ahtuba", "worm_01"); f == nil {
			db.Close()
			t.Fatal("real map and bait returned no fish")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
