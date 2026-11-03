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
		code    = flag.String("code", "", "卡密（仅作记录，不参与校验）")
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

	t := license.Ticket{
		Code:     strings.TrimSpace(*code),
		Note:     strings.TrimSpace(*note),
		ExpireAt: time.Now().AddDate(0, 0, *days).Unix(),
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
	fmt.Printf("票据已生成: %s\n签发: %s\n到期: %s（%d 天）\n",
		*out,
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
