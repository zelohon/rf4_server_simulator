// Package license 实现授权票据的签发与校验。
//
// 票据由离线工具（cmd/license-gen）用 Ed25519 私钥签发，服务端只内置公钥。
// 因此即使二进制被逆向，也无法伪造新票据；删除票据或篡改内容都会被拒绝。
package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrNoTicket 表示授权文件不存在。
	ErrNoTicket = errors.New("license: ticket missing")
	// ErrBadSignature 表示签名不合法或格式损坏。
	ErrBadSignature = errors.New("license: bad signature")
	// ErrExpired 表示票据已过期。
	ErrExpired = errors.New("license: expired")
	// ErrOnlineRejected 表示在线复核明确判定卡密不可用。
	ErrOnlineRejected = errors.New("license: online rejected")
)

// TicketVersion 是当前票据格式版本。
//
// v4 起票据落盘只保留「版本 + 密文 + 签名」三样东西：
// 卡密、卡密标识、签发时间、到期时间全部收进密文（见 Payload）。
// 打开文件看不到任何可读信息，也看不出有效期还剩多久。
const TicketVersion = 4

// ticketFile 是票据在 <root>/data 下的文件名。
//
// 刻意用了中性的名字：授权是内部约束，不对使用者展示，
// 文件名里出现 license/auth 会直接提示"这里有授权校验"。
const ticketFile = "runtime.dat"

// Ticket 是落盘结构。
//
// Blob 是 Payload 的 AES-256-GCM 密文；Sig 是对 "版本|密文" 的 Ed25519 签名。
// 先验签再解密：密文被替换或篡改会先被签名挡住，不需要碰密钥。
type Ticket struct {
	Version int    `json:"v"`
	Blob    string `json:"b"`
	Sig     string `json:"sig"`
}

// Payload 是票据的实际内容，序列化后整体加密。
type Payload struct {
	Lic      string `json:"lic"`           // 卡密 SHA-256 前 12 位，仅用于人工核对
	Enc      string `json:"enc,omitempty"` // 卡密密文，供启动时在线复核
	IssuedAt int64  `json:"issued_at"`
	ExpireAt int64  `json:"expire_at"`
	Note     string `json:"note,omitempty"`
}

func (t *Ticket) signedBytes() []byte {
	return []byte(fmt.Sprintf("%d|%s", t.Version, t.Blob))
}

// LicID 把卡密折算成票据里使用的标识（SHA-256 前 12 位）。
func LicID(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])[:12]
}

// Sign 加密内容并签名，返回可落盘的票据。
func Sign(p *Payload, priv ed25519.PrivateKey) (*Ticket, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("license: bad private key")
	}
	blob, err := SealPayload(p)
	if err != nil {
		return nil, err
	}
	t := &Ticket{Version: TicketVersion, Blob: blob}
	t.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, t.signedBytes()))
	return t, nil
}

// Verify 校验签名与有效期，通过后返回解密出来的内容。
func Verify(t *Ticket, pub ed25519.PublicKey, now time.Time) (*Payload, error) {
	if t.Version != TicketVersion {
		return nil, ErrBadSignature
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(t.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrBadSignature
	}
	if !ed25519.Verify(pub, t.signedBytes(), sig) {
		return nil, ErrBadSignature
	}
	p, err := OpenPayload(t.Blob)
	if err != nil {
		return nil, ErrBadSignature
	}
	if now.Unix() > p.ExpireAt {
		return nil, ErrExpired
	}
	return p, nil
}

// Load 从文件读取票据。
func Load(path string) (*Ticket, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrNoTicket
	}
	var t Ticket
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, ErrBadSignature
	}
	return &t, nil
}

// Guard 只做本地校验：签名、格式、有效期。
//
// gateway 与 gameserver 用这个——它们只消费凭证，不联网，
// 保证断网或平台故障时整套服务不会集体起不来。
func Guard(rootDir string) error {
	if DevMode(rootDir) {
		return nil
	}
	t, err := Load(filepath.Join(rootDir, "data", ticketFile))
	if err != nil {
		return err
	}
	_, err = Verify(t, PublicKey(), time.Now())
	return err
}

// GuardWith 用指定公钥校验，便于测试与灰度切换。
func GuardWith(pub ed25519.PublicKey, rootDir string) error {
	if DevMode(rootDir) {
		return nil
	}
	t, err := Load(filepath.Join(rootDir, "data", ticketFile))
	if err != nil {
		return err
	}
	_, err = Verify(t, pub, time.Now())
	return err
}

// GuardOnline 本地校验 + 在线复核，供 admin 使用。
//
// 判定顺序：
//  1. 本地票据无效（缺失/签名错/已过期）→ 拒绝，不联网；
//  2. 票据里没有卡密密文（纯离线票）→ 放行，只受有效期约束；
//  3. 连不上平台 → 放行（离线场景），仍受有效期约束；
//  4. 平台明确判定卡密不可用 → 拒绝。
//
// 这样「有网受平台控制、无网受时间限制」同时成立。
func GuardOnline(rootDir string, timeout time.Duration) error {
	if DevMode(rootDir) {
		return nil
	}
	t, err := Load(filepath.Join(rootDir, "data", ticketFile))
	if err != nil {
		return err
	}
	p, err := Verify(t, PublicKey(), time.Now())
	if err != nil {
		return err
	}
	code, err := OpenCode(p.Enc)
	if err != nil {
		// 没有密文说明是纯离线票据，按有效期放行。
		return nil
	}
	status, _ := onlineCheck(code, timeout)
	if status == OnlineInvalid {
		return ErrOnlineRejected
	}
	return nil
}

// GuardConfig 按配置文件路径推导项目根目录后再校验授权。
//
// 三个服务都以 “项目根/config.yaml” 的方式启动，票据固定放在 <root>/data/runtime.dat。
func GuardConfig(configPath string) error {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	return Guard(filepath.Dir(abs))
}

// GuardConfigOnline 按配置文件路径推导项目根目录后做在线复核。
func GuardConfigOnline(configPath string, timeout time.Duration) error {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	return GuardOnline(filepath.Dir(abs), timeout)
}

// Remaining 返回票据剩余时长，便于内部巡检判断是否需要提前预警。
func Remaining(rootDir string) (time.Duration, error) {
	return RemainingWith(PublicKey(), rootDir)
}

// RemainingWith 用指定公钥解读剩余时长，便于测试。
func RemainingWith(pub ed25519.PublicKey, rootDir string) (time.Duration, error) {
	t, err := Load(filepath.Join(rootDir, "data", ticketFile))
	if err != nil {
		return 0, err
	}
	p, err := Verify(t, pub, time.Now())
	if err != nil {
		return 0, err
	}
	return time.Until(time.Unix(p.ExpireAt, 0)), nil
}

// DevMode 允许开发者在本机跳过校验，方便调试。
//
// 触发方式：环境变量 RF4_SKIP_LICENSE=1，或存在 data/.devmode 文件。
func DevMode(rootDir string) bool {
	if os.Getenv("RF4_SKIP_LICENSE") == "1" {
		return true
	}
	if _, err := os.Stat(filepath.Join(rootDir, "data", ".devmode")); err == nil {
		return true
	}
	return false
}
