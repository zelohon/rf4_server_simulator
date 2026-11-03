package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerify(t *testing.T) {
	pub, priv := newKey(t)
	tk := &Ticket{Lic: LicID("TEST-CODE"), ExpireAt: time.Now().Add(24 * time.Hour).Unix()}
	if err := Sign(tk, priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(tk, pub, time.Now()); err != nil {
		t.Fatalf("合法票据应通过，得到 %v", err)
	}

	// 篡改到期时间
	bad := *tk
	bad.ExpireAt += 86400
	if err := Verify(&bad, pub, time.Now()); err != ErrBadSignature {
		t.Fatalf("篡改应被拒绝，得到 %v", err)
	}

	// 篡改卡密
	bad2 := *tk
	bad2.Lic = LicID("OTHER")
	if err := Verify(&bad2, pub, time.Now()); err != ErrBadSignature {
		t.Fatalf("篡改卡密应被拒绝，得到 %v", err)
	}

	// 错误公钥
	otherPub, _ := newKey(t)
	if err := Verify(tk, otherPub, time.Now()); err != ErrBadSignature {
		t.Fatalf("换公钥应被拒绝，得到 %v", err)
	}
}

func TestExpired(t *testing.T) {
	pub, priv := newKey(t)
	tk := &Ticket{Lic: LicID("X"), ExpireAt: time.Now().Add(-time.Minute).Unix()}
	if err := Sign(tk, priv); err != nil {
		t.Fatal(err)
	}
	if err := Verify(tk, pub, time.Now()); err != ErrExpired {
		t.Fatalf("过期票据应被拒绝，得到 %v", err)
	}
}

func TestGuardWith(t *testing.T) {
	pub, priv := newKey(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 无票据
	if err := GuardWith(pub, dir); err != ErrNoTicket {
		t.Fatalf("缺少票据应拒绝，得到 %v", err)
	}

	// 正常票据
	tk := &Ticket{Lic: LicID("C"), ExpireAt: time.Now().Add(48 * time.Hour).Unix()}
	if err := Sign(tk, priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(tk)
	path := filepath.Join(dir, "data", "auth.ticket")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GuardWith(pub, dir); err != nil {
		t.Fatalf("有效票据应放行，得到 %v", err)
	}

	// 损坏的文件
	if err := os.WriteFile(path, []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GuardWith(pub, dir); err != ErrBadSignature {
		t.Fatalf("损坏文件应拒绝，得到 %v", err)
	}

	// 开发模式放行
	if err := os.WriteFile(filepath.Join(dir, "data", ".devmode"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GuardWith(pub, dir); err != nil {
		t.Fatalf("devmode 应放行，得到 %v", err)
	}
}
