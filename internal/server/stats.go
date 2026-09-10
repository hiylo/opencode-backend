package server

import (
	"context"
	"net/http"
	"time"
)

// handleStats reports usage statistics. Requires a web session (admin).
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireWeb(r) {
		writeErr(w, http.StatusUnauthorized, "web session required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	taskStats, err := s.store.TaskStats(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "task stats failed")
		return
	}
	tokenUsage, err := s.store.TokenUsageByAudit(ctx, 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token usage failed")
		return
	}
	archives, err := s.store.CountArchives(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "archive count failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":       taskStats,
		"tokenUsage":  tokenUsage,
		"archives":    archives,
	})
}