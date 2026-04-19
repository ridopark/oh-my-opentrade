package simbroker

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHistoricalOptions implements ports.HistoricalOptionsPort for testing.
type mockHistoricalOptions struct {
	row *domain.HistoricalOptionChainRow
	err error
}

func (m *mockHistoricalOptions) GetHistoricalChain(_ context.Context, _ domain.Symbol, _ time.Time,
	_ domain.OptionRight, _, _ int) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}

func (m *mockHistoricalOptions) GetHistoricalChainBulk(_ context.Context, _ []domain.Symbol,
	_, _ time.Time) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}

func (m *mockHistoricalOptions) GetHistoricalContract(_ context.Context, _ domain.Symbol, _ time.Time,
	_ float64, _ time.Time, _ domain.OptionRight) (*domain.HistoricalOptionChainRow, error) {
	return m.row, m.err
}

func (m *mockHistoricalOptions) HasData(_ context.Context, _ domain.Symbol, _ time.Time) (bool, error) {
	return m.row != nil, nil
}

func (m *mockHistoricalOptions) SaveBatch(_ context.Context, _ []domain.HistoricalOptionChainRow) error {
	return nil
}

func newTestBroker() *Broker {
	return New(Config{SlippageBPS: 5, DisableFillChan: true}, zerolog.Nop())
}


func makeOptionExitIntent(sym string, meta map[string]string) domain.OrderIntent {
	inst := &domain.Instrument{
		Type:             domain.InstrumentTypeOption,
		Symbol:           domain.Symbol(sym),
		UnderlyingSymbol: domain.Symbol("AAPL"),
	}
	return domain.OrderIntent{
		Symbol:     domain.Symbol(sym),
		Direction:  domain.DirectionCloseLong,
		Quantity:   1,
		Instrument: inst,
		Meta:       meta,
	}
}

