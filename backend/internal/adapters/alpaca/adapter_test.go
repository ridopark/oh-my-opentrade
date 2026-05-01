package alpaca

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func TestAdapter_ImplementsMarketDataPort(t *testing.T) {
	var _ ports.MarketDataPort = (*Adapter)(nil)
}

func TestAdapter_ImplementsBrokerPort(t *testing.T) {
	var _ ports.BrokerPort = (*Adapter)(nil)
}

func TestAdapter_ImplementsQuoteProvider(t *testing.T) {
	var _ execution.QuoteProvider = (*Adapter)(nil)
}

func TestNewAdapter_MissingAPIKey(t *testing.T) {
	// Arrange
	cfg := config.AlpacaConfig{
		APIKeyID:     "",
		APISecretKey: "secret",
		BaseURL:      "https://test",
		DataURL:      "wss://test",
		PaperMode:    true,
	}

	// Act
	adapter, err := NewAdapter(cfg, zerolog.Nop())

	// Assert
	require.Error(t, err)
	assert.Nil(t, adapter)
}

func TestNewAdapter_MissingAPISecret(t *testing.T) {
	// Arrange
	cfg := config.AlpacaConfig{
		APIKeyID:     "key",
		APISecretKey: "",
		BaseURL:      "https://test",
		DataURL:      "wss://test",
		PaperMode:    true,
	}

	// Act
	adapter, err := NewAdapter(cfg, zerolog.Nop())

	// Assert
	require.Error(t, err)
	assert.Nil(t, adapter)
}

func TestWithRateLimit_OverridesBothGlobalAndBackground(t *testing.T) {
	cfg := config.AlpacaConfig{
		APIKeyID:      "k",
		APISecretKey:  "s",
		BaseURL:       "https://paper-api.alpaca.markets",
		DataURL:       "https://data.alpaca.markets",
		CryptoDataURL: "wss://stream.data.alpaca.markets",
		PaperMode:     true,
	}
	a, err := NewAdapter(cfg, zerolog.Nop(), WithNoStream(), WithRateLimit(1000))
	require.NoError(t, err)
	require.NotNil(t, a)
	require.NotNil(t, a.rest)
	require.NotNil(t, a.rest.limiter)
	assert.Equal(t, 1000, a.rest.limiter.LimitPerMin(),
		"WithRateLimit must bump the global cap; default is 200")
	assert.Equal(t, 1000, a.rest.limiter.BackgroundLimitPerMin(),
		"WithRateLimit must also bump the background cap (default 120) so background-priority backfill calls aren't bottlenecked there")
}

func TestWithRateLimit_ZeroFallsBackToDefault(t *testing.T) {
	cfg := config.AlpacaConfig{
		APIKeyID:      "k",
		APISecretKey:  "s",
		BaseURL:       "https://paper-api.alpaca.markets",
		DataURL:       "https://data.alpaca.markets",
		CryptoDataURL: "wss://stream.data.alpaca.markets",
		PaperMode:     true,
	}
	a, err := NewAdapter(cfg, zerolog.Nop(), WithNoStream(), WithRateLimit(0))
	require.NoError(t, err)
	assert.Equal(t, defaultRateLimit, a.rest.limiter.LimitPerMin(),
		"WithRateLimit(0) must keep the default rather than zeroing the limiter")
}

func TestAlpacaWithNoStream_DoesNotPanic(t *testing.T) {
	// Arrange
	cfg := config.AlpacaConfig{
		APIKeyID:      "k",
		APISecretKey:  "s",
		BaseURL:       "https://paper-api.alpaca.markets",
		DataURL:       "https://data.alpaca.markets",
		CryptoDataURL: "wss://stream.data.alpaca.markets",
		PaperMode:     true,
	}

	// Act
	a, err := NewAdapter(cfg, zerolog.Nop(), WithNoStream())

	// Assert
	require.NoError(t, err)
	require.NotNil(t, a)
	// Verify WS clients are nil when WithNoStream is used
	assert.Nil(t, a.ws)
	assert.Nil(t, a.cryptoWs)
	assert.Nil(t, a.tradeStream)
	// REST client should still be initialized
	assert.NotNil(t, a.rest)
}
