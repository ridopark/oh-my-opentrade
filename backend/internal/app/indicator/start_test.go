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

// TestService_Start_DrivesUpdateFromBus asserts that after Start, publishing
// EventMarketBarSanitized causes the indicator to Update internal state and
// fire HTF Subscribe callbacks. This locks the single-driver contract: the
// indicator service is the sole subscriber that drives Update; monitor and
// runner read state via LastSnapshot / Subscribe callbacks.
func TestService_Start_DrivesUpdateFromBus(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")
	const tfHTF = domain.Timeframe("5m")

	bus := memory.NewBus()
	svc := indicator.NewService("start_test")
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
		ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "tenant-x", envMode, "idem-"+bar.Time.String(), bar)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if pubErr := bus.Publish(context.Background(), *ev); pubErr != nil {
			t.Fatalf("Publish: %v", pubErr)
		}
	}

	if hits != 1 {
		t.Fatalf("expected 1 HTF callback (one 5m close from 5 1m bars), got %d", hits)
	}
	got, ok := svc.LastSnapshot(sym, tfHTF)
	if !ok {
		t.Fatalf("LastSnapshot missing for (%s, %s) after publish", sym, tfHTF)
	}
	if got.RSI == 0 && got.EMA9 == 0 && got.VWAP == 0 {
		t.Fatalf("LastSnapshot looks zero — Update did not propagate state: %+v", got)
	}
}

// TestService_Start_PropagatesEnvelopeToCallbacks asserts the event envelope
// (TenantID, EnvMode, IdemKey, OccurredAt) reaches Subscribe callbacks
// unchanged. Monitor's HTF callback uses this to derive HTF events without
// re-parsing the parent event.
func TestService_Start_PropagatesEnvelopeToCallbacks(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("MSFT")
	const tfHTF = domain.Timeframe("5m")

	bus := memory.NewBus()
	svc := indicator.NewService("envelope_test")
	svc.SetSessionOpen(anchor)
	if err := svc.Start(context.Background(), bus); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var got domain.MarketBarEnvelope
	svc.Subscribe(sym, tfHTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, env domain.MarketBarEnvelope) {
		got = env
	})

	envMode, _ := domain.NewEnvMode("paper")
	bars := indicatortest.MakeBars(sym, 100.0, anchor, 5)
	// Publish only the first 4 with anonymous idem keys; on the 5th publish a
	// uniquely-stamped envelope and assert it reaches the callback that fires
	// for THAT closure.
	for i, bar := range bars[:4] {
		ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "tenant-A", envMode, "early-"+string(rune('a'+i)), bar)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		_ = bus.Publish(context.Background(), *ev)
	}
	wantIdem := "stamped-htf-key"
	wantTime := bars[4].Time.Add(2 * time.Second) // arbitrary distinct OccurredAt
	stamped := domain.NewBacktestEvent(domain.EventMarketBarSanitized, "tenant-A", envMode, wantIdem, bars[4], wantTime)
	if err := bus.Publish(context.Background(), stamped); err != nil {
		t.Fatalf("Publish stamped: %v", err)
	}

	if got.TenantID != "tenant-A" {
		t.Errorf("envelope TenantID: got %q want %q", got.TenantID, "tenant-A")
	}
	if got.EnvMode != envMode {
		t.Errorf("envelope EnvMode: got %v want %v", got.EnvMode, envMode)
	}
	if got.IdemKey != wantIdem {
		t.Errorf("envelope IdemKey: got %q want %q", got.IdemKey, wantIdem)
	}
	if !got.OccurredAt.Equal(wantTime) {
		t.Errorf("envelope OccurredAt: got %v want %v", got.OccurredAt, wantTime)
	}
}

// TestService_Start_RejectsNilBus locks the precondition.
func TestService_Start_RejectsNilBus(t *testing.T) {
	svc := indicator.NewService("nil_bus_test")
	if err := svc.Start(context.Background(), nil); err == nil {
		t.Fatal("Start(ctx, nil) should error")
	}
}
