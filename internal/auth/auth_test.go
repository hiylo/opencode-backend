package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hiylo/opencode-backend/internal/store"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dsn := store.SQLiteDSN(filepath.Join(t.TempDir(), "test.db"))
	st, err := store.OpenFromConfig(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewManager(st)
}

func TestInitializeAndVerify(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)

	created, err := m.Initialize(ctx, "admin")
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !created {
		t.Fatalf("expected first-run create")
	}

	ok, err := m.VerifyPassword(ctx, "admin")
	if err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, _ = m.VerifyPassword(ctx, "wrong")
	if ok {
		t.Fatalf("wrong password accepted")
	}

	// Second initialize is a no-op.
	created, err = m.Initialize(ctx, "other")
	if err != nil {
		t.Fatalf("re-init: %v", err)
	}
	if created {
		t.Fatalf("expected second init to be no-op")
	}
	ok, _ = m.VerifyPassword(ctx, "other")
	if ok {
		t.Fatalf("re-init overwrote password")
	}
}

func TestSetPasswordTooShort(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)
	if err := m.SetPassword(ctx, "ab"); err == nil {
		t.Fatalf("expected error for short password")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)

	raw, err := m.CreateToken(ctx, "my-phone")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(raw) < 20 {
		t.Fatalf("token too short: %q", raw)
	}

	rec, err := m.VerifyToken(ctx, raw)
	if err != nil || rec == nil {
		t.Fatalf("verify valid token: %v", err)
	}
	if rec.Name != "my-phone" {
		t.Fatalf("unexpected name %q", rec.Name)
	}

	// Raw token is never stored.
	if rec.TokenHash == raw {
		t.Fatalf("raw token leaked into store")
	}

	// Wrong token rejected.
	if _, err := m.VerifyToken(ctx, "ocb_wrong"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for bad token, got %v", err)
	}

	// Revoked token rejected.
	if err := m.RevokeToken(ctx, rec.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := m.VerifyToken(ctx, raw); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after revoke, got %v", err)
	}
}

func TestTokenPrefix(t *testing.T) {
	m := newTestManager(t)
	raw, err := m.CreateToken(context.Background(), "x")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if raw[:4] != "ocb_" {
		t.Fatalf("expected ocb_ prefix, got %q", raw)
	}
}
