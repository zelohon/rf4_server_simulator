package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMaxPlayerSkills 验证一键技能满：熟练度全部封顶、可升级节点写成 max_level、
// 不可升级节点保持缺省，并同时打开无限技能点开关。
func TestMaxPlayerSkills(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "skills_max.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const uid = int64(1000001)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO skill_cat_def(cat_id,ord,title_key,category_kind) VALUES(1,0,'',1)`)
	mustExec(`INSERT INTO skill_cat_def(cat_id,ord,title_key,category_kind) VALUES(2,1,'',2)`)
	mustExec(`INSERT INTO skill_def(cat_id,node_id,unlock_pct,max_level,effect_pct,group_id,enabled,ordinal) VALUES(1,501,0,3,1,0,1,0)`)
	mustExec(`INSERT INTO skill_def(cat_id,node_id,unlock_pct,max_level,effect_pct,group_id,enabled,ordinal) VALUES(1,502,50,5,1,0,0,1)`)
	mustExec(`INSERT INTO skill_def(cat_id,node_id,unlock_pct,max_level,effect_pct,group_id,enabled,ordinal) VALUES(2,601,0,0,1,0,0,0)`)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}

	cats, nodes, err := db.MaxPlayerSkills(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if cats != 2 || nodes != 2 {
		t.Fatalf("统计应为 2 个大类 / 2 个可升级节点，实得 %d / %d", cats, nodes)
	}

	all, _ := db.ListSkills(ctx, uid)
	for _, cat := range []int64{1, 2} {
		if all[cat] != maxSkillPoints {
			t.Fatalf("大类 %d 熟练度应 %v，实得 %v", cat, maxSkillPoints, all[cat])
		}
	}
	if lv, _ := db.GetSkillNode(ctx, uid, 501); lv != 3 {
		t.Fatalf("节点 501 应满级 3，实得 %d", lv)
	}
	if lv, _ := db.GetSkillNode(ctx, uid, 502); lv != 5 {
		t.Fatalf("节点 502 应满级 5，实得 %d", lv)
	}
	if lv, _ := db.GetSkillNode(ctx, uid, 601); lv != 0 {
		t.Fatalf("max_level=0 的节点不应写等级，实得 %d", lv)
	}
	if !db.SkillUnlimited(ctx, uid) {
		t.Fatal("一键技能满应同时打开无限技能点")
	}

	// 关闭无限技能点后，管理员偏移应归零，可用点数回到自然值（等级 1 → 0）。
	if err := db.SetSkillBonus(ctx, uid, 500); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillUnlimited(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if db.SkillUnlimited(ctx, uid) {
		t.Fatal("关闭后开关应为 false")
	}
	_, _, bonus, _ := db.SkillPointsAvailable(ctx, uid)
	if bonus != 0 {
		t.Fatalf("关闭无限技能点应清空偏移，实得 %d", bonus)
	}
	if lv, _ := db.GetSkillNode(ctx, uid, 501); lv != 3 {
		t.Fatalf("关闭无限技能点不应回退已学等级，实得 %d", lv)
	}
}
