package tasks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hiylo/opencode-backend/internal/push"
	"github.com/hiylo/opencode-backend/internal/store"
)

// newTestEnv wires a store, hub and fake upstream into an executor.
func newTestEnv(t *testing.T, upstream http.HandlerFunc) (*Executor, store.Store, *push.Hub) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)

	dsn := store.SQLiteDSN(filepath.Join(t.TempDir(), "test.db"))
	st, err := store.OpenFromConfig(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := push.NewHub()
	go hub.Run()

	exec := NewExecutor(st, hub, up.URL)
	return exec, st, hub
}

// fakeUpstream returns a handler simulating createSession + admitted prompt +
// idle session + assistant message.
func fakeUpstream(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"ses_test123","directory":"/w"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_test123/prompt":
			_, _ = w.Write([]byte(`{"data":{"admittedSeq":1,"id":"msg_x","sessionID":"ses_test123"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/status":
			_, _ = w.Write([]byte(`{"ses_test123":{"type":"idle"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_test123/message":
			_, _ = w.Write([]byte(`[{"role":"assistant","content":[{"type":"text","text":"这是结果"}]}]`))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestExecutorRunsTaskToCompletion(t *testing.T) {
	exec, st, _ := newTestEnv(t, fakeUpstream(t))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Seed a task.
	task := &store.Task{ID: "task_1", Directory: "/w", Prompt: "run something"}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Run the worker loop in the background briefly.
	done := make(chan struct{})
	go func() {
		defer close(done)
		exec.Run(ctx)
	}()

	// Wait for completion.
	var got *store.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = st.GetTask(ctx, "task_1")
		if got != nil && got.Status == store.TaskSucceeded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done

	if got == nil {
		t.Fatalf("task not found")
	}
	if got.Status != store.TaskSucceeded {
		t.Fatalf("status %q want %q (err=%s)", got.Status, store.TaskSucceeded, got.Error)
	}
	if got.Result != "这是结果" {
		t.Fatalf("result %q want %q", got.Result, "这是结果")
	}
	if got.SessionID != "ses_test123" {
		t.Fatalf("session id %q not written back", got.SessionID)
	}
}

func TestExecutorFailureMarksTaskFailed(t *testing.T) {
	// Upstream always 500s on prompt.
	exec, st, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"ses_fail"}`))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	task := &store.Task{ID: "task_fail", Directory: "/w", Prompt: "will fail"}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		exec.Run(ctx)
	}()

	var got *store.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = st.GetTask(ctx, "task_fail")
		if got != nil && got.Status != store.TaskQueued && got.Status != store.TaskRunning {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done

	if got == nil {
		t.Fatalf("task not found")
	}
	if got.Status != store.TaskFailed {
		t.Fatalf("status %q want %q", got.Status, store.TaskFailed)
	}
	if got.Error == "" {
		t.Fatalf("expected non-empty error")
	}
}

func TestExecutorReusesExistingSession(t *testing.T) {
	upstream := fakeUpstream(t)
	exec, st, _ := newTestEnv(t, upstream)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Task already targets an existing session; it must not call createSession.
	task := &store.Task{ID: "task_existing", SessionID: "ses_test123", Prompt: "use existing"}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		exec.Run(ctx)
	}()
	var got *store.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = st.GetTask(ctx, "task_existing")
		if got != nil && got.Status == store.TaskSucceeded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	if got == nil || got.Status != store.TaskSucceeded {
		t.Fatalf("task did not succeed: %+v", got)
	}
}

func TestParsePromptResponseEmptyText(t *testing.T) {
	// Ensure a session with no assistant messages returns the sentinel.
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"ses_empty"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_empty/prompt":
			_, _ = w.Write([]byte(`{"data":{"admittedSeq":1}}`))
		case r.URL.Path == "/session/status":
			_, _ = w.Write([]byte(`{"ses_empty":{"type":"idle"}}`))
		case r.URL.Path == "/session/ses_empty/message":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}
	exec, st, _ := newTestEnv(t, upstream)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	task := &store.Task{ID: "task_empty", Prompt: "hello"}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		exec.Run(ctx)
	}()
	var got *store.Task
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = st.GetTask(ctx, "task_empty")
		if got != nil && got.Status == store.TaskSucceeded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	if got == nil || got.Status != store.TaskSucceeded {
		t.Fatalf("did not succeed: %+v", got)
	}
	if got.Result != "(no assistant text)" {
		t.Fatalf("result %q", got.Result)
	}
}