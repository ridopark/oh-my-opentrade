package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor/monitortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// After WarmUpNative with 800 5m bars, the calculator's per-(sym, "5m")
// state must produce a converged EMA200 on the next snapshot. Pins the
// invariant that monitor's HTF state seeds without going through the
// session-anchored aggregator.
func TestWarmUpNative_SeedsCalculatorState_5m(t *testing.T) {
	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "warmup_native_test")

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

// 15m anchor TF with 200 bars (EquitySpec.Required["15m"]) — EMA50 must
// converge.
func TestWarmUpNative_15m_PopulatesHTFSnap(t *testing.T) {
	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "warmup_native_test")

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
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "warmup_native_test")

	n := svc.WarmUpNative(domain.Symbol("AAPL"), domain.Timeframe("5m"), nil)
	assert.Equal(t, 0, n)
	_, ok := svc.GetHTFSnapshot("AAPL", "5m")
	assert.False(t, ok)
}

// Pins the load-bearing contract that the 1m WarmUp path does NOT seed
// HTF state when warmup bars are pre-sessionOpen — because BarAggregator
// silently drops them. WarmUpNative exists precisely to bypass this gate;
// a future change that "helpfully" makes the aggregator accept pre-anchor
// bars must update this test consciously, not silently re-introduce the
// double-feed it caused before the parity fix.
func TestServiceWarmUp_DoesNotSeedHTFFromPreSessionBars(t *testing.T) {
	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "warmup_native_test")

	sym := domain.Symbol("AAPL")
	sessionOpen := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
	svc.InitAggregators([]domain.Symbol{sym}, sessionOpen)

	bars := makeSyntheticBars("AAPL", "1m", 800, 100.0, time.Minute)
	for i := range bars {
		bars[i].Time = sessionOpen.Add(-time.Duration(800-i) * time.Minute)
	}

	n := svc.WarmUp(bars)
	require.Equal(t, 800, n)

	_, ok := svc.GetHTFSnapshot("AAPL", "5m")
	assert.False(t, ok, "1m WarmUp must not seed 5m HTF state from pre-sessionOpen bars")
	_, ok = svc.GetHTFSnapshot("AAPL", "15m")
	assert.False(t, ok)
	_, ok = svc.GetHTFSnapshot("AAPL", "1h")
	assert.False(t, ok)
}

// Idempotency: a second WarmUpNative call for the same (sym, tf) must not
// double-feed the calculator. Returns 0 (no-op) and leaves snap unchanged.
func TestWarmUpNative_IsIdempotent(t *testing.T) {
	bus := memory.NewBus()
	svc, _ := monitortest.NewSvc(bus, &mockRepository{}, "warmup_native_test")

	bars := makeSyntheticBars("AAPL", "5m", 800, 100.0, 5*time.Minute)
	n1 := svc.WarmUpNative(domain.Symbol("AAPL"), domain.Timeframe("5m"), bars)
	require.Equal(t, 800, n1)
	first, ok := svc.GetHTFSnapshot("AAPL", "5m")
	require.True(t, ok)

	n2 := svc.WarmUpNative(domain.Symbol("AAPL"), domain.Timeframe("5m"), bars)
	assert.Equal(t, 0, n2, "second call must return 0 (no-op)")

	second, ok := svc.GetHTFSnapshot("AAPL", "5m")
	require.True(t, ok)
	assert.Equal(t, first.EMA200, second.EMA200, "EMA200 must not ratchet on second call")
	assert.Equal(t, first.RSI, second.RSI)
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
