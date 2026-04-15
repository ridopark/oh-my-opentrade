package strategy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

func TestLivenessTracker_RecordTickAndEval(t *testing.T) {
	tr := NewLivenessTracker()
	at := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
	tr.Register("alpha", "AAPL")

	tr.RecordTick("alpha", "AAPL", at)
	tr.RecordEval("alpha", "AAPL", at, &domain.DecisionReason{At: at, Outcome: "HOLD", Summary: "below VWAP"})

	snap := tr.Snapshot("alpha")
	if len(snap) != 1 {
		t.Fatalf("want 1 symbol, got %d", len(snap))
	}
	row := snap[0]
	if row.Symbol != "AAPL" {
		t.Fatalf("symbol=%q", row.Symbol)
	}
	if row.EvalCount != 1 || row.BarsToday != 1 {
		t.Fatalf("evalCount=%d barsToday=%d", row.EvalCount, row.BarsToday)
	}
	if row.LastTickAt.IsZero() || row.LastEvalAt.IsZero() {
		t.Fatalf("timestamps should be set: tick=%v eval=%v", row.LastTickAt, row.LastEvalAt)
	}
	if row.LastDecision == nil || row.LastDecision.Outcome != "HOLD" {
		t.Fatalf("decision=%+v", row.LastDecision)
	}
}

func TestLivenessTracker_ConcurrentRecord(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	tr.Register("alpha", "MSFT")

	const goroutines = 16
	const iters = 500
	var wg sync.WaitGroup
	at := time.Now()

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sym := "AAPL"
			if id%2 == 0 {
				sym = "MSFT"
			}
			for i := 0; i < iters; i++ {
				tr.RecordTick("alpha", sym, at)
				tr.RecordEval("alpha", sym, at, nil)
				if i%10 == 0 {
					tr.RecordSignal("alpha", sym, at)
				}
				if i%50 == 0 {
					_ = tr.Snapshot("alpha")
				}
			}
		}(g)
	}
	wg.Wait()

	snap := tr.Snapshot("alpha")
	if len(snap) != 2 {
		t.Fatalf("want 2 symbols, got %d", len(snap))
	}
	var totalEvals uint64
	for _, row := range snap {
		totalEvals += row.EvalCount
	}
	if got, want := totalEvals, uint64(goroutines*iters); got != want {
		t.Fatalf("totalEvals=%d want=%d", got, want)
	}
}

func TestLivenessTracker_DayRollover(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")

	// Day 1: 11:00 ET on April 14.
	ny := domain.NYLocation()
	day1 := time.Date(2026, 4, 14, 11, 0, 0, 0, ny)
	for i := 0; i < 5; i++ {
		tr.RecordEval("alpha", "AAPL", day1, nil)
	}
	if snap := tr.Snapshot("alpha"); snap[0].BarsToday != 5 {
		t.Fatalf("barsToday after day1=%d", snap[0].BarsToday)
	}

	// Day 2: next trading day ET, barsToday must reset on first eval.
	day2 := time.Date(2026, 4, 15, 9, 45, 0, 0, ny)
	tr.RecordEval("alpha", "AAPL", day2, nil)
	snap := tr.Snapshot("alpha")
	if snap[0].BarsToday != 1 {
		t.Fatalf("barsToday after rollover=%d (want 1)", snap[0].BarsToday)
	}
	// evalCount is cumulative and must NOT reset.
	if snap[0].EvalCount != 6 {
		t.Fatalf("evalCount=%d want 6", snap[0].EvalCount)
	}
}

func TestLivenessTracker_DayRolloverCryptoUTC(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "BTC/USD")

	// 23:50 UTC → 00:10 UTC next day should rollover for crypto.
	d1 := time.Date(2026, 4, 14, 23, 50, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 15, 0, 10, 0, 0, time.UTC)
	tr.RecordEval("alpha", "BTC/USD", d1, nil)
	tr.RecordEval("alpha", "BTC/USD", d1, nil)
	tr.RecordEval("alpha", "BTC/USD", d2, nil)

	snap := tr.Snapshot("alpha")
	if snap[0].BarsToday != 1 {
		t.Fatalf("crypto barsToday after UTC rollover=%d", snap[0].BarsToday)
	}
}

