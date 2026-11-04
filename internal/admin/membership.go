package admin

import (
	"net/http"
	"time"
)

// 管理员调整有效期不生成购买订单或扣款。
func (s *Server) handlePlayerMembership(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID       int64 `json:"uid"`
		Kind      int64 `json:"kind"`
		ExpiresAt int64 `json:"expiresAt"`
	}
	if err := readJSON(r, &req); err != nil || req.UID <= 0 {
		httpError(w, 400, "参数错误")
		return
	}
	if err := s.db.SetMembership(r.Context(), req.UID, req.Kind, req.ExpiresAt, adminOf(r), time.Now().Unix()); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
