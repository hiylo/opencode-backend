package automation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hiylo/opencode-backend/internal/store"
)

func newTestEngine(t *testing.T) (*Engine, store.Store) {
	t.Helper()
	dsn := store.SQLiteDSN(filepath.Join(t.TempDir(), "test.db"))
	st, err := store.OpenFromConfig(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewEngine(st, time.Hour), st
}

func TestFireCreatesTask(t *testing.T) {
	ctx := context.Background()
	eng, st := newTestEngine(t)

	rule := &store.Rule{ID: "rule_1", Name: "nightly", Kind: store.TriggerCron, Schedule: "5m", Directory: "/w", Prompt: "run tests", Enabled: true}
	if err := st.CreateRule(ctx, rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	if err := eng.Fire(ctx, "rule_1"); err != nil {
		t.Fatalf("fire: %v", err)
	}

	tasks, err := st.ListTasks(ctx, "", 10)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Prompt != "run tests" {
		t.Fatalf("task prompt %q", tasks[0].Prompt)
	}
	got, _ := st.GetRule(ctx, "rule_1")
	if got.LastFiredAt == nil {
		t.Fatalf("last_fired_at not set")
	}
}

func TestFireDisabledRuleNoOp(t *testing.T) {
	ctx := context.Background()
	eng, st := newTestEngine(t)
	rule := &store.Rule{ID: "rule_dis", Name: "off", Kind: store.TriggerCron, Prompt: "p", Enabled: false}
	if err := st.CreateRule(ctx, rule); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := eng.Fire(ctx, "rule_dis"); err != nil {
		t.Fatalf("fire disabled: %v", err)
	}
	tasks, _ := st.ListTasks(ctx, "", 10)
	if len(tasks) != 0 {
		t.Fatalf("disabled rule created a task")
	}
}

func TestFireKindMatchesTarget(t *testing.T) {
	ctx := context.Background()
	eng, st := newTestEngine(t)
	rule := &store.Rule{ID: "rule_web", Name: "webhook", Kind: store.TriggerHTTP, Schedule: "/workspaces/opencode", Prompt: "post", Enabled: true}
	if err := st.CreateRule(ctx, rule); err != nil {
		t.Fatalf("create: %v", err)
	}

	fired, err := eng.FireKind(ctx, store.TriggerHTTP, "/workspaces/opencode")
	if err != nil || !fired {
		t.Fatalf("firekind: fired=%v err=%v", fired, err)
	}
	tasks, _ := st.ListTasks(ctx, "", 10)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	// Non-matching target must not fire.
	fired, err = eng.FireKind(ctx, store.TriggerHTTP, "/other")
	if err != nil {
		t.Fatalf("firekind nonmatch err: %v", err)
	}
	if fired {
		t.Fatalf("non-matching target fired")
	}
}

func TestCronDueInterval(t *testing.T) {
	ctx := context.Background()
	eng, st := newTestEngine(t)
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	// Rule with interval schedule.
	rule := &store.Rule{ID: "cron_int", Name: "every5m", Kind: store.TriggerCron, Schedule: "5m", Prompt: "p", Enabled: true}
	if err := st.CreateRule(ctx, rule); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Never fired -> due immediately.
	due, err := eng.cronDue(rule, base)
	if err != nil || !due {
		t.Fatalf("never-fired rule should be due: due=%v err=%v", due, err)
	}

	// After firing at base-4m, now=base -> not due (only 4m elapsed < 5m).
	rule.LastFiredAt = ptrTime(base.Add(-4 * time.Minute))
	due, err = eng.cronDue(rule, base)
	if err != nil || due {
		t.Fatalf("should not be due yet: due=%v err=%v", due, err)
	}

	// After firing at base-6m, now=base -> due.
	rule.LastFiredAt = ptrTime(base.Add(-6 * time.Minute))
	due, err = eng.cronDue(rule, base)
	if err != nil || !due {
		t.Fatalf("should be due: due=%v err=%v", due, err)
	}
}

func TestNextCronEverySecond(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	next, err := nextCron(base, []string{"*", "*", "*", "*", "*", "*"})
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if !next.After(base) {
		t.Fatalf("next must be after base: %v", next)
	}
	if next.Sub(base) != time.Second {
		t.Fatalf("expected +1s, got %v", next.Sub(base))
	}
}

func TestNextCronAtMinute(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	// Every 5 minutes -> next is 12:05:00.
	next, err := nextCron(base, []string{"0", "*/5", "*", "*", "*", "*"})
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	want := time.Date(2026, 9, 10, 12, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v want %v", next, want)
	}
}

func TestNextCronInvalidFields(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if _, err := nextCron(base, []string{"0", "60", "*", "*", "*", "*"}); err == nil {
		t.Fatalf("expected error for minute=60")
	}
	if _, err := nextCron(base, []string{"*", "*", "*", "*", "*"}); err == nil {
		t.Fatalf("expected error for 5 fields")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
