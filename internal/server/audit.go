package server

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// handleAudit lists recent audit entries. Requires a web session (admin).
// Optional query: ?tokenId=&limit=
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireWeb(r) {
		writeErr(w, http.StatusUnauthorized, "web session required")
		return
	}

	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	entries, err := s.store.ListAudit(ctx, q.Get("tokenId"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list audit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": entries})
}
