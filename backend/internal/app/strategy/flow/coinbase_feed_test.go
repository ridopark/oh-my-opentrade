package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCoinbaseMatch_MakerBuy(t *testing.T) {
	// Maker side = "buy" means taker is seller.
	raw := `{
		"type": "match",
		"product_id": "BTC-USD",
		"price": "60123.45",
		"size": "0.5",
		"side": "buy",
		"time": "2026-04-15T12:00:00.000000Z"
	}`

	trade, err := ParseCoinbaseMatch([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "coinbase", trade.Venue)
	assert.Equal(t, "BTC/USD", trade.Symbol)
	assert.InDelta(t, 60123.45, trade.Price, 0.001)
	assert.InDelta(t, 0.5, trade.Size, 0.001)
	assert.Equal(t, "sell", trade.TakerSide, "maker=buy -> taker=sell")
}

func TestParseCoinbaseMatch_MakerSell(t *testing.T) {
	// Maker side = "sell" means taker is buyer.
	raw := `{
		"type": "match",
		"product_id": "ETH-USD",
		"price": "3200.00",
		"size": "10",
		"side": "sell",
		"time": "2026-04-15T12:00:00.000000Z"
	}`

	trade, err := ParseCoinbaseMatch([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "ETH/USD", trade.Symbol)
	assert.Equal(t, "buy", trade.TakerSide, "maker=sell -> taker=buy")
}

func TestParseCoinbaseMatch_NonMatchType(t *testing.T) {
	raw := `{
		"type": "subscriptions",
		"channels": []
	}`
	_, err := ParseCoinbaseMatch([]byte(raw))
	assert.Error(t, err)
}

func TestParseCoinbaseMatch_LastMatch(t *testing.T) {
	raw := `{
		"type": "last_match",
		"product_id": "SOL-USD",
		"price": "145.00",
		"size": "100",
		"side": "sell",
		"time": "2026-04-15T12:00:00Z"
	}`

	trade, err := ParseCoinbaseMatch([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "SOL/USD", trade.Symbol)
	assert.Equal(t, "buy", trade.TakerSide) // maker=sell -> taker=buy
}

func TestNormalizeCoinbaseSymbol(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"BTC-USD", "BTC/USD"},
		{"ETH-USD", "ETH/USD"},
		{"SOL-USD", "SOL/USD"},
		{"btc-usd", "BTC/USD"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeCoinbaseSymbol(tt.input))
		})
	}
}
