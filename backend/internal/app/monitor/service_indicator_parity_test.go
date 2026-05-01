package monitor_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestService_IndicatorShadowParity_BitEqual(t *testing.T) {
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
	dates := []time.Time{
		time.Date(2026, 4, 15, 9, 30, 0, 0, loc),
		time.Date(2026, 4, 20, 9, 30, 0, 0, loc),
		time.Date(2026, 4, 25, 9, 30, 0, 0, loc),
	}
	const barsPerDate = 200

	idx := indicator.NewService("shadow_parity")

	calc := monitor.NewIndicatorCalculator()
	calc.Label = "monitor_parity_under_test"

	for d, anchor := range dates {
		for _, ss := range symbols {
			bars := indicatortest.MakeBars(ss.sym, ss.basePrice, anchor, barsPerDate)
			for i, b := range bars {
				monSnap := calc.Update(b)
				idxSnap := idx.Update(b)
				ctx := fmt.Sprintf("sym=%s date=%d bar=%d", b.Symbol, d, i)

				indicatortest.AssertSnapshotsBitEqual(t, "Update parity", idxSnap, monSnap, ctx)

				lastIdx, ok := idx.LastSnapshot(b.Symbol, b.Timeframe)
				if !ok {
					t.Fatalf("indicator.LastSnapshot missing for %s/%s after bar %d", b.Symbol, b.Timeframe, i)
				}
				indicatortest.AssertSnapshotsBitEqual(t, "LastSnapshot parity (indicator)", lastIdx, idxSnap, ctx)

				calcSnap, ok := calc.SnapshotForTest(b.Symbol, b.Timeframe)
				if !ok {
					t.Fatalf("monitor.SnapshotForTest missing for %s/%s after bar %d", b.Symbol, b.Timeframe, i)
				}
				indicatortest.AssertSnapshotsBitEqual(t, "SnapshotForTest parity (monitor)", calcSnap, monSnap, ctx)
			}
		}
	}
}
