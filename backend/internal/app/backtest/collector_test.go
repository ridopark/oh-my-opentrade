package backtest_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	backtest "github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustEvent(t *testing.T, eventType domain.EventType, payload any, idemKey string) domain.Event {
	t.Helper()

	evt, err := domain.NewEvent(eventType, "tenant-1", domain.EnvModePaper, idemKey, payload)
	require.NoError(t, err)
	return *evt
}

func newCollector(t *testing.T, cfg backtest.Config) (*memory.Bus, *backtest.Collector) {
	t.Helper()

	bus := memory.NewBus()
	c, err := backtest.NewCollector(bus, cfg, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, c)
	return bus, c
}

func publishFill(t *testing.T, bus *memory.Bus, idemKey string, payload map[string]any) {
	t.Helper()
	err := bus.Publish(context.Background(), mustEvent(t, domain.EventFillReceived, payload, idemKey))
	require.NoError(t, err)
}

func publishMarketBar(t *testing.T, bus *memory.Bus, idemKey string, bar domain.MarketBar) {
	t.Helper()
	err := bus.Publish(context.Background(), mustEvent(t, domain.EventMarketBarReceived, bar, idemKey))
	require.NoError(t, err)
}

func fillPayload(symbol, side string, qty, price float64, filledAt time.Time) map[string]any {
	return map[string]any{
		"broker_order_id": "sim-1",
		"intent_id":       "some-uuid",
		"symbol":          symbol,
		"side":            side,
		"quantity":        qty,
		"price":           price,
		"filled_at":       filledAt,
	}
}

func sells(trades []backtest.TradeRecord) []backtest.TradeRecord {
	out := make([]backtest.TradeRecord, 0, len(trades))
	for _, tr := range trades {
		if tr.Side == "sell" {
			out = append(out, tr)
		}
	}
	return out
}

func TestNewCollector_DefaultPeriodsPerYearAndSubscribes(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 100, PeriodsPerYear: 0})

	filledAt := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	publishFill(t, bus, "fill-buy-1", fillPayload("AAPL", "buy", 1.0, 100.0, filledAt))

	base := time.Date(2026, 1, 1, 9, 31, 0, 0, time.UTC)
	symbol := domain.Symbol("AAPL")
	e0 := 100.0
	e1 := 110.0
	e2 := 99.0
	e3 := 108.9
	publishMarketBar(t, bus, "bar-1", domain.MarketBar{Time: base, Symbol: symbol, Close: e0})
	publishMarketBar(t, bus, "bar-2", domain.MarketBar{Time: base.Add(time.Minute), Symbol: symbol, Close: e1})
	publishMarketBar(t, bus, "bar-3", domain.MarketBar{Time: base.Add(2 * time.Minute), Symbol: symbol, Close: e2})
	publishMarketBar(t, bus, "bar-4", domain.MarketBar{Time: base.Add(3 * time.Minute), Symbol: symbol, Close: e3})

	r := c.Result()
	require.NotEmpty(t, r.Trades)

	equity := []float64{e0, e1, e2, e3}
	returns := make([]float64, 0, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		returns = append(returns, (equity[i]-equity[i-1])/equity[i-1])
	}
	mean := (returns[0] + returns[1] + returns[2]) / float64(len(returns))
	var sumSq float64
	for _, rr := range returns {
		d := rr - mean
		sumSq += d * d
	}
	std := math.Sqrt(sumSq / float64(len(returns)-1))
	expected := (mean / std) * math.Sqrt(252)

	assert.InDelta(t, expected, r.SharpeRatio, 1e-9)
}

func TestOnFill_FIFOClosesPosition(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 100_000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 100.0, 10.0, t0))
	publishFill(t, bus, "b2", fillPayload("AAPL", "buy", 100.0, 12.0, t0.Add(time.Second)))
	publishFill(t, bus, "s1", fillPayload("AAPL", "sell", 150.0, 11.0, t0.Add(2*time.Second)))
	publishFill(t, bus, "s2", fillPayload("AAPL", "sell", 50.0, 13.0, t0.Add(3*time.Second)))

	r := c.Result()

	ss := sells(r.Trades)
	require.Len(t, ss, 2)
	assert.InDelta(t, 50.0, ss[0].PnL, 1e-9)
	assert.InDelta(t, 50.0, ss[1].PnL, 1e-9)

	assert.Equal(t, 2, r.TradeCount)
	assert.Equal(t, 2, r.WinCount)
	assert.Equal(t, 0, r.LossCount)
	assert.InDelta(t, 100.0, r.WinRate, 1e-9)

	assert.InDelta(t, 100.0, r.TotalPnL, 1e-9)
	assert.InDelta(t, 0.1, r.TotalReturn, 1e-12)

	assert.InDelta(t, 50.0, r.AvgWin, 1e-9)
	assert.InDelta(t, 0.0, r.AvgLoss, 1e-9)
	assert.InDelta(t, 50.0, r.LargestWin, 1e-9)
	assert.InDelta(t, 0.0, r.LargestLoss, 1e-9)
	assert.InDelta(t, 0.0, r.ProfitFactor, 1e-9)
}

