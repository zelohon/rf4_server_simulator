package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rf4server/internal/admin"
	"rf4server/internal/config"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

func TestHandleAdminNotReady(t *testing.T) {
	a := &app{}
	rr := httptest.NewRecorder()
	a.handleAdmin(rr, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rr.Body.String(), "管理后台尚未就绪") {
		t.Fatalf("unexpected body: %q", rr.Body.String())
	}
}

func TestHandleAdminServesEmbeddedControlDeck(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := admin.NewServer(config.Admin{JWTSecret: "test"}, db, logging.Discard(), admin.WebFS())
	a := &app{adminSrv: srv}

	rr := httptest.NewRecorder()
	a.handleAdmin(rr, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "RF4 Control Deck") {
		t.Fatalf("admin page: code=%d body=%q", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	a.handleAdmin(rr, httptest.NewRequest(http.MethodGet, "/admin/api/me", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("admin api: code=%d body=%q", rr.Code, rr.Body.String())
	}
}
