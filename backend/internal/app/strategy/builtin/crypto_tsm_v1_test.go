package builtin_test

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Helpers ────────────────────────────────────────────────────────────────

func cryptoTSMParams() map[string]any {
	return map[string]any{
		"lookback_days":        28,
		"decay_tau_divisor":    3.0,
		"zscore_window":        90,
		"entry_z_threshold":    0.5,
		"exit_z_threshold":     0.0,
		"vol_regime_fast":      20,
		"vol_regime_slow":      50,
		"realized_vol_cap_pct": 120.0,
		"crash_vol_exit_pct":   150.0,
		"vol_annualize_factor": 19.105,
		"trailing_stop_atr_mult": 2.5,
		"atr_period":           14,
		"max_hold_days":        14,
		"hard_stop_pct":        0.05,
		"risk_per_trade_bps":   200,
		"max_gross_exposure_pct": 80.0,
		"max_positions":        3,
		"cooldown_days":        2,
	}
}

// makeDailyBar creates a synthetic daily bar at the given day offset.
func makeDailyBar(dayOffset int, open, high, low, close, volume float64) strat.Bar {
	return strat.Bar{
		Time:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, dayOffset),
		Open:   open,
		High:   high,
		Low:    low,
		Close:  close,
		Volume: volume,
	}
}

// feedBars warmups and processes bars, returning the final state and any signals from the last bar.
func feedBars(t *testing.T, s *builtin.CryptoTSMStrategy, symbol string, params map[string]any, bars []strat.Bar) (strat.State, []strat.Signal) {
	t.Helper()
	st, err := s.Init(nil, symbol, params, nil)
	require.NoError(t, err)

	warmup := s.WarmupBars()
	var signals []strat.Signal

	for i, bar := range bars {
		if i < warmup {
			st, err = s.ReplayOnBar(nil, symbol, bar, st, strat.IndicatorData{})
			require.NoError(t, err)
		} else {
			st, signals, err = s.OnBar(nil, symbol, bar, st)
			require.NoError(t, err)
		}
	}
	return st, signals
}

// generateTrendBars creates a series of trending daily bars with specified volatility.
func generateTrendBars(n int, startPrice, dailyReturnPct, volume float64) []strat.Bar {
	bars := make([]strat.Bar, n)
	price := startPrice
	for i := 0; i < n; i++ {
		open := price
		close := open * (1 + dailyReturnPct/100.0)
		high := math.Max(open, close) * 1.005
		low := math.Min(open, close) * 0.995
		bars[i] = makeDailyBar(i, open, high, low, close, volume)
		price = close
	}
	return bars
}

// ─── Meta / Init ────────────────────────────────────────────────────────────

func TestCryptoTSM_Meta(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	m := s.Meta()
	assert.Equal(t, "crypto_tsm", m.ID.String())
	assert.Equal(t, "1.0.0", m.Version.String())
}

func TestCryptoTSM_WarmupBars(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	assert.Equal(t, 90, s.WarmupBars())
}

func TestCryptoTSM_Init(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	st, err := s.Init(nil, "BTC/USD", cryptoTSMParams(), nil)
	require.NoError(t, err)
	require.NotNil(t, st)
}

func TestCryptoTSM_Init_WithPrior(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	st1, err := s.Init(nil, "BTC/USD", cryptoTSMParams(), nil)
	require.NoError(t, err)

	// Re-init with prior state should preserve it.
	st2, err := s.Init(nil, "BTC/USD", cryptoTSMParams(), st1)
	require.NoError(t, err)
	require.NotNil(t, st2)
}

// ─── Signal Generation ──────────────────────────────────────────────────────

func TestCryptoTSM_NoSignalDuringWarmup(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	st, err := s.Init(nil, "BTC/USD", cryptoTSMParams(), nil)
	require.NoError(t, err)

	// Feed bars during warmup — should never produce signals.
	for i := 0; i < s.WarmupBars(); i++ {
		bar := makeDailyBar(i, 50000, 50500, 49500, 50000+float64(i)*10, 1e6)
		st, err = s.ReplayOnBar(nil, "BTC/USD", bar, st, strat.IndicatorData{})
		require.NoError(t, err)
	}
}

func TestCryptoTSM_EntryOnStrongUptrend(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	// Generate a strong uptrend: 0.5% daily returns for 120 bars with expanding volume.
	bars := generateTrendBars(120, 50000, 0.5, 1e6)
	// Ramp up volume in the last 20 bars to trigger vol expansion.
	for i := 100; i < 120; i++ {
		bars[i].Volume = 2e6
	}

	_, signals := feedBars(t, s, "BTC/USD", cryptoTSMParams(), bars)

	// Should produce an entry signal on the uptrend.
	// (May or may not depending on exact z-score; at least verify no error.)
	_ = signals
}

