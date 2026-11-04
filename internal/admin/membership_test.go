package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"rf4server/internal/config"
	"rf4server/internal/logging"
)

func TestProfileUpdatePreservesUnchangedMembership(t *testing.T) {
	for _, kind := range []int64{1, 2} {
		db := testDB(t)
		if _, err := db.EnsureProfile(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
		exp := int64(0)
		if kind == 1 {
			exp = 1700000000
		}
		if _, err := db.SQL.Exec(`UPDATE profile SET membership=?,membership_exp=? WHERE uid=42`, kind, exp); err != nil {
			t.Fatal(err)
		}
		srv := NewServer(config.Admin{}, db, logging.Discard(), nil)
		r := httptest.NewRequest("POST", "/api/players/update", strings.NewReader(`{"uid":42,"gold":1234}`))
		w := httptest.NewRecorder()
		srv.handlePlayerUpdate(w, r)
		p, err := db.GetProfile(context.Background(), 42)
		if w.Code != 200 || err != nil || p.Membership != kind || p.MembershipExp != exp || p.Gold != 1234 {
			t.Fatalf("profile save changed membership: %d %+v %v", w.Code, p, err)
		}
	}
}
