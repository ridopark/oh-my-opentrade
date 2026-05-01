package indicator_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

func isolationAnchor(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	return time.Date(2026, 4, 29, 9, 30, 0, 0, loc)
}

func TestService_ParallelInstances_StateIsolated(t *testing.T) {
	anchor := isolationAnchor(t)

	svc1 := indicator.NewService("backtest_a")
	svc2 := indicator.NewService("backtest_b")
	svc1.SetSessionOpen(anchor)
	svc2.SetSessionOpen(anchor)

	const symA domain.Symbol = "AAPL"
	const symB domain.Symbol = "MSFT"
	const tf1m domain.Timeframe = "1m"
	const tf5m domain.Timeframe = "5m"

	var hits1, hits2 int
	svc1.Subscribe(symA, tf5m, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		hits1++
	})
	svc2.Subscribe(symB, tf5m, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		hits2++
	})

	barsA := indicatortest.MakeBars(symA, 200.0, anchor, 30)
	barsB := indicatortest.MakeBars(symB, 400.0, anchor, 30)
	for _, b := range barsA {
		svc1.Update(b)
	}
	for _, b := range barsB {
		svc2.Update(b)
	}

	if _, ok := svc1.LastSnapshot(symA, tf1m); !ok {
		t.Fatalf("svc1 missing AAPL/1m snapshot after feed")
	}
	if _, ok := svc1.LastSnapshot(symB, tf1m); ok {
		t.Fatalf("svc1 leaked MSFT state from svc2")
	}
	if _, ok := svc2.LastSnapshot(symB, tf1m); !ok {
		t.Fatalf("svc2 missing MSFT/1m snapshot after feed")
	}
	if _, ok := svc2.LastSnapshot(symA, tf1m); ok {
		t.Fatalf("svc2 leaked AAPL state from svc1")
	}
	if hits1 == 0 {
		t.Fatalf("svc1 5m subscriber never fired")
	}
	if hits2 == 0 {
		t.Fatalf("svc2 5m subscriber never fired")
	}
}

func TestService_ConcurrentInstances_NoCrossContamination(t *testing.T) {
	anchor := isolationAnchor(t)
	const goroutines = 8
	const barsPer = 60

	type result struct {
		sym  domain.Symbol
		snap domain.IndicatorSnapshot
	}
	results := make([]result, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			sym := domain.Symbol(fmt.Sprintf("SYM%02d", g))
			svc := indicator.NewService(fmt.Sprintf("concurrent_%d", g))
			svc.SetSessionOpen(anchor)
			bars := indicatortest.MakeBars(sym, 100.0+float64(g)*10.0, anchor, barsPer)
			var last domain.IndicatorSnapshot
			for _, b := range bars {
				last = svc.Update(b)
			}
			results[g] = result{sym: sym, snap: last}
		}()
	}
	wg.Wait()

	for i, r := range results {
		if r.snap.Symbol != r.sym {
			t.Fatalf("goroutine %d: snap.Symbol=%q want %q", i, r.snap.Symbol, r.sym)
		}
		if r.snap.EMA9 == 0 {
			t.Fatalf("goroutine %d: %s EMA9 zero after %d bars", i, r.sym, barsPer)
		}
	}

	verify := indicator.NewService("verify_oracle")
	verify.SetSessionOpen(anchor)
	for g, r := range results {
		bars := indicatortest.MakeBars(r.sym, 100.0+float64(g)*10.0, anchor, barsPer)
		var last domain.IndicatorSnapshot
		for _, b := range bars {
			last = verify.Update(b)
		}
		indicatortest.AssertSnapshotsBitEqual(t, "concurrent isolation", r.snap, last,
			fmt.Sprintf("g=%d sym=%s", g, r.sym))
	}
}
