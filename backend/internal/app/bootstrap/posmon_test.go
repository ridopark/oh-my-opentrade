package bootstrap

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

func TestBuildPosMonitor_Backtest(t *testing.T) {
	t.Parallel()

	clock := func() time.Time { return time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC) }
	gate := execution.NewPositionGate(&stubBroker{}, zerolog.Nop())

	bundle, err := BuildPositionMonitor(PosMonitorDeps{
		EventBus:     &stubEventBus{},
		PositionGate: gate,
		TenantID:     "test",
		EnvMode:      domain.EnvModePaper,
		Clock:        clock,
		IsBacktest:   true,
		Logger:       zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildPositionMonitor returned error: %v", err)
	}
	if bundle.PriceCache == nil {
		t.Fatal("expected non-nil PriceCache")
	}
	if bundle.Service == nil {
		t.Fatal("expected non-nil Service")
	}
	if bundle.Service.PositionCount() != 0 {
		t.Fatalf("expected 0 positions, got %d", bundle.Service.PositionCount())
	}
}

func TestBuildPosMonitor_Live(t *testing.T) {
	t.Parallel()

	clock := func() time.Time { return time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC) }
	broker := &stubBroker{}
	gate := execution.NewPositionGate(broker, zerolog.Nop())

	bundle, err := BuildPositionMonitor(PosMonitorDeps{
		EventBus:     &stubEventBus{},
		PositionGate: gate,
		Broker:       broker,
		Repo:         &stubRepo{},
		TenantID:     "default",
		EnvMode:      domain.EnvModePaper,
		Clock:        clock,
		IsBacktest:   false,
		Logger:       zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildPositionMonitor returned error: %v", err)
	}
	if bundle.PriceCache == nil {
		t.Fatal("expected non-nil PriceCache")
	}
	if bundle.Service == nil {
		t.Fatal("expected non-nil Service")
	}
}
