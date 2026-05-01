package backtest

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestMakeSnapshotFn_DrivesIndicatorService(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 23, 9, 30, 0, 0, loc)

	type symSpec struct {
		sym       domain.Symbol
		basePrice float64
	}
	symbols := []symSpec{
		{"AAPL", 200.0},
		{"MU", 120.0},
		{"SPY", 540.0},
	}

	idx := indicator.NewService("backtest_test_idx")
	parallel := indicator.NewService("backtest_parallel")
	snapshotFn := indicator.SnapshotFn(idx)

	const barsPerSym = 200
	for _, ss := range symbols {
		bars := indicatortest.MakeBars(ss.sym, ss.basePrice, anchor, barsPerSym)
		for _, b := range bars {
			snapshotFn(b)
			parallel.Update(b)
		}
	}

	for _, ss := range symbols {
		got, ok := idx.LastSnapshot(ss.sym, domain.Timeframe("1m"))
		if !ok {
			t.Fatalf("idx.LastSnapshot missing for %s/1m after warmup", ss.sym)
		}
		want, ok := parallel.LastSnapshot(ss.sym, domain.Timeframe("1m"))
		if !ok {
			t.Fatalf("parallel.LastSnapshot missing for %s/1m after warmup", ss.sym)
		}
		indicatortest.AssertSnapshotsBitEqual(t, "snapshotFn parity", got, want, string(ss.sym))
	}
}
