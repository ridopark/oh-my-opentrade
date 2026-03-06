package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarketBar(t *testing.T) {
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("5m")
	now := time.Now()

	t.Run("valid creation", func(t *testing.T) {
		bar, err := domain.NewMarketBar(now, sym, tf, 50000.0, 50100.0, 49900.0, 50050.0, 10.5)
		require.NoError(t, err)
		assert.Equal(t, now, bar.Time)
		assert.Equal(t, sym, bar.Symbol)
		assert.Equal(t, tf, bar.Timeframe)
		assert.Equal(t, 50000.0, bar.Open)
		assert.Equal(t, 50100.0, bar.High)
		assert.Equal(t, 49900.0, bar.Low)
		assert.Equal(t, 50050.0, bar.Close)
		assert.Equal(t, 10.5, bar.Volume)
		assert.False(t, bar.Suspect) // Defaults to false
	})

	t.Run("invalid - high less than low", func(t *testing.T) {
		_, err := domain.NewMarketBar(now, sym, tf, 50000.0, 49000.0, 49900.0, 49500.0, 10.5)
		assert.ErrorContains(t, err, "high cannot be less than low")
	})

	t.Run("valid - zero volume (crypto idle bar)", func(t *testing.T) {
		bar, err := domain.NewMarketBar(now, sym, tf, 50000.0, 50100.0, 49900.0, 50050.0, 0)
		require.NoError(t, err)
		assert.Equal(t, 0.0, bar.Volume)
	})

	t.Run("invalid - negative volume", func(t *testing.T) {
		_, err := domain.NewMarketBar(now, sym, tf, 50000.0, 50100.0, 49900.0, 50050.0, -5.0)
		assert.ErrorContains(t, err, "volume must not be negative")
	})
}

func TestOrderIntent(t *testing.T) {
	tenantID := "tenant-1"
	envMode, _ := domain.NewEnvMode("Paper")
	sym, _ := domain.NewSymbol("BTC/USD")
	dir, _ := domain.NewDirection("LONG")
	intentID := uuid.New()

	t.Run("valid creation", func(t *testing.T) {
		intent, err := domain.NewOrderIntent(
			intentID,
			tenantID,
			envMode,
			sym,
			dir,
			50000.0, // LimitPrice
			49000.0, // StopLoss
			10,      // MaxSlippageBPS
			1.5,     // Quantity
			"test-strategy",
			"looking good",
			0.85, // Confidence
			"idempotency-123",
		)
		require.NoError(t, err)
		assert.Equal(t, intentID, intent.ID)
		assert.Equal(t, tenantID, intent.TenantID)
		assert.Equal(t, envMode, intent.EnvMode)
		assert.Equal(t, sym, intent.Symbol)
		assert.Equal(t, dir, intent.Direction)
		assert.Equal(t, 50000.0, intent.LimitPrice)
		assert.Equal(t, 49000.0, intent.StopLoss)
		assert.Equal(t, 10, intent.MaxSlippageBPS)
		assert.Equal(t, 1.5, intent.Quantity)
		assert.Equal(t, "test-strategy", intent.Strategy)
		assert.Equal(t, "looking good", intent.Rationale)
		assert.Equal(t, 0.85, intent.Confidence)
		assert.Equal(t, "idempotency-123", intent.IdempotencyKey)
	})

	t.Run("invalid - missing idempotency key", func(t *testing.T) {
		_, err := domain.NewOrderIntent(
			intentID, tenantID, envMode, sym, dir,
			50000.0, 49000.0, 10, 1.5, "strat", "rat", 0.85, "",
		)
		assert.ErrorContains(t, err, "idempotency key is required")
	})

	t.Run("invalid - zero or negative stop loss", func(t *testing.T) {
		_, err := domain.NewOrderIntent(
			intentID, tenantID, envMode, sym, dir,
			50000.0, 0, 10, 1.5, "strat", "rat", 0.85, "idempotency-123",
		)
		assert.ErrorContains(t, err, "stop loss must be greater than zero")

		_, err = domain.NewOrderIntent(
			intentID, tenantID, envMode, sym, dir,
			50000.0, -100.0, 10, 1.5, "strat", "rat", 0.85, "idempotency-123",
		)
		assert.ErrorContains(t, err, "stop loss must be greater than zero")
	})

	t.Run("invalid - zero or negative limit price", func(t *testing.T) {
		_, err := domain.NewOrderIntent(
			intentID, tenantID, envMode, sym, dir,
			0, 49000.0, 10, 1.5, "strat", "rat", 0.85, "idempotency-123",
		)
		assert.ErrorContains(t, err, "limit price must be greater than zero")
	})

	t.Run("invalid - confidence out of range", func(t *testing.T) {
		_, err := domain.NewOrderIntent(
			intentID, tenantID, envMode, sym, dir,
			50000.0, 49000.0, 10, 1.5, "strat", "rat", -0.1, "idempotency-123",
		)
		assert.ErrorContains(t, err, "confidence must be between 0 and 1")

		_, err = domain.NewOrderIntent(
			intentID, tenantID, envMode, sym, dir,
			50000.0, 49000.0, 10, 1.5, "strat", "rat", 1.1, "idempotency-123",
		)
		assert.ErrorContains(t, err, "confidence must be between 0 and 1")
	})
}

