package alpaca

import (
	"testing"

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
	adapter, err := NewAdapter(cfg)

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
	adapter, err := NewAdapter(cfg)

	// Assert
	require.Error(t, err)
	assert.Nil(t, adapter)
}
