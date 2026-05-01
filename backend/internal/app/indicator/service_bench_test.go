package indicator_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

const benchPrewarmBars = 250

func newWarmService(tb testing.TB) (*indicator.Service, []domain.MarketBar) {
	tb.Helper()
	t := &testing.T{}
	bars := makeBars(t, benchPrewarmBars+1)
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

func BenchmarkLastSnapshot(b *testing.B) {
	svc, bars := newWarmService(b)
	last := bars[benchPrewarmBars-1]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = svc.LastSnapshot(last.Symbol, last.Timeframe)
	}
}
