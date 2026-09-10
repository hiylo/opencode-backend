package server

import "net/http"

// handleIndex serves the config page when embedded assets exist, otherwise a
// plain text hint. The backend is headless: this endpoint just documents that
// the config UI is reachable on the same port at /config.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.hasWebUI {
		s.serveWebUI(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("opencode-backend is running (headless).\nConfig UI: GET /config (needs web session)\n"))
}