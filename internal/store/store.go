package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Common errors returned by the store.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// Setting is a single configuration key/value persisted in the store.
type Setting struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// Token represents an API token issued to a client device.
type Token struct {
	ID        string
	Name      string
	TokenHash string // sha256 hex of the raw token; the raw token is never stored.
	CreatedAt time.Time
	RevokedAt *time.Time
	LastUsed  *time.Time
}

// Store is the persistence abstraction shared by the app.
// It is implemented by both SQLite and PostgreSQL drivers.
type Store interface {
	// Close releases the underlying database.
	Close() error
	// GetSetting reads a single setting value; returns ErrNotFound when missing.
	GetSetting(ctx context.Context, key string) (string, error)
	// SetSetting upserts a single setting value.
	SetSetting(ctx context.Context, key, value string) error
	// CreateToken persists a new token record. Returns the record with generated ID.
	CreateToken(ctx context.Context, t *Token) error
	// GetTokenByHash looks up a non-revoked token by its sha256 hash.
	GetTokenByHash(ctx context.Context, hash string) (*Token, error)
	// ListTokens returns all tokens, revoked ones last, newest first.
	ListTokens(ctx context.Context) ([]*Token, error)
	// RevokeToken marks a token revoked by id. Returns ErrNotFound if missing.
	RevokeToken(ctx context.Context, id string) error
	// TouchToken updates LastUsed for a token.
	TouchToken(ctx context.Context, id string) error
	// Ping verifies database connectivity.
	Ping(ctx context.Context) error

	// ---- Tasks (async orchestration queue) ----

	// CreateTask persists a queued task.
	CreateTask(ctx context.Context, t *Task) error
	// ListTasks returns tasks, newest first, with optional status filter.
	ListTasks(ctx context.Context, status string, limit int) ([]*Task, error)
	// GetTask loads a single task.
	GetTask(ctx context.Context, id string) (*Task, error)
	// ClaimNextTask picks the oldest queued task and marks it running.
	ClaimNextTask(ctx context.Context) (*Task, error)
	// UpdateTaskProgress records a progress note for a running task.
	UpdateTaskProgress(ctx context.Context, id, progress string) error
	// SetTaskSession records the resolved session id for a task.
	SetTaskSession(ctx context.Context, id, sessionID string) error
	// CompleteTask marks a task succeeded with a result.
	CompleteTask(ctx context.Context, id, result string) error
	// FailTask marks a task failed.
	FailTask(ctx context.Context, id, errMsg string) error
	// CancelTask marks a queued/running task canceled. Returns true if changed.
	CancelTask(ctx context.Context, id string) (bool, error)
}

// Open opens a store for the given driver/dsn. It applies all migrations
// before returning, so the returned store is ready for use.
func Open(ctx context.Context, driver, dsn string) (Store, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	// SQLite has no connection pool concept; PG benefits from sensible limits.
	if driver == "postgres" {
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(time.Hour)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &sqlStore{db: db, driver: driver}, nil
}

type sqlStore struct {
	db     *sql.DB
	driver string
}

func (s *sqlStore) Close() error { return s.db.Close() }

func (s *sqlStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *sqlStore) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`,
		key,
	).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return val, err
}

func (s *sqlStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value,
	)
	return err
}

func (s *sqlStore) CreateToken(ctx context.Context, t *Token) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tokens (id, name, token_hash, created_at, revoked_at, last_used)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, NULL, NULL)`,
		t.ID, t.Name, t.TokenHash,
	)
	return err
}

func (s *sqlStore) GetTokenByHash(ctx context.Context, hash string) (*Token, error) {
	t := &Token{}
	var revoked *time.Time
	var lastUsed *time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, token_hash, created_at, revoked_at, last_used
		FROM tokens WHERE token_hash = ? AND revoked_at IS NULL LIMIT 1`,
		hash,
	).Scan(&t.ID, &t.Name, &t.TokenHash, &t.CreatedAt, &revoked, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	t.RevokedAt = revoked
	t.LastUsed = lastUsed
	return t, err
}

func (s *sqlStore) ListTokens(ctx context.Context) ([]*Token, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_hash, created_at, revoked_at, last_used
		FROM tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Token
	for rows.Next() {
		t := &Token{}
		var revoked *time.Time
		var lastUsed *time.Time
		if err := rows.Scan(&t.ID, &t.Name, &t.TokenHash, &t.CreatedAt, &revoked, &lastUsed); err != nil {
			return nil, err
		}
		t.RevokedAt = revoked
		t.LastUsed = lastUsed
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqlStore) RevokeToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = ? AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) TouchToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET last_used = CURRENT_TIMESTAMP WHERE id = ?`,
		id,
	)
	return err
}