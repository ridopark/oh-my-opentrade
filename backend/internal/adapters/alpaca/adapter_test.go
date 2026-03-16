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
