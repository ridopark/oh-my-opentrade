package domain_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNYLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	return loc
}

func etTime(t *testing.T, hh, mm int) time.Time {
	t.Helper()
	loc := mustNYLocation(t)
	return time.Date(2026, time.March, 4, hh, mm, 0, 0, loc)
}

func mustSymbol(t *testing.T, s string) domain.Symbol {
	t.Helper()
	sym, err := domain.NewSymbol(s)
	require.NoError(t, err)
	return sym
}

func mustTF(t *testing.T, tf string) domain.Timeframe {
	t.Helper()
	tfV, err := domain.NewTimeframe(tf)
	require.NoError(t, err)
	return tfV
}

func must1mBar(
	t *testing.T,
	tm time.Time,
	sym domain.Symbol,
	open, high, low, close, volume float64,
) domain.MarketBar {
	t.Helper()
	tf1m := mustTF(t, "1m")
	bar, err := domain.NewMarketBar(tm, sym, tf1m, open, high, low, close, volume)
	require.NoError(t, err)
	return bar
}

func TestAggregator_5mBasic(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	start := sessionOpen
	var closed []domain.MarketBar
	for i := 0; i < 5; i++ {
		bar := must1mBar(
			t,
			start.Add(time.Duration(i)*time.Minute),
			sym,
			100+float64(i),
			101+float64(i),
			99+float64(i),
			100.5+float64(i),
			10,
		)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
		}
	}

	require.Len(t, closed, 1)
	out := closed[0]

	assert.Equal(t, sym, out.Symbol)
	assert.Equal(t, tf5m, out.Timeframe)
	assert.Equal(t, sessionOpen, out.Time)
	assert.Equal(t, 100.0, out.Open)
	assert.Equal(t, 105.0, out.High)
	assert.Equal(t, 99.0, out.Low)
	assert.Equal(t, 104.5, out.Close)
	assert.Equal(t, 50.0, out.Volume)
}

func TestAggregator_15mBasic(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf15m := mustTF(t, "15m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf15m, sessionOpen)
	require.NoError(t, err)

	start := sessionOpen
	var closed []domain.MarketBar
	for i := 0; i < 15; i++ {
		bar := must1mBar(
			t,
			start.Add(time.Duration(i)*time.Minute),
			sym,
			200+float64(i),
			201+float64(i),
			199+float64(i),
			200.5+float64(i),
			1,
		)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
		}
	}

	require.Len(t, closed, 1)
	out := closed[0]

	assert.Equal(t, sym, out.Symbol)
	assert.Equal(t, tf15m, out.Timeframe)
	assert.Equal(t, sessionOpen, out.Time)
	assert.Equal(t, 200.0, out.Open)
	assert.Equal(t, 215.0, out.High)
	assert.Equal(t, 199.0, out.Low)
	assert.Equal(t, 214.5, out.Close)
	assert.Equal(t, 15.0, out.Volume)
}

func TestAggregator_OHLCVCorrectness(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	bars := []domain.MarketBar{
		must1mBar(t, etTime(t, 9, 30), sym, 10, 12, 9, 11, 100),
		must1mBar(t, etTime(t, 9, 31), sym, 11, 13, 10, 12, 200),
		must1mBar(t, etTime(t, 9, 32), sym, 12, 14, 8, 9, 300),
		must1mBar(t, etTime(t, 9, 33), sym, 9, 10, 9, 10, 400),
		must1mBar(t, etTime(t, 9, 34), sym, 10, 11, 10, 10.5, 500),
	}

	var out domain.MarketBar
	var ok bool
	for _, b := range bars {
		out, ok = agg.Push(b)
	}
	require.True(t, ok)

	assert.Equal(t, 10.0, out.Open)
	assert.Equal(t, 14.0, out.High)
	assert.Equal(t, 8.0, out.Low)
	assert.Equal(t, 10.5, out.Close)
	assert.Equal(t, 1500.0, out.Volume)
}

func TestAggregator_MultipleBuckets(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	var closed []domain.MarketBar
	for i := 0; i < 10; i++ {
		bar := must1mBar(
			t,
			sessionOpen.Add(time.Duration(i)*time.Minute),
			sym,
			100,
			101,
			99,
			100,
			1,
		)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
		}
	}

	require.Len(t, closed, 2)
	assert.Equal(t, sessionOpen, closed[0].Time)
	assert.Equal(t, sessionOpen.Add(5*time.Minute), closed[1].Time)
}

func TestAggregator_PartialBucket(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		bar := must1mBar(
			t,
			sessionOpen.Add(time.Duration(i)*time.Minute),
			sym,
			100,
			101,
			99,
			100,
			1,
		)
		_, ok := agg.Push(bar)
		assert.False(t, ok)
	}
}

func TestAggregator_SessionAlignment(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	start := etTime(t, 9, 33)
	var closed []domain.MarketBar
	var closedOn []time.Time
	for i := 0; i < 7; i++ {
		tm := start.Add(time.Duration(i) * time.Minute)
		bar := must1mBar(t, tm, sym, 100, 101, 99, 100, 1)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
			closedOn = append(closedOn, tm)
		}
	}

	require.Len(t, closed, 2)
	assert.Equal(t, etTime(t, 9, 30), closed[0].Time)
	assert.Equal(t, etTime(t, 9, 35), closed[1].Time)
	assert.Equal(t, etTime(t, 9, 34), closedOn[0])
	assert.Equal(t, etTime(t, 9, 39), closedOn[1])
}

