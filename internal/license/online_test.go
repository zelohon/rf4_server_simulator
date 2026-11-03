package license

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

func withPublicKey(t *testing.T, pub ed25519.PublicKey) {
	t.Helper()
	old := publicKeyHex
	publicKeyHex = hexEncode(pub)
	t.Cleanup(func() { publicKeyHex = old })
}

func writeTicket(t *testing.T, dir string, p *Payload, priv ed25519.PrivateKey) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	tk, err := Sign(p, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(tk, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "data", ticketFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSealCodeRoundTrip 卡密密文必须能原样还原。
func TestSealCodeRoundTrip(t *testing.T) {
	const code = "FSODA6E2C41A6E23688AFE42860AFDD1"
	enc, err := SealCode(code)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if strings.Contains(enc, code) || strings.Contains(enc, "FSODA") {
		t.Fatalf("密文里出现了卡密片段: %s", enc)
	}
	got, err := OpenCode(enc)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != code {
		t.Fatalf("还原不一致: got=%q want=%q", got, code)
	}

	enc2, _ := SealCode(code)
	if enc == enc2 {
		t.Fatal("两次加密结果相同，nonce 未随机")
	}
}

func TestSealCodeEmpty(t *testing.T) {
	enc, err := SealCode("")
	if err != nil || enc != "" {
		t.Fatalf("空卡密应返回空串: enc=%q err=%v", enc, err)
	}
	if _, err := OpenCode(""); err != ErrNoSecret {
		t.Fatalf("空密文应返回 ErrNoSecret，得到 %v", err)
	}
}

// TestGuardOnlineOfflinePass 连不上平台时按有效期放行。
func TestGuardOnlineOfflinePass(t *testing.T) {
	pub, priv := newKeyPair(t)
	withPublicKey(t, pub)

	old := onlineCheck
	onlineCheck = func(string, time.Duration) (OnlineStatus, string) {
		return OnlineOffline, "network"
	}
	t.Cleanup(func() { onlineCheck = old })

	dir := t.TempDir()
	enc, _ := SealCode("SOME-CODE")
	writeTicket(t, dir, &Payload{
		Lic: LicID("SOME-CODE"), Enc: enc,
		ExpireAt: time.Now().Add(time.Hour).Unix(),
	}, priv)

	if err := GuardOnline(dir, time.Second); err != nil {
		t.Fatalf("离线应放行，得到 %v", err)
	}
}

// TestGuardOnlineRejected 平台明确判定卡密不可用时必须拒绝。
func TestGuardOnlineRejected(t *testing.T) {
	pub, priv := newKeyPair(t)
	withPublicKey(t, pub)

	old := onlineCheck
	onlineCheck = func(string, time.Duration) (OnlineStatus, string) {
		return OnlineInvalid, "-401"
	}
	t.Cleanup(func() { onlineCheck = old })

	dir := t.TempDir()
	enc, _ := SealCode("SOME-CODE")
	writeTicket(t, dir, &Payload{
		Lic: LicID("SOME-CODE"), Enc: enc,
		ExpireAt: time.Now().Add(time.Hour).Unix(),
	}, priv)

	if err := GuardOnline(dir, time.Second); err != ErrOnlineRejected {
		t.Fatalf("应返回 ErrOnlineRejected，得到 %v", err)
	}
}

// TestGuardOnlineValid 平台确认有效时放行。
func TestGuardOnlineValid(t *testing.T) {
	pub, priv := newKeyPair(t)
	withPublicKey(t, pub)

	old := onlineCheck
	onlineCheck = func(string, time.Duration) (OnlineStatus, string) {
		return OnlineValid, "ok"
	}
	t.Cleanup(func() { onlineCheck = old })

	dir := t.TempDir()
	enc, _ := SealCode("SOME-CODE")
	writeTicket(t, dir, &Payload{
		Lic: LicID("SOME-CODE"), Enc: enc,
		ExpireAt: time.Now().Add(time.Hour).Unix(),
	}, priv)

	if err := GuardOnline(dir, time.Second); err != nil {
		t.Fatalf("有效卡密应放行，得到 %v", err)
	}
}

// TestGuardOnlineNoSecret 纯离线票据不联网，也不该被平台结论影响。
func TestGuardOnlineNoSecret(t *testing.T) {
	pub, priv := newKeyPair(t)
	withPublicKey(t, pub)

	called := false
	old := onlineCheck
	onlineCheck = func(string, time.Duration) (OnlineStatus, string) {
		called = true
		return OnlineInvalid, "-401"
	}
	t.Cleanup(func() { onlineCheck = old })

	dir := t.TempDir()
	writeTicket(t, dir, &Payload{
		Lic: LicID("X"), ExpireAt: time.Now().Add(time.Hour).Unix(),
	}, priv)

	if err := GuardOnline(dir, time.Second); err != nil {
		t.Fatalf("纯离线票据应放行，得到 %v", err)
	}
	if called {
		t.Fatal("没有卡密密文时不应发起在线请求")
	}
}

// TestGuardOnlineExpiredFirst 已过期的票据不应再浪费一次在线请求。
func TestGuardOnlineExpiredFirst(t *testing.T) {
	pub, priv := newKeyPair(t)
	withPublicKey(t, pub)

	called := false
	old := onlineCheck
	onlineCheck = func(string, time.Duration) (OnlineStatus, string) {
		called = true
		return OnlineValid, "ok"
	}
	t.Cleanup(func() { onlineCheck = old })

	dir := t.TempDir()
	enc, _ := SealCode("SOME-CODE")
	writeTicket(t, dir, &Payload{
		Lic: LicID("SOME-CODE"), Enc: enc,
		ExpireAt: time.Now().Add(-time.Hour).Unix(),
	}, priv)

	if err := GuardOnline(dir, time.Second); err != ErrExpired {
		t.Fatalf("过期票据应返回 ErrExpired，得到 %v", err)
	}
	if called {
		t.Fatal("本地已判定过期，不应再请求平台")
	}
}

// TestCheckOnlineTimeout 超时必须归类为离线，不能误判成卡密无效。
func TestCheckOnlineTimeout(t *testing.T) {
	st, _ := CheckOnline("SOME-CODE", time.Nanosecond)
	if st != OnlineOffline {
		t.Fatalf("超时应返回 offline，得到 %v", st)
	}
}
