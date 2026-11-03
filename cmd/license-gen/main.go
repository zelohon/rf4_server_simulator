// license-gen —— 离线授权票据签发工具。
//
// 这是「作者自己用」的工具，不随程序分发，也不要放进 bin 目录。
// 私钥文件（默认 license-private.key）必须单独保管，泄露等于把签发权交出去。
//
// 用法：
//
//	license-gen -init                                 生成密钥对（公钥写进 internal/license/publickey.go）
//	license-gen -code 卡密 -days 180 -out auth.ticket  签发一张 180 天的票据
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"rf4server/internal/license"
)

func main() {
	var (
		initKey = flag.Bool("init", false, "生成新的密钥对")
		keyPath = flag.String("key", "license-private.key", "私钥文件路径")
		code    = flag.String("code", "", "卡密：以密文写入票据，供客户机启动时在线复核")
		check   = flag.Bool("check", false, "签发前先在线验证卡密是否可用")
		days    = flag.Int("days", 180, "有效天数")
		out     = flag.String("out", "auth.ticket", "票据输出路径")
		note    = flag.String("note", "", "备注，例如客户名")
	)
	flag.Parse()

	if *initKey {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fail("生成密钥失败", err)
		}
		if err := os.WriteFile(*keyPath, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
			fail("写入私钥失败", err)
		}
		fmt.Println("私钥已写入:", *keyPath, "（务必单独保管，不要随程序分发）")
		fmt.Println("把下面这行公钥填进 internal/license/publickey.go 的 publicKeyHex：")
		fmt.Println(hex.EncodeToString(pub))
		return
	}

	raw, err := os.ReadFile(*keyPath)
	if err != nil {
		fail("读取私钥失败", err)
	}
	privBytes, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(privBytes) != ed25519.PrivateKeySize {
		fail("私钥格式错误", nil)
	}

	// 签发前可选地先验一次卡密，避免把一张已停用的卡打进客户包。
	if *check && strings.TrimSpace(*code) != "" {
		status, detail := license.CheckOnline(strings.TrimSpace(*code), 8*time.Second)
		switch status {
		case license.OnlineValid:
			fmt.Println("在线校验: 卡密有效")
		case license.OnlineOffline:
			fmt.Printf("在线校验: 连不上平台，跳过（%s）\n", detail)
		default:
			fail("在线校验: 卡密不可用（"+detail+"），已中止签发", nil)
		}
	}

	t := license.Ticket{
		Lic:      license.LicID(*code),
		Note:     strings.TrimSpace(*note),
		ExpireAt: time.Now().AddDate(0, 0, *days).Unix(),
	}
	// 卡密以密文写进票据，供客户机启动时在线复核。
	// 不传 -code 则生成纯离线票据，只受有效期约束。
	if enc, err := license.SealCode(strings.TrimSpace(*code)); err != nil {
		fail("卡密加密失败", err)
	} else {
		t.Enc = enc
	}
	if err := license.Sign(&t, ed25519.PrivateKey(privBytes)); err != nil {
		fail("签名失败", err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		fail("序列化失败", err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fail("写入票据失败", err)
	}
	fmt.Printf("票据已生成: %s\n", *out)
	if t.Lic != "" {
		fmt.Printf("卡密标识: %s（仅哈希，卡密本身不写入票据）\n", t.Lic)
	}
	fmt.Printf("签发: %s\n到期: %s（%d 天）\n",
		time.Unix(t.IssuedAt, 0).Format("2006-01-02 15:04"),
		time.Unix(t.ExpireAt, 0).Format("2006-01-02 15:04"),
		*days)
}

func fail(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(1)
}
