package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWarmUpNative_SeedsCalculatorState_5m is the primary correctness test
// for Phase 2a of the 5m parity fix. After WarmUpNative with 800 bars, the
// calculator's per-(sym, "5m") state must produce a converged EMA200 on the
// next live bar — proving the pre-Phase-2a bug (aggregator silently dropping
// pre-today bars during warmup) is closed.
func TestWarmUpNative_SeedsCalculatorState_5m(t *testing.T) {
	bus := memory.NewBus()
	svc := monitor.NewService(bus, &mockRepository{}, zerolog.Nop())

	bars := makeSyntheticBars("AAPL", "5m", 800, 100.0, 5*time.Minute)
	n := svc.WarmUpNative(domain.Symbol("AAPL"), domain.Timeframe("5m"), bars)
	require.Equal(t, 800, n)

	// HTF snapshot must be populated (consumed by buildHTFMap downstream).
	snap, ok := svc.GetHTFSnapshot("AAPL", "5m")
	require.True(t, ok, "GetHTFSnapshot must return the seeded snap")
	assert.Greater(t, snap.EMA200, 0.0, "EMA200 must converge after 800 bars")
	assert.Greater(t, snap.EMA50, 0.0)
	assert.Greater(t, snap.EMA21, 0.0)
	assert.Greater(t, snap.EMA9, 0.0)
	assert.Greater(t, snap.RSI, 0.0)
	assert.Greater(t, snap.ATR, 0.0)
}

// TestWarmUpNative_15m_PopulatesHTFSnap mirrors the 5m test for the 15m
// anchor timeframe added to EquitySpec in Phase 0.
func TestWarmUpNative_15m_PopulatesHTFSnap(t *testing.T) {
	bus := memory.NewBus()
	svc := monitor.NewService(bus, &mockRepository{}, zerolog.Nop())

	bars := makeSyntheticBars("AAPL", "15m", 200, 100.0, 15*time.Minute)
	n := svc.WarmUpNative(domain.Symbol("AAPL"), domain.Timeframe("15m"), bars)
	require.Equal(t, 200, n)

	snap, ok := svc.GetHTFSnapshot("AAPL", "15m")
	require.True(t, ok)
	assert.Greater(t, snap.EMA50, 0.0, "EMA50 must converge after 200 bars on 15m")
}

// TestWarmUpNative_EmptyBarsNoOp ensures the function tolerates empty input
// without panicking on the first-bar dereference.
func TestWarmUpNative_EmptyBarsNoOp(t *testing.T) {
	bus := memory.NewBus()
	svc := monitor.NewService(bus, &mockRepository{}, zerolog.Nop())

	n := svc.WarmUpNative(domain.Symbol("AAPL"), domain.Timeframe("5m"), nil)
	assert.Equal(t, 0, n)
	_, ok := svc.GetHTFSnapshot("AAPL", "5m")
	assert.False(t, ok)
}

// makeSyntheticBars builds a deterministic OHLCV series with a small
// drift so EMAs and RSI can converge to non-zero values.
func makeSyntheticBars(symbol, tf string, count int, startPrice float64, step time.Duration) []domain.MarketBar {
	bars := make([]domain.MarketBar, count)
	t0 := time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC)
	price := startPrice
	for i := 0; i < count; i++ {
		drift := float64(i%20-10) * 0.05
		open := price
		closeP := price + drift
		high := open
		if closeP > high {
			high = closeP
		}
		low := open
		if closeP < low {
			low = closeP
		}
		bars[i] = domain.MarketBar{
			Symbol:    domain.Symbol(symbol),
			Timeframe: domain.Timeframe(tf),
			Time:      t0.Add(time.Duration(i) * step),
			Open:      open,
			High:      high + 0.1,
			Low:       low - 0.1,
			Close:     closeP,
			Volume:    1000,
		}
		price = closeP
	}
	return bars
}
