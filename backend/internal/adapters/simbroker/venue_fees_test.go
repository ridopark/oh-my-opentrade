package simbroker_test

import (
	"math"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestCryptoVenueFees_HyperliquidTaker(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()

	ctx := simbroker.FeeContext{
		Symbol:    "BTC/USD",
		Venue:     domain.VenueHyperliquid,
		Side:      "BUY",
		Qty:       1.0,
		Notional:  50000.0, // 1 BTC at $50k
		FillPrice: 50000.0,
		OrderType: "market", // taker
	}

	result := fees.Compute(ctx)

	// 3.5 bps of $50,000 = $17.50
	expected := 50000.0 * 3.5 / 10000.0
	assert.InDelta(t, expected, result.Commission, 0.001, "Hyperliquid taker fee should be 3.5 bps")
	assert.InDelta(t, expected, result.Total, 0.001)
	assert.Equal(t, 0.0, result.Regulatory)
	assert.Equal(t, 0.0, result.Exchange)
}

func TestCryptoVenueFees_CoinbaseMaker(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()

	ctx := simbroker.FeeContext{
		Symbol:    "ETH/USD",
		Venue:     domain.VenueCoinbase,
		Side:      "SELL",
		Qty:       10.0,
		Notional:  30000.0, // 10 ETH at $3k
		FillPrice: 3000.0,
		OrderType: "limit", // maker
	}

	result := fees.Compute(ctx)

	// 6.0 bps of $30,000 = $18.00
	expected := 30000.0 * 6.0 / 10000.0
	assert.InDelta(t, expected, result.Commission, 0.001, "Coinbase maker fee should be 6.0 bps")
	assert.InDelta(t, expected, result.Total, 0.001)
}

func TestCryptoVenueFees_UnknownVenueUsesDefault(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()

	ctx := simbroker.FeeContext{
		Symbol:    "SOL/USD",
		Venue:     domain.Venue("deribit"),
		Side:      "BUY",
		Qty:       100.0,
		Notional:  10000.0,
		FillPrice: 100.0,
		OrderType: "market", // taker
	}

	result := fees.Compute(ctx)

	// Default taker: 10.0 bps of $10,000 = $10.00
	expected := 10000.0 * 10.0 / 10000.0
	assert.InDelta(t, expected, result.Commission, 0.001, "unknown venue should use 10.0 bps taker default")
	assert.InDelta(t, expected, result.Total, 0.001)
}

func TestCryptoVenueFees_LimitGetsMaker_MarketGetsTaker(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()
	notional := 100000.0

	t.Run("market order gets taker rate", func(t *testing.T) {
		ctx := simbroker.FeeContext{
			Symbol:    "BTC/USD",
			Venue:     domain.VenueBinanceFut,
			Qty:       2.0,
			Notional:  notional,
			FillPrice: 50000.0,
			OrderType: "market",
		}
		result := fees.Compute(ctx)
		// Binance Futures taker: 5.0 bps
		expected := notional * 5.0 / 10000.0
		assert.InDelta(t, expected, result.Total, 0.001)
	})

	t.Run("limit order gets maker rate", func(t *testing.T) {
		ctx := simbroker.FeeContext{
			Symbol:    "BTC/USD",
			Venue:     domain.VenueBinanceFut,
			Qty:       2.0,
			Notional:  notional,
			FillPrice: 50000.0,
			OrderType: "limit",
		}
		result := fees.Compute(ctx)
		// Binance Futures maker: 2.0 bps
		expected := notional * 2.0 / 10000.0
		assert.InDelta(t, expected, result.Total, 0.001)
	})

	t.Run("empty order type defaults to taker", func(t *testing.T) {
		ctx := simbroker.FeeContext{
			Symbol:    "BTC/USD",
			Venue:     domain.VenueBinanceFut,
			Qty:       2.0,
			Notional:  notional,
			FillPrice: 50000.0,
			OrderType: "", // empty = taker
		}
		result := fees.Compute(ctx)
		expected := notional * 5.0 / 10000.0
		assert.InDelta(t, expected, result.Total, 0.001)
	})
}

func TestCryptoVenueFees_Name(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()
	assert.Equal(t, "crypto_venue", fees.Name())
}

func TestCryptoVenueFees_AllDefaultVenues(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()
	notional := 100000.0

	tests := []struct {
		name      string
		venue     domain.Venue
		makerBPS  float64
		takerBPS  float64
	}{
		{"Hyperliquid", domain.VenueHyperliquid, 1.0, 3.5},
		{"BinanceFutures", domain.VenueBinanceFut, 2.0, 5.0},
		{"Bybit", domain.VenueBybit, 1.0, 6.0},
		{"Coinbase", domain.VenueCoinbase, 6.0, 8.0},
	}

	for _, tt := range tests {
		t.Run(tt.name+"_maker", func(t *testing.T) {
			ctx := simbroker.FeeContext{
				Venue: tt.venue, Notional: notional, OrderType: "limit",
			}
			result := fees.Compute(ctx)
			expected := notional * tt.makerBPS / 10000.0
			assert.InDelta(t, expected, result.Total, 0.001)
		})
		t.Run(tt.name+"_taker", func(t *testing.T) {
			ctx := simbroker.FeeContext{
				Venue: tt.venue, Notional: notional, OrderType: "market",
			}
			result := fees.Compute(ctx)
			expected := notional * tt.takerBPS / 10000.0
			assert.InDelta(t, expected, result.Total, 0.001)
		})
	}
}

func TestFeeScheduleByName_CryptoVenue(t *testing.T) {
	sched, err := simbroker.FeeScheduleByName("crypto_venue")
	assert.NoError(t, err)
	assert.Equal(t, "crypto_venue", sched.Name())
}

func TestCryptoVenueFees_ZeroNotional(t *testing.T) {
	fees := simbroker.DefaultCryptoFees()
	ctx := simbroker.FeeContext{
		Venue:     domain.VenueHyperliquid,
		Notional:  0,
		OrderType: "market",
	}
	result := fees.Compute(ctx)
	assert.True(t, math.Abs(result.Total) < 1e-10, "zero notional should produce zero fees")
}
