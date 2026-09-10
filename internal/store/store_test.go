package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	dsn := SQLiteDSN(filepath.Join(dir, "test.db"))
	st, err := OpenFromConfig(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, err := st.GetSetting(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing key, got %v", err)
	}
	if err := st.SetSetting(ctx, "k1", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := st.GetSetting(ctx, "k1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "v1" {
		t.Fatalf("got %q want %q", got, "v1")
	}
	// Upsert overwrites.
	if err := st.SetSetting(ctx, "k1", "v2"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = st.GetSetting(ctx, "k1")
	if got != "v2" {
		t.Fatalf("after upsert got %q want %q", got, "v2")
	}
}

func TestTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	tok := &Token{ID: "id1", Name: "device-a", TokenHash: "hash-a"}
	if err := st.CreateToken(ctx, tok); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetTokenByHash(ctx, "hash-a")
	if err != nil {
		t.Fatalf("get by hash: %v", err)
	}
	if got.ID != "id1" || got.Name != "device-a" {
		t.Fatalf("unexpected token: %+v", got)
	}

	// Duplicate hash should be rejected.
	dup := &Token{ID: "id2", Name: "dup", TokenHash: "hash-a"}
	if err := st.CreateToken(ctx, dup); err == nil {
		t.Fatalf("expected conflict on duplicate hash")
	}

	// Unknown hash -> not found.
	if _, err := st.GetTokenByHash(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Revoke makes the token un-queryable.
	if err := st.RevokeToken(ctx, "id1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := st.GetTokenByHash(ctx, "hash-a"); err != ErrNotFound {
		t.Fatalf("revoked token still found: %v", err)
	}

	// Double revoke -> not found.
	if err := st.RevokeToken(ctx, "id1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound on double revoke, got %v", err)
	}

	toks, err := st.ListTokens(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(toks) != 1 {
		t.Fatalf("expected 1 token after revoke, got %d", len(toks))
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := SQLiteDSN(filepath.Join(dir, "test.db"))
	ctx := context.Background()

	st1, err := OpenFromConfig(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	st1.Close()

	// Reopening the same file must not fail migrations.
	st2, err := OpenFromConfig(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if err := st2.Ping(ctx); err != nil {
		t.Fatalf("ping after reopen: %v", err)
	}
}

func TestOpenSQLiteFile(t *testing.T) {
	// WAL journal files should be created next to the db file.
	dir := t.TempDir()
	path := filepath.Join(dir, "data.db")
	dsn := SQLiteDSN(path)
	ctx := context.Background()

	st, err := OpenFromConfig(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}

func TestTaskRetryBackoff(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// Seed a task, then simulate: claim -> fail -> retry.
	task := &Task{ID: "retry1", SessionID: "", Directory: "/w", Prompt: "p"}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	claimed, err := st.ClaimNextTask(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("attempts after first claim = %d, want 1", claimed.Attempts)
	}

	// Fail then retry with 5s backoff.
	if err := st.FailTask(ctx, claimed.ID, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := st.RetryTask(ctx, claimed.ID, 5); err != nil {
		t.Fatalf("retry: %v", err)
	}

	got, err := st.GetTask(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != TaskQueued {
		t.Fatalf("status %q want queued", got.Status)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (claim already counted one)", got.Attempts)
	}
	if got.Error != "" {
		t.Fatalf("error not cleared: %q", got.Error)
	}
	// available_at should be in the future (>= now - small skew).
	if !got.AvailableAt.After(time.Now().Add(-2 * time.Second)) {
		t.Fatalf("available_at not scheduled in future: %v", got.AvailableAt)
	}

	// A claim now must NOT pick it up (backoff window active).
	next, err := st.ClaimNextTask(ctx)
	if err != ErrNotFound {
		t.Fatalf("expected no claimable task during backoff, got %+v err=%v", next, err)
	}
}

func TestRetryTaskUnknownID(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.RetryTask(ctx, "nope", 5); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
