package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	return pub, priv
}

func mustSign(t *testing.T, p *Payload, priv ed25519.PrivateKey) *Ticket {
	t.Helper()
	tk, err := Sign(p, priv)
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	return tk
}

func TestSignVerify(t *testing.T) {
	pub, priv := newKeyPair(t)
	p := &Payload{
		Lic:      LicID("TEST-CODE"),
		IssuedAt: time.Now().Unix(),
		ExpireAt: time.Now().Add(24 * time.Hour).Unix(),
	}
	tk := mustSign(t, p, priv)

	got, err := Verify(tk, pub, time.Now())
	if err != nil {
		t.Fatalf("合法票据应通过，得到 %v", err)
	}
	if got.Lic != p.Lic || got.ExpireAt != p.ExpireAt {
		t.Fatalf("还原内容不一致: got=%+v want=%+v", got, p)
	}

	// 换公钥
	otherPub, _ := newKeyPair(t)
	if _, err := Verify(tk, otherPub, time.Now()); err != ErrBadSignature {
		t.Fatalf("换公钥应被拒绝，得到 %v", err)
	}
}

// 篡改密文必须被签名挡住。
func TestTamperBlob(t *testing.T) {
	pub, priv := newKeyPair(t)
	tk := mustSign(t, &Payload{
		Lic: LicID("A"), ExpireAt: time.Now().Add(time.Hour).Unix(),
	}, priv)

	bad := *tk
	bad.Blob = tk.Blob[:len(tk.Blob)-2] + "AA"
	if _, err := Verify(&bad, pub, time.Now()); err == nil {
		t.Fatal("篡改密文后仍然通过验签")
	}
}

// 把一张票据的密文拼到另一张的签名上，必须失败。
func TestSwapBlob(t *testing.T) {
	pub, priv := newKeyPair(t)
	t1 := mustSign(t, &Payload{Lic: LicID("A"), ExpireAt: time.Now().Add(time.Hour).Unix()}, priv)
	t2 := mustSign(t, &Payload{Lic: LicID("B"), ExpireAt: time.Now().Add(100 * time.Hour).Unix()}, priv)

	bad := *t2
	bad.Blob = t1.Blob
	if _, err := Verify(&bad, pub, time.Now()); err == nil {
		t.Fatal("拼接两张票据后仍然通过验签")
	}
}

func TestExpired(t *testing.T) {
	pub, priv := newKeyPair(t)
	tk := mustSign(t, &Payload{
		Lic: LicID("X"), ExpireAt: time.Now().Add(-time.Minute).Unix(),
	}, priv)
	if _, err := Verify(tk, pub, time.Now()); err != ErrExpired {
		t.Fatalf("过期票据应被拒绝，得到 %v", err)
	}
}

func TestBadVersion(t *testing.T) {
	pub, priv := newKeyPair(t)
	tk := mustSign(t, &Payload{Lic: LicID("X"), ExpireAt: time.Now().Add(time.Hour).Unix()}, priv)
	bad := *tk
	bad.Version = 3
	if _, err := Verify(&bad, pub, time.Now()); err != ErrBadSignature {
		t.Fatalf("旧版本票据应被拒绝，得到 %v", err)
	}
}

// 落盘文件里不能出现任何可读信息（卡密、标识、备注、到期日、字段名）。
func TestTicketFileIsOpaque(t *testing.T) {
	_, priv := newKeyPair(t)
	const code = "YC10AF64A1A53C63C458E0DAB09CEAC1"
	enc, _ := SealCode(code)
	tk := mustSign(t, &Payload{
		Lic:      LicID(code),
		Enc:      enc,
		IssuedAt: time.Now().Unix(),
		ExpireAt: time.Now().Add(180 * 24 * time.Hour).Unix(),
		Note:     "客户A",
	}, priv)

	raw, _ := json.MarshalIndent(tk, "", "  ")
	text := string(raw)

	for _, needle := range []string{
		code, "YC10AF", LicID(code), "客户A",
		"expire_at", "issued_at", "\"lic\"", "\"enc\"", "\"note\"",
	} {
		if strings.Contains(text, needle) {
			t.Fatalf("票据文件泄露了 %q:\n%s", needle, text)
		}
	}
}

func TestGuardWith(t *testing.T) {
	pub, priv := newKeyPair(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := GuardWith(pub, dir); err != ErrNoTicket {
		t.Fatalf("缺少票据应拒绝，得到 %v", err)
	}

	tk := mustSign(t, &Payload{Lic: LicID("C"), ExpireAt: time.Now().Add(48 * time.Hour).Unix()}, priv)
	raw, _ := json.Marshal(tk)
	path := filepath.Join(dir, "data", ticketFile)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GuardWith(pub, dir); err != nil {
		t.Fatalf("有效票据应放行，得到 %v", err)
	}

	if err := os.WriteFile(path, []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GuardWith(pub, dir); err != ErrBadSignature {
		t.Fatalf("损坏文件应拒绝，得到 %v", err)
	}
}

func TestDevMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if DevMode(dir) {
		t.Fatal("默认不应处于开发模式")
	}
	if err := os.WriteFile(filepath.Join(dir, "data", ".devmode"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !DevMode(dir) {
		t.Fatal("存在 .devmode 时应进入开发模式")
	}
	if err := Guard(dir); err != nil {
		t.Fatalf("开发模式下应跳过校验，得到 %v", err)
	}
}

func TestRemaining(t *testing.T) {
	pub, priv := newKeyPair(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	tk := mustSign(t, &Payload{Lic: LicID("C"), ExpireAt: time.Now().Add(48 * time.Hour).Unix()}, priv)
	raw, _ := json.Marshal(tk)
	os.WriteFile(filepath.Join(dir, "data", ticketFile), raw, 0o644)

	d, err := RemainingWith(pub, dir)
	if err != nil {
		t.Fatalf("Remaining 失败: %v", err)
	}
	if d < 47*time.Hour || d > 49*time.Hour {
		t.Fatalf("剩余时长异常: %v", d)
	}
}
