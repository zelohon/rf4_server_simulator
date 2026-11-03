package gateway

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// 账号相关错误（登录流程据此给出不同应答）。
var (
	ErrAccountNotFound    = errors.New("账号不存在")
	ErrBadPassword        = errors.New("密码错误")
	ErrAccountBanned      = errors.New("账号已封禁")
	ErrMissingCredentials = errors.New("请输入账号和密码")
)

// emptyPasswordSHA1 是空字符串的 SHA1：客户端密码留空时会发送这个值。
const emptyPasswordSHA1 = "d41d8cd98f00b204e9800998ecf8427e"

// Account 网关账号：客户端登录框里的邮箱/账号映射到独立 uid。
type Account struct {
	Login         string `json:"login"`
	PasswordSHA1  string `json:"password_sha1,omitempty"`
	Userid        int64  `json:"uid"`
	CreatedAt     int64  `json:"created_ts"`
	LastLoginAt   int64  `json:"last_login_ts"`
	LastIP        string `json:"last_ip"`
	ClientVersion string `json:"client_version"`
	Banned        bool   `json:"banned"`
	Note          string `json:"note"`
}

// HashPassword 把客户端上送的凭据规范化成 SHA1 hex。
// 客户端发的是 SHA1(password)（40 位 hex）；若收到明文则在此补做一次。
func HashPassword(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) == 40 && isHex(v) {
		return strings.ToLower(v)
	}
	sum := sha1.Sum([]byte(v))
	return hex.EncodeToString(sum[:])
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// ---- SQLite 实现 ----

const accountColumns = `login, password_sha1, uid, created_ts, last_login_ts,
	last_ip, client_version, banned, note`

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var banned int
	err := row.Scan(&a.Login, &a.PasswordSHA1, &a.Userid, &a.CreatedAt,
		&a.LastLoginAt, &a.LastIP, &a.ClientVersion, &banned, &a.Note)
	a.Banned = banned != 0
	return a, err
}

func (s *SQLiteStore) LookupAccount(ctx context.Context, login string) (Account, bool, error) {
	row := s.DB.SQL.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM gateway_account WHERE login=?`, login)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	return a, true, nil
}

func (s *SQLiteStore) CreateAccount(ctx context.Context, a Account) error {
	_, err := s.DB.SQL.ExecContext(ctx, `
		INSERT INTO gateway_account(login, password_sha1, uid, created_ts,
			last_login_ts, last_ip, client_version, banned, note)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		a.Login, a.PasswordSHA1, a.Userid, a.CreatedAt, a.LastLoginAt,
		a.LastIP, a.ClientVersion, boolInt(a.Banned), a.Note)
	if err != nil {
		return err
	}
	// 顺手建档，并把默认昵称改成账号名：登录 edge1 进游戏就叫 edge1。
	if _, err := s.DB.EnsureProfile(ctx, a.Userid); err != nil {
		s.log.Warn("账号已建但档案初始化失败", "login", a.Login, "uid", a.Userid, "err", err)
		return nil
	}
	name := ProfileNameFromLogin(a.Login)
	if _, err := s.DB.SQL.ExecContext(ctx,
		`UPDATE profile SET name=?, updated_ts=? WHERE uid=? AND name='player'`,
		name, float64(time.Now().Unix()), a.Userid); err != nil {
		s.log.Warn("设置角色名失败", "login", a.Login, "uid", a.Userid, "err", err)
	}
	// 邮箱留空会让客户端每次登录都弹"保护您的游戏资料安全"，
	// 直接用账号名补一个本地邮箱，客户端据此认为已绑定。
	if _, err := s.DB.SQL.ExecContext(ctx,
		`UPDATE profile SET email=? WHERE uid=? AND COALESCE(email,'')=''`,
		ProfileEmailFromLogin(a.Login), a.Userid); err != nil {
		s.log.Warn("设置账号邮箱失败", "login", a.Login, "uid", a.Userid, "err", err)
	}
	return nil
}

// ProfileEmailFromLogin 由登录名生成档案邮箱：已是邮箱就直接用，否则补 @rf4.local。
func ProfileEmailFromLogin(login string) string {
	n := strings.TrimSpace(login)
	if strings.Contains(n, "@") {
		return n
	}
	if n == "" {
		return ""
	}
	return n + "@rf4.local"
}

