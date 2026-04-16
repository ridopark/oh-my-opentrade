package hyperliquid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func newTestOIAdapter(t *testing.T, handler http.Handler) (*OpenInterestAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := newTestClient(t, srv.URL)
	rest := NewRESTClient(c)
	return NewOpenInterestAdapter(rest, zerolog.Nop()), srv
}

func TestOpenInterestAdapter_Success(t *testing.T) {
	metaResp := `[
		{"universe": [
			{"name":"BTC","szDecimals":5},
			{"name":"ETH","szDecimals":4},
			{"name":"SOL","szDecimals":2}
		]},
		[
			{"funding":"0.00015","markPx":"50000.0","openInterest":"1200.5","oraclePx":"50010","premium":"0.0001"},
			{"funding":"0.00020","markPx":"3000.0","openInterest":"8500.0","oraclePx":"3001","premium":"0.00012"},
			{"funding":"0.00010","markPx":"150.0","openInterest":"50000.0","oraclePx":"150.5","premium":"0.00005"}
		]
	]`

	tests := []struct {
		name      string
		symbol    domain.Symbol
		wantOI    float64
		wantOIUsd float64
		wantMark  float64
	}{
		{
			name:      "BTC",
			symbol:    domain.Symbol("BTC/USD"),
			wantOI:    1200.5,
			wantOIUsd: 60025000.0, // 1200.5 * 50000
			wantMark:  50000.0,
		},
		{
			name:      "ETH",
			symbol:    domain.Symbol("ETH/USD"),
			wantOI:    8500.0,
			wantOIUsd: 25500000.0, // 8500 * 3000
			wantMark:  3000.0,
		},
		{
			name:      "SOL",
			symbol:    domain.Symbol("SOL/USD"),
			wantOI:    50000.0,
			wantOIUsd: 7500000.0, // 50000 * 150
			wantMark:  150.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oia, srv := newTestOIAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(metaResp))
			}))
			defer srv.Close()

			snap, err := oia.OpenInterest(context.Background(), domain.VenueHyperliquid, tt.symbol)
			require.NoError(t, err)

			assert.Equal(t, domain.VenueHyperliquid, snap.Venue)
			assert.Equal(t, tt.symbol, snap.Symbol)
			assert.InDelta(t, tt.wantOI, snap.OI, 0.1)
			assert.InDelta(t, tt.wantOIUsd, snap.OIUsd, 100.0)
			assert.InDelta(t, tt.wantMark, snap.MarkPrice, 0.01)
		})
	}
}

func TestOpenInterestAdapter_NotFound(t *testing.T) {
	metaResp := `[
		{"universe": [{"name":"BTC","szDecimals":5}]},
		[{"funding":"0.00015","markPx":"50000.0","openInterest":"1200","oraclePx":"50010","premium":"0.0001"}]
	]`

	oia, srv := newTestOIAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(metaResp))
	}))
	defer srv.Close()

	_, err := oia.OpenInterest(context.Background(), domain.VenueHyperliquid, domain.Symbol("DOGE/USD"))
	assert.ErrorIs(t, err, ErrAssetNotFound)
}

func TestOpenInterestAdapter_APIError(t *testing.T) {
	oia, srv := newTestOIAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server error`))
	}))
	defer srv.Close()

	_, err := oia.OpenInterest(context.Background(), domain.VenueHyperliquid, domain.Symbol("BTC/USD"))
	assert.Error(t, err)
}