func TestOnFill_PartialSellMatchingAndMarkToMarketOpenPositions(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 10_000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 100.0, 10.0, t0))
	publishFill(t, bus, "b2", fillPayload("AAPL", "buy", 100.0, 11.0, t0.Add(time.Second)))
	publishFill(t, bus, "s1", fillPayload("AAPL", "sell", 50.0, 12.0, t0.Add(2*time.Second)))

	publishMarketBar(t, bus, "bar-1", domain.MarketBar{Time: t0.Add(3 * time.Second), Symbol: domain.Symbol("AAPL"), Close: 11.0})

	r := c.Result()

	ss := sells(r.Trades)
	require.Len(t, ss, 1)
	assert.InDelta(t, 100.0, ss[0].PnL, 1e-9)

	assert.Equal(t, 1, r.TradeCount)
	assert.Equal(t, 1, r.WinCount)
	assert.Equal(t, 0, r.LossCount)

	assert.InDelta(t, 100.0, r.TotalPnL, 1e-9)  // realized P&L only (not mark-to-market)
	assert.InDelta(t, 1.0, r.TotalReturn, 1e-12)
}

func TestOnFill_IgnoresInvalidPayloads(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 1000, PeriodsPerYear: 252})

	err := bus.Publish(context.Background(), mustEvent(t, domain.EventFillReceived, "not-a-map", "bad-1"))
	require.NoError(t, err)

	publishFill(t, bus, "bad-2", fillPayload("", "buy", 10.0, 10.0, time.Now().UTC()))
	publishFill(t, bus, "bad-3", fillPayload("AAPL", "buy", 0.0, 10.0, time.Now().UTC()))

	r := c.Result()
	assert.Empty(t, r.Trades)
	assert.Equal(t, 0, r.TradeCount)
	assert.InDelta(t, 1000.0, r.FinalEquity, 1e-9)
	assert.InDelta(t, 0.0, r.TotalPnL, 1e-9)
}

func TestOnBar_UpdatesMaxDrawdownWhenEquityDropsBelowPeak(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 1000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 10.0, 50.0, t0))
	publishMarketBar(t, bus, "bar-1", domain.MarketBar{Time: t0.Add(time.Minute), Symbol: domain.Symbol("AAPL"), Close: 50})
	publishMarketBar(t, bus, "bar-2", domain.MarketBar{Time: t0.Add(2 * time.Minute), Symbol: domain.Symbol("AAPL"), Close: 40})
	publishMarketBar(t, bus, "bar-3", domain.MarketBar{Time: t0.Add(3 * time.Minute), Symbol: domain.Symbol("AAPL"), Close: 55})

	r := c.Result()
	assert.InDelta(t, 10.0, r.MaxDrawdown, 1e-9)
	assert.InDelta(t, 0.0, r.TotalPnL, 1e-9) // no closed trades = no realized P&L
	assert.Equal(t, 0, r.TradeCount)
}

func TestResult_NoTrades(t *testing.T) {
	_, c := newCollector(t, backtest.Config{InitialEquity: 1234, PeriodsPerYear: 252})
	r := c.Result()

	assert.InDelta(t, 1234.0, r.InitialEquity, 1e-9)
	assert.InDelta(t, 1234.0, r.FinalEquity, 1e-9)
	assert.InDelta(t, 0.0, r.TotalPnL, 1e-9)
	assert.InDelta(t, 0.0, r.TotalReturn, 1e-9)
	assert.Equal(t, 0, r.TradeCount)
	assert.Equal(t, 0, r.WinCount)
	assert.Equal(t, 0, r.LossCount)
	assert.InDelta(t, 0.0, r.WinRate, 1e-9)
	assert.InDelta(t, 0.0, r.MaxDrawdown, 1e-9)
	assert.InDelta(t, 0.0, r.SharpeRatio, 1e-9)
	assert.InDelta(t, 0.0, r.ProfitFactor, 1e-9)
}