func TestComputeOptionExitPrice(t *testing.T) {
	t.Run("same-day BSM exit", func(t *testing.T) {
		b := newTestBroker()

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
		underlyingPrice := 153.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"delta_at_entry":   "0.55",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-06",
			"underlying":       "AAPL",
		}
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		// BSM should produce a positive price above intrinsic (3.0)
		assert.Greater(t, price, 3.0, "BSM exit should exceed intrinsic for ITM call with time value")
		assert.Less(t, price, 10.0, "BSM exit should be reasonable")

		// Compare with delta-linear approximation to verify BSM is different
		deltaLinear := 5.00 + 0.55*(153.0-150.0) // 5.0 + 1.65 = 6.65
		// BSM result should differ from naive delta-linear
		diff := price - deltaLinear
		assert.Greater(t, math.Abs(diff), 0.01,
			"BSM should differ from simple delta-linear approximation")
	})

	t.Run("same-day fallback to delta-linear when IV missing", func(t *testing.T) {
		b := newTestBroker()

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
		underlyingPrice := 153.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"delta_at_entry":   "0.55",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-06",
			"underlying":       "AAPL",
			// iv_at_entry intentionally omitted
		}
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		// Delta-linear: premium + delta * (underlyingPrice - entryUnderlying) - spread
		// = 5.00 + 0.55 * 3.0 - spread
		expectedBase := 5.00 + 0.55*3.0 // 6.65
		// Spread: premium=5.00 is in [5,10) tier => 0.5%
		spreadCost := 5.00 * 0.005 // 0.025
		expected := expectedBase - spreadCost

		assert.InDelta(t, expected, price, 0.02,
			"without IV, should fall back to delta-linear")
	})

	t.Run("multi-day exit uses historical bid", func(t *testing.T) {
		b := newTestBroker()
		expectedBid := 4.20
		b.SetHistoricalOptions(&mockHistoricalOptions{
			row: &domain.HistoricalOptionChainRow{
				Bid:   expectedBid,
				Ask:   4.80,
				Delta: 0.52,
			},
		})

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-07 10:00")
		underlyingPrice := 152.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"delta_at_entry":   "0.55",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-06", // different from bar date
			"underlying":       "AAPL",
		}
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		assert.Equal(t, expectedBid, price,
			"multi-day exit with historical data should use bid price")
	})

	t.Run("multi-day exit falls back to BSM when no historical data", func(t *testing.T) {
		b := newTestBroker()
		b.SetHistoricalOptions(&mockHistoricalOptions{
			row: nil, // no historical data
			err: fmt.Errorf("not found"),
		})

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-07 10:00")
		underlyingPrice := 152.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"delta_at_entry":   "0.55",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-06",
			"underlying":       "AAPL",
		}
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		// Should fall through to BSM since historical lookup failed
		assert.Greater(t, price, 0.0, "should still produce a valid price via BSM fallback")
		assert.Greater(t, price, 2.0, "ITM call with 3 DTE should have meaningful value")
	})

	t.Run("expired option returns intrinsic", func(t *testing.T) {
		b := newTestBroker()

		// Bar time is after expiry
		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-11 10:00")
		underlyingPrice := 155.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-10",
			"underlying":       "AAPL",
		}
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		// Intrinsic = 155 - 150 = 5.0
		assert.InDelta(t, 5.0, price, 0.01, "expired ITM call should return intrinsic")
	})

	t.Run("expired OTM option returns minimum", func(t *testing.T) {
		b := newTestBroker()

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-11 10:00")
		underlyingPrice := 145.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"premium":          "3.00",
			"entry_underlying": "148.0",
			"entry_date":       "2026-04-10",
			"underlying":       "AAPL",
		}
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		// OTM: intrinsic = 145 - 150 = -5, clamped to 0 (worthless).
		// Previously floored at $0.01 which silently added
		// (contracts * $0.01 * 100) of spurious P&L per universe.
		assert.InDelta(t, 0.0, price, 0.001, "expired OTM call should return 0")
	})

	t.Run("nil meta returns zero", func(t *testing.T) {
		b := newTestBroker()
		intent := domain.OrderIntent{
			Symbol:    domain.Symbol("AAPL260410C00150000"),
			Direction: domain.DirectionCloseLong,
			Meta:      nil,
		}
		price := b.computeOptionExitPrice(intent, 150.0, time.Now())
		assert.Equal(t, 0.0, price)
	})

	t.Run("missing strike and expiry falls back to position avgCost", func(t *testing.T) {
		b := newTestBroker()
		sym := "AAPL260410C00150000"
		// Seed a position so the fallback has something to return
		b.positions[sym] = &position{
			symbol:   domain.Symbol(sym),
			side:     "buy",
			quantity: 1,
			avgCost:  4.50,
		}
		intent := makeOptionExitIntent(sym, map[string]string{
			"underlying": "AAPL",
			// strike and expiry missing
		})
		price := b.computeOptionExitPrice(intent, 150.0, time.Now())
		assert.Equal(t, 4.50, price, "should fall back to position avgCost")
	})

	t.Run("put exit uses correct intrinsic on expiry", func(t *testing.T) {
		b := newTestBroker()

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-11 10:00")
		underlyingPrice := 145.0

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "PUT",
			"iv_at_entry":      "0.30",
			"premium":          "3.00",
			"entry_underlying": "148.0",
			"entry_date":       "2026-04-10",
			"underlying":       "AAPL",
		}
		intent := makeOptionExitIntent("AAPL260410P00150000", meta)

		price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)

		// Put intrinsic = strike - underlying = 150 - 145 = 5.0
		assert.InDelta(t, 5.0, price, 0.01, "expired ITM put should return intrinsic")
	})

	t.Run("spread tier selection", func(t *testing.T) {
		b := newTestBroker()
		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
		underlyingPrice := 150.0 // ATM, no underlying move

		tests := []struct {
			name       string
			premium    string
			spreadPct  float64
		}{
			{"high premium >= 10", "12.00", 0.003},
			{"mid premium >= 5", "7.00", 0.005},
			{"low premium >= 2", "3.00", 0.008},
			{"very low premium < 2", "1.50", 0.015},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				meta := map[string]string{
					"strike":           "150.0",
					"expiry":           "2026-04-10",
					"option_right":     "CALL",
					"iv_at_entry":      "0.30",
					"premium":          tc.premium,
					"entry_underlying": "150.0",
					"entry_date":       "2026-04-06",
					"underlying":       "AAPL",
				}
				intent := makeOptionExitIntent("AAPL260410C00150000", meta)

				price := b.computeOptionExitPrice(intent, underlyingPrice, barTime)
				require.Greater(t, price, 0.0, "should produce positive price")
			})
		}
	})
}

