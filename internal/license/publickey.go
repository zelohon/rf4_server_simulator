package license

import (
	"crypto/ed25519"
	"encoding/hex"
)

// publicKeyHex 是签发公钥，由 cmd/license-gen -init 生成后写入此处。
//
// 只能验证签名，不能签发新票据——这正是离线授权能成立的前提：
// 即使把这几个服务的二进制全部逆向，拿到的也只是公钥。
var publicKeyHex = "6fae45d849130b802372873265fb7be102171ba141dddcb9b483bc085adb0350"

// PublicKey 返回内置公钥；未配置时返回 nil，Verify 会拒绝所有票据。
func PublicKey() ed25519.PublicKey {
	b, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(b)
}
