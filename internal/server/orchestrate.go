package server

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// handleProjects lists orchestrated projects (session groups) from OpenCode.
// Requires a valid APP token.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.requireToken(r); !ok {
		writeErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	items, err := s.openCode.ListSessions(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "opencode: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": items})
}

// handleProjectSessions returns the session details for a single project
// directory (currently the same aggregate shape as projects).
func (s *Server) handleProjectSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.requireToken(r); !ok {
		writeErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	items, err := s.openCode.ListSessions(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "opencode: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}