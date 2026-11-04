package gameserver

import (
	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/proto"
	"testing"
)

func TestPlayerWarningsReturnsNativeTypedArray(t *testing.T) {
	srv := NewServer(config.Game{}, logging.Discard(), nil)
	sess := newProbeSess(srv)
	handler := srv.dispatch.lookup(3, 18)
	if handler == nil {
		t.Fatal("player warnings handler missing")
	}
	body, ok := handler(&Ctx{Sess: sess})
	if !ok {
		t.Fatal("warnings request not handled")
	}
	value, n, err := proto.DecodeValue(body, 0, sess.Table())
	if err != nil || n != len(body) || value.Kind != proto.KindArgs || value.ArgCodeStr() != "189" || len(value.Items) != 0 {
		t.Fatalf("warnings must be lcjmijeeebm[]: body=%x err=%v", body, err)
	}
	if code, ok := ContainerCodeForTypeID(3005); !ok || code != value.ArgCodeStr() {
		t.Fatalf("wrong native warning array alias %q", code)
	}
	if reason := srv.dispatch.checkRegisteredResponse(3, 18, body, sess.Table()); reason != "" {
		t.Fatal(reason)
	}
	for _, invalid := range []*proto.Value{proto.Null(), proto.Args(proto.S("524"))} {
		raw, err := proto.EncodeValue(invalid, sess.Table())
		if err != nil {
			t.Fatal(err)
		}
		if reason := srv.dispatch.checkRegisteredResponse(3, 18, raw, sess.Table()); reason == "" {
			t.Fatal("invalid warnings response accepted")
		}
	}
}