func TestLivenessTracker_SnapshotStability(t *testing.T) {
	tr := NewLivenessTracker()
	// Register symbols out of sort order; Snapshot must return them sorted.
	syms := []string{"TSLA", "AAPL", "NVDA", "MSFT"}
	for _, s := range syms {
		tr.Register("alpha", s)
	}
	at := time.Now()
	for _, s := range syms {
		tr.RecordEval("alpha", s, at, nil)
	}

	snap := tr.Snapshot("alpha")
	want := []string{"AAPL", "MSFT", "NVDA", "TSLA"}
	if len(snap) != len(want) {
		t.Fatalf("len=%d want=%d", len(snap), len(want))
	}
	for i, w := range want {
		if snap[i].Symbol != w {
			t.Fatalf("snap[%d]=%q want %q", i, snap[i].Symbol, w)
		}
	}

	// Mutating the returned slice must not affect subsequent snapshots.
	snap[0].EvalCount = 9999
	snap2 := tr.Snapshot("alpha")
	if snap2[0].EvalCount == 9999 {
		t.Fatalf("snapshot mutation leaked into tracker")
	}
}

func TestLivenessTracker_UnknownStrategyEmpty(t *testing.T) {
	tr := NewLivenessTracker()
	snap := tr.Snapshot("does_not_exist")
	if len(snap) != 0 {
		t.Fatalf("want empty, got %d", len(snap))
	}
}

// TestLivenessTracker_EvaluationEmitThrottle verifies the 1-second throttle
// rule: the first RecordEval after registration publishes immediately, and
// subsequent calls within the window coalesce into a single event. Once the
// window elapses a second publish is admitted.
func TestLivenessTracker_EvaluationEmitThrottle(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")

	var mu sync.Mutex
	var events []domain.Event
	tr.SetPublisher(func(evt domain.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt)
	})

	at := time.Now()
	// 10 rapid calls should emit exactly 1 (the first).
	for i := 0; i < 10; i++ {
		tr.RecordEval("alpha", "AAPL", at, &domain.DecisionReason{At: at, Outcome: "HOLD"})
	}

	mu.Lock()
	count1 := len(events)
	mu.Unlock()
	if count1 != 1 {
		t.Fatalf("want exactly 1 event after 10 rapid calls, got %d", count1)
	}

	evt := events[0]
	if evt.Type != domain.EventStrategyEvaluation {
		t.Fatalf("event type=%q", evt.Type)
	}
	payload, ok := evt.Payload.(domain.StrategyEvaluationPayload)
	if !ok {
		t.Fatalf("payload type=%T", evt.Payload)
	}
	if payload.Strategy != "alpha" || payload.Symbol != "AAPL" {
		t.Fatalf("payload=%+v", payload)
	}
	if payload.LastDecision == nil || payload.LastDecision.Outcome != "HOLD" {
		t.Fatalf("payload decision=%+v", payload.LastDecision)
	}

	// After crossing the 1-second gap, one more publish should go through.
	// We simulate elapsed time by rewinding the entry's lastEmitNano rather
	// than actually sleeping, to keep the test fast and hermetic.
	entry := tr.entry("alpha", "AAPL")
	entry.lastEmitNano.Store(time.Now().Add(-2 * time.Second).UnixNano())
	tr.RecordEval("alpha", "AAPL", at, nil)

	mu.Lock()
	count2 := len(events)
	mu.Unlock()
	if count2 != 2 {
		t.Fatalf("want 2 events after gap elapsed, got %d", count2)
	}
}

