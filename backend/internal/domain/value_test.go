package domain_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvMode(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		mode, err := domain.NewEnvMode("Paper")
		require.NoError(t, err)
		assert.Equal(t, domain.EnvModePaper, mode)

		mode, err = domain.NewEnvMode("Live")
		require.NoError(t, err)
		assert.Equal(t, domain.EnvModeLive, mode)
	})

	t.Run("invalid creation", func(t *testing.T) {
		_, err := domain.NewEnvMode("Test")
		assert.Error(t, err)

		_, err = domain.NewEnvMode("")
		assert.Error(t, err)
	})
}

func TestDirection(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		dir, err := domain.NewDirection("LONG")
		require.NoError(t, err)
		assert.Equal(t, domain.DirectionLong, dir)

		dir, err = domain.NewDirection("SHORT")
		require.NoError(t, err)
		assert.Equal(t, domain.DirectionShort, dir)
	})

	t.Run("invalid creation", func(t *testing.T) {
		_, err := domain.NewDirection("FLAT")
		assert.Error(t, err)

		_, err = domain.NewDirection("")
		assert.Error(t, err)
	})
}

func TestSymbol(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		sym, err := domain.NewSymbol("BTC/USD")
		require.NoError(t, err)
		assert.Equal(t, "BTC/USD", sym.String())
	})

	t.Run("invalid creation", func(t *testing.T) {
		_, err := domain.NewSymbol("")
		assert.Error(t, err)
	})
}

func TestVenue(t *testing.T) {
	t.Run("zero value is unspecified", func(t *testing.T) {
		var v domain.Venue
		assert.True(t, v.IsUnspecified())
		assert.Equal(t, "", v.String())
	})

	t.Run("explicit venues round-trip", func(t *testing.T) {
		v, err := domain.NewVenue("coinbase")
		require.NoError(t, err)
		assert.Equal(t, domain.VenueCoinbase, v)
		assert.False(t, v.IsUnspecified())
	})

	t.Run("unknown venue is accepted so adapters can add new ones", func(t *testing.T) {
		// Domain must not gatekeep venue names; adapters introduce new
		// venues (e.g. a new DEX) without a domain change.
		v, err := domain.NewVenue("some-new-dex")
		require.NoError(t, err)
		assert.Equal(t, domain.Venue("some-new-dex"), v)
	})
}

func TestDefaultVenue(t *testing.T) {
	tests := []struct {
		name string
		ac   domain.AssetClass
		want domain.Venue
	}{
		{"equity defaults to alpaca", domain.AssetClassEquity, domain.VenueAlpaca},
		{"crypto defaults to coinbase", domain.AssetClassCrypto, domain.VenueCoinbase},
		{"unknown asset class is unspecified", domain.AssetClass(""), domain.VenueUnspecified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, domain.DefaultVenue(tt.ac))
		})
	}
}

func TestQualifiedSymbol_String(t *testing.T) {
	t.Run("qualified with venue", func(t *testing.T) {
		qs := domain.QualifiedSymbol{Venue: domain.VenueHyperliquid, Symbol: domain.Symbol("BTC-PERP")}
		assert.Equal(t, "hyperliquid:BTC-PERP", qs.String())
	})

	t.Run("unspecified venue shows bare symbol for legacy log compat", func(t *testing.T) {
		qs := domain.QualifiedSymbol{Symbol: domain.Symbol("AAPL")}
		assert.Equal(t, "AAPL", qs.String())
	})
}

func TestQS_DefaultsVenueFromAssetClass(t *testing.T) {
	sym := domain.Symbol("BTC/USD")

	crypto := domain.QS(sym, domain.AssetClassCrypto)
	assert.Equal(t, domain.VenueCoinbase, crypto.Venue)
	assert.Equal(t, sym, crypto.Symbol)

	equity := domain.QS(domain.Symbol("AAPL"), domain.AssetClassEquity)
	assert.Equal(t, domain.VenueAlpaca, equity.Venue)
}

func TestOrderIntent_ResolvedVenue(t *testing.T) {
	t.Run("explicit venue wins", func(t *testing.T) {
		o := domain.OrderIntent{AssetClass: domain.AssetClassCrypto, Venue: domain.VenueHyperliquid}
		assert.Equal(t, domain.VenueHyperliquid, o.ResolvedVenue())
	})

	t.Run("falls back to default by asset class", func(t *testing.T) {
		equity := domain.OrderIntent{AssetClass: domain.AssetClassEquity}
		assert.Equal(t, domain.VenueAlpaca, equity.ResolvedVenue())

		crypto := domain.OrderIntent{AssetClass: domain.AssetClassCrypto}
		assert.Equal(t, domain.VenueCoinbase, crypto.ResolvedVenue())
	})
}

