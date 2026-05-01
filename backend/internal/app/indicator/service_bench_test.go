package indicator_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

const benchPrewarmBars = 250

func newWarmService(tb testing.TB) (*indicator.Service, []domain.MarketBar) {
	tb.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		tb.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2025, 1, 6, 9, 30, 0, 0, loc)
	bars := indicatortest.MakeBars(testSymbol, 200.0, anchor, benchPrewarmBars+1)
	svc := indicator.NewService("bench")
	for _, b := range bars[:benchPrewarmBars] {
		svc.Update(b)
	}
	return svc, bars
}

// BenchmarkUpdate uses microsecond timestamp increments so the calc's
// replay-dedup short-circuit does not fire on the hot path.
func BenchmarkUpdate(b *testing.B) {
	svc, bars := newWarmService(b)
	next := bars[benchPrewarmBars]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next.Time = next.Time.Add(time.Microsecond)
		svc.Update(next)
	}
}

// BenchmarkUpdateWithSubscriber measures the 1m hot path when one subscriber
// is registered for a 5m HTF aggregator. Microsecond-spaced bars never close
// a 5m bucket, so the callback never fires; this isolates the per-bar
// aggregator dispatch cost from the (rare) HTF-close cost.
func BenchmarkUpdateWithSubscriber(b *testing.B) {
	svc, bars := newWarmService(b)
	svc.SetSessionOpen(bars[0].Time)
	svc.Subscribe(testSymbol, domain.Timeframe("5m"), func(_ domain.MarketBar, _ domain.IndicatorSnapshot) {})
	next := bars[benchPrewarmBars]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next.Time = next.Time.Add(time.Microsecond)
		svc.Update(next)
	}
}

func BenchmarkLastSnapshot(b *testing.B) {
	svc, bars := newWarmService(b)
	last := bars[benchPrewarmBars-1]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.LastSnapshot(last.Symbol, last.Timeframe)
	}
}
