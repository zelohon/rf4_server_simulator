package admin

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 账号管理：客户端登录框里的 login / password 对应一个固定 uid。
// 密码与客户端一致，按 SHA1(明文) 存储；接口也接受 40 位 hex 直接写入。

const accountUIDBase = 1000001

func hashPassword(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) == 40 && isHexStr(v) {
		return strings.ToLower(v)
	}
	sum := sha1.Sum([]byte(v))
	return hex.EncodeToString(sum[:])
}

// profileNameFromLogin 与网关一致：邮箱取 @ 前一段，截断到 20 个字符。
func profileNameFromLogin(login string) string {
	n := strings.TrimSpace(login)
	if i := strings.IndexByte(n, '@'); i > 0 {
		n = n[:i]
	}
	r := []rune(n)
	if len(r) > 20 {
		r = r[:20]
	}
	if len(r) == 0 {
		return "player"
	}
	return string(r)
}

// profileEmailFromLogin 由登录名生成档案邮箱；客户端用这个字段判断是否提示绑定。
func profileEmailFromLogin(login string) string {
	n := strings.TrimSpace(login)
	if strings.Contains(n, "@") {
		return n
	}
	if n == "" {
		return ""
	}
	return n + "@rf4.local"
}

func isHexStr(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

type accountRow struct {
	Login         string  `json:"login"`
	UID           int64   `json:"uid"`
	CreatedTS     float64 `json:"created_ts"`
	LastLoginTS   float64 `json:"last_login_ts"`
	LastIP        string  `json:"last_ip"`
	ClientVersion string  `json:"client_version"`
	Banned        bool    `json:"banned"`
	Note          string  `json:"note"`
	PlayerName    string  `json:"player_name"`
	Level         int64   `json:"level"`
}

func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.SQL.QueryContext(r.Context(), `
		SELECT a.login, a.uid, a.created_ts, a.last_login_ts, a.last_ip,
		       a.client_version, a.banned, a.note,
		       COALESCE(p.name,''), COALESCE(p.level,0)
		FROM gateway_account a
		LEFT JOIN profile p ON p.uid = a.uid
		ORDER BY a.uid`)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := make([]accountRow, 0, 16)
	for rows.Next() {
		var a accountRow
		var banned int
		if err := rows.Scan(&a.Login, &a.UID, &a.CreatedTS, &a.LastLoginTS,
			&a.LastIP, &a.ClientVersion, &banned, &a.Note,
			&a.PlayerName, &a.Level); err != nil {
			httpError(w, 500, err.Error())
			return
		}
		a.Banned = banned != 0
		out = append(out, a)
	}
	writeJSON(w, map[string]any{"accounts": out})
}

