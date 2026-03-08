package alpaca

import (
	"testing"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCryptoBarToMarketBar(t *testing.T) {
	ts := time.Date(2025, 3, 5, 12, 0, 0, 0, time.UTC)
	cb := alpacastream.CryptoBar{
		Symbol:    "BTC/USD",
		Open:      60000.0,
		High:      61000.0,
		Low:       59500.0,
		Close:     60500.0,
		Volume:    1.5,
		Timestamp: ts,
	}

	bar, err := CryptoBarToMarketBar(cb)
	require.NoError(t, err)

	assert.Equal(t, "BTC/USD", bar.Symbol.String())
	assert.Equal(t, "1m", bar.Timeframe.String())
	assert.Equal(t, ts, bar.Time)
	assert.Equal(t, 60000.0, bar.Open)
	assert.Equal(t, 61000.0, bar.High)
	assert.Equal(t, 59500.0, bar.Low)
	assert.Equal(t, 60500.0, bar.Close)
	assert.Equal(t, 1.5, bar.Volume)
}

func TestCryptoBarToMarketBar_NormalizesSymbol(t *testing.T) {
	cb := alpacastream.CryptoBar{
		Symbol:    "BTCUSD",
		Open:      60000.0,
		High:      61000.0,
		Low:       59500.0,
		Close:     60500.0,
		Volume:    1.5,
		Timestamp: time.Now(),
	}

	bar, err := CryptoBarToMarketBar(cb)
	require.NoError(t, err)
	assert.Equal(t, "BTC/USD", bar.Symbol.String())
}

func TestCryptoBarToMarketBar_ZeroVolume(t *testing.T) {
	cb := alpacastream.CryptoBar{
		Symbol:    "BTC/USD",
		Open:      60000.0,
		High:      61000.0,
		Low:       59500.0,
		Close:     60500.0,
		Volume:    0,
		Timestamp: time.Now(),
	}

	bar, err := CryptoBarToMarketBar(cb)
	require.NoError(t, err, "zero volume is valid for crypto idle bars")
	assert.Equal(t, 0.0, bar.Volume)
}

func TestNewCryptoWSClient_RequiresCredentials(t *testing.T) {
	_, err := NewCryptoWSClient("wss://test", "", "secret", "us", nil, zerolog.Nop())
	assert.ErrorIs(t, err, ErrCryptoWSMissingCredentials)

	_, err = NewCryptoWSClient("wss://test", "key", "", "us", nil, zerolog.Nop())
	assert.ErrorIs(t, err, ErrCryptoWSMissingCredentials)
}

func TestNewCryptoWSClient_DefaultFeed(t *testing.T) {
	client, err := NewCryptoWSClient("wss://test", "key", "secret", "", nil, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "us", client.feed)
}

func TestNewCryptoWSClient_DefaultURL(t *testing.T) {
	client, err := NewCryptoWSClient("", "key", "secret", "us", nil, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "wss://stream.data.alpaca.markets", client.cryptoDataURL)
}