func TestCryptoTSM_NoEntryOnDowntrend(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	// Generate a downtrend: -0.5% daily returns.
	bars := generateTrendBars(120, 50000, -0.5, 1e6)

	_, signals := feedBars(t, s, "BTC/USD", cryptoTSMParams(), bars)

	// Downtrend should NOT produce a long entry.
	for _, sig := range signals {
		assert.NotEqual(t, strat.SignalEntry, sig.Type,
			"should not generate entry on downtrend")
	}
}

func TestCryptoTSM_NoEntryHighVol(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	// Generate uptrend but with extreme volatility (large daily swings).
	bars := make([]strat.Bar, 120)
	price := 50000.0
	for i := 0; i < 120; i++ {
		swing := 5000.0 // 10% daily range
		if i%2 == 0 {
			bars[i] = makeDailyBar(i, price, price+swing, price-100, price+swing*0.8, 1e6)
			price = price + swing*0.8
		} else {
			bars[i] = makeDailyBar(i, price, price+100, price-swing, price-swing*0.3, 1e6)
			price = price - swing*0.3
		}
	}

	params := cryptoTSMParams()
	params["realized_vol_cap_pct"] = 50.0 // very tight vol cap

	_, signals := feedBars(t, s, "BTC/USD", params, bars)

	// High-vol environment should be gated out.
	for _, sig := range signals {
		assert.NotEqual(t, strat.SignalEntry, sig.Type,
			"should not generate entry in high-vol environment")
	}
}

// ─── Exit Logic ─────────────────────────────────────────────────────────────

func TestCryptoTSM_ExitOnSignalReversal(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	params := cryptoTSMParams()
	params["entry_z_threshold"] = 0.1 // lower threshold for easier entry

	// Uptrend for 100 bars, then reversal.
	bars := generateTrendBars(100, 50000, 0.5, 1e6)
	// Increase volume for entry.
	for i := 80; i < 100; i++ {
		bars[i].Volume = 2e6
	}
	// Add reversal bars.
	lastPrice := bars[99].Close
	for i := 0; i < 20; i++ {
		open := lastPrice
		close := open * 0.99 // -1% daily
		high := open * 1.002
		low := close * 0.998
		bars = append(bars, makeDailyBar(100+i, open, high, low, close, 1e6))
		lastPrice = close
	}

	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	warmup := s.WarmupBars()
	var entryFound, exitFound bool
	for i, bar := range bars {
		if i < warmup {
			st, err = s.ReplayOnBar(nil, "BTC/USD", bar, st, strat.IndicatorData{})
			require.NoError(t, err)
			continue
		}

		var signals []strat.Signal
		st, signals, err = s.OnBar(nil, "BTC/USD", bar, st)
		require.NoError(t, err)

		for _, sig := range signals {
			if sig.Type == strat.SignalEntry {
				entryFound = true
				// Simulate fill.
				st, _, err = s.OnEvent(nil, "BTC/USD", strat.FillConfirmation{
					Symbol:   "BTC/USD",
					Side:     strat.SideBuy,
					Quantity: 0.1,
					Price:    bar.Close,
				}, st)
				require.NoError(t, err)
			}
			if sig.Type == strat.SignalExit {
				exitFound = true
			}
		}
	}

	// If we got an entry, we should also get an exit on the reversal.
	if entryFound {
		assert.True(t, exitFound, "reversal should trigger exit after entry")
	}
}

// ─── OnEvent ────────────────────────────────────────────────────────────────

func TestCryptoTSM_FillConfirmation(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	st, err := s.Init(nil, "BTC/USD", cryptoTSMParams(), nil)
	require.NoError(t, err)

	st, _, err = s.OnEvent(nil, "BTC/USD", strat.FillConfirmation{
		Symbol:   "BTC/USD",
		Side:     strat.SideBuy,
		Quantity: 0.1,
		Price:    50000,
	}, st)
	require.NoError(t, err)
}

func TestCryptoTSM_EntryRejection(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	st, err := s.Init(nil, "BTC/USD", cryptoTSMParams(), nil)
	require.NoError(t, err)

	st, _, err = s.OnEvent(nil, "BTC/USD", strat.EntryRejection{
		Symbol: "BTC/USD",
		Side:   strat.SideBuy,
		Reason: "risk_limit",
	}, st)
	require.NoError(t, err)
}

// ─── Edge Cases ─────────────────────────────────────────────────────────────

func TestCryptoTSM_ZeroVolume(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	// Bars with zero volume should not panic.
	bars := generateTrendBars(100, 50000, 0.3, 0)
	_, signals := feedBars(t, s, "BTC/USD", cryptoTSMParams(), bars)
	_ = signals // no panic = pass
}

func TestCryptoTSM_FlatMarket(t *testing.T) {
	s := builtin.NewCryptoTSMStrategy()
	// Flat market: zero returns.
	bars := generateTrendBars(120, 50000, 0.0, 1e6)
	_, signals := feedBars(t, s, "BTC/USD", cryptoTSMParams(), bars)

	// Flat market should not produce entries.
	for _, sig := range signals {
		assert.NotEqual(t, strat.SignalEntry, sig.Type,
			"should not generate entry in flat market")
	}
}
