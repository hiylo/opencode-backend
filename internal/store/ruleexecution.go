package store

import (
	"context"
	"database/sql"
	"time"
)

// RuleExecution records a single firing of an automation rule and the task
// it produced, giving operators a history of rule activity.
type RuleExecution struct {
	ID          int64     `json:"id"`
	RuleID      string    `json:"ruleId"`
	TaskID      string    `json:"taskId"`
	TriggeredAt time.Time `json:"triggeredAt"`
}

// RecordRuleExecution inserts a rule execution log entry.
func (s *sqlStore) RecordRuleExecution(ctx context.Context, ruleID, taskID string) error {
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO rule_executions (rule_id, task_id, triggered_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)`),
		ruleID, taskID)
	return err
}

// ListRuleExecutions returns the recent executions of a single rule, newest
// first. If ruleID is empty, returns executions across all rules.
func (s *sqlStore) ListRuleExecutions(ctx context.Context, ruleID string, limit int) ([]*RuleExecution, error) {
	query := `SELECT id, rule_id, task_id, triggered_at FROM rule_executions`
	args := []any{}
	if ruleID != "" {
		query += ` WHERE rule_id = ?`
		args = append(args, ruleID)
	}
	query += ` ORDER BY id DESC`
	if limit <= 0 {
		limit = 50
	}
	query += ` LIMIT ` + itoa(limit)

	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*RuleExecution, 0)
	for rows.Next() {
		e := &RuleExecution{}
		if err := rows.Scan(&e.ID, &e.RuleID, &e.TaskID, &e.TriggeredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountRuleExecutions returns the total executions for a rule (or all rules
// when ruleID is empty).
func (s *sqlStore) CountRuleExecutions(ctx context.Context, ruleID string) (int, error) {
	query := `SELECT COUNT(*) FROM rule_executions`
	var args []any
	if ruleID != "" {
		query += ` WHERE rule_id = ?`
		args = append(args, ruleID)
	}
	var n int
	err := s.db.QueryRowContext(ctx, s.q(query), args...).Scan(&n)
	return n, err
}

var _ = sql.ErrNoRows
