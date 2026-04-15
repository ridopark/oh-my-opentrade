package strategy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
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