// ProfileNameFromLogin 由登录名生成默认角色名：
// 邮箱取 @ 前一段，长度按字符截断到 20，避免超出客户端的昵称上限。
func ProfileNameFromLogin(login string) string {
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

func (s *SQLiteStore) TouchAccount(ctx context.Context, login, ip, version string) error {
	_, err := s.DB.SQL.ExecContext(ctx,
		`UPDATE gateway_account SET last_login_ts=?, last_ip=?, client_version=? WHERE login=?`,
		time.Now().Unix(), ip, version, login)
	return err
}

func (s *SQLiteStore) DeleteAccount(ctx context.Context, login string) error {
	_, err := s.DB.SQL.ExecContext(ctx, `DELETE FROM gateway_account WHERE login=?`, login)
	return err
}

func (s *SQLiteStore) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.SQL.QueryContext(ctx,
		`SELECT `+accountColumns+` FROM gateway_account ORDER BY uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SetAccountPassword(ctx context.Context, login, sha1Hex string) error {
	res, err := s.DB.SQL.ExecContext(ctx,
		`UPDATE gateway_account SET password_sha1=? WHERE login=?`, sha1Hex, login)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

func (s *SQLiteStore) SetAccountBanned(ctx context.Context, login string, banned bool) error {
	res, err := s.DB.SQL.ExecContext(ctx,
		`UPDATE gateway_account SET banned=? WHERE login=?`, boolInt(banned), login)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// NextAccountUserid 为新账号分配 uid：避开已有 profile 与已分配账号，从 base 起递增。
func (s *SQLiteStore) NextAccountUserid(ctx context.Context, base int64) (int64, error) {
	tx, err := s.DB.SQL.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// 首个账号直接接管既有默认档案（userid_start），避免启用账号后
	// 老玩家的角色“凭空消失”：升级后第一次登录仍能拿回原来的档。
	var used int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM gateway_account`).Scan(&used); err != nil {
		return 0, err
	}
	if used == 0 && base > 0 {
		var hasProfile int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM profile WHERE uid=?`, base).Scan(&hasProfile); err != nil {
			return 0, err
		}
		if hasProfile > 0 {
			const key = "account_uid_seq"
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=?`,
				key, int64ToStr(base), int64ToStr(base)); err != nil {
				return 0, err
			}
			if err := tx.Commit(); err != nil {
				return 0, err
			}
			return base, nil
		}
	}

	const key = "account_uid_seq"
	var cur int64
	err = tx.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM kv WHERE k=?`, key).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		cur = 0
		// 首次：跳过已存在的玩家档案
		var maxProfile int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(uid),0) FROM profile`).Scan(&maxProfile); err != nil {
			return 0, err
		}
		cur = maxProfile
		if cur < base-1 {
			cur = base - 1
		}
	} else if err != nil {
		return 0, err
	}
	next := cur + 1
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=?`,
		key, int64ToStr(next), int64ToStr(next)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ---- MemStore 实现（测试用）----

func (m *MemStore) LookupAccount(_ context.Context, login string) (Account, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[login]
	return a, ok, nil
}

func (m *MemStore) CreateAccount(_ context.Context, a Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accounts == nil {
		m.accounts = map[string]Account{}
	}
	if _, exists := m.accounts[a.Login]; exists {
		return errors.New("账号已存在")
	}
	m.accounts[a.Login] = a
	if a.Userid > m.accountUid {
		m.accountUid = a.Userid
	}
	return nil
}

func (m *MemStore) TouchAccount(_ context.Context, login, ip, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[login]
	if !ok {
		return ErrAccountNotFound
	}
	a.LastLoginAt = time.Now().Unix()
	a.LastIP = ip
	a.ClientVersion = version
	m.accounts[login] = a
	return nil
}

func (m *MemStore) DeleteAccount(_ context.Context, login string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accounts, login)
	return nil
}

func (m *MemStore) ListAccounts(_ context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		out = append(out, a)
	}
	return out, nil
}

func (m *MemStore) SetAccountPassword(_ context.Context, login, sha1Hex string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[login]
	if !ok {
		return ErrAccountNotFound
	}
	a.PasswordSHA1 = sha1Hex
	m.accounts[login] = a
	return nil
}

func (m *MemStore) SetAccountBanned(_ context.Context, login string, banned bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[login]
	if !ok {
		return ErrAccountNotFound
	}
	a.Banned = banned
	m.accounts[login] = a
	return nil
}

func (m *MemStore) NextAccountUserid(_ context.Context, base int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accountUid < base-1 {
		m.accountUid = base - 1
	}
	m.accountUid++
	return m.accountUid, nil
}
