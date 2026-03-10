package bootstrap

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

type stubBarSaver struct{}

func (s *stubBarSaver) SaveMarketBars(_ context.Context, bars []domain.MarketBar) (int, error) {
	return len(bars), nil
}

func TestBuildIngestion(t *testing.T) {
	bundle, err := BuildIngestion(IngestionDeps{
		EventBus:   &stubEventBus{},
		Repo:       &stubRepo{},
		BarSaver:   &stubBarSaver{},
		IsBacktest: false,
		Logger:     zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildIngestion returned error: %v", err)
	}
	if bundle.Service == nil {
		t.Fatal("Service is nil")
	}
	if bundle.Filter == nil {
		t.Fatal("Filter is nil")
	}
	if bundle.BarWriter == nil {
		t.Fatal("BarWriter should be non-nil in live mode")
	}
}

func TestBuildIngestion_Backtest(t *testing.T) {
	bundle, err := BuildIngestion(IngestionDeps{
		EventBus:   &stubEventBus{},
		Repo:       &stubRepo{},
		IsBacktest: true,
		Logger:     zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildIngestion returned error: %v", err)
	}
	if bundle.Service == nil {
		t.Fatal("Service is nil")
	}
	if bundle.BarWriter != nil {
		t.Fatal("BarWriter should be nil in backtest mode")
	}
}

func TestBuildMonitor(t *testing.T) {
	svc, err := BuildMonitor(MonitorDeps{
		EventBus: &stubEventBus{},
		Repo:     &stubRepo{},
		Logger:   zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildMonitor returned error: %v", err)
	}
	if svc == nil {
		t.Fatal("Service is nil")
	}
}

func TestBuildPerfServices(t *testing.T) {
	bundle, err := BuildPerfServices(PerfDeps{
		EventBus:      &stubEventBus{},
		PnLRepo:       &stubPnLRepo{},
		Broker:        &stubBroker{},
		TradeReader:   nil,
		InitialEquity: 100000.0,
		Logger:        zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildPerfServices returned error: %v", err)
	}
	if bundle.LedgerWriter == nil {
		t.Fatal("LedgerWriter is nil")
	}
	if bundle.SignalTracker == nil {
		t.Fatal("SignalTracker is nil")
	}
}
