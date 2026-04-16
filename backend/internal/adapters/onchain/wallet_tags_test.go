package onchain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultWalletTags_ContainsExpectedEntities(t *testing.T) {
	tags := DefaultWalletTags()
	require.NotEmpty(t, tags)

	// Collect all entities present.
	entities := make(map[string]bool)
	for _, wt := range tags {
		entities[wt.Entity] = true
	}

	for _, expected := range []string{"BlackRock", "Fidelity", "Grayscale", "Binance", "Coinbase", "Kraken"} {
		t.Run(expected, func(t *testing.T) {
			assert.True(t, entities[expected], "expected entity %q in default wallet tags", expected)
		})
	}
}

func TestDefaultWalletTags_AddressLookup(t *testing.T) {
	tags := DefaultWalletTags()

	wt, ok := tags["0x example_binance_hot_1"]
	require.True(t, ok, "binance hot wallet should be present")
	assert.Equal(t, "binance_hot", wt.Tag)
	assert.Equal(t, "Binance", wt.Entity)
	assert.Equal(t, CategoryExchangeHot, wt.Category)
	assert.True(t, wt.IsExchange())
}

func TestDefaultWalletTags_CustodianNotExchange(t *testing.T) {
	tags := DefaultWalletTags()

	wt, ok := tags["0x example_blackrock_btc_custody_1"]
	require.True(t, ok)
	assert.Equal(t, CategoryETFCustodian, wt.Category)
	assert.False(t, wt.IsExchange(), "ETF custodian should not be classified as exchange")
}

func TestMergeWalletTags(t *testing.T) {
	base := DefaultWalletTags()
	additional := map[string]WalletTag{
		"0x_custom_1": {Address: "0x_custom_1", Tag: "custom_whale", Entity: "Whale", Category: CategoryMarketMaker},
		// Override an existing entry.
		"0x example_binance_hot_1": {Address: "0x example_binance_hot_1", Tag: "binance_hot_updated", Entity: "Binance", Category: CategoryExchangeHot},
	}

	merged := MergeWalletTags(base, additional)

	// New entry present.
	wt, ok := merged["0x_custom_1"]
	require.True(t, ok)
	assert.Equal(t, "custom_whale", wt.Tag)

	// Override applied.
	wt2, ok := merged["0x example_binance_hot_1"]
	require.True(t, ok)
	assert.Equal(t, "binance_hot_updated", wt2.Tag)

	// Original base entry still present.
	_, ok = merged["0x example_coinbase_hot_1"]
	assert.True(t, ok)
}

func TestWalletTag_Categories(t *testing.T) {
	tests := []struct {
		name       string
		category   string
		isExchange bool
	}{
		{"etf custodian", CategoryETFCustodian, false},
		{"exchange hot", CategoryExchangeHot, true},
		{"exchange cold", CategoryExchangeCold, true},
		{"market maker", CategoryMarketMaker, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wt := WalletTag{Category: tt.category}
			assert.Equal(t, tt.isExchange, wt.IsExchange())
		})
	}
}
