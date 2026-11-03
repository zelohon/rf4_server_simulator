package license

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// 卡密密文：票据里以密文形式带上「这张票是哪张卡签发的」，
// 供启动时向授权平台复核；票据本身不出现卡密明文。
//
// 防护边界（如实说明）：主密钥编译在二进制里，逆向者最终可以把本机
// 那一份票据解出来。它挡住的是「客户用记事本打开 data/runtime.dat
// 就能看见卡密」这种情况，不是「专业逆向」。真正不能伪造的是票据签名
// ——签名私钥从不离开签发机。

// 密钥拆成 4 段并以错位顺序拼接，避免完整密钥以连续字节出现在二进制中。
var (
	sealSegA = []byte{0xa9, 0xdd, 0x30, 0xdc, 0xa0, 0x8c, 0x06, 0x55}
	sealSegB = []byte{0x65, 0xde, 0x3f, 0x02, 0xe0, 0x33, 0x5e, 0x0b}
	sealSegC = []byte{0x82, 0x94, 0x0b, 0x37, 0x90, 0x62, 0x94, 0x50}
	sealSegD = []byte{0xe4, 0xb5, 0x1d, 0xc5, 0x16, 0x53, 0x59, 0x91}
	// 拼接顺序下标，[2,0,3,1] 表示 C A D B。
	sealOrder = [4]int{2, 0, 3, 1}
	sealSalt  = []byte("rf4-local-runtime-v3")
)

// ErrNoSecret 表示票据里没有卡密密文（纯离线票据）。
var ErrNoSecret = errors.New("license: no sealed code")

func sealKey() []byte {
	segs := [4][]byte{sealSegA, sealSegB, sealSegC, sealSegD}
	raw := make([]byte, 0, 32)
	for _, i := range sealOrder {
		raw = append(raw, segs[i]...)
	}
	h := sha256.New()
	h.Write(raw)
	h.Write(sealSalt)
	return h.Sum(nil)
}

// SealCode 把卡密加密成票据里的 enc 字段。
//
// 空卡密返回空串，表示这张票据只做离线时效校验、不做在线复核。
func SealCode(code string) (string, error) {
	if code == "" {
		return "", nil
	}
	block, err := aes.NewCipher(sealKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(code), nil)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// OpenCode 从 enc 字段还原卡密。
func OpenCode(enc string) (string, error) {
	if enc == "" {
		return "", ErrNoSecret
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(sealKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrNoSecret
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
