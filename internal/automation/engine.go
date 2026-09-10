package automation

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/hiylo/opencode-backend/internal/store"
)

// Engine evaluates automation rules and enqueues tasks when a rule fires.
// Cron rules are polled on a ticker; git/http rules are fired explicitly via
// Fire() (typically from an HTTP webhook handler).
type Engine struct {
	store store.Store
	// interval is the poll period for cron rules.
	interval time.Duration
	// now is a clock hook for tests.
	now func() time.Time
}

// NewEngine creates an automation engine polling cron rules every interval.
func NewEngine(st store.Store, interval time.Duration) *Engine {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Engine{store: st, interval: interval, now: time.Now}
}

// Run polls enabled cron rules and fires any that are due. It blocks until ctx
// is canceled.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.pollCron(ctx); err != nil {
				log.Printf("automation: cron poll: %v", err)
			}
		}
	}
}

// pollCron checks all enabled cron rules and fires due ones.
func (e *Engine) pollCron(ctx context.Context) error {
	rules, err := e.store.ListRules(ctx)
	if err != nil {
		return err
	}
	now := e.now()
	for _, r := range rules {
		if !r.Enabled || r.Kind != store.TriggerCron {
			continue
		}
		if due, err := e.cronDue(r, now); err != nil {
			log.Printf("automation: rule %s cron parse: %v", r.ID, err)
			continue
		} else if due {
			if err := e.Fire(ctx, r.ID); err != nil {
				log.Printf("automation: fire rule %s: %v", r.ID, err)
			}
		}
	}
	return nil
}

// Fire enqueues a task for the given rule (cron/git/http all use this path).
// Returns ErrNotFound if the rule does not exist.
func (e *Engine) Fire(ctx context.Context, ruleID string) error {
	rule, err := e.store.GetRule(ctx, ruleID)
	if err != nil {
		return err
	}
	if !rule.Enabled {
		return nil
	}
	t := &store.Task{
		ID:        newRuleTaskID(ruleID),
		Directory: rule.Directory,
		Prompt:    rule.Prompt,
	}
	if err := e.store.CreateTask(ctx, t); err != nil {
		return err
	}
	return e.store.MarkRuleFired(ctx, ruleID)
}

// FireKind enqueues a task for the first enabled rule of the given kind whose
// schedule matches target. Returns (fired bool). Used by webhook handlers that
// do not know the rule id but carry a target (repo path / http path).
func (e *Engine) FireKind(ctx context.Context, kind, target string) (bool, error) {
	rules, err := e.store.ListRules(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range rules {
		if !r.Enabled || r.Kind != kind {
			continue
		}
		if target != "" && r.Schedule != "" && !matchTarget(r.Schedule, target) {
			continue
		}
		if err := e.Fire(ctx, r.ID); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// cronDue reports whether a cron rule's schedule has elapsed since last fire.
// The schedule uses simple "N s/m/h" or cron "* * * * * *" (second-level).
// For MVP only interval-like schedules are evaluated; full cron is parsed
// loosely to avoid pulling an external dependency offline.
func (e *Engine) cronDue(r *store.Rule, now time.Time) (bool, error) {
	s := strings.TrimSpace(r.Schedule)
	if s == "" {
		return false, nil
	}
	last := r.LastFiredAt
	if last == nil {
		return true, nil // never fired -> fire immediately
	}
	// Interval syntax: "30s", "5m", "1h".
	if d, err := time.ParseDuration(s); err == nil {
		return now.Sub(*last) >= d, nil
	}
	// Cron syntax (6 fields, second-level). Compute next fire from last fire.
	next, err := nextCron(*last, strings.Fields(s))
	if err != nil {
		return false, err
	}
	return !now.Before(next), nil
}

func matchTarget(schedule, target string) bool {
	return schedule == target
}

func newRuleTaskID(ruleID string) string {
	return "task_" + ruleID + "_" + time.Now().Format("20060102150405")
}
