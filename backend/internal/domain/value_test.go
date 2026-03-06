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