func TestAggregator_Reset(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		bar := must1mBar(
			t,
			sessionOpen.Add(time.Duration(i)*time.Minute),
			sym,
			10,
			11,
			9,
			10,
			1,
		)
		_, ok := agg.Push(bar)
		require.False(t, ok)
	}

	newOpen := time.Date(2026, time.March, 5, 9, 30, 0, 0, mustNYLocation(t))
	agg.Reset(newOpen)

	var out domain.MarketBar
	var ok bool
	for i := 0; i < 5; i++ {
		bar := must1mBar(
			t,
			newOpen.Add(time.Duration(i)*time.Minute),
			sym,
			100,
			101,
			99,
			100,
			2,
		)
		out, ok = agg.Push(bar)
	}
	require.True(t, ok)
	assert.Equal(t, newOpen, out.Time)
	assert.Equal(t, 10.0, out.Volume)
}

func TestAggregator_SymbolMismatch(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	other := mustSymbol(t, "MSFT")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	wrong := must1mBar(t, sessionOpen, other, 999, 1000, 998, 999, 1)
	_, ok := agg.Push(wrong)
	require.False(t, ok)

	var closed []domain.MarketBar
	for i := 0; i < 5; i++ {
		bar := must1mBar(
			t,
			sessionOpen.Add(time.Duration(i)*time.Minute),
			sym,
			10+float64(i),
			11+float64(i),
			9+float64(i),
			10.5+float64(i),
			1,
		)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
		}
	}

	require.Len(t, closed, 1)
	assert.Equal(t, 10.0, closed[0].Open)
}

func TestAggregator_1hBasic(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf1h := mustTF(t, "1h")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf1h, sessionOpen)
	require.NoError(t, err)

	var closed []domain.MarketBar
	for i := 0; i < 60; i++ {
		bar := must1mBar(
			t,
			sessionOpen.Add(time.Duration(i)*time.Minute),
			sym,
			100+float64(i),
			101+float64(i),
			99+float64(i),
			100.5+float64(i),
			10,
		)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
		}
	}

	require.Len(t, closed, 1)
	out := closed[0]

	assert.Equal(t, sym, out.Symbol)
	assert.Equal(t, tf1h, out.Timeframe)
	assert.Equal(t, sessionOpen, out.Time)
	assert.Equal(t, 100.0, out.Open)
	assert.Equal(t, 160.0, out.High)
	assert.Equal(t, 99.0, out.Low)
	assert.Equal(t, 159.5, out.Close)
	assert.Equal(t, 600.0, out.Volume)
}

func TestAggregator_1hOHLCVCorrectness(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf1h := mustTF(t, "1h")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf1h, sessionOpen)
	require.NoError(t, err)

	var closed domain.MarketBar
	var ok bool
	for i := 0; i < 60; i++ {
		o := 150.0
		h := 155.0
		l := 145.0
		c := 152.0
		if i == 10 {
			h = 170.0
		}
		if i == 30 {
			l = 130.0
		}
		if i == 59 {
			c = 160.0
		}
		bar := must1mBar(t, sessionOpen.Add(time.Duration(i)*time.Minute), sym, o, h, l, c, 5)
		closed, ok = agg.Push(bar)
	}
	require.True(t, ok)

	assert.Equal(t, 150.0, closed.Open)
	assert.Equal(t, 170.0, closed.High)
	assert.Equal(t, 130.0, closed.Low)
	assert.Equal(t, 160.0, closed.Close)
	assert.Equal(t, 300.0, closed.Volume)
}

func TestAggregator_1dValidationAccepted(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf1d := mustTF(t, "1d")
	sessionOpen := etTime(t, 9, 30)

	_, err := domain.NewBarAggregator(sym, tf1d, sessionOpen)
	require.NoError(t, err)
}

func TestAggregator_1hClockAligned(t *testing.T) {
	sym := mustSymbol(t, "BTC/USD")

	agg, err := domain.NewClockAlignedAggregator(sym, mustTF(t, "1h"))
	require.NoError(t, err)

	start := time.Date(2026, time.March, 4, 14, 0, 0, 0, time.UTC)
	var closed []domain.MarketBar
	for i := 0; i < 60; i++ {
		bar := must1mBar(t, start.Add(time.Duration(i)*time.Minute), sym, 50000, 50100, 49900, 50050, 1)
		c, ok := agg.Push(bar)
		if ok {
			closed = append(closed, c)
		}
	}
	require.Len(t, closed, 1)
	assert.Equal(t, mustTF(t, "1h"), closed[0].Timeframe)
}

func TestAggregator_InvalidTargetTF(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf1m := mustTF(t, "1m")

	_, err := domain.NewBarAggregator(sym, tf1m, etTime(t, 9, 30))
	require.Error(t, err)
}

func TestAggregator_TimeframeField(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	var out domain.MarketBar
	var ok bool
	for i := 0; i < 5; i++ {
		bar := must1mBar(t, sessionOpen.Add(time.Duration(i)*time.Minute), sym, 1, 2, 1, 1.5, 1)
		out, ok = agg.Push(bar)
	}
	require.True(t, ok)
	assert.Equal(t, tf5m, out.Timeframe)
}

func TestAggregator_BarTime(t *testing.T) {
	sym := mustSymbol(t, "AAPL")
	tf5m := mustTF(t, "5m")
	sessionOpen := etTime(t, 9, 30)

	agg, err := domain.NewBarAggregator(sym, tf5m, sessionOpen)
	require.NoError(t, err)

	var out domain.MarketBar
	var ok bool
	for i := 0; i < 5; i++ {
		bar := must1mBar(t, sessionOpen.Add(time.Duration(i)*time.Minute), sym, 1, 2, 1, 1.5, 1)
		out, ok = agg.Push(bar)
	}
	require.True(t, ok)
	assert.Equal(t, sessionOpen, out.Time)
	assert.NotEqual(t, etTime(t, 9, 34), out.Time)
}