func TestResult_SingleTradeWin(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 1000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 10.0, 10.0, t0))
	publishFill(t, bus, "s1", fillPayload("AAPL", "sell", 10.0, 12.0, t0.Add(time.Second)))

	r := c.Result()
	assert.InDelta(t, 20.0, r.TotalPnL, 1e-9)
	assert.InDelta(t, 2.0, r.TotalReturn, 1e-9)
	assert.Equal(t, 1, r.TradeCount)
	assert.Equal(t, 1, r.WinCount)
	assert.Equal(t, 0, r.LossCount)
	assert.InDelta(t, 100.0, r.WinRate, 1e-9)
	assert.InDelta(t, 20.0, r.AvgWin, 1e-9)
	assert.InDelta(t, 20.0, r.LargestWin, 1e-9)
}

func TestResult_ProfitFactorAvgWinAvgLossLargestWinLargestLoss(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 10_000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 100.0, 10.0, t0))
	publishFill(t, bus, "s1", fillPayload("AAPL", "sell", 100.0, 12.0, t0.Add(time.Second)))

	publishFill(t, bus, "b2", fillPayload("AAPL", "buy", 50.0, 20.0, t0.Add(2*time.Second)))
	publishFill(t, bus, "s2", fillPayload("AAPL", "sell", 50.0, 18.0, t0.Add(3*time.Second)))

	r := c.Result()
	assert.Equal(t, 2, r.TradeCount)
	assert.Equal(t, 1, r.WinCount)
	assert.Equal(t, 1, r.LossCount)
	assert.InDelta(t, 50.0, r.WinRate, 1e-9)

	assert.InDelta(t, 100.0, r.TotalPnL, 1e-9)
	assert.InDelta(t, 1.0, r.TotalReturn, 1e-9)

	assert.InDelta(t, 2.0, r.ProfitFactor, 1e-9)
	assert.InDelta(t, 200.0, r.AvgWin, 1e-9)
	assert.InDelta(t, 100.0, r.AvgLoss, 1e-9)
	assert.InDelta(t, 200.0, r.LargestWin, 1e-9)
	assert.InDelta(t, -100.0, r.LargestLoss, 1e-9)
}

func TestResult_AllLosses(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 1000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)

	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 10.0, 10.0, t0))
	publishFill(t, bus, "s1", fillPayload("AAPL", "sell", 10.0, 8.0, t0.Add(time.Second)))

	publishFill(t, bus, "b2", fillPayload("AAPL", "buy", 5.0, 20.0, t0.Add(2*time.Second)))
	publishFill(t, bus, "s2", fillPayload("AAPL", "sell", 5.0, 18.0, t0.Add(3*time.Second)))

	r := c.Result()
	assert.Equal(t, 2, r.TradeCount)
	assert.Equal(t, 0, r.WinCount)
	assert.Equal(t, 2, r.LossCount)
	assert.InDelta(t, 0.0, r.WinRate, 1e-9)

	assert.InDelta(t, -30.0, r.TotalPnL, 1e-9)
	assert.InDelta(t, -3.0, r.TotalReturn, 1e-9)

	assert.InDelta(t, 0.0, r.AvgWin, 1e-9)
	assert.InDelta(t, 15.0, r.AvgLoss, 1e-9)
	assert.InDelta(t, 0.0, r.LargestWin, 1e-9)
	assert.InDelta(t, -20.0, r.LargestLoss, 1e-9)
	assert.InDelta(t, 0.0, r.ProfitFactor, 1e-9)
}

func TestPrintReport_DoesNotPanic(t *testing.T) {
	r := &backtest.Result{InitialEquity: 100, FinalEquity: 110, TotalPnL: 10, TotalReturn: 10}
	require.NotPanics(t, func() {
		r.PrintReport()
	})
}

func TestWriteJSON_WritesValidJSON(t *testing.T) {
	bus, c := newCollector(t, backtest.Config{InitialEquity: 1000, PeriodsPerYear: 252})
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	publishFill(t, bus, "b1", fillPayload("AAPL", "buy", 10.0, 10.0, t0))
	publishFill(t, bus, "s1", fillPayload("AAPL", "sell", 10.0, 12.0, t0.Add(time.Second)))

	r := c.Result()
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")

	require.NoError(t, r.WriteJSON(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded backtest.Result
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.InDelta(t, r.InitialEquity, decoded.InitialEquity, 1e-9)
	assert.InDelta(t, r.FinalEquity, decoded.FinalEquity, 1e-9)
}