// TestLivenessTracker_EvaluationEmitDifferentKeys asserts throttle state is
// per (strategy, symbol) — two distinct keys each get their own first-emit
// even when called back-to-back.
func TestLivenessTracker_EvaluationEmitDifferentKeys(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	tr.Register("alpha", "MSFT")
	tr.Register("beta", "AAPL")

	var mu sync.Mutex
	var events []domain.Event
	tr.SetPublisher(func(evt domain.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt)
	})

	at := time.Now()
	tr.RecordEval("alpha", "AAPL", at, nil)
	tr.RecordEval("alpha", "MSFT", at, nil)
	tr.RecordEval("beta", "AAPL", at, nil)

	// Second call for each key within the throttle window should be dropped.
	tr.RecordEval("alpha", "AAPL", at, nil)
	tr.RecordEval("alpha", "MSFT", at, nil)
	tr.RecordEval("beta", "AAPL", at, nil)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 {
		t.Fatalf("want 3 events (one per key), got %d", len(events))
	}

	seen := make(map[string]bool)
	for _, evt := range events {
		p := evt.Payload.(domain.StrategyEvaluationPayload)
		seen[p.Strategy+":"+p.Symbol] = true
	}
	for _, key := range []string{"alpha:AAPL", "alpha:MSFT", "beta:AAPL"} {
		if !seen[key] {
			t.Fatalf("missing emit for %s (seen=%v)", key, seen)
		}
	}
}

// TestLivenessTracker_EvaluationNoPublisher confirms RecordEval remains
// side-effect-free when no publisher is installed. This is the backtest
// code path (Runner is constructed without SetPublisher wiring) and the
// broader DisableLiveness path which short-circuits RecordEval entirely.
func TestLivenessTracker_EvaluationNoPublisher(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	// Intentionally omit SetPublisher.

	at := time.Now()
	for i := 0; i < 5; i++ {
		tr.RecordEval("alpha", "AAPL", at, nil)
	}

	// No crash, counters still increment.
	snap := tr.Snapshot("alpha")
	if len(snap) != 1 || snap[0].EvalCount != 5 {
		t.Fatalf("counters wrong after nil-publisher path: snap=%+v", snap)
	}
}

// TestLivenessTracker_BarsPerMinute_Rotation drives rotateAll manually at
// known wall-clock minute boundaries and verifies (a) each slot holds the
// eval delta for its minute, (b) slots advance modulo 60, and (c) the
// Snapshot's barsPerMinute array is ordered oldest->newest relative to the
// most recent rotation.
func TestLivenessTracker_BarsPerMinute_Rotation(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	entry := tr.entry("alpha", "AAPL")

	// Minute 10: 3 evals.
	t10 := time.Date(2026, 4, 15, 14, 10, 30, 0, time.UTC)
	for i := 0; i < 3; i++ {
		tr.RecordEval("alpha", "AAPL", t10, nil)
	}
	tr.rotateAll(t10)
	if got := entry.barsPerMinute[10]; got != 3 {
		t.Fatalf("slot 10 after first rotation=%d want 3", got)
	}

	// Minute 11: 5 more evals (cumulative evalCount=8, delta=5).
	t11 := t10.Add(time.Minute)
	for i := 0; i < 5; i++ {
		tr.RecordEval("alpha", "AAPL", t11, nil)
	}
	tr.rotateAll(t11)
	if got := entry.barsPerMinute[11]; got != 5 {
		t.Fatalf("slot 11 after second rotation=%d want 5", got)
	}
	if got := entry.barsPerMinute[10]; got != 3 {
		t.Fatalf("slot 10 must not be overwritten by slot-11 rotation, got %d", got)
	}

	// Minute 12: zero evals -> slot records 0 delta (important: sparkline
	// draws "silent minute" differently from "tracker never ran").
	t12 := t11.Add(time.Minute)
	tr.rotateAll(t12)
	if got := entry.barsPerMinute[12]; got != 0 {
		t.Fatalf("slot 12 after idle rotation=%d want 0", got)
	}

	// Snapshot ordering: newest slot is index 59, oldest index 0. Slot 12
	// was the most recent rotation, so out[59] == barsPerMinute[12] == 0,
	// out[58] == slot 11 (5), out[57] == slot 10 (3).
	snap := tr.Snapshot("alpha")
	if len(snap) != 1 || len(snap[0].BarsPerMinute) != 60 {
		t.Fatalf("snap barsPerMinute len=%d", len(snap[0].BarsPerMinute))
	}
	bpm := snap[0].BarsPerMinute
	if bpm[59] != 0 {
		t.Fatalf("newest slot (idx 59)=%d want 0", bpm[59])
	}
	if bpm[58] != 5 {
		t.Fatalf("idx 58=%d want 5", bpm[58])
	}
	if bpm[57] != 3 {
		t.Fatalf("idx 57=%d want 3", bpm[57])
	}
}

