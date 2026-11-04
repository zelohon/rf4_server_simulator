package gameserver

import (
	"bytes"
	"testing"

	"rf4server/internal/proto"
)

// 24/1 只记录频道并回 null：回完整频道对象会让客户端以约 16ms 间隔重发，
// 最终断开连接。
func TestSocialMoveReturnsNullAndRecordsChannel(t *testing.T) {
	srv := &Server{}
	sess := newProbeSess(srv)
	sess.state = make(map[string]any)
	body, ok := hSocialMove(miscCtx(sess, 24, 1, proto.Args524(proto.StrVal("nav12_02"))))
	if !ok {
		t.Fatal("24/1 未处理")
	}
	if !bytes.Equal(body, []byte{0x00}) {
		t.Fatalf("24/1 响应应为 null，实得 %x", body)
	}
	v, exists := sess.GetState("channel")
	if !exists {
		t.Fatal("24/1 未记录会话频道")
	}
	m, isMap := v.(map[string]any)
	if !isMap || m["name"] != "nav12_02" {
		t.Fatalf("24/1 记录的频道=%#v", v)
	}
}
