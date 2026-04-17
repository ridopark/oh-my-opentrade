package coinbase

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestToCoinbaseGranularity(t *testing.T) {
	cases := []struct {
		name    string
		input   domain.Timeframe
		want    int
		wantErr bool
	}{
		{name: "1m", input: "1m", want: 60},
		{name: "5m", input: "5m", want: 300},
		{name: "15m", input: "15m", want: 900},
		{name: "1h", input: "1h", want: 3600},
		{name: "6h", input: "6h", want: 21600},
		{name: "1d", input: "1d", want: 86400},
		{name: "30m rejected (not a coinbase bucket)", input: "30m", wantErr: true},
		{name: "2h rejected", input: "2h", wantErr: true},
		{name: "empty rejected", input: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toCoinbaseGranularity(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
