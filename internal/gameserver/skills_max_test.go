package gameserver

import (
	"context"
	"path/filepath"
	"testing"

	"rf4server/internal/store"
)

// TestSkillMaxUnlimitedPoints 验证"一键技能满"后：
// 技能树全部满级、311.f0 与档案 f6 显示无限点数、
// 即便自然点数已用尽，5/2 学习仍然放行；关闭开关后回到正常点数约束。
func TestSkillMaxUnlimitedPoints(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "skills_max.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	importStaticForSkillsCraft(t, db, "item_def", "skill_def", "skill_cat_def")
	ctx := context.Background()
	const uid = int64(1000001)
	srv := newSkillsCraftServer(t, db)
	if err := srv.EnsurePlayer(ctx, uid); err != nil {
		t.Fatal(err)
	}

	cats, nodes, err := db.MaxPlayerSkills(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if cats == 0 || nodes == 0 {
		t.Fatalf("拉满统计异常：cats=%d nodes=%d", cats, nodes)
	}
	if !db.SkillUnlimited(ctx, uid) {
		t.Fatal("一键技能满应同时打开无限技能点")
	}

	// 全部可升级节点都应是满级。
	// 注意：连接池只有 1 条连接，必须先收完行再逐条查等级，否则会自锁。
	rows, err := db.SQL.Query(`SELECT node_id, max_level FROM skill_def WHERE max_level>0`)
	if err != nil {
		t.Fatal(err)
	}
	type nodeMax struct{ id, max int64 }
	var defs []nodeMax
	for rows.Next() {
		var nd nodeMax
		if err := rows.Scan(&nd.id, &nd.max); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		defs = append(defs, nd)
	}
	rows.Close()
	if len(defs) == 0 {
		t.Fatal("静态库没有可升级节点")
	}
	for _, nd := range defs {
		if lv, _ := db.GetSkillNode(ctx, uid, nd.id); lv != nd.max {
			t.Fatalf("节点 %d 应满级 %d，实得 %d", nd.id, nd.max, lv)
		}
	}

	if got := srv.skillAvailablePoints(ctx, uid); got != store.UnlimitedSkillPoints {
		t.Fatalf("无限技能点下可用点应 %d，实得 %d", store.UnlimitedSkillPoints, got)
	}
	tree, err := srv.BuildSkillTree311(ctx, uid, ^uint64(0))
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(tree.Fields[0].Num); got != uint64(store.UnlimitedSkillPoints) {
		t.Fatalf("311.f0 应 %d，实得 %d", store.UnlimitedSkillPoints, got)
	}
	prof, err := srv.BuildProfile307(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := u64Of(prof.Fields[6].Num); got != uint64(store.UnlimitedSkillPoints) {
		t.Fatalf("档案 f6 应 %d，实得 %d", store.UnlimitedSkillPoints, got)
	}

	// 把节点清空后重新学习：自然点数不足（等级 1 → 0），靠无限技能点放行。
	if err := db.SetSkillNode(ctx, uid, 502, 0); err != nil {
		t.Fatal(err)
	}
	if ok, reason := srv.skillLearn(ctx, uid, 1, 502); !ok {
		t.Fatalf("无限技能点下学习应成功，实得拒绝原因 %q", reason)
	}

	// 关闭开关后回到正常约束：可用点 0，学习被拒。
	if err := db.SetSkillUnlimited(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if got := srv.skillAvailablePoints(ctx, uid); got != 0 {
		t.Fatalf("关闭后可用点应 0，实得 %d", got)
	}
	if err := db.SetSkillNode(ctx, uid, 502, 0); err != nil {
		t.Fatal(err)
	}
	if ok, reason := srv.skillLearn(ctx, uid, 1, 502); ok || reason != "no-points" {
		t.Fatalf("关闭后应因点数不足拒绝，实得 ok=%v reason=%q", ok, reason)
	}
}
