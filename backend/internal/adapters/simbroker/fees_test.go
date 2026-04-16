package simbroker_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlpacaEquityFees(t *testing.T) {
	f := simbroker.AlpacaEquityFees{}

	t.Run("BUY pays nothing", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			Symbol: "AAPL", Side: "buy", Qty: 100, Notional: 10000, FillPrice: 100,
		})
		assert.Equal(t, 0.0, got.Total)
	})

	t.Run("SELL charges SEC + TAF + ORF", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			Symbol: "AAPL", Side: "sell", Qty: 1000, Notional: 100_000, FillPrice: 100,
		})
		// SEC: 100000 * 8e-6 = 0.80
		// TAF: 1000 * 0.000166 = 0.166 -> within [0.01, 8.30]
		// ORF: 1000 * 0.0000029 = 0.0029
		expected := 0.80 + 0.166 + 0.0029
		assert.InDelta(t, expected, got.Regulatory, 1e-9)
		assert.Equal(t, 0.0, got.Commission)
		assert.Equal(t, 0.0, got.Exchange)
		assert.InDelta(t, expected, got.Total, 1e-9)
	})

	t.Run("TAF floor applied on tiny SELL", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			Symbol: "AAPL", Side: "sell", Qty: 1, Notional: 50, FillPrice: 50,
		})
		// TAF raw = 0.000166 → floored to 0.01
		// SEC = 50 * 8e-6 = 0.0004
		// ORF = 1 * 0.0000029 = 0.0000029
		expected := 0.01 + 0.0004 + 0.0000029
		assert.InDelta(t, expected, got.Total, 1e-9)
	})

	t.Run("TAF cap applied on huge SELL", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			Symbol: "AAPL", Side: "sell", Qty: 1_000_000, Notional: 100_000_000, FillPrice: 100,
		})
		// TAF raw = 166 → capped to 8.30
		assert.Greater(t, got.Total, 8.30)
		// SEC = 1e8 * 8e-6 = 800
		// ORF = 1e6 * 2.9e-6 = 2.9
		expected := 800 + 8.30 + 2.9
		assert.InDelta(t, expected, got.Total, 1e-6)
	})

	t.Run("option fees return zero under equity schedule", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			IsOption: true, Side: "sell", Qty: 10, Notional: 1000,
		})
		assert.Equal(t, 0.0, got.Total)
	})
}

func TestIBKRTieredOptionsFees(t *testing.T) {
	f := simbroker.IBKRTieredOptionsFees{}

	t.Run("BUY charges commission + exchange", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			IsOption: true, Side: "buy", Qty: 10, Notional: 5000, FillPrice: 5,
		})
		// Commission: 0.65*10 = 6.5 (above 1.00 min)
		// Exchange:   0.04*10 = 0.40
		// Regulatory: 0 (buy)
		assert.InDelta(t, 6.50, got.Commission, 1e-9)
		assert.InDelta(t, 0.40, got.Exchange, 1e-9)
		assert.Equal(t, 0.0, got.Regulatory)
		assert.InDelta(t, 6.90, got.Total, 1e-9)
	})

	t.Run("commission floor applied on single contract", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			IsOption: true, Side: "buy", Qty: 1, Notional: 500, FillPrice: 5,
		})
		// 0.65 raw → floored to 1.00
		assert.InDelta(t, 1.00, got.Commission, 1e-9)
	})

	t.Run("SELL adds SEC fee on notional", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			IsOption: true, Side: "sell", Qty: 10, Notional: 5000, FillPrice: 5,
		})
		// SEC = 5000 * 8e-6 = 0.04
		assert.InDelta(t, 0.04, got.Regulatory, 1e-9)
	})

	t.Run("equity order returns zero", func(t *testing.T) {
		got := f.Compute(simbroker.FeeContext{
			IsOption: false, Side: "buy", Qty: 100, Notional: 10000,
		})
		assert.Equal(t, 0.0, got.Total)
	})
}

func TestNoFees(t *testing.T) {
	f := simbroker.NoFees{}
	got := f.Compute(simbroker.FeeContext{
		Symbol: "X", Side: "sell", Qty: 1000, Notional: 100_000,
	})
	assert.Equal(t, simbroker.Fees{}, got)
	assert.Equal(t, "none", f.Name())
}

func TestFeeScheduleByName(t *testing.T) {
	t.Run("alpaca_equity", func(t *testing.T) {
		fs, err := simbroker.FeeScheduleByName("alpaca_equity")
		require.NoError(t, err)
		assert.Equal(t, "alpaca_equity", fs.Name())
	})
	t.Run("ibkr_options", func(t *testing.T) {
		fs, err := simbroker.FeeScheduleByName("ibkr_options")
		require.NoError(t, err)
		assert.Equal(t, "ibkr_options", fs.Name())
	})
	t.Run("empty defaults to none", func(t *testing.T) {
		fs, err := simbroker.FeeScheduleByName("")
		require.NoError(t, err)
		assert.Equal(t, "none", fs.Name())
	})
	t.Run("unknown returns error", func(t *testing.T) {
		_, err := simbroker.FeeScheduleByName("weird")
		require.Error(t, err)
	})
}
