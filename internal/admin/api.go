// Package admin —— Web 管理后台：JWT 认证 + REST API + 内嵌 SPA。
//
// 全部变更操作写 audit_log；需要在线推送或走游戏域事务的指令写入共享
// SQLite admin_queue，由 gameserver 执行。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"rf4server/internal/config"
	"rf4server/internal/gameserver"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

// Server 管理后台。
type Server struct {
	cfg    config.Admin
	db     *store.DB
	log    *plog
	jwtKey []byte
	mux    *http.ServeMux
	srv    *http.Server
	ln     net.Listener

	// 会话计数（由 gameserver 统计写入共享 KV；这里只读展示）
	sessionCount atomic.Int64
}

type plog struct{ l logger }

type logger interface {
	Info(string, ...any)
	Warn(string, ...any)
	Error(string, ...any)
}

// NewServer 构建管理后台（webFS 为内嵌前端，nil 时只有 API）。
// 运行时指令写入共享 SQLite 队列，由 gameserver 轮询消费。
func NewServer(cfg config.Admin, db *store.DB, log logger, webFS fs.FS) *Server {
	s := &Server{cfg: cfg, db: db, log: &plog{l: log}, jwtKey: []byte(cfg.JWTSecret)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleLogin)

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return s.withAuth(h)
	}
	mux.HandleFunc("GET /api/me", auth(s.handleMe))
	mux.HandleFunc("GET /api/dashboard", auth(s.handleDashboard))
	mux.HandleFunc("GET /api/tunables", auth(s.handleTunableList))
	mux.HandleFunc("POST /api/tunables", auth(s.handleTunableSet))
	mux.HandleFunc("GET /api/players", auth(s.handlePlayers))
	mux.HandleFunc("GET /api/players/detail", auth(s.handlePlayerDetail))
	mux.HandleFunc("POST /api/players/update", auth(s.handlePlayerUpdate))
	mux.HandleFunc("POST /api/players/grant", auth(s.handleGrantItem))
	mux.HandleFunc("POST /api/players/items/delete", auth(s.handlePlayerItemDelete))
	mux.HandleFunc("POST /api/players/keepnet/add", auth(s.handlePlayerKeepnetAdd))
	mux.HandleFunc("POST /api/players/keepnet/delete", auth(s.handlePlayerKeepnetDelete))
	mux.HandleFunc("POST /api/players/skills/update", auth(s.handlePlayerSkillUpdate))
	mux.HandleFunc("POST /api/players/skills/points", auth(s.handlePlayerSkillPoints))
	mux.HandleFunc("POST /api/players/skills/max", auth(s.handlePlayerSkillMax))
	mux.HandleFunc("POST /api/players/skills/unlimited", auth(s.handlePlayerSkillUnlimited))
	mux.HandleFunc("POST /api/players/kick", auth(s.handleKick))
	mux.HandleFunc("POST /api/players/ban", auth(s.handleBan))
	mux.HandleFunc("POST /api/players/membership", auth(s.handleGrantMembership))
	mux.HandleFunc("POST /api/players/membership/revoke", auth(s.handleRevokeMembership))
	mux.HandleFunc("GET /api/catalog/items", auth(s.handleCatalogItems))
	mux.HandleFunc("GET /api/stores", auth(s.handleStores))
	mux.HandleFunc("GET /api/stores/items", auth(s.handleStoreItems))
	mux.HandleFunc("POST /api/stores/items/update", auth(s.handleStoreItemUpdate))
	mux.HandleFunc("POST /api/stores/items/reset", auth(s.handleStoreItemReset))
	mux.HandleFunc("GET /api/fish/areas", auth(s.handleFishAreas))
	mux.HandleFunc("GET /api/fish/config", auth(s.handleFishConfig))
	mux.HandleFunc("POST /api/fish/config", auth(s.handleFishConfigSave))
	mux.HandleFunc("POST /api/fish/config/reset", auth(s.handleFishConfigReset))
	mux.HandleFunc("GET /api/fish/pool", auth(s.handleFishPool))
	mux.HandleFunc("POST /api/mail/send", auth(s.handleMailSend))
	mux.HandleFunc("GET /api/audit", auth(s.handleAudit))
	mux.HandleFunc("GET /api/accounts", auth(s.handleAccountList))
	mux.HandleFunc("POST /api/accounts/create", auth(s.handleAccountCreate))
	mux.HandleFunc("POST /api/accounts/password", auth(s.handleAccountPassword))
	mux.HandleFunc("POST /api/accounts/update", auth(s.handleAccountUpdate))
	mux.HandleFunc("POST /api/accounts/ban", auth(s.handleAccountBan))
	mux.HandleFunc("POST /api/accounts/delete", auth(s.handleAccountDelete))

	if webFS != nil {
		mux.Handle("/", http.FileServer(http.FS(webFS)))
	}
	s.mux = mux
	s.srv = &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return s
}

// SetSessionCount 供外部更新在线会话数。
func (s *Server) SetSessionCount(n int64) { s.sessionCount.Store(n) }

