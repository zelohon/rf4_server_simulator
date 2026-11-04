package gateway

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"rf4server/internal/config"
	"rf4server/internal/metrics"
	"rf4server/internal/proto"
)

// 指标（NewLoginServer 里注册；nil 时为 no-op 计数器）。
var (
	loginRequests prometheus.Counter
	loginIssued   prometheus.Counter
	loginErrors   prometheus.Counter
)

// GreetingUUID 客户端校验的服务端问候（协议常量，实现在 proto.GreetingUUID）。
const GreetingUUID = proto.GreetingUUID

const loginXMLTpl = `<logon success="true"><host>%s</host><port>%d</port>` +
	`<region>%s</region><server>%s</server><userid>%d</userid>` +
	`<token>%s</token><logintoken>%s</logintoken>` +
	`<logintokenexpires>%d</logintokenexpires></logon>`

// legacyLoginXMLTpl 是旧版 CN 客户端在 7515 明文兼容口上要求的响应格式。
// 客户端按 <response> 根节点解析；返回 <logon> 会报 Invalid URI。
const legacyLoginXMLTpl = `<?xml version="1.0" encoding="UTF-8"?>
<response success="true"><token>%s</token><host>%s</host><port>%d</port>` +
	`<region>%s</region><userid>%d</userid><logintoken>%s</logintoken>` +
	`<logintokenexpires>%d</logintokenexpires></response>`

// LoginServer HTTPS 登录服务：任何路径都返回 <logon> XML（客户端打 /login.php 或 /steam.php）。
type LoginServer struct {
	cfg       config.Gateway
	store     Store
	log       *slog.Logger
	handler   http.Handler
	srv       *http.Server
	ln        net.Listener
	compatSrv *http.Server
	compatLn  net.Listener
	userid    int64 // 本进程内递增序号，共享序号不可用时使用
}

// NewLoginServer 构建登录服务（含观测指标注册）。
func NewLoginServer(cfg config.Gateway, store Store, log *slog.Logger, obs *metrics.Server) (*LoginServer, error) {
	s := &LoginServer{cfg: cfg, store: store, log: log}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 TLS 证书: %w", err)
	}
	// RF4 CN 客户端会把上游 URL 原样编码进路径，例如：
	// /lv54/https://api.rf4game.ru/login.php。http.ServeMux 会把其中的
	// 双斜杠清理为单斜杠并返回 307，客户端随后会因 URI 被破坏而登录失败。
	// 登录服务本来就接受任意路径，因此直接使用 HandlerFunc，保留 RequestURI。
	s.handler = http.HandlerFunc(s.handleLogin)
	s.srv = &http.Server{
		Addr:              cfg.LoginAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		},
	}
	if cfg.LoginCompatAddr != "" {
		s.compatSrv = &http.Server{
			Addr:              cfg.LoginCompatAddr,
			Handler:           s.handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
	}
	// 指标（无观测端口时用本地一次性注册表，避免 nil 指标 panic）
	if obs != nil {
		reg := obs.Registry()
		loginRequests = metrics.NewCounter(reg, "gateway_login_requests_total", "登录请求数")
		loginIssued = metrics.NewCounter(reg, "gateway_tokens_issued_total", "签发 token 数")
		loginErrors = metrics.NewCounter(reg, "gateway_login_errors_total", "登录处理错误数")
	} else {
		reg := prometheus.NewRegistry()
		loginRequests = metrics.NewCounter(reg, "gateway_login_requests_total", "登录请求数")
		loginIssued = metrics.NewCounter(reg, "gateway_tokens_issued_total", "签发 token 数")
		loginErrors = metrics.NewCounter(reg, "gateway_login_errors_total", "登录处理错误数")
	}
	return s, nil
}

// Start 创建 TLS listener 并开始服务；返回错误 channel 与绑定错误。
func (s *LoginServer) Start() (<-chan error, error) {
	ln, err := net.Listen("tcp", s.cfg.LoginAddr)
	if err != nil {
		return nil, fmt.Errorf("登录服务监听 %s: %w", s.cfg.LoginAddr, err)
	}
	s.ln = tls.NewListener(ln, s.srv.TLSConfig)
	errCh := make(chan error, 2)
	go func() {
		if err := s.srv.Serve(s.ln); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("登录服务 %s: %w", s.cfg.LoginAddr, err)
		}
	}()
	if s.compatSrv != nil {
		compatLn, err := net.Listen("tcp", s.cfg.LoginCompatAddr)
		if err != nil {
			_ = s.ln.Close()
			return nil, fmt.Errorf("登录兼容口监听 %s: %w", s.cfg.LoginCompatAddr, err)
		}
		s.compatLn = compatLn
		go func() {
			if err := s.compatSrv.Serve(s.compatLn); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("登录兼容口 %s: %w", s.cfg.LoginCompatAddr, err)
			}
		}()
	}
	return errCh, nil
}

