package indicator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// TestService_AppendPublish_DrainsAfterUpdate asserts that events queued by
// a Subscribe callback via AppendPublish are published to the bus AFTER the
// indicator's Update completes — the fan-out happens inside the indicator's
// MarketBarSanitized handler, not from inside the callback itself. This is
// the migrated replacement for monitor's pendingHTFEvents queue.
func TestService_AppendPublish_DrainsAfterUpdate(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")
	const tfHTF = domain.Timeframe("5m")

	bus := memory.NewBus()
	svc := indicator.NewService("append_publish_test")
	svc.SetSessionOpen(anchor)
	if err := svc.Start(context.Background(), bus); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// HTF MarketBarSanitized subscriber on the bus — the queue-drain destination.
	var derivedMu sync.Mutex
	var derived []domain.Event
	derivedTypeKey := domain.EventType("derived-from-htf-callback")
	if err := bus.Subscribe(context.Background(), derivedTypeKey, func(_ context.Context, ev domain.Event) error {
		derivedMu.Lock()
		derived = append(derived, ev)
		derivedMu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("subscribe derived: %v", err)
	}

	// Subscribe callback that enqueues a derived event via AppendPublish on every
	// HTF close. The callback itself must NOT see the derived event arrive on the
	// bus while it is still running — the bus-publish is deferred until after
	// the indicator's lock is released.
	var sawSelfDuringCallback bool
	svc.Subscribe(sym, tfHTF, func(closed domain.MarketBar, _ domain.IndicatorSnapshot, env domain.MarketBarEnvelope) {
		derivedMu.Lock()
		if len(derived) > 0 {
			sawSelfDuringCallback = true
		}
		derivedMu.Unlock()
		ev := domain.NewBacktestEvent(derivedTypeKey, env.TenantID, env.EnvMode, env.IdemKey+"-derived", closed, env.OccurredAt)
		svc.AppendPublish(ev)
	})

	bars := indicatortest.MakeBars(sym, 200.0, anchor, 5)
	envMode, _ := domain.NewEnvMode("paper")
	for _, bar := range bars {
		ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "tenant-DRAIN", envMode, "idem-"+bar.Time.String(), bar)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if pubErr := bus.Publish(context.Background(), *ev); pubErr != nil {
			t.Fatalf("Publish: %v", pubErr)
		}
	}

	derivedMu.Lock()
	gotCount := len(derived)
	gotEvent := domain.Event{}
	if gotCount > 0 {
		gotEvent = derived[0]
	}
	derivedMu.Unlock()

	if gotCount != 1 {
		t.Fatalf("expected exactly 1 drained event (one 5m close), got %d", gotCount)
	}
	if sawSelfDuringCallback {
		t.Fatal("derived event was visible on the bus during the callback — drain ran before lock release, contract violated")
	}
	if gotEvent.TenantID != "tenant-DRAIN" {
		t.Errorf("drained event TenantID: got %q want %q", gotEvent.TenantID, "tenant-DRAIN")
	}
	if gotEvent.IdempotencyKey == "" {
		t.Errorf("drained event missing IdempotencyKey")
	}
}