func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
		Note     string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, "参数错误")
		return
	}
	login := strings.TrimSpace(req.Login)
	if login == "" || strings.TrimSpace(req.Password) == "" {
		httpError(w, 400, "账号和密码不能为空")
		return
	}
	uid, err := s.nextAccountUID(r.Context())
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	now := float64(time.Now().Unix())
	if _, err := s.db.SQL.ExecContext(r.Context(), `
		INSERT INTO gateway_account(login, password_sha1, uid, created_ts,
			last_login_ts, last_ip, client_version, banned, note)
		VALUES(?,?,?,?,0,'','',0,?)`,
		login, hashPassword(req.Password), uid, now, req.Note); err != nil {
		httpError(w, 409, "账号已存在或 uid 冲突："+err.Error())
		return
	}
	// 预建档案并直接用账号名当角色名，控制台和游戏内都显示账号名。
	if _, err := s.db.EnsureProfile(r.Context(), uid); err != nil {
		s.log.l.Warn("账号已建但档案初始化失败", "login", login, "uid", uid, "err", err)
	} else if _, err := s.db.SQL.ExecContext(r.Context(),
		`UPDATE profile SET name=?, updated_ts=? WHERE uid=? AND name='player'`,
		profileNameFromLogin(login), float64(time.Now().Unix()), uid); err != nil {
		s.log.l.Warn("设置角色名失败", "login", login, "uid", uid, "err", err)
	} else if _, err := s.db.SQL.ExecContext(r.Context(),
		`UPDATE profile SET email=? WHERE uid=? AND COALESCE(email,'')=''`,
		profileEmailFromLogin(login), uid); err != nil {
		s.log.l.Warn("设置账号邮箱失败", "login", login, "uid", uid, "err", err)
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_create", login, "")
	writeJSON(w, map[string]any{"ok": true, "uid": uid})
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Login) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		httpError(w, 400, "密码不能为空")
		return
	}
	res, err := s.db.SQL.ExecContext(r.Context(),
		`UPDATE gateway_account SET password_sha1=? WHERE login=?`,
		hashPassword(req.Password), req.Login)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpError(w, 404, "账号不存在")
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_password", req.Login, "")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string  `json:"login"`
		Password string  `json:"password"`
		Note     *string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Login) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	sets := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if strings.TrimSpace(req.Password) != "" {
		sets = append(sets, "password_sha1=?")
		args = append(args, hashPassword(req.Password))
	}
	if req.Note != nil {
		sets = append(sets, "note=?")
		args = append(args, *req.Note)
	}
	if len(sets) == 0 {
		httpError(w, 400, "没有需要修改的内容")
		return
	}
	args = append(args, req.Login)
	res, err := s.db.SQL.ExecContext(r.Context(),
		"UPDATE gateway_account SET "+strings.Join(sets, ", ")+" WHERE login=?", args...)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpError(w, 404, "账号不存在")
		return
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_update", req.Login, "")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAccountBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login  string `json:"login"`
		Banned bool   `json:"banned"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Login) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	banned := 0
	if req.Banned {
		banned = 1
	}
	res, err := s.db.SQL.ExecContext(r.Context(),
		`UPDATE gateway_account SET banned=? WHERE login=?`, banned, req.Login)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpError(w, 404, "账号不存在")
		return
	}
	action := "account_unban"
	if req.Banned {
		action = "account_ban"
		var uid int64
		_ = s.db.SQL.QueryRowContext(r.Context(),
			`SELECT uid FROM gateway_account WHERE login=?`, req.Login).Scan(&uid)
		if uid > 0 {
			_ = s.db.QueuePush(r.Context(), mustJSON(map[string]any{"op": "kick", "uid": uid}))
		}
	}
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), action, req.Login, "")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login string `json:"login"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Login) == "" {
		httpError(w, 400, "参数错误")
		return
	}
	res, err := s.db.SQL.ExecContext(r.Context(),
		`DELETE FROM gateway_account WHERE login=?`, req.Login)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpError(w, 404, "账号不存在")
		return
	}
	// 档案保留：删号不删角色数据，避免误删无法恢复。
	_ = s.db.AuditLogWrite(r.Context(), adminOf(r), "account_delete", req.Login, "")
	writeJSON(w, map[string]any{"ok": true})
}

// nextAccountUID 与网关共用 kv 计数器，保证两边分配的 uid 不冲突。
func (s *Server) nextAccountUID(ctx context.Context) (int64, error) {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// 与网关一致：首个账号接管既有默认档案，避免升级后老角色丢失。
	var used int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM gateway_account`).Scan(&used); err != nil {
		return 0, err
	}
	if used == 0 {
		var hasProfile int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM profile WHERE uid=?`, accountUIDBase).Scan(&hasProfile); err != nil {
			return 0, err
		}
		if hasProfile > 0 {
			const key = "account_uid_seq"
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=?`,
				key, strconv.Itoa(accountUIDBase), strconv.Itoa(accountUIDBase)); err != nil {
				return 0, err
			}
			if err := tx.Commit(); err != nil {
				return 0, err
			}
			return accountUIDBase, nil
		}
	}

	const key = "account_uid_seq"
	var cur int64
	err = tx.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM kv WHERE k=?`, key).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		var maxProfile int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(uid),0) FROM profile`).Scan(&maxProfile); err != nil {
			return 0, err
		}
		cur = maxProfile
		if cur < accountUIDBase-1 {
			cur = accountUIDBase - 1
		}
	} else if err != nil {
		return 0, err
	}
	next := cur + 1
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=?`,
		key, strconv.FormatInt(next, 10), strconv.FormatInt(next, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}
