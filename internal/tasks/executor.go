package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/hiylo/opencode-backend/internal/push"
	"github.com/hiylo/opencode-backend/internal/store"
)

// Executor runs claimed tasks against the OpenCode server and reports
// progress through the push hub.
type Executor struct {
	store        store.Store
	hub          *push.Hub
	openCodeBase string // base URL of the OpenCode HTTP server
	httpClient   *http.Client
	maxRetries   int // additional attempts after the first failure
}

// NewExecutor wires an executor. openCodeBase is required; the executor only
// sends prompts over the upstream HTTP API, it never proxies raw traffic.
func NewExecutor(st store.Store, hub *push.Hub, openCodeBase string) *Executor {
	return &Executor{
		store:        st,
		hub:          hub,
		openCodeBase: openCodeBase,
		httpClient:   &http.Client{Timeout: 90 * time.Second},
		maxRetries:   2,
	}
}

// WithMaxRetries sets how many times a failed task is re-queued before it is
// permanently marked failed. Returns the receiver for chaining.
func (e *Executor) WithMaxRetries(n int) *Executor {
	if n < 0 {
		n = 0
	}
	e.maxRetries = n
	return e
}

// Run is the worker loop: claim one queued task, execute it, repeat.
// It returns when ctx is canceled.
func (e *Executor) Run(ctx context.Context) {
	// Recover tasks left "running" by a previous crash/restart.
	if n, err := e.store.RecoverStaleRunning(ctx); err != nil {
		log.Printf("tasks: recover stale running: %v", err)
	} else if n > 0 {
		log.Printf("tasks: recovered %d stale running tasks back to queued", n)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		t, err := e.store.ClaimNextTask(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// No work: back off before polling again.
				time.Sleep(2 * time.Second)
				continue
			}
			log.Printf("tasks: claim failed: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		e.execute(ctx, t)
	}
}

// execute drives one task to completion and updates the store + pushes events.
// On failure it either re-queues the task for another attempt (bounded by
// maxRetries, with exponential backoff) or marks it permanently failed.
func (e *Executor) execute(ctx context.Context, t *store.Task) {
	pushTask := func(status string, task *store.Task) {
		e.hub.Broadcast(push.Message{
			Type: "task.event",
			Payload: mustJSON(map[string]any{
				"id":     t.ID,
				"status": status,
			}),
			Severity: severityFor(status),
		})
	}

	failWithRetry := func(errMsg string) {
		if t.Attempts > e.maxRetries {
			_ = e.store.FailTask(ctx, t.ID, errMsg)
			pushTask("failed", t)
			return
		}
		// Re-queue for another attempt with exponential backoff.
		backoff := 5 * (1 << (t.Attempts - 1)) // 5s, 10s, 20s...
		if backoff > 120 {
			backoff = 120
		}
		if err := e.store.RetryTask(ctx, t.ID, backoff); err != nil {
			_ = e.store.FailTask(ctx, t.ID, errMsg)
			pushTask("failed", t)
			return
		}
		pushTask("retrying", t)
		log.Printf("tasks: %s failed (attempt %d/%d), will retry in %ds: %s", t.ID, t.Attempts, e.maxRetries, backoff, errMsg)
	}

	pushTask("running", t)

	// canceled reports whether this task was canceled mid-execution.
	canceled := func() bool {
		c, err := e.store.IsTaskCanceled(ctx, t.ID)
		return err == nil && c
	}

	// Step 1: ensure a session exists to run the prompt in.
	sessionID := t.SessionID
	if sessionID == "" {
		if canceled() {
			pushTask("canceled", t)
			return
		}
		created, err := e.createSession(ctx, t.Directory)
		if err != nil {
			failWithRetry("create session: " + err.Error())
			return
		}
		sessionID = created
		_ = e.store.SetTaskSession(ctx, t.ID, sessionID)
		_ = e.store.UpdateTaskProgress(ctx, t.ID, "session created")
	}

	// Step 2: prompt the session and drain the response.
	result, err := e.promptSession(ctx, sessionID, t.Prompt, t.Directory, canceled, func(partial string) {
		_ = e.store.UpdateTaskProgress(ctx, t.ID, partial)
	})
	if err != nil {
		if errors.Is(err, errCanceled) {
			pushTask("canceled", t)
			return
		}
		failWithRetry(err.Error())
		return
	}

	_ = e.store.CompleteTask(ctx, t.ID, result)
	pushTask("succeeded", t)
}

var errCanceled = errors.New("task canceled")

// createSession creates a new OpenCode session via the HTTP API.
func (e *Executor) createSession(ctx context.Context, directory string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"directory": directory,
		"title":     "opencode-backend task",
	})
	resp, err := e.httpClient.Post(e.openCodeBase+"/session", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("create session: %s", resp.Status)
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// Try alternative field naming used by some server versions.
		return "", fmt.Errorf("create session: parse: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("create session: empty id in response")
	}
	return out.ID, nil
}

// promptSession submits a prompt using the V2 admitted-prompt API
// (POST /session/{id}/prompt), then waits for the session to become idle
// and returns the latest assistant text. Progress callbacks report phases.
// canceled is polled so a client cancellation aborts the wait promptly.
func (e *Executor) promptSession(ctx context.Context, sessionID, prompt, directory string, canceled func() bool, onProgress func(string)) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"id":       "msg_ocb" + time.Now().Format("20060102150405"),
		"prompt":   map[string]any{"text": prompt},
		"delivery": "steer",
		"resume":   true,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.openCodeBase+"/api/session/"+sessionID+"/prompt", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if directory != "" {
		req.Header.Set("x-opencode-directory", directory)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("prompt: %s: %s", resp.Status, string(raw))
	}
	if onProgress != nil {
		onProgress("prompt admitted")
	}

	// Wait for the session to stop generating, then pull the last assistant text.
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if canceled != nil && canceled() {
			return "", errCanceled
		}
		time.Sleep(3 * time.Second)
		busy, err := e.isSessionBusy(ctx, sessionID)
		if err != nil {
			return "", err
		}
		if !busy {
			break
		}
		if onProgress != nil {
			onProgress("agent working...")
		}
	}

	result, err := e.lastAssistantText(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return result, nil
}

// isSessionBusy reports whether a session is currently generating, by reading
// the /session/status map.
func (e *Executor) isSessionBusy(ctx context.Context, sessionID string) (bool, error) {
	resp, err := e.httpClient.Get(e.openCodeBase + "/session/status")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var statuses map[string]struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &statuses); err != nil {
		return false, err
	}
	st, ok := statuses[sessionID]
	if !ok {
		return false, nil
	}
	return st.Type == "busy", nil
}

// lastAssistantText loads a session's messages from /session/{id}/message and
// returns the last assistant text block.
func (e *Executor) lastAssistantText(ctx context.Context, sessionID string) (string, error) {
	resp, err := e.httpClient.Get(e.openCodeBase + "/session/" + sessionID + "/message")
	if err != nil {
		return "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("get session messages: %s", resp.Status)
	}

	var doc []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse session messages: %w", err)
	}
	for i := len(doc) - 1; i >= 0; i-- {
		m := doc[i]
		if m.Role != "assistant" {
			continue
		}
		var texts []string
		for _, c := range m.Content {
			if c.Type == "text" && c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n"), nil
		}
	}
	return "(no assistant text)", nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
// severityFor maps a task status to a push severity for notification routing.
func severityFor(status string) string {
	switch status {
	case "failed":
		return push.Critical
	case "retrying":
		return push.Warning
	default:
		return push.Info
	}
}