func TestSymbol_ToSlashFormat(t *testing.T) {
	tests := []struct{ in, want string }{
		{"BTCUSD", "BTC/USD"},
		{"ETHUSD", "ETH/USD"},
		{"BTC/USD", "BTC/USD"}, // idempotent
		{"AAPL", "AAPL"},       // equity unchanged
		{"SPY", "SPY"},          // short symbol unchanged
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := domain.Symbol(tt.in).ToSlashFormat()
			assert.Equal(t, domain.Symbol(tt.want), got)
		})
	}
}

func TestSymbol_ToNoSlashFormat(t *testing.T) {
	tests := []struct{ in, want string }{
		{"BTC/USD", "BTCUSD"},
		{"BTCUSD", "BTCUSD"}, // idempotent
		{"AAPL", "AAPL"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := domain.Symbol(tt.in).ToNoSlashFormat()
			assert.Equal(t, domain.Symbol(tt.want), got)
		})
	}
}

func TestSymbol_IsCryptoSymbol(t *testing.T) {
	assert.True(t, domain.Symbol("BTC/USD").IsCryptoSymbol())
	assert.True(t, domain.Symbol("ETH/USD").IsCryptoSymbol())
	assert.False(t, domain.Symbol("AAPL").IsCryptoSymbol())
	assert.False(t, domain.Symbol("BTCUSD").IsCryptoSymbol()) // no slash = false
}

func TestTimeframe(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		validTimeframes := []string{"1m", "5m", "15m", "1h", "1d"}
		for _, tfStr := range validTimeframes {
			tf, err := domain.NewTimeframe(tfStr)
			require.NoError(t, err)
			assert.Equal(t, tfStr, tf.String())
		}
	})

	t.Run("invalid creation", func(t *testing.T) {
		invalidTimeframes := []string{"", "1s", "1M", "1w", "2m"}
		for _, tfStr := range invalidTimeframes {
			_, err := domain.NewTimeframe(tfStr)
			assert.Error(t, err)
		}
	})
}

func TestRegimeType(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		regime, err := domain.NewRegimeType("TREND")
		require.NoError(t, err)
		assert.Equal(t, domain.RegimeTrend, regime)

		regime, err = domain.NewRegimeType("TREND_UP")
		require.NoError(t, err)
		assert.Equal(t, domain.RegimeTrendUp, regime)

		regime, err = domain.NewRegimeType("TREND_DOWN")
		require.NoError(t, err)
		assert.Equal(t, domain.RegimeTrendDown, regime)

		regime, err = domain.NewRegimeType("BALANCE")
		require.NoError(t, err)
		assert.Equal(t, domain.RegimeBalance, regime)

		regime, err = domain.NewRegimeType("REVERSAL")
		require.NoError(t, err)
		assert.Equal(t, domain.RegimeReversal, regime)
	})

	t.Run("invalid creation", func(t *testing.T) {
		_, err := domain.NewRegimeType("CHOP")
		assert.Error(t, err)

		_, err = domain.NewRegimeType("")
		assert.Error(t, err)
	})

	t.Run("IsTrend helper", func(t *testing.T) {
		assert.True(t, domain.RegimeTrend.IsTrend())
		assert.True(t, domain.RegimeTrendUp.IsTrend())
		assert.True(t, domain.RegimeTrendDown.IsTrend())
		assert.False(t, domain.RegimeBalance.IsTrend())
		assert.False(t, domain.RegimeReversal.IsTrend())
	})
}


func TestNewAssetClass_Valid(t *testing.T) {
	t.Run("EQUITY", func(t *testing.T) {
		assetClass, err := domain.NewAssetClass("EQUITY")
		require.NoError(t, err)
		assert.Equal(t, domain.AssetClassEquity, assetClass)
	})

	t.Run("CRYPTO", func(t *testing.T) {
		assetClass, err := domain.NewAssetClass("CRYPTO")
		require.NoError(t, err)
		assert.Equal(t, domain.AssetClassCrypto, assetClass)
	})
}

func TestNewAssetClass_Invalid(t *testing.T) {
	t.Run("FOREX", func(t *testing.T) {
		_, err := domain.NewAssetClass("FOREX")
		assert.Error(t, err)
	})

	t.Run("empty string", func(t *testing.T) {
		_, err := domain.NewAssetClass("")
		assert.Error(t, err)
	})
}

func TestAssetClass_Is24x7(t *testing.T) {
	t.Run("Crypto is 24x7", func(t *testing.T) {
		assetClass := domain.AssetClassCrypto
		assert.True(t, assetClass.Is24x7())
	})

	t.Run("Equity is not 24x7", func(t *testing.T) {
		assetClass := domain.AssetClassEquity
		assert.False(t, assetClass.Is24x7())
	})
}

func TestAssetClass_SupportsShort(t *testing.T) {
	t.Run("Equity supports short", func(t *testing.T) {
		assetClass := domain.AssetClassEquity
		assert.True(t, assetClass.SupportsShort())
	})

	t.Run("Crypto does not support short", func(t *testing.T) {
		assetClass := domain.AssetClassCrypto
		assert.False(t, assetClass.SupportsShort())
	})
}
