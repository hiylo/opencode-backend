package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrate applies schema migrations to the database.
// Migration version is tracked in a meta table, so both SQLite and
// PostgreSQL start from the same version sequence.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	current, err := currentVersion(ctx, db)
	if err != nil {
		return err
	}

	for current < len(migrations) {
		next := current + 1
		m := migrations[current]
		if err := m.apply(ctx, db); err != nil {
			return fmt.Errorf("migration %d (%s): %w", next, m.name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, next); err != nil {
			return fmt.Errorf("record migration %d: %w", next, err)
		}
		current = next
	}
	return nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read current version: %w", err)
	}
	return v, nil
}

// migration is a single versioned schema step with a portable name.
type migration struct {
	name  string
	apply func(ctx context.Context, db *sql.DB) error
}

// migrations is the ordered list of schema steps.
// IMPORTANT: never reorder or edit existing entries; append new ones only.
var migrations = []migration{
	{name: "initial", apply: migrationInitial},
	{name: "tasks", apply: migrationTasks},
}

// migrationInitial creates the base tables shared by all drivers.
// DDL uses only portable constructs that both SQLite and PostgreSQL accept.
func migrationInitial(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS tokens (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			revoked_at TIMESTAMP NULL,
			last_used TIMESTAMP NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// migrationTasks adds the async orchestration task table.
func migrationTasks(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL DEFAULT '',
			directory TEXT NOT NULL DEFAULT '',
			prompt TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'queued',
			error TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL DEFAULT '',
			progress TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP NULL,
			finished_at TIMESTAMP NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_created ON tasks(status, created_at)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