// Addr 实际监听地址（Start 之后有效）。
func (s *LoginServer) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// CompatAddr 明文 HTTP 兼容口的实际监听地址；未启用时为空。
func (s *LoginServer) CompatAddr() string {
	if s.compatLn == nil {
		return ""
	}
	return s.compatLn.Addr().String()
}

// Shutdown 优雅停机。
func (s *LoginServer) Shutdown(ctx context.Context) error {
	var first error
	if s.compatSrv != nil {
		first = s.compatSrv.Shutdown(ctx)
	}
	if err := s.srv.Shutdown(ctx); first == nil {
		first = err
	}
	return first
}

func (s *LoginServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	loginRequests.Inc()
	q := r.URL.Query()
	login := strings.TrimSpace(q.Get("login"))
	password := q.Get("password")
	clientVersion := q.Get("version")
	clientID := q.Get("id")
	_ = r.Body.Close()

	if os.Getenv("RF4_LOGIN_PROBE") == "1" {
		s.log.Info("登录请求",
			"method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr,
			"host", r.Host, "login", login, "version", clientVersion,
			"id", clientID, "region", q.Get("region"), "locale", q.Get("locale"),
			"pwd_len", len(password), "ua", r.UserAgent())
	}

	// 账号模式：必须带账号与密码，且账号必须已存在（由控制台预先创建）。
	userid := s.cfg.UseridStart
	if s.cfg.AuthEnabled {
		if login == "" || password == "" || strings.EqualFold(password, emptyPasswordSHA1) {
			s.writeLoginFailure(w, r, login, ErrMissingCredentials)
			return
		}
		acct, err := s.authenticate(r.Context(), login, password, r.RemoteAddr, clientVersion)
		if err != nil {
			s.writeLoginFailure(w, r, login, err)
			return
		}
		userid = acct.Userid
	} else if userid <= 0 {
		userid, _ = s.nextUserid(r.Context())
	}
	info := TokenInfo{Userid: userid, IssuedAt: time.Now().Unix(), Region: s.cfg.Region}
	ttl := time.Duration(s.cfg.TokenTTL) * 24 * time.Hour
	expires := time.Now().Add(ttl).Unix()
	var token, logintoken, xml string
	if r.TLS == nil {
		// 旧版 CN 客户端会把 token 当 URI 片段使用，不能包含 '|' 或 '/'。
		// 这里沿用旧后端的 32hex token / 48hex logintoken，并登记到同一 SQLite。
		token = NewLegacyToken()
		logintoken = NewLegacyLoginToken()
		if err := s.store.SaveToken(r.Context(), token, info, ttl); err != nil {
			loginErrors.Inc()
			s.log.Error("保存兼容 token 失败", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := s.store.SaveToken(r.Context(), logintoken, info, ttl); err != nil {
			loginErrors.Inc()
			s.log.Error("保存兼容登录 token 失败", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		xml = fmt.Sprintf(legacyLoginXMLTpl,
			token, s.cfg.AdvertiseHost, s.cfg.AdvertisePort,
			s.cfg.Region, userid, logintoken, expires)
	} else {
		token = NewToken(userid)
		if err := s.store.SaveToken(r.Context(), token, info, ttl); err != nil {
			loginErrors.Inc()
			s.log.Error("保存 token 失败", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// HTTPS 作者通道继续使用 4 段 logintoken。
		logintoken = NewLogintoken(userid)
		xml = fmt.Sprintf(loginXMLTpl,
			s.cfg.AdvertiseHost, s.cfg.AdvertisePort,
			s.cfg.Region, s.cfg.ServerName, userid,
			token, logintoken, expires)
	}
	loginIssued.Inc()
	if r.TLS == nil {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(xml)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml))
	s.log.Info("登录",
		"method", r.Method, "request_uri", r.RequestURI, "path", r.URL.Path,
		"raw_path", r.URL.RawPath, "remote", r.RemoteAddr,
		"userid", userid, "region", s.cfg.Region, "realtime", s.cfg.AdvertiseHost)
}

// authenticate 校验或建立账号，返回该账号绑定的 uid。
func (s *LoginServer) authenticate(ctx context.Context, login, password, remote, version string) (Account, error) {
	hashed := HashPassword(password)
	acct, ok, err := s.store.LookupAccount(ctx, login)
	if err != nil {
		return Account{}, err
	}
	if ok {
		if acct.Banned {
			return Account{}, ErrAccountBanned
		}
		if !strings.EqualFold(acct.PasswordSHA1, hashed) {
			return Account{}, ErrBadPassword
		}
		_ = s.store.TouchAccount(ctx, login, remote, version)
		return acct, nil
	}
	if !s.cfg.AuthAutoRegister {
		return Account{}, ErrAccountNotFound
	}
	uid, err := s.store.NextAccountUserid(ctx, s.cfg.UseridStart)
	if err != nil {
		return Account{}, err
	}
	now := time.Now().Unix()
	fresh := Account{
		Login: login, PasswordSHA1: hashed, Userid: uid,
		CreatedAt: now, LastLoginAt: now, LastIP: remote, ClientVersion: version,
	}
	if err := s.store.CreateAccount(ctx, fresh); err != nil {
		// 并发下可能已被另一请求创建，回查一次按已存在账号处理。
		if again, ok2, err2 := s.store.LookupAccount(ctx, login); err2 == nil && ok2 {
			if !strings.EqualFold(again.PasswordSHA1, hashed) {
				return Account{}, ErrBadPassword
			}
			return again, nil
		}
		return Account{}, err
	}
	s.log.Info("新账号注册", "login", login, "userid", uid, "remote", remote)
	return fresh, nil
}

// writeLoginFailure 返回客户端可识别的失败应答（success="false"）。
func (s *LoginServer) writeLoginFailure(w http.ResponseWriter, r *http.Request, login string, err error) {
	loginErrors.Inc()
	reason := "登录失败"
	switch {
	case errors.Is(err, ErrBadPassword):
		reason = "账号或密码错误"
	case errors.Is(err, ErrAccountNotFound):
		reason = "账号不存在"
	case errors.Is(err, ErrAccountBanned):
		reason = "账号已被封禁"
	case errors.Is(err, ErrMissingCredentials):
		reason = "请输入账号和密码"
	}
	xml := fmt.Sprintf(`<logon success="false"><reason>%s</reason></logon>`, html.EscapeString(reason))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(xml)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml))
	s.log.Warn("登录失败", "login", login, "remote", r.RemoteAddr, "reason", reason)
}

func (s *LoginServer) nextUserid(ctx context.Context) (int64, error) {
	// 从共享存储获取下一个 userid；共享存储不可用时退化为进程内序号。
	if v, err := s.store.NextUserid(ctx); err == nil {
		return v, nil
	} else {
		s.log.Warn("共享 userid 序号不可用，退化进程内序号", "err", err)
	}
	return atomic.AddInt64(&s.userid, 1), nil
}

// NewToken 生成 4 段 token：hex(32B)|unix|userid|base64(168B)。
// 整串是 RC4 密钥，客户端会在 realtime 握手时回传。
func NewToken(userid int64) string {
	var h [32]byte
	_, _ = rand.Read(h[:])
	var b [168]byte
	_, _ = rand.Read(b[:])
	return strings.Join([]string{
		hex.EncodeToString(h[:]),
		strconv.FormatInt(time.Now().Unix(), 10),
		strconv.FormatInt(userid, 10),
		base64.StdEncoding.EncodeToString(b[:]),
	}, "|")
}

// NewLegacyToken 生成旧版 CN 客户端使用的 32 位十六进制 token。
func NewLegacyToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewLegacyLoginToken 生成旧版 CN 客户端使用的 48 位十六进制 logintoken。
func NewLegacyLoginToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewLogintoken 生成登录 XML 使用的短 token，供辅助 realtime 通道认证。
// 该 token 同时作为 RC4 密钥；第三段是玩家 uid。
func NewLogintoken(userid int64) string {
	var s [6]byte
	_, _ = rand.Read(s[:])
	var t [8]byte
	_, _ = rand.Read(t[:])
	return strings.Join([]string{
		"local",
		hex.EncodeToString(s[:]),
		strconv.FormatInt(userid, 10),
		hex.EncodeToString(t[:]),
	}, "|")
}

// ---- 局域网/Radmin 多账号：按来源 IP 分配 userid ----

var (
	ipUIDOnce sync.Once
	ipUIDMu   sync.Mutex
	ipUIDMap  = map[string]int64{}
)

func (s *LoginServer) useridForRemote(remote string) int64 {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	base := s.cfg.UseridStart
	if base <= 0 {
		base = 1000001
	}
	// 本机保持原固定 UID，避免现有存档换号。
	if host == "" || host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return base
	}
	ipUIDOnce.Do(func() {
		if b, err := os.ReadFile("gateway_ip_uids.json"); err == nil {
			_ = json.Unmarshal(b, &ipUIDMap)
		}
		if ipUIDMap == nil {
			ipUIDMap = map[string]int64{}
		}
	})
	ipUIDMu.Lock()
	defer ipUIDMu.Unlock()
	if uid, ok := ipUIDMap[host]; ok && uid > 0 {
		return uid
	}
	max := base
	for _, v := range ipUIDMap {
		if v > max {
			max = v
		}
	}
	uid := max + 1
	ipUIDMap[host] = uid
	if b, err := json.MarshalIndent(ipUIDMap, "", "  "); err == nil {
		_ = os.WriteFile("gateway_ip_uids.json", b, 0o644)
	}
	s.log.Info("LAN 账号分配", "ip", host, "userid", uid)
	return uid
}