func TestComputeOptionExitPrice_VIXBetaScaling(t *testing.T) {
	baseMeta := map[string]string{
		"strike":           "150.0",
		"expiry":           "2026-04-10",
		"option_right":     "CALL",
		"iv_at_entry":      "0.30",
		"premium":          "5.00",
		"entry_underlying": "150.0",
		"entry_date":       "2026-04-06",
		"underlying":       "AAPL",
	}

	t.Run("VIX spike increases exit price", func(t *testing.T) {
		// Baseline: no VIX adjustment
		bBase := newTestBroker()
		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
		basePrice := bBase.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", baseMeta), 153.0, barTime)

		// With VIX spike: entry 15, now 21 (+40%)
		bAdj := New(Config{SlippageBPS: 5, DisableFillChan: true, VIXIVBeta: 1.0}, zerolog.Nop())
		bAdj.prices[domain.Symbol("VIX")] = 21.0

		metaWithVIX := make(map[string]string)
		for k, v := range baseMeta {
			metaWithVIX[k] = v
		}
		metaWithVIX["vix_at_entry"] = "15.0"

		adjPrice := bAdj.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", metaWithVIX), 153.0, barTime)

		assert.Greater(t, adjPrice, basePrice,
			"VIX spike should increase call exit price (higher IV = more time value)")
	})

	t.Run("VIX drop decreases exit price", func(t *testing.T) {
		bBase := newTestBroker()
		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
		basePrice := bBase.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", baseMeta), 153.0, barTime)

		bAdj := New(Config{SlippageBPS: 5, DisableFillChan: true, VIXIVBeta: 1.0}, zerolog.Nop())
		bAdj.prices[domain.Symbol("VIX")] = 12.0

		metaWithVIX := make(map[string]string)
		for k, v := range baseMeta {
			metaWithVIX[k] = v
		}
		metaWithVIX["vix_at_entry"] = "15.0"

		adjPrice := bAdj.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", metaWithVIX), 153.0, barTime)

		assert.Less(t, adjPrice, basePrice,
			"VIX drop should decrease call exit price (lower IV = less time value)")
	})

	t.Run("no VIX data leaves price unchanged", func(t *testing.T) {
		bBase := newTestBroker()
		bAdj := New(Config{SlippageBPS: 5, DisableFillChan: true, VIXIVBeta: 1.0}, zerolog.Nop())
		// VIX price NOT set — should gracefully skip

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")

		basePrice := bBase.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", baseMeta), 153.0, barTime)
		adjPrice := bAdj.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", baseMeta), 153.0, barTime)

		assert.InDelta(t, basePrice, adjPrice, 0.01,
			"missing VIX data should produce same price as no adjustment")
	})
}

