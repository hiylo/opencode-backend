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
	"time"

	"github.com/hiylo/opencode-backend/internal/auth"
	"github.com/hiylo/opencode-backend/internal/automation"
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
		case "/session/ses_test123/message":
			_, _ = w.Write([]byte(`[{"role":"assistant","content":[{"type":"text","text":"这是结果"}]}]`))
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
	srv.SetAutomation(automation.NewEngine(st, time.Hour))
	srv.testMux = srv.routesMux()
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

func TestRulesCRUDAndWebhook(t *testing.T) {
	s := newTestServer(t)

	// Web session (rules are admin-managed).
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}

	// Create a cron rule.
	rec = s.do(t, http.MethodPost, "/api/rules", `{"name":"nightly","kind":"cron","schedule":"5m","directory":"/w","prompt":"run tests","enabled":true}`, wh)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create rule status %d: %s", rec.Code, rec.Body.String())
	}
	var rule struct {
		ID string `json:"ID"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rule)
	if rule.ID == "" {
		t.Fatalf("no rule id")
	}

	// List includes it.
	rec = s.do(t, http.MethodGet, "/api/rules", "", wh)
	if rec.Code != http.StatusOK {
		t.Fatalf("list rules status %d", rec.Code)
	}
	var list struct {
		Rules []map[string]any `json:"rules"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(list.Rules))
	}

	// Rules require web session (not token).
	rec = s.do(t, http.MethodGet, "/api/rules", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}

	// Delete rule.
	rec = s.do(t, http.MethodDelete, "/api/rules/"+rule.ID, "", wh)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete rule status %d", rec.Code)
	}
	rec = s.do(t, http.MethodDelete, "/api/rules/"+rule.ID, "", wh)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on double delete, got %d", rec.Code)
	}
}

func TestWebhookFiresRule(t *testing.T) {
	s := newTestServer(t)

	// Login and create an HTTP rule via web session.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}

	rec = s.do(t, http.MethodPost, "/api/rules", `{"name":"hook","kind":"http","schedule":"/workspaces/opencode","prompt":"run on hook","enabled":true}`, wh)
	var rule struct {
		ID string `json:"ID"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rule)

	// Fire webhook for matching target.
	rec = s.do(t, http.MethodPost, "/api/webhook?target=/workspaces/opencode", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook status %d: %s", rec.Code, rec.Body.String())
	}
	var fired struct {
		Fired bool `json:"fired"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &fired)
	if !fired.Fired {
		t.Fatalf("webhook did not fire")
	}

	// A task must have been created (visible with an APP token).
	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"hooker"}`, wh)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	th := map[string]string{"Authorization": "Bearer " + tok.Token}
	rec = s.do(t, http.MethodGet, "/api/tasks", "", th)
	var list struct {
		Tasks []map[string]any `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("expected 1 task from webhook, got %d", len(list.Tasks))
	}

	// Non-matching target -> 404.
	rec = s.do(t, http.MethodPost, "/api/webhook?target=/nope", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-match, got %d", rec.Code)
	}
}

func TestBatchCreatesMultipleTasks(t *testing.T) {
	s := newTestServer(t)

	// Login + token.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}
	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"batch"}`, wh)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	th := map[string]string{"Authorization": "Bearer " + tok.Token}

	// Batch create for two targets.
	rec = s.do(t, http.MethodPost, "/api/batch",
		`{"prompt":"add logging","targets":[{"directory":"/a"},{"sessionId":"ses_1"}]}`, th)
	if rec.Code != http.StatusCreated {
		t.Fatalf("batch status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Count != 2 {
		t.Fatalf("expected 2 tasks, got %d", out.Count)
	}

	// Verify two tasks in store.
	rec = s.do(t, http.MethodGet, "/api/tasks", "", th)
	var list struct {
		Tasks []map[string]any `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(list.Tasks))
	}

	// Empty targets rejected.
	rec = s.do(t, http.MethodPost, "/api/batch", `{"prompt":"x","targets":[]}`, th)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty targets, got %d", rec.Code)
	}

	// No token rejected.
	rec = s.do(t, http.MethodPost, "/api/batch", `{"prompt":"x","targets":[{"directory":"/a"}]}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}

func TestAuditLogging(t *testing.T) {
	s := newTestServer(t)

	// Login + token.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}
	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"audit-phone"}`, wh)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)

	// Make a token-authenticated call.
	th := map[string]string{"Authorization": "Bearer " + tok.Token}
	rec = s.do(t, http.MethodGet, "/api/projects", "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status %d", rec.Code)
	}

	// Audit should have an entry for this token call.
	rec = s.do(t, http.MethodGet, "/api/audit", "", wh)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status %d", rec.Code)
	}
	var out struct {
		Audit []struct {
			TokenName string `json:"TokenName"`
			Path      string `json:"Path"`
		} `json:"audit"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Audit) == 0 {
		t.Fatalf("expected audit entries")
	}
	// The newest entry should be our projects call.
	last := out.Audit[0]
	if last.Path != "/api/projects" {
		t.Fatalf("last audit path %q want /api/projects", last.Path)
	}
	if last.TokenName != "audit-phone" {
		t.Fatalf("audit token name %q", last.TokenName)
	}

	// Audit requires web session.
	rec = s.do(t, http.MethodGet, "/api/audit", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestArchiveSessionFlow(t *testing.T) {
	s := newTestServer(t)

	// Login + token.
	rec := s.do(t, http.MethodPost, "/api/web/session", `{"password":"admin"}`, nil)
	var login struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	wh := map[string]string{"X-Web-Session": login.Session}
	rec = s.do(t, http.MethodPost, "/api/tokens", `{"name":"archiver"}`, wh)
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	th := map[string]string{"Authorization": "Bearer " + tok.Token}

	// Archive a session (fake upstream returns a message list).
	rec = s.do(t, http.MethodPost, "/api/archives", `{"sessionId":"ses_test123","format":"markdown"}`, th)
	if rec.Code != http.StatusCreated {
		t.Fatalf("archive status %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" {
		t.Fatalf("no archive id")
	}

	// List.
	rec = s.do(t, http.MethodGet, "/api/archives", "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d", rec.Code)
	}
	var list struct {
		Archives []map[string]any `json:"archives"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Archives) != 1 {
		t.Fatalf("expected 1 archive, got %d", len(list.Archives))
	}

	// Get full content.
	rec = s.do(t, http.MethodGet, "/api/archives/"+created.ID, "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
	var arch struct {
		Content string `json:"Content"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &arch)
	if !strings.Contains(arch.Content, "这是结果") {
		t.Fatalf("archive content missing assistant text: %q", arch.Content)
	}

	// Delete.
	rec = s.do(t, http.MethodDelete, "/api/archives/"+created.ID, "", th)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status %d", rec.Code)
	}
	rec = s.do(t, http.MethodGet, "/api/archives/"+created.ID, "", th)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}