func TestIndicatorSnapshot(t *testing.T) {
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("5m")
	now := time.Now()

	t.Run("valid creation", func(t *testing.T) {
		snap, err := domain.NewIndicatorSnapshot(
			now, sym, tf,
			65.5, 80.0, 75.0, 50100.0, 50000.0, 50050.0, 100.0, 150.0,
		)
		require.NoError(t, err)
		assert.Equal(t, now, snap.Time)
		assert.Equal(t, sym, snap.Symbol)
		assert.Equal(t, tf, snap.Timeframe)
		assert.Equal(t, 65.5, snap.RSI)
		assert.Equal(t, 80.0, snap.StochK)
		assert.Equal(t, 75.0, snap.StochD)
		assert.Equal(t, 50100.0, snap.EMA9)
		assert.Equal(t, 50000.0, snap.EMA21)
		assert.Equal(t, 50050.0, snap.VWAP)
		assert.Equal(t, 100.0, snap.Volume)
		assert.Equal(t, 150.0, snap.VolumeSMA)
	})

	t.Run("invalid - rsi out of range", func(t *testing.T) {
		_, err := domain.NewIndicatorSnapshot(
			now, sym, tf,
			-1.0, 80.0, 75.0, 50100.0, 50000.0, 50050.0, 100.0, 150.0,
		)
		assert.ErrorContains(t, err, "RSI must be between 0 and 100")

		_, err = domain.NewIndicatorSnapshot(
			now, sym, tf,
			100.1, 80.0, 75.0, 50100.0, 50000.0, 50050.0, 100.0, 150.0,
		)
		assert.ErrorContains(t, err, "RSI must be between 0 and 100")
	})
}

func TestMarketRegime(t *testing.T) {
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("1h")
	regimeType, _ := domain.NewRegimeType("TREND")
	since := time.Now().Add(-1 * time.Hour)

	t.Run("valid creation", func(t *testing.T) {
		regime, err := domain.NewMarketRegime(sym, tf, regimeType, since, 0.9)
		require.NoError(t, err)
		assert.Equal(t, sym, regime.Symbol)
		assert.Equal(t, tf, regime.Timeframe)
		assert.Equal(t, regimeType, regime.Type)
		assert.Equal(t, since, regime.Since)
		assert.Equal(t, 0.9, regime.Strength)
	})

	t.Run("invalid - strength out of range", func(t *testing.T) {
		_, err := domain.NewMarketRegime(sym, tf, regimeType, since, -0.1)
		assert.ErrorContains(t, err, "strength must be between 0 and 1")

		_, err = domain.NewMarketRegime(sym, tf, regimeType, since, 1.1)
		assert.ErrorContains(t, err, "strength must be between 0 and 1")
	})
}

func TestStrategyDNA(t *testing.T) {
	tenantID := "tenant-1"
	envMode, _ := domain.NewEnvMode("Live")
	id := uuid.New()

	t.Run("valid creation", func(t *testing.T) {
		params := map[string]any{"window": 14, "multiplier": 2.5}
		metrics := map[string]float64{"win_rate": 0.65, "profit_factor": 1.5}

		dna, err := domain.NewStrategyDNA(id, tenantID, envMode, 1, params, metrics)
		require.NoError(t, err)
		assert.Equal(t, id, dna.ID)
		assert.Equal(t, tenantID, dna.TenantID)
		assert.Equal(t, envMode, dna.EnvMode)
		assert.Equal(t, 1, dna.Version)
		assert.Equal(t, params, dna.Parameters)
		assert.Equal(t, metrics, dna.PerformanceMetrics)
	})
}

func TestTrade(t *testing.T) {
	tenantID := "tenant-1"
	envMode, _ := domain.NewEnvMode("Paper")
	tradeID := uuid.New()
	sym, _ := domain.NewSymbol("ETH/USD")
	now := time.Now()

	t.Run("valid creation", func(t *testing.T) {
		trade, err := domain.NewTrade(now, tenantID, envMode, tradeID, sym, "BUY", 2.0, 3000.0, 1.5, "FILLED", "debate", "test rationale")
		require.NoError(t, err)
		assert.Equal(t, now, trade.Time)
		assert.Equal(t, tenantID, trade.TenantID)
		assert.Equal(t, envMode, trade.EnvMode)
		assert.Equal(t, tradeID, trade.TradeID)
		assert.Equal(t, sym, trade.Symbol)
		assert.Equal(t, "BUY", trade.Side)
		assert.Equal(t, 2.0, trade.Quantity)
		assert.Equal(t, 3000.0, trade.Price)
		assert.Equal(t, 1.5, trade.Commission)
		assert.Equal(t, "FILLED", trade.Status)
	})

	t.Run("invalid - negative quantity", func(t *testing.T) {
		_, err := domain.NewTrade(now, tenantID, envMode, tradeID, sym, "BUY", -2.0, 3000.0, 1.5, "FILLED", "", "")
		assert.ErrorContains(t, err, "quantity cannot be negative")
	})
}
