package proto

import "testing"

func TestTryParseAuthPacketAcceptsLegacyAndAuthorTokens(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{name: "CN 32hex", token: "e28f0bc4bd956205b2636faa3e6d50bb"},
		{name: "CN 48hex", token: "59112aad24b1f0b5f9ad80e993c772353b33d2d181339e89"},
		{name: "author 4-part", token: "abcd|1|7247281|payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			packet := append([]byte{AuthMagic, 0x00}, PackU32(uint32(len(tc.token)))...)
			packet = append(packet, tc.token...)
			got, consumed, ok := TryParseAuthPacket(packet)
			if !ok || consumed != len(packet) || got != tc.token {
				t.Fatalf("解析失败: token=%q consumed=%d ok=%v", got, consumed, ok)
			}
		})
	}
}

func TestTryParseAuthPacketRejectsUnknownTokenFormat(t *testing.T) {
	for _, token := range []string{"not-a-token", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		packet := append([]byte{AuthMagic, 0x00}, PackU32(uint32(len(token)))...)
		packet = append(packet, token...)
		if _, _, ok := TryParseAuthPacket(packet); ok {
			t.Fatalf("不应接受 token %q", token)
		}
	}
}