// Start 启动监听（返回错误 channel 与绑定错误）。
func (s *Server) Start() (<-chan error, error) {
	ln, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	return errCh, nil
}

// Addr 实际监听地址（Start 后有效）。
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.cfg.HTTPAddr
	}
	return s.ln.Addr().String()
}

// Shutdown 优雅停机。
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

// ---- 认证 ----

type claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, "参数错误")
		return
	}
	ok, err := s.db.AdminVerify(r.Context(), req.Username, req.Password)
	if err != nil || !ok {
		s.log.l.Warn("管理登录失败", "user", req.Username)
		httpError(w, 401, "用户名或密码错误")
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), req.Username, "login", "", "")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Username: req.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(12 * time.Hour)),
		},
	})
	signed, err := token.SignedString(s.jwtKey)
	if err != nil {
		httpError(w, 500, "签发失败")
		return
	}
	writeJSON(w, map[string]string{"token": signed})
}

func (s *Server) withAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			httpError(w, 401, "未认证")
			return
		}
		tok, err := jwt.ParseWithClaims(strings.TrimPrefix(authz, "Bearer "), &claims{},
			func(t *jwt.Token) (any, error) { return s.jwtKey, nil })
		if err != nil || !tok.Valid {
			httpError(w, 401, "会话过期")
			return
		}
		cl := tok.Claims.(*claims)
		r = r.WithContext(context.WithValue(r.Context(), ctxAdmin{}, cl.Username))
		h(w, r)
	}
}

type ctxAdmin struct{}

func adminOf(r *http.Request) string {
	v, _ := r.Context().Value(ctxAdmin{}).(string)
	return v
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"username": adminOf(r)})
}

// ---- 仪表盘 ----

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var players, items, mails, audits int64
	_ = s.db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile`).Scan(&players)
	_ = s.db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM item`).Scan(&items)
	_ = s.db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail`).Scan(&mails)
	_ = s.db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&audits)
	tunables, _ := s.db.TunableList(ctx)
	writeJSON(w, map[string]any{
		"sessions": s.sessionCount.Load(),
		"players":  players,
		"items":    items,
		"mails":    mails,
		"audits":   audits,
		"tunables": len(tunables),
		"pg":       "up",
	})
}

// ---- 运行参数 ----

func (s *Server) handleTunableList(w http.ResponseWriter, r *http.Request) {
	m, err := s.db.TunableList(r.Context())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, m)
}

func (s *Server) handleTunableSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.TunableSet(r.Context(), req.Key, req.Value, adminOf(r)); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "tunable", req.Key, req.Value)
	// 通知主服立即刷新运行参数；后台也会按缓存周期生效。
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "reload_tunables"}))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- 玩家管理 ----

type playerRow struct {
	UID     int64   `json:"uid"`
	Name    string  `json:"name"`
	Level   int64   `json:"level"`
	XP      int64   `json:"xp"`
	Silver  int64   `json:"silver"`
	Gold    int64   `json:"gold"`
	Bank    int64   `json:"bank"`
	Banned  bool    `json:"banned"`
	Updated float64 `json:"updated_ts"`
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var rows []playerRow
	var err error
	if q == "" {
		err = queryPlayers(ctx, s.db, &rows,
			`SELECT uid, name, level, xp, silver, gold, bank, COALESCE(banned,0), COALESCE(updated_ts,0) FROM profile ORDER BY uid LIMIT 100`)
	} else {
		err = queryPlayers(ctx, s.db, &rows,
			`SELECT uid, name, level, xp, silver, gold, bank, COALESCE(banned,0), COALESCE(updated_ts,0) FROM profile WHERE CAST(uid AS TEXT)=? OR name LIKE '%'||?||'%' ORDER BY uid LIMIT 100`, q, q)
	}
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, rows)
}

func queryPlayers(ctx context.Context, db *store.DB, out *[]playerRow, q string, args ...any) error {
	rows, err := db.SQL.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p playerRow
		var banned int64
		if err := rows.Scan(&p.UID, &p.Name, &p.Level, &p.XP, &p.Silver, &p.Gold, &p.Bank, &banned, &p.Updated); err != nil {
			return err
		}
		p.Banned = banned != 0
		*out = append(*out, p)
	}
	return rows.Err()
}

func (s *Server) handlePlayerDetail(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := s.db.GetProfile(ctx, uid)
	if err != nil {
		httpError(w, 404, "玩家不存在")
		return
	}
	items, _ := s.db.ListPlayerItemsAdmin(ctx, uid)
	keepnet, _ := s.db.KeepnetList(ctx, uid)
	mails, _ := s.db.MailList(ctx, uid, false)
	skills, _ := s.db.ListPlayerSkillsAdmin(ctx, uid)
	mailRows := make([]map[string]any, 0, len(mails))
	for _, m := range mails {
		mailRows = append(mailRows, map[string]any{
			"id": m.ID, "sender": m.Sender, "text": m.Text, "kind": m.Kind,
			"created_ts": m.CreatedTS, "read": m.Read,
		})
	}
	skillTotal, skillSpent, skillBonus, skillAvail := s.db.SkillPointsAvailable(ctx, uid)
	unlimited := s.db.SkillUnlimited(ctx, uid)
	if unlimited {
		// 与游戏服 311.f0 保持一致：开启无限技能点后面板显示固定值。
		skillAvail = store.UnlimitedSkillPoints
	}
	writeJSON(w, map[string]any{
		"uid": p.UID, "name": p.Name, "level": p.Level, "xp": p.XP,
		"silver": p.Silver, "gold": p.Gold, "bank": p.Bank,
		"membership": p.Membership, "membership_expire_at": p.MembershipExpireAt,
		"stars": p.Stars, "email": p.Email,
		"role": p.Role, "banned": p.Banned,
		"item_count": len(items), "keepnet_count": len(keepnet), "mail_count": len(mails),
		"items": items, "keepnet": keepnet, "mails": mailRows, "skills": skills,
		"skill_total": skillTotal, "skill_spent": skillSpent,
		"skill_bonus": skillBonus, "available_points": skillAvail,
		"skill_unlimited": unlimited,
	})
}

func (s *Server) handlePlayerUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID    int64   `json:"uid"`
		Name   *string `json:"name"`
		Level  *int64  `json:"level"`
		XP     *int64  `json:"xp"`
		Silver *int64  `json:"silver"`
		Gold   *int64  `json:"gold"`
		Bank   *int64  `json:"bank"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := s.db.GetProfile(ctx, req.UID)
	if err != nil {
		httpError(w, 404, "玩家不存在")
		return
	}
	apply := func(cur int64, nv *int64) int64 {
		if nv != nil {
			return *nv
		}
		return cur
	}
	name := p.Name
	if req.Name != nil && *req.Name != "" {
		name = *req.Name
	}
	// 等级和经验必须保持一致：只改等级时按进度曲线回填对应经验；
	// 否则下一次经验结算会按累计经验重新计算等级。
	lvl, xpAfter := gameserver.AdminResolveLevelXP(req.Level, req.XP, p.Level, p.XP)
	_, err = s.db.SQL.ExecContext(ctx, `
		UPDATE profile SET name=?, level=?, xp=?, silver=?, gold=?, bank=?, updated_ts=?
		WHERE uid=?`,
		name, lvl, xpAfter,
		apply(p.Silver, req.Silver), apply(p.Gold, req.Gold), apply(p.Bank, req.Bank),
		float64(time.Now().Unix()), req.UID)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	detail, _ := json.Marshal(req)
	_ = s.db.AuditLogWrite(ctx, adminOf(r), "player_update", strconv.FormatInt(req.UID, 10), string(detail))
	_ = s.db.QueuePush(ctx, mustJSON(map[string]any{"op": "refresh_player", "uid": req.UID}))
	writeJSON(w, map[string]any{"ok": true})
}

