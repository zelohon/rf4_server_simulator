package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

// TestPlayerSkillMaxAPI 验证一键技能满接口：节点写满、无限技能点打开、
// 队列同时推送技能面板与档案刷新，详情页能读到开关状态，且可反向关闭。
func TestPlayerSkillMaxAPI(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.EnsureAdmin(ctx, "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	const uid = int64(1000001)
	if _, err := db.EnsureProfile(ctx, uid); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.SQL.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mustExec(`INSERT INTO skill_cat_def(cat_id,ord,title_key,category_kind) VALUES(1,0,'',1)`)
	mustExec(`INSERT INTO skill_def(cat_id,node_id,unlock_pct,max_level,effect_pct,group_id,enabled,ordinal) VALUES(1,501,0,4,1,0,1,0)`)

	cfg := config.Admin{HTTPAddr: "127.0.0.1:0", JWTSecret: "test"}
	srv := NewServer(cfg, db, logging.Discard(), WebFS())
	if _, err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	base := "http://" + srv.Addr()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, token, body string) (int, string) {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(resp.Body)
		return resp.StatusCode, out.String()
	}
	code, body := call("POST", "/api/login", "", `{"username":"admin","password":"admin123"}`)
	if code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &login)

	code, body = call("POST", "/api/players/skills/max", login.Token, `{"uid":1000001}`)
	if code != 200 || !strings.Contains(body, `"unlimited":true`) {
		t.Fatalf("skills max: %d %s", code, body)
	}
	if lv, _ := db.GetSkillNode(ctx, uid, 501); lv != 4 {
		t.Fatalf("节点 501 应满级 4，实得 %d", lv)
	}
	if pts, _ := db.GetSkillPoints(ctx, uid, 1); pts != 100 {
		t.Fatalf("熟练度应 100，实得 %v", pts)
	}
	if !db.SkillUnlimited(ctx, uid) {
		t.Fatal("一键技能满应打开无限技能点")
	}
	assertQueueContains(t, db, "refresh_skills")
	assertQueueContains(t, db, "refresh_player")

	code, body = call("GET", "/api/players/detail?uid=1000001", login.Token, "")
	if code != 200 || !strings.Contains(body, `"skill_unlimited":true`) {
		t.Fatalf("detail: %d %s", code, body)
	}

	code, body = call("POST", "/api/players/skills/unlimited", login.Token,
		`{"uid":1000001,"on":false}`)
	if code != 200 || !strings.Contains(body, `"unlimited":false`) {
		t.Fatalf("skills unlimited off: %d %s", code, body)
	}
	if db.SkillUnlimited(ctx, uid) {
		t.Fatal("关闭后开关应为 false")
	}
	if lv, _ := db.GetSkillNode(ctx, uid, 501); lv != 4 {
		t.Fatalf("关闭无限技能点不应回退等级，实得 %d", lv)
	}
}
