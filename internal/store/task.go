package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Task status lifecycle.
const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
	TaskCanceled  = "canceled"
)

// Task is an asynchronous orchestration job submitted by a client.
type Task struct {
	ID        string
	SessionID string // target OpenCode session id ("" = new session)
	Directory string // working directory hint for new sessions
	Prompt    string
	Status    string
	Error     string
	Result    string
	Progress  string
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
	StartedAt *time.Time
	FinishedAt *time.Time
}

// CreateTask persists a queued task.
func (s *sqlStore) CreateTask(ctx context.Context, t *Task) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (id, session_id, directory, prompt, status, error, result, progress, attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', '', '', 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		t.ID, t.SessionID, t.Directory, t.Prompt, TaskQueued,
	)
	return err
}

// ListTasks returns tasks ordered newest first, with an optional status filter.
func (s *sqlStore) ListTasks(ctx context.Context, status string, limit int) ([]*Task, error) {
	query := `SELECT id, session_id, directory, prompt, status, error, result, progress, attempts, created_at, updated_at, started_at, finished_at FROM tasks`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// GetTask loads a single task.
func (s *sqlStore) GetTask(ctx context.Context, id string) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, directory, prompt, status, error, result, progress, attempts, created_at, updated_at, started_at, finished_at
		FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ClaimNextTask atomically picks the oldest queued task for execution.
func (s *sqlStore) ClaimNextTask(ctx context.Context) (*Task, error) {
	// Single statement keeps it atomic enough for a single-node backend.
	row := s.db.QueryRowContext(ctx, `
		UPDATE tasks SET status = ?, started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = (
			SELECT id FROM tasks WHERE status = ? ORDER BY created_at ASC LIMIT 1
		)
		RETURNING id, session_id, directory, prompt, status, error, result, progress, attempts, created_at, updated_at, started_at, finished_at`,
		TaskRunning, TaskQueued)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// UpdateTaskProgress records a progress note for a running task.
func (s *sqlStore) UpdateTaskProgress(ctx context.Context, id, progress string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET progress = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, progress, id)
	return err
}

// SetTaskSession records the resolved session id for a task.
func (s *sqlStore) SetTaskSession(ctx context.Context, id, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET session_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID, id)
	return err
}

// CompleteTask marks a task as succeeded with a result.
func (s *sqlStore) CompleteTask(ctx context.Context, id, result string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, result = ?, finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, TaskSucceeded, result, id)
	return err
}

// FailTask marks a task as failed.
func (s *sqlStore) FailTask(ctx context.Context, id, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, error = ?, finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, TaskFailed, errMsg, id)
	return err
}

// CancelTask marks a queued/running task as canceled.
func (s *sqlStore) CancelTask(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status IN (?, ?)`,
		TaskCanceled, id, TaskQueued, TaskRunning)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (*Task, error) {
	t := &Task{}
	var started, finished *time.Time
	err := row.Scan(&t.ID, &t.SessionID, &t.Directory, &t.Prompt, &t.Status,
		&t.Error, &t.Result, &t.Progress, &t.Attempts, &t.CreatedAt, &t.UpdatedAt, &started, &finished)
	if err != nil {
		return nil, err
	}
	t.StartedAt = started
	t.FinishedAt = finished
	return t, err
}

func scanTasks(rows *sql.Rows) ([]*Task, error) {
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
