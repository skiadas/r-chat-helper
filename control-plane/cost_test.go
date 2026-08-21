package controlplane

import (
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DBPath = t.TempDir() + "/test.db"
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app
}

func TestComputeCostMicros(t *testing.T) {
	r := &Rate{InputMicros: 140000, OutputMicros: 280000, CacheMicros: 2800}
	got := ComputeCostMicros(Tokens{Input: 1000, Output: 100, CacheRead: 500000}, r)
	// (1000*140000 + 100*280000 + 500000*2800) / 1e6 = (140M + 28M + 1400M)/1e6 = 1568
	if got != 1568 {
		t.Fatalf("ComputeCostMicros = %d, want 1568", got)
	}
	if ComputeCostMicros(Tokens{Input: 1}, nil) != -1 {
		t.Fatal("expected -1 when rate is nil")
	}
}

func TestDiffTokensClamps(t *testing.T) {
	got := DiffTokens(Tokens{Input: 5, Output: 2}, Tokens{Input: 10, Output: 1})
	if got.Input != 0 || got.Output != 1 {
		t.Fatalf("DiffTokens = %+v, want input=0 output=1", got)
	}
}

func TestRecordUsageFrozenCost(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}
	if err := app.UpsertRate(ctx, Rate{Provider: "class-go", Model: LockedModelID, InputMicros: 140000, OutputMicros: 280000, CacheMicros: 2800}); err != nil {
		t.Fatal(err)
	}

	// No rate for another model -> no event, snapshot left untouched.
	if e, err := app.RecordUsage(ctx, "alice", "s1", "other-model", Tokens{Input: 5000}); err != nil || e != nil {
		t.Fatalf("expected nil event for missing rate, got %v err=%v", e, err)
	}

	e1, err := app.RecordUsage(ctx, "alice", "s1", LockedModelID, Tokens{Input: 5000, Output: 3})
	if err != nil {
		t.Fatal(err)
	}
	if e1 == nil || e1.CostMicros != (5000*140000+3*280000)/1_000_000 {
		t.Fatalf("unexpected first event: %+v", e1)
	}

	// Second usage only prices the delta.
	e2, err := app.RecordUsage(ctx, "alice", "s1", LockedModelID, Tokens{Input: 5200, Output: 8})
	if err != nil {
		t.Fatal(err)
	}
	if e2 == nil || e2.InputTokens != 200 || e2.OutputTokens != 5 {
		t.Fatalf("unexpected second event: %+v", e2)
	}

	// Same totals again -> no event.
	e3, err := app.RecordUsage(ctx, "alice", "s1", LockedModelID, Tokens{Input: 5200, Output: 8})
	if err != nil || e3 != nil {
		t.Fatalf("expected nil for no-op, got %v err=%v", e3, err)
	}

	spent, err := app.SpendByStudent(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if spent != e1.CostMicros+e2.CostMicros {
		t.Fatalf("spent = %d, want %d", spent, e1.CostMicros+e2.CostMicros)
	}
}

func TestRecordInteractionPricesPerMessage(t *testing.T) {
	app := newTestApp(t)
	ctx := t.Context()
	if err := app.AddStudent(ctx, "bob", "bob@college.edu", "Bob", 0); err != nil {
		t.Fatal(err)
	}
	if err := app.UpsertRate(ctx, Rate{Provider: "class-go", Model: LockedModelID, InputMicros: 140000, OutputMicros: 280000, CacheMicros: 2800}); err != nil {
		t.Fatal(err)
	}

	// Two identical interactions must each be priced in full (no cumulative diff).
	e1, err := app.RecordInteraction(ctx, "bob", "s1", LockedModelID, Tokens{Input: 1000, Output: 5})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := app.RecordInteraction(ctx, "bob", "s1", LockedModelID, Tokens{Input: 1000, Output: 5})
	if err != nil {
		t.Fatal(err)
	}
	if e1 == nil || e2 == nil || e1.CostMicros != e2.CostMicros {
		t.Fatalf("expected two equal fully-priced events, got %+v and %+v", e1, e2)
	}

	// Zero usage produces no event.
	e3, err := app.RecordInteraction(ctx, "bob", "s1", LockedModelID, Tokens{})
	if err != nil || e3 != nil {
		t.Fatalf("expected nil for zero usage, got %v err=%v", e3, err)
	}
}
