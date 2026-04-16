package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBinanceAggTrade_TakerBuy(t *testing.T) {
	// m=false means buyer is taker (taker side = buy).
	raw := `{
		"e": "aggTrade",
		"s": "BTCUSDT",
		"p": "60123.45",
		"q": "0.5",
		"m": false,
		"T": 1713182400000
	}`

	trade, err := ParseBinanceAggTrade([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "binance", trade.Venue)
	assert.Equal(t, "BTC/USD", trade.Symbol)
	assert.InDelta(t, 60123.45, trade.Price, 0.001)
	assert.InDelta(t, 0.5, trade.Size, 0.001)
	assert.Equal(t, "buy", trade.TakerSide)
}

func TestParseBinanceAggTrade_TakerSell(t *testing.T) {
	// m=true means buyer is maker, so taker is seller.
	raw := `{
		"e": "aggTrade",
		"s": "ETHUSDT",
		"p": "3200.00",
		"q": "10.0",
		"m": true,
		"T": 1713182400000
	}`

	trade, err := ParseBinanceAggTrade([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "ETH/USD", trade.Symbol)
	assert.Equal(t, "sell", trade.TakerSide)
}

func TestParseBinanceAggTrade_SOL(t *testing.T) {
	raw := `{
		"e": "aggTrade",
		"s": "SOLUSDT",
		"p": "145.67",
		"q": "100",
		"m": false,
		"T": 1713182400000
	}`

	trade, err := ParseBinanceAggTrade([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "SOL/USD", trade.Symbol)
}

func TestNormalizeBinanceSymbol(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"BTCUSDT", "BTC/USD"},
		{"ETHUSDT", "ETH/USD"},
		{"SOLUSDT", "SOL/USD"},
		{"btcusdt", "BTC/USD"},
		{"BTCBUSD", "BTC/USD"},
		{"BTCUSD", "BTC/USD"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeBinanceSymbol(tt.input))
		})
	}
}