// handleGrantItem 将发物品指令写入共享队列，由主服务器执行以保持在线状态一致。
func (s *Server) handleGrantItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID    int64  `json:"uid"`
		ItemID string `json:"item_id"`
		Qty    int64  `json:"qty"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 || req.ItemID == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if req.Qty <= 0 {
		req.Qty = 1
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{
		"op": "grant_item", "uid": req.UID, "item_id": req.ItemID, "qty": req.Qty,
	}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "grant_item",
		strconv.FormatInt(req.UID, 10), fmt.Sprintf("%s x%d", req.ItemID, req.Qty))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleKick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID int64 `json:"uid"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "kick", "uid": req.UID}))
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "kick", strconv.FormatInt(req.UID, 10), "")
	writeJSON(w, map[string]any{"ok": true})
}

// handleGrantMembership 开通或续期会员。
//
// 参数 days <= 0 表示长期；续期从原到期日顺延（见 store.GrantMembership）。
func (s *Server) handleGrantMembership(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID  int64 `json:"uid"`
		Days int   `json:"days"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	exp, err := s.db.GrantMembership(r.Context(), req.UID, req.Days)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "membership.grant",
		strconv.FormatInt(req.UID, 10), fmt.Sprintf("days=%d exp=%d", req.Days, exp))
	writeJSON(w, map[string]any{
		"ok": true, "membership": 1, "membership_expire_at": exp,
		"days_left": (exp - time.Now().Unix()) / 86400,
	})
}

// handleRevokeMembership 取消会员。
func (s *Server) handleRevokeMembership(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID int64 `json:"uid"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.RevokeMembership(r.Context(), req.UID); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "membership.revoke",
		strconv.FormatInt(req.UID, 10), "")
	writeJSON(w, map[string]any{"ok": true, "membership": 0})
}

func (s *Server) handleBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID    int64 `json:"uid"`
		Banned bool  `json:"banned"`
	}
	if err := readJSON(r, &req); err != nil || req.UID == 0 {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.SetBanned(r.Context(), req.UID, req.Banned); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	action := "unban"
	if req.Banned {
		action = "ban"
		_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "kick", "uid": req.UID}))
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), action, strconv.FormatInt(req.UID, 10), "")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	entries, err := s.db.AuditList(r.Context(), limit)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, entries)
}

// ---- HTTP 辅助 ----

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var _ = logging.Attrs
