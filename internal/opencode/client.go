package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client wraps HTTP access to a local OpenCode server.
// It does not proxy traffic; it provides orchestration-style queries
// (list/aggregate sessions, status, config) that power the backend features.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a Client talking to the given OpenCode base URL.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Health is the response of GET /global/health.
type Health struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version,omitempty"`
}

// Ping checks whether the OpenCode server is reachable and healthy.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode health returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var h Health
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("opencode health: parse response: %w", err)
	}
	if !h.Healthy {
		return fmt.Errorf("opencode reports unhealthy")
	}
	return nil
}

// SessionInfo mirrors the fields used from GET /session/status.
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Directory string `json:"directory,omitempty"`
	Model     string `json:"model,omitempty"`
	Busy      bool   `json:"busy"`
}

// ListSessions returns the set of currently known sessions by querying
// the session status endpoint and collapsing by directory, so a server
// watching several workspaces appears once with its session count.
func (c *Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opencode session status: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode session status returned %s", resp.Status)
	}

	type rawStatus struct {
		Type string `json:"type"`
	}
	var raw map[string]rawStatus
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("opencode session status: parse: %w", err)
	}

	// Collapse sessions by count only: the status endpoint keys by session id
	// and reports a type (busy/idle), without per-session directory info.
	// We return one entry per session id, flattening type to Busy.
	out := make([]SessionInfo, 0, len(raw))
	for id, st := range raw {
		out = append(out, SessionInfo{
			ID:   id,
			Busy: st.Type == "busy",
		})
	}
	return out, nil
}

// Config is the response of GET /config (server config, not the per-project one).
type Config struct {
	Version string `json:"version,omitempty"`
}

// GetVersion fetches the OpenCode server version string.
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/config", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("opencode config: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("opencode config returned %s", resp.Status)
	}
	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("opencode config: parse: %w", err)
	}
	return cfg.Version, nil
}

// BaseURL returns the configured OpenCode base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// ResolveURL builds an absolute URL against the OpenCode server for path p.
func (c *Client) ResolveURL(p string) string {
	u, err := url.Parse(c.baseURL + p)
	if err != nil {
		return c.baseURL + p
	}
	return u.String()
}
// Message is a single message in a session export.
type Message struct {
	Role    string
	Content string
}

// FetchSessionMessages retrieves the message list of a session from
// GET /session/{id}/message and flattens content parts to text.
func (c *Client) FetchSessionMessages(ctx context.Context, sessionID string) ([]Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session/"+sessionID+"/message", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch messages: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch messages: %s", resp.Status)
	}

	var doc []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}

	var out []Message
	for _, m := range doc {
		var sb strings.Builder
		for _, c := range m.Content {
			if c.Text != "" {
				sb.WriteString(c.Text)
				sb.WriteString("\n")
			}
		}
		out = append(out, Message{Role: m.Role, Content: strings.TrimSpace(sb.String())})
	}
	return out, nil
}

// ExportMarkdown renders a session message list as a Markdown transcript.
func ExportMarkdown(sessionID string, msgs []Message) string {
	var sb strings.Builder
	sb.WriteString("# Session " + sessionID + "\n\n")
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		label := "User"
		if m.Role == "assistant" {
			label = "Assistant"
		} else if m.Role == "tool" {
			label = "Tool"
		}
		sb.WriteString("## " + label + "\n\n")
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}
