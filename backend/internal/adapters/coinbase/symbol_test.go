package coinbase

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestToCoinbaseProduct(t *testing.T) {
	cases := []struct {
		name    string
		input   domain.Symbol
		want    string
		wantErr bool
	}{
		{name: "BTC/USD", input: "BTC/USD", want: "BTC-USD"},
		{name: "ETH/USD", input: "ETH/USD", want: "ETH-USD"},
		{name: "SOL/USD", input: "SOL/USD", want: "SOL-USD"},
		{name: "lowercase normalises", input: "btc/usd", want: "BTC-USD"},
		{name: "equity rejected", input: "AAPL", wantErr: true},
		{name: "empty rejected", input: "", wantErr: true},
		{name: "no slash rejected", input: "BTCUSD", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toCoinbaseProduct(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
