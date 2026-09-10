package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hiylo/opencode-backend/internal/auth"
	"github.com/hiylo/opencode-backend/internal/config"
	"github.com/hiylo/opencode-backend/internal/opencode"
	"github.com/hiylo/opencode-backend/internal/push"
	"github.com/hiylo/opencode-backend/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	ctx := context.Background()
	dsn := store.SQLiteDSN(filepath.Join(t.TempDir(), "test.db"))
	st, err := store.OpenFromConfig(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	am := auth.NewManager(st)
	if _, err := am.Initialize(ctx, "admin"); err != nil {
		t.Fatalf("init auth: %v", err)
	}

	// Fake upstream OpenCode that reports healthy + one busy session.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true}`))
		case "/session/status":
			_, _ = w.Write([]byte(`{"ses_a":{"type":"busy"}}`))
		case "/config":
			_, _ = w.Write([]byte(`{"version":"v9.9.9"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := &config.Config{
		ListenAddr:  "127.0.0.1:0",
		OpenCodeURL: upstream.URL,
		DBDriver:    "sqlite",
	}
	oc := opencode.New(upstream.URL)
	hub := push.NewHub()
	go hub.Run()

	srv := New(cfg, st, am, oc, hub)
	mux := http.NewServeMux()
	srv.Routes(mux)
	srv.testMux = mux
	return srv
}

// do performs a request against the test mux.
func (s *Server) do(t *testing.T, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.testMux.ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t)
	rec := s.do(t, http.MethodGet, "/api/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["status"] != "ok" || out["upstream"] != true {
		t.Fatalf("unexpected health body: %s", rec.Body.String())
	}
}

func TestWebLoginFlow(t *testing.T) {
	s := newTestServer(t)

	// Wrong password -> 401.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"nope"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status %d", rec.Code)
	}

	// Correct password -> session id.
	rec = s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", rec.Code, rec.Body.String())
	}
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	if login.Session == "" {
		t.Fatalf("no session id")
	}
	h := map[string]string{"X-Web-Session": login.Session}

	// Change password.
	rec = s.do(t, http.MethodPost, "/api/web/password", `{"newPassword":"newpass"}`, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password status %d: %s", rec.Code, rec.Body.String())
	}

	// Old password now fails, new works.
	rec = s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password still works")
	}
	rec = s.do(t, http.MethodPost, "/api/web/session", `{"password":"newpass"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("new password rejected: %d", rec.Code)
	}
}

func TestTokenAuthFlow(t *testing.T) {
	s := newTestServer(t)

	// Login as web admin and create a token.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}

	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"phone"}`, wh)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token status %d: %s", rec.Code, rec.Body.String())
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	if !strings.HasPrefix(tok.Token, "ocb_") {
		t.Fatalf("bad token prefix: %q", tok.Token)
	}

	// Use token to read projects.
	th := map[string]string{"Authorization": "Bearer " + tok.Token}
	rec = s.do(t, http.MethodGet, "/api/projects", "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status %d: %s", rec.Code, rec.Body.String())
	}
	var proj struct {
		Projects []map[string]any `json:"projects"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &proj)
	if len(proj.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(proj.Projects))
	}

	// Projects without token -> 401.
	rec = s.do(t, http.MethodGet, "/api/projects", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	// Revoke token then it must be rejected.
	toks, err := s.auth.ListTokens(context.Background())
	if err != nil || len(toks) != 1 {
		t.Fatalf("list tokens: %v n=%d", err, len(toks))
	}
	if err := s.auth.RevokeToken(context.Background(), toks[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rec = s.do(t, http.MethodGet, "/api/projects", "", th)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token still accepted: %d", rec.Code)
	}
}

func TestSystemEndpointWithToken(t *testing.T) {
	s := newTestServer(t)
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}
	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"sys"}`, wh)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	th := map[string]string{"Authorization": "Bearer " + tok.Token}

	rec = s.do(t, http.MethodGet, "/api/system", "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("system status %d", rec.Code)
	}
	var sys struct {
		Backend         string `json:"backend"`
		OpenCodeVersion string `json:"opencodeVersion"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sys)
	if sys.Backend != "opencode-backend" {
		t.Fatalf("bad backend %q", sys.Backend)
	}
	if sys.OpenCodeVersion != "v9.9.9" {
		t.Fatalf("bad version %q", sys.OpenCodeVersion)
	}
}
func TestTasksCRUD(t *testing.T) {
	s := newTestServer(t)

	// Login and create a token.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}
	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"tasker"}`, wh)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	th := map[string]string{"Authorization": "Bearer " + tok.Token}

	// Create task.
	rec = s.do(t, http.MethodPost, "/api/tasks", `{"prompt":"do stuff","directory":"/w"}`, th)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task status %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID     string `json:"ID"`
		Status string `json:"Status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" {
		t.Fatalf("no task id")
	}
	if created.Status != "" && created.Status != "queued" {
		t.Fatalf("unexpected initial status %q", created.Status)
	}

	// List includes it.
	rec = s.do(t, http.MethodGet, "/api/tasks", "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d", rec.Code)
	}
	var list struct {
		Tasks []map[string]any `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(list.Tasks))
	}

	// Get single.
	rec = s.do(t, http.MethodGet, "/api/tasks/"+created.ID, "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
}

func TestTaskRequiresToken(t *testing.T) {
	s := newTestServer(t)
	rec := s.do(t, http.MethodPost, "/api/tasks", `{"prompt":"x"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}
