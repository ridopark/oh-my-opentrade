package indicator_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// TestService_DualDriver_StarvesCallbacks documents the regression PR 6a-2
// introduced and this refactor closes: when TWO callers drive Update for the
// same (sym, time) bar, the BarAggregator dedup at domain/aggregator.go:117
// short-circuits the second call, and ONLY the first caller's Subscribe
// callbacks fire. If a future change re-introduces a second caller (e.g.
// monitor or runner stops trusting the indicator's bus subscription and
// drives Update defensively), this test catches it: the callback count
// stays at 1, not 2.
//
// Run alongside TestService_Start_DrivesUpdateFromBus which proves the
// happy-path single-driver flow works.
func TestService_DualDriver_StarvesCallbacks(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")
	const tfHTF = domain.Timeframe("5m")

	svc := indicator.NewService("dual_driver_test")
	svc.SetSessionOpen(anchor)

	var hits int
	svc.Subscribe(sym, tfHTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		hits++
	})

	bars := indicatortest.MakeBars(sym, 200.0, anchor, 5)

	// First caller drives all 5 bars: HTF aggregator closes once, callback
	// fires once.
	for _, bar := range bars {
		svc.Update(bar)
	}
	firstPassHits := hits
	if firstPassHits != 1 {
		t.Fatalf("first-pass callbacks: got %d want 1 (one 5m close)", firstPassHits)
	}

	// Second caller drives the SAME bars (simulates monitor + runner both
	// calling Update on a shared instance — the PR 6a-2 era bug). The calc
	// state.lastBarTime and BarAggregator.lastBarTime have already advanced
	// to bars[4].Time, so each Push returns ok=false and no callback fires.
	for _, bar := range bars {
		svc.Update(bar)
	}
	if hits != firstPassHits {
		t.Fatalf("second-pass added callback fires: got total %d want %d (dedup contract broken)", hits, firstPassHits)
	}
}

// TestService_BusAndDirect_SingleDriver asserts the production wiring (bus
// subscription via Start) drives Update exactly once even when test code
// calls UpdateWithEnv defensively for the same bar. The dedup short-circuit
// keeps callback count == 1.
func TestService_BusAndDirect_SingleDriver(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")
	const tfHTF = domain.Timeframe("5m")

	bus := memory.NewBus()
	svc := indicator.NewService("bus_and_direct_test")
	svc.SetSessionOpen(anchor)
	if err := svc.Start(context.Background(), bus); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var hits int
	svc.Subscribe(sym, tfHTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		hits++
	})

	bars := indicatortest.MakeBars(sym, 200.0, anchor, 5)
	envMode, _ := domain.NewEnvMode("paper")

	for _, bar := range bars {
		// Drive once via the bus (production path).
		ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "tenant-X", envMode, "idem-"+bar.Time.String(), bar)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if pubErr := bus.Publish(context.Background(), *ev); pubErr != nil {
			t.Fatalf("Publish: %v", pubErr)
		}
		// Defensively call Update again with the SAME bar — should be a no-op.
		svc.UpdateWithEnv(bar, domain.MarketBarEnvelope{TenantID: "tenant-X", EnvMode: envMode})
	}

	if hits != 1 {
		t.Fatalf("expected 1 callback (one 5m close, dedup'd second caller), got %d", hits)
	}
}
