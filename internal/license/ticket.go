// Package license 实现离线授权票据的签发与校验。
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
)

// TicketVersion 是当前票据格式版本。
//
// v2 起不再在票据里保存卡密明文，只保存卡密的 SHA-256 前缀（Lic），
// 用于区分是哪张卡签发的。卡密只存在于签发者的机器上，绝不随包分发。
const TicketVersion = 2

// ticketFile 是票据在 <root>/data 下的文件名。
//
// 刻意用了中性的名字：授权是内部约束，不对使用者展示，
// 文件名里出现 license/auth 会直接提示"这里有授权校验"。
const ticketFile = "runtime.dat"

// Ticket 是离线授权凭证。
type Ticket struct {
	Version  int    `json:"v"`
	Lic      string `json:"lic"` // 卡密 SHA-256 前 12 位
	IssuedAt int64  `json:"issued_at"`
	ExpireAt int64  `json:"expire_at"`
	Note     string `json:"note,omitempty"`
	Sig      string `json:"sig"`
}

func (t *Ticket) payload() []byte {
	return []byte(fmt.Sprintf("%d|%s|%d|%d|%s",
		t.Version, t.Lic, t.IssuedAt, t.ExpireAt, t.Note))
}

// LicID 把卡密折算成票据里使用的标识（SHA-256 前 12 位）。
//
// 票据只带这个标识，不带卡密本身，所以把程序分发给客户时
// 即使有人打开这个文件也拿不到可用的卡密。
func LicID(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])[:12]
}

// Sign 用私钥签发票据。
func Sign(t *Ticket, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("license: bad private key")
	}
	t.Version = TicketVersion
	if t.IssuedAt == 0 {
		t.IssuedAt = time.Now().Unix()
	}
	t.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, t.payload()))
	return nil
}

// Verify 校验签名与有效期。
func Verify(t *Ticket, pub ed25519.PublicKey, now time.Time) error {
	if t.Version != TicketVersion {
		return ErrBadSignature
	}
	if len(pub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	sig, err := base64.StdEncoding.DecodeString(t.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, t.payload(), sig) {
		return ErrBadSignature
	}
	if now.Unix() > t.ExpireAt {
		return ErrExpired
	}
	return nil
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

// Guard 校验 <rootDir>/data/runtime.dat。
//
// 返回 nil 表示放行。调用方在失败时必须静默退出，不要向使用者展示
// 任何与授权相关的提示——这是产品要求，也避免给破解者提供定位线索。
func Guard(rootDir string) error {
	return GuardWith(PublicKey(), rootDir)
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
	return Verify(t, pub, time.Now())
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

// Remaining 返回票据剩余时长，便于内部巡检判断是否需要提前预警。
func Remaining(rootDir string) (time.Duration, error) {
	t, err := Load(filepath.Join(rootDir, "data", ticketFile))
	if err != nil {
		return 0, err
	}
	if err := Verify(t, PublicKey(), time.Now()); err != nil {
		return 0, err
	}
	return time.Until(time.Unix(t.ExpireAt, 0)), nil
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
