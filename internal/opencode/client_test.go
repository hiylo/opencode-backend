package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer spins up a fake OpenCode HTTP server.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestPingHealthy(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"0.1.0"}`))
	})
	c := New(srv.URL)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestPingUnhealthy(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":false}`))
	})
	c := New(srv.URL)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatalf("expected error for unhealthy upstream")
	}
}

func TestPingNon200(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	c := New(srv.URL)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatalf("expected error for non-200")
	}
}

func TestListSessionsObjectMap(t *testing.T) {
	// /session/status returns an object keyed by session id.
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/status" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body := map[string]map[string]string{
			"ses_a": {"type": "busy"},
			"ses_b": {"type": "idle"},
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	c := New(srv.URL)
	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	busy := 0
	for _, s := range sessions {
		if s.Busy {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("expected 1 busy session, got %d", busy)
	}
}

func TestListSessionsError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	})
	c := New(srv.URL)
	if _, err := c.ListSessions(context.Background()); err == nil {
		t.Fatalf("expected error on non-200")
	}
}

func TestGetVersion(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"v1.2.3"}`))
	})
	c := New(srv.URL)
	v, err := c.GetVersion(context.Background())
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if v != "v1.2.3" {
		t.Fatalf("got %q want %q", v, "v1.2.3")
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	c := New("http://example.com:4096/")
	if c.BaseURL() != "http://example.com:4096" {
		t.Fatalf("trailing slash not trimmed: %q", c.BaseURL())
	}
}

func TestExportMarkdown(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: ""}, // empty should be skipped
	}
	out := ExportMarkdown("ses_1", msgs)
	if !strings.Contains(out, "# Session ses_1") {
		t.Fatalf("missing header")
	}
	if !strings.Contains(out, "## User") || !strings.Contains(out, "hello") {
		t.Fatalf("missing user block")
	}
	if !strings.Contains(out, "## Assistant") || !strings.Contains(out, "hi there") {
		t.Fatalf("missing assistant block")
	}
	if strings.Count(out, "## User") != 1 {
		t.Fatalf("empty user message not skipped: %s", out)
	}
}
