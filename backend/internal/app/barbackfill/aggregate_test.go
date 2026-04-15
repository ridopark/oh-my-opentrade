package barbackfill

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeMinuteBars(t *testing.T, sym domain.Symbol, start time.Time, n int) []domain.MarketBar {
	t.Helper()
	out := make([]domain.MarketBar, 0, n)
	for i := 0; i < n; i++ {
		p := 100.0 + float64(i)
		b, err := domain.NewMarketBar(
			start.Add(time.Duration(i)*time.Minute),
			sym,
			domain.Timeframe("1m"),
			p, p+0.5, p-0.5, p+0.2, 10,
		)
		require.NoError(t, err)
		out = append(out, b)
	}
	return out
}

func TestAggregateHTF(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	nowMid := time.Date(2026, 4, 15, 11, 30, 0, 0, loc) // Wed mid-session
	nowPre := time.Date(2026, 4, 15, 8, 0, 0, 0, loc)   // Wed pre-market
	sessionOpenUTC := time.Date(2026, 4, 15, 9, 30, 0, 0, loc).UTC()
	preOpenUTC := time.Date(2026, 4, 15, 4, 0, 0, 0, loc).UTC()
	cryptoStart := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)

	cases := []struct {
		name           string
		sym            domain.Symbol
		start          time.Time
		n              int
		now            time.Time
		wantEmpty      bool
		wantAtLeast5m  int
		wantAtLeast15m int
	}{
		{
			name:           "equity_today_open_anchor",
			sym:            domain.Symbol("AAPL"),
			start:          sessionOpenUTC,
			n:              60,
			now:            nowMid,
			wantAtLeast5m:  12,
			wantAtLeast15m: 4,
		},
		{
			name:          "equity_pre_open_falls_back_to_prev_rth",
			sym:           domain.Symbol("AAPL"),
			start:         preOpenUTC,
			n:             30,
			now:           nowPre,
			wantAtLeast5m: 1,
		},
		{
			name:           "crypto_clock_aligned_before_nyse_open",
			sym:            domain.Symbol("BTC/USD"),
			start:          cryptoStart,
			n:              60,
			now:            nowMid,
			wantAtLeast5m:  12,
			wantAtLeast15m: 4,
		},
		{
			name:      "empty_input_returns_nil",
			sym:       domain.Symbol("AAPL"),
			start:     sessionOpenUTC,
			n:         0,
			now:       nowMid,
			wantEmpty: true,
		},
		{
			name:      "short_input_no_htf_closes",
			sym:       domain.Symbol("AAPL"),
			start:     sessionOpenUTC,
			n:         3,
			now:       nowMid,
			wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bars []domain.MarketBar
			if tc.n > 0 {
				bars = makeMinuteBars(t, tc.sym, tc.start, tc.n)
			}
			got := AggregateHTF(tc.sym, bars, tc.now)
			if tc.wantEmpty {
				assert.Empty(t, got)
				return
			}
			per := map[domain.Timeframe]int{}
			for _, b := range got {
				per[b.Timeframe]++
				assert.Equal(t, tc.sym, b.Symbol)
				assert.True(t, b.High >= b.Low)
				assert.True(t, b.Volume > 0)
			}
			if tc.wantAtLeast5m > 0 {
				assert.GreaterOrEqual(t, per["5m"], tc.wantAtLeast5m)
			}
			if tc.wantAtLeast15m > 0 {
				assert.GreaterOrEqual(t, per["15m"], tc.wantAtLeast15m)
			}
		})
	}
}
