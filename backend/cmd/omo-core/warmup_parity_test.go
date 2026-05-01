package main

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

type stubBarRepo struct {
	bars map[domain.Symbol][]domain.MarketBar
}

func (s *stubBarRepo) GetMarketBars(_ context.Context, sym domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return s.bars[sym], nil
}

func TestRunEquityWarmup_IndicatorMonitorParity(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}

	type symSpec struct {
		sym       domain.Symbol
		basePrice float64
	}
	symbols := []symSpec{
		{"AAPL", 200.0},
		{"MU", 120.0},
		{"SPY", 540.0},
	}

	todayOpen := time.Date(2026, 4, 23, 9, 30, 0, 0, loc)

	const barsPerSession = 180

	stub := &stubBarRepo{bars: make(map[domain.Symbol][]domain.MarketBar)}
	for _, ss := range symbols {
		stub.bars[ss.sym] = indicatortest.MakeBars(ss.sym, ss.basePrice, todayOpen, barsPerSession)
	}

	idx := indicator.NewService("test_idx")
	monSvc := monitor.NewService(nil, nil, zerolog.Nop(), monitor.WithIndicatorShadow(idx))
	syms := make([]domain.Symbol, 0, len(symbols))
	for _, ss := range symbols {
		syms = append(syms, ss.sym)
	}
	monSvc.InitAggregators(syms, todayOpen)

	deps := EquityWarmupDeps{
		Monitor:   monSvc,
		Fetcher:   stub,
		Symbols:   syms,
		Timeframe: domain.Timeframe("1m"),
		TodayOpen: todayOpen,
		Now:       todayOpen.Add(time.Duration(barsPerSession) * time.Minute),
		Log:       zerolog.Nop(),
	}
	runEquityWarmup(context.Background(), deps)

	for _, ss := range symbols {
		got, ok := idx.LastSnapshot(ss.sym, domain.Timeframe("1m"))
		if !ok {
			t.Fatalf("indicator.LastSnapshot missing for %s/1m after warmup", ss.sym)
		}
		want, ok := monSvc.GetLastSnapshot(string(ss.sym))
		if !ok {
			t.Fatalf("monitor.GetLastSnapshot missing for %s after warmup", ss.sym)
		}
		indicatortest.AssertSnapshotsBitEqual(t, "warmup parity", got, want, string(ss.sym))
	}
}