// TestLivenessTracker_BarsPerMinute_WrapsHourBoundary checks the modulo-60
// slot rollover: rotating at minute 59, then 0, must overwrite the oldest
// slot, not index beyond the ring.
func TestLivenessTracker_BarsPerMinute_WrapsHourBoundary(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	entry := tr.entry("alpha", "AAPL")

	t59 := time.Date(2026, 4, 15, 14, 59, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		tr.RecordEval("alpha", "AAPL", t59, nil)
	}
	tr.rotateAll(t59)
	if entry.barsPerMinute[59] != 2 {
		t.Fatalf("slot 59=%d want 2", entry.barsPerMinute[59])
	}

	t00 := time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		tr.RecordEval("alpha", "AAPL", t00, nil)
	}
	tr.rotateAll(t00)
	if entry.barsPerMinute[0] != 7 {
		t.Fatalf("slot 0 after wrap=%d want 7", entry.barsPerMinute[0])
	}

	// After the hour boundary rotation, newest=slot 0; oldest should be
	// slot 1 (empty). Verify oldest->newest ordering still holds.
	snap := tr.Snapshot("alpha")
	bpm := snap[0].BarsPerMinute
	if bpm[59] != 7 {
		t.Fatalf("newest after wrap=%d want 7", bpm[59])
	}
	// slot 59 appears at index 58 (newest-1).
	if bpm[58] != 2 {
		t.Fatalf("prev minute slot expected at idx 58=%d want 2", bpm[58])
	}
}

// TestLivenessTracker_BarsPerMinute_ConcurrentReaders ensures Snapshot does
// not panic when rotateAll runs concurrently. Run with -race to catch
// unsynchronized writes to the ring.
func TestLivenessTracker_BarsPerMinute_ConcurrentReaders(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	tr.Register("alpha", "MSFT")

	var stop atomic.Bool
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for !stop.Load() {
			tr.RecordEval("alpha", "AAPL", now, nil)
			tr.RecordEval("alpha", "MSFT", now, nil)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for !stop.Load() {
			tr.rotateAll(now)
			now = now.Add(time.Minute)
		}
	}()

	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap := tr.Snapshot("alpha")
		for _, row := range snap {
			if len(row.BarsPerMinute) != 60 {
				t.Fatalf("barsPerMinute len=%d during concurrent rotation", len(row.BarsPerMinute))
			}
		}
	}
	stop.Store(true)
	wg.Wait()
}

// TestLivenessTracker_StartStopLifecycle ensures Start/Stop don't panic on
// repeat calls and Stop actually drains the sampler goroutine.
func TestLivenessTracker_StartStopLifecycle(t *testing.T) {
	tr := NewLivenessTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr.Start(ctx)
	tr.Start(ctx) // second call no-ops via startOnce

	done := make(chan struct{})
	go func() {
		tr.Stop()
		tr.Stop() // idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Stop did not return within timeout — sampler leaked?")
	}
}

// Guard against accidentally holding the tracker's internal mutex during a
// Record* call — the Snapshot path takes RLock, so if RecordEval holds
// Lock it would deadlock here.
func TestLivenessTracker_SnapshotConcurrentWithRecord(t *testing.T) {
	tr := NewLivenessTracker()
	tr.Register("alpha", "AAPL")
	var stop atomic.Bool
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			tr.RecordEval("alpha", "AAPL", time.Now(), nil)
		}
	}()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = tr.Snapshot("alpha")
	}
	stop.Store(true)
	wg.Wait()
}

// stubStrategy is a minimal start.Strategy implementation used to verify the
// reasonFromOutcome fallback path for strategies that do NOT implement
// HoldReasoner. Methods return zero values — only interface satisfaction
// matters here.
type stubStrategy struct{}

