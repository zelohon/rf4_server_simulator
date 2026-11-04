package gameserver

import (
	"context"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"testing"
)

func TestShortcutInvalidRequestsReturnFailure(t *testing.T) {
	srv, db := newDetachTestServer(t)
	for _, tc := range []struct {
		name    string
		handler Handler
		args    []*proto.Value
	}{
		{"missing slot", hShortcutBind, nil},
		{"overflow bind", hShortcutBind, []*proto.Value{proto.U64(2147483648), proto.Guid("11111111111111111111111111111111")}},
		{"overflow unbind", hShortcutUnbind, []*proto.Value{proto.U64(2147483648)}},
		{"missing item", hShortcutBind, []*proto.Value{proto.U32(1)}},
		{"unowned item", hShortcutBind, []*proto.Value{proto.U32(1), proto.Guid("11111111111111111111111111111111")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &ctxReplyCapture{}
			ctx := &Ctx{Sess: newProbeSess(srv), Tree: proto.Args524(tc.args...), DecodeOK: true,
				ExpectResponse: true, replyCapture: capture, Log: logging.Discard()}
			_, ok := tc.handler(ctx)
			if _, _, _, failure := capture.snapshot(); !ok || !failure {
				t.Fatal("无效快捷栏请求未返回原生错误")
			}
		})
	}
	bindings, err := db.ListShortcuts(context.Background(), 1000001)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("无效请求修改了快捷栏: bindings=%v err=%v", bindings, err)
	}
}
