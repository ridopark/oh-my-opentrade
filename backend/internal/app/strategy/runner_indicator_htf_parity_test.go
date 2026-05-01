package strategy_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// TestRunner_HTF_IndicatorServiceParity asserts the runner's HTF state
// is sourced from the injected indicator.Service. After driving N 1m RTH
// bars through HandleBarDirectTyped the test compares
// indicator.Service.LastSnapshot(sym, "5m") against a control
// IndicatorCalculator fed the SAME closed 5m bars in isolation; both
// views must be bit-equal at every 5m close.
func TestRunner_HTF_IndicatorServiceParity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 20, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")
	const tfHTF = domain.Timeframe("5m")

	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	idx := indicator.NewService("runner_htf_parity")
	r := strategy.NewRunner(bus, router, "test-tenant", envMode, nil, strategy.WithIndicator(idx))

	fs := newFakeStrategy("htf_parity_strat", "1.0.0")
	id, _ := start.NewInstanceID("htf_parity_strat:1.0.0:" + sym.String())
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:    []string{sym.String()},
		Timeframes: []string{"1m", "5m"},
		Priority:   100,
	}, start.LifecycleLiveActive, nil)

	tctx := newTestCtx()
	if err := inst.InitSymbol(tctx, sym.String(), nil); err != nil {
		t.Fatalf("init symbol: %v", err)
	}
	router.Register(inst)

	r.InitAggregators(anchor)

	control := monitor.NewIndicatorCalculator()
	control.Label = "runner_htf_parity_control"
	ctrlAgg, err := domain.NewBarAggregator(sym, tfHTF, anchor)
	if err != nil {
		t.Fatalf("ctrl aggregator: %v", err)
	}

	bars := indicatortest.MakeBars(sym, 200.0, anchor, 240)
	closedCount := 0
	for i, bar := range bars {
		if err := r.HandleBarDirectTyped(context.Background(), bar, "test-tenant", envMode); err != nil {
			t.Fatalf("HandleBarDirectTyped bar %d: %v", i, err)
		}
		closed, emitted := ctrlAgg.Push(bar)
		if !emitted {
			continue
		}
		controlSnap := control.Update(closed)
		idxSnap, ok := idx.LastSnapshot(sym, tfHTF)
		if !ok {
			t.Fatalf("indicator.LastSnapshot missing for %s/%s after %d closures", sym, tfHTF, closedCount+1)
		}
		ctxStr := fmt.Sprintf("closed=%d closedTime=%s", closedCount, closed.Time.Format(time.RFC3339))
		indicatortest.AssertSnapshotsBitEqual(t, "runner HTF -> indicator.Service", idxSnap, controlSnap, ctxStr)
		closedCount++
	}
	if closedCount == 0 {
		t.Fatalf("no 5m closures observed across %d 1m bars", len(bars))
	}
}