func TestComputeOptionExitPrice_TODSeasonality(t *testing.T) {
	meta := map[string]string{
		"strike":           "150.0",
		"expiry":           "2026-04-10",
		"option_right":     "CALL",
		"iv_at_entry":      "0.30",
		"premium":          "5.00",
		"entry_underlying": "150.0",
		"entry_date":       "2026-04-06",
		"underlying":       "AAPL",
	}

	t.Run("opening IV premium vs midday dip", func(t *testing.T) {
		bAdj := New(Config{SlippageBPS: 5, DisableFillChan: true, TODSeasonalEnabled: true}, zerolog.Nop())
		intent := makeOptionExitIntent("AAPL260410C00150000", meta)

		// 9:40 ET = opening premium
		openTime, _ := time.ParseInLocation("2006-01-02 15:04", "2026-04-06 09:40", mustLoadET())
		openPrice := bAdj.computeOptionExitPrice(intent, 153.0, openTime)

		// 12:30 ET = midday dip
		midTime, _ := time.ParseInLocation("2006-01-02 15:04", "2026-04-06 12:30", mustLoadET())
		midPrice := bAdj.computeOptionExitPrice(intent, 153.0, midTime)

		assert.Greater(t, openPrice, midPrice,
			"opening IV premium should produce higher exit price than midday dip")
	})
}

func TestComputeOptionExitPrice_EarningsRamp(t *testing.T) {
	t.Run("earnings tomorrow vs no earnings", func(t *testing.T) {
		bBase := newTestBroker()
		bAdj := New(Config{SlippageBPS: 5, DisableFillChan: true, EarningsRampEnabled: true}, zerolog.Nop())

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")

		baseMeta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-06",
			"underlying":       "AAPL",
		}
		basePrice := bBase.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", baseMeta), 153.0, barTime)

		earningsMeta := make(map[string]string)
		for k, v := range baseMeta {
			earningsMeta[k] = v
		}
		earningsMeta["days_to_earnings"] = "1"

		earningsPrice := bAdj.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", earningsMeta), 153.0, barTime)

		assert.Greater(t, earningsPrice, basePrice,
			"1-day-to-earnings IV ramp should increase exit price significantly")
		// sqrt(5)/sqrt(1) ≈ 2.24x IV multiplier — price should be notably higher
		assert.Greater(t, earningsPrice, basePrice*1.3,
			"earnings ramp should produce >30% higher exit price")
	})

	t.Run("no earnings meta leaves price unchanged", func(t *testing.T) {
		bBase := newTestBroker()
		bAdj := New(Config{SlippageBPS: 5, DisableFillChan: true, EarningsRampEnabled: true}, zerolog.Nop())

		barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")

		meta := map[string]string{
			"strike":           "150.0",
			"expiry":           "2026-04-10",
			"option_right":     "CALL",
			"iv_at_entry":      "0.30",
			"premium":          "5.00",
			"entry_underlying": "150.0",
			"entry_date":       "2026-04-06",
			"underlying":       "AAPL",
		}

		basePrice := bBase.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", meta), 153.0, barTime)
		adjPrice := bAdj.computeOptionExitPrice(
			makeOptionExitIntent("AAPL260410C00150000", meta), 153.0, barTime)

		assert.InDelta(t, basePrice, adjPrice, 0.01,
			"no earnings meta should produce same price")
	})
}

func TestMinutesSinceMarketOpen(t *testing.T) {
	et := mustLoadET()
	tests := []struct {
		name     string
		time     time.Time
		expected int
	}{
		{"market open", time.Date(2026, 4, 6, 9, 30, 0, 0, et), 0},
		{"10:00 ET", time.Date(2026, 4, 6, 10, 0, 0, 0, et), 30},
		{"12:00 ET", time.Date(2026, 4, 6, 12, 0, 0, 0, et), 150},
		{"16:00 ET", time.Date(2026, 4, 6, 16, 0, 0, 0, et), 390},
		{"pre-market", time.Date(2026, 4, 6, 8, 0, 0, 0, et), 0},
		{"after hours", time.Date(2026, 4, 6, 18, 0, 0, 0, et), 390},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := minutesSinceMarketOpen(tc.time)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func mustLoadET() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}