func (stubStrategy) Meta() start.Meta { return start.Meta{} }
func (stubStrategy) WarmupBars() int  { return 0 }
func (stubStrategy) Init(_ start.Context, _ string, _ map[string]any, _ start.State) (start.State, error) {
	return nil, nil
}
func (stubStrategy) OnBar(_ start.Context, _ string, _ start.Bar, st start.State) (start.State, []start.Signal, error) {
	return st, nil, nil
}
func (stubStrategy) OnEvent(_ start.Context, _ string, _ any, st start.State) (start.State, []start.Signal, error) {
	return st, nil, nil
}

// stubReasoner DOES implement HoldReasoner. Used to verify reasonFromOutcome
// promotes the recorded reason into the returned DecisionReason when the
// strategy returned zero signals for this bar.
type stubReasoner struct {
	reason *domain.DecisionReason
}

func (r *stubReasoner) Meta() start.Meta { return start.Meta{} }
func (r *stubReasoner) WarmupBars() int  { return 0 }
func (r *stubReasoner) Init(_ start.Context, _ string, _ map[string]any, _ start.State) (start.State, error) {
	return nil, nil
}
func (r *stubReasoner) OnBar(_ start.Context, _ string, _ start.Bar, st start.State) (start.State, []start.Signal, error) {
	return st, nil, nil
}
func (r *stubReasoner) OnEvent(_ start.Context, _ string, _ any, st start.State) (start.State, []start.Signal, error) {
	return st, nil, nil
}
func (r *stubReasoner) LastHoldReason(_ string) *domain.DecisionReason { return r.reason }

func TestReasonFromOutcome_EmptySignalsNoReasoner_ReturnsGenericHold(t *testing.T) {
	got := reasonFromOutcome(nil, stubStrategy{}, "AAPL")
	if got == nil {
		t.Fatal("expected non-nil DecisionReason")
	}
	if got.Outcome != "HOLD" {
		t.Fatalf("outcome=%q want HOLD", got.Outcome)
	}
	if got.Summary != "" {
		t.Fatalf("summary=%q want empty (no HoldReasoner installed)", got.Summary)
	}
	if got.At.IsZero() {
		t.Fatal("At should be set to now")
	}
}

func TestReasonFromOutcome_EmptySignalsWithReasoner_UsesRecorded(t *testing.T) {
	at := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
	recorded := &domain.DecisionReason{
		At:      at,
		Outcome: "HOLD",
		Summary: "below VWAP bias",
		Tags:    map[string]string{"gate": "bias", "score": "0.42", "threshold": "0.75"},
	}
	got := reasonFromOutcome(nil, &stubReasoner{reason: recorded}, "AAPL")
	if got == nil {
		t.Fatal("expected non-nil DecisionReason")
	}
	if got.Summary != "below VWAP bias" {
		t.Fatalf("summary=%q want %q", got.Summary, "below VWAP bias")
	}
	if got.Tags["gate"] != "bias" || got.Tags["score"] != "0.42" || got.Tags["threshold"] != "0.75" {
		t.Fatalf("tags=%+v", got.Tags)
	}
}

func TestReasonFromOutcome_EmptySignalsReasonerReturnsNil_FallsBack(t *testing.T) {
	got := reasonFromOutcome(nil, &stubReasoner{reason: nil}, "AAPL")
	if got == nil || got.Outcome != "HOLD" || got.Summary != "" {
		t.Fatalf("want generic HOLD, got %+v", got)
	}
}

func TestReasonFromOutcome_NonEmptySignals_IgnoresReasoner(t *testing.T) {
	// Even when a HoldReasoner is present, a non-empty signal slice wins —
	// the signal expresses the actual outcome (ENTRY/EXIT).
	sig := start.Signal{Type: start.SignalEntry, Side: start.SideBuy, Tags: map[string]string{"setup": "x"}}
	r := &stubReasoner{reason: &domain.DecisionReason{Summary: "should be ignored"}}
	got := reasonFromOutcome([]start.Signal{sig}, r, "AAPL")
	if got.Outcome != "ENTRY" {
		t.Fatalf("outcome=%q want ENTRY", got.Outcome)
	}
	if got.Summary == "should be ignored" {
		t.Fatal("HoldReasoner must not override signal-derived summary")
	}
}
