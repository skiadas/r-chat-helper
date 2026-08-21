package controlplane

import (
	"context"
	"time"
)

// DiffTokens returns the per-session token delta since a previous snapshot,
// clamping any negative values (which would indicate a reset) to zero.
func DiffTokens(cur, prev Tokens) Tokens {
	d := func(c, p int64) int64 {
		if c < p {
			return 0
		}
		return c - p
	}
	return Tokens{Input: d(cur.Input, prev.Input), Output: d(cur.Output, prev.Output), CacheRead: d(cur.CacheRead, prev.CacheRead)}
}

func (d Tokens) Zero() bool {
	return d.Input == 0 && d.Output == 0 && d.CacheRead == 0
}

// ComputeCostMicros prices a token delta at list rates, returning frozen
// micro-dollars, or -1 if no rate is available.
func ComputeCostMicros(delta Tokens, r *Rate) int64 {
	if r == nil {
		return -1
	}
	return (delta.Input*r.InputMicros + delta.Output*r.OutputMicros + delta.CacheRead*r.CacheMicros) / 1_000_000
}

// RecordUsage diffs a session's new cumulative token totals against the stored
// snapshot, prices the delta at the current rate, stores the frozen usage
// event, and advances the snapshot. Returns nil when there is no delta.
func (a *App) RecordUsage(ctx context.Context, studentID, sessionID, model string, cur Tokens) (*UsageEvent, error) {
	prev, hasPrev, err := a.SnapshotFor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var delta Tokens
	if hasPrev {
		delta = DiffTokens(cur, *prev)
	} else {
		delta = cur
	}
	if delta.Zero() {
		return nil, nil
	}
	r, err := a.RateFor(ctx, "class-go", model)
	if err != nil {
		return nil, err
	}
	cost := ComputeCostMicros(delta, r)
	now := time.Now().Unix()
	if cost >= 0 {
		e := UsageEvent{
			StudentID:       studentID,
			SessionID:       sessionID,
			Model:           model,
			RecordedAt:      now,
			InputTokens:     delta.Input,
			OutputTokens:    delta.Output,
			CacheReadTokens: delta.CacheRead,
			CostMicros:      cost,
		}
		if err := a.AddUsageEvent(ctx, e); err != nil {
			return nil, err
		}
		if err := a.UpsertSnapshot(ctx, sessionID, cur, now); err != nil {
			return nil, err
		}
		return &e, nil
	}
	// No rate yet: leave the snapshot untouched so the eventual first sync
	// prices the accumulated usage rather than dropping it.
	return nil, nil
}

// RecordInteraction prices a single interaction's token usage (a per-message
// delta, not a cumulative counter) at list rates and stores a frozen usage
// event. Unlike RecordUsage it does not diff against a stored snapshot,
// because this app's client reports per-interaction usage directly.
func (a *App) RecordInteraction(ctx context.Context, studentID, sessionID, model string, delta Tokens) (*UsageEvent, error) {
	if delta.Zero() {
		return nil, nil
	}
	r, err := a.RateFor(ctx, "class-go", model)
	if err != nil {
		return nil, err
	}
	cost := ComputeCostMicros(delta, r)
	if cost < 0 {
		return nil, nil
	}
	e := UsageEvent{
		StudentID:       studentID,
		SessionID:       sessionID,
		Model:           model,
		RecordedAt:      time.Now().Unix(),
		InputTokens:     delta.Input,
		OutputTokens:    delta.Output,
		CacheReadTokens: delta.CacheRead,
		CostMicros:      cost,
	}
	if err := a.AddUsageEvent(ctx, e); err != nil {
		return nil, err
	}
	return &e, nil
}
