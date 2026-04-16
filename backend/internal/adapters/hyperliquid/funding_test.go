package hyperliquid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func newTestFundingAdapter(t *testing.T, handler http.Handler) (*FundingAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := newTestClient(t, srv.URL)
	rest := NewRESTClient(c)
	ws := NewWSSubscriber(TestnetWSURL, c.Address(), zerolog.Nop())
	return NewFundingAdapter(c, rest, ws, zerolog.Nop()), srv
}

func TestFundingAdapter_Latest(t *testing.T) {
	metaResp := `[
		{"universe": [{"name":"BTC","szDecimals":5},{"name":"ETH","szDecimals":4}]},
		[
			{"funding":"0.00015","markPx":"50000.0","openInterest":"1200","oraclePx":"50010","premium":"0.0001"},
			{"funding":"0.00020","markPx":"3000.0","openInterest":"8500","oraclePx":"3001","premium":"0.00012"}
		]
	]`

	fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(metaResp))
	}))
	defer srv.Close()

	t.Run("BTC", func(t *testing.T) {
		fr, err := fa.Latest(context.Background(), domain.VenueHyperliquid, domain.Symbol("BTC/USD"))
		require.NoError(t, err)
		assert.Equal(t, domain.VenueHyperliquid, fr.Venue)
		assert.Equal(t, domain.Symbol("BTC/USD"), fr.Symbol)
		assert.InDelta(t, 0.00015, fr.Rate, 1e-8)
		assert.InDelta(t, 50000.0, fr.MarkPrice, 0.01)
		assert.Equal(t, 1, fr.IntervalHours)
	})

	t.Run("ETH", func(t *testing.T) {
		fr, err := fa.Latest(context.Background(), domain.VenueHyperliquid, domain.Symbol("ETH/USD"))
		require.NoError(t, err)
		assert.InDelta(t, 0.00020, fr.Rate, 1e-8)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := fa.Latest(context.Background(), domain.VenueHyperliquid, domain.Symbol("DOGE/USD"))
		assert.ErrorIs(t, err, ErrAssetNotFound)
	})
}

func TestFundingAdapter_History(t *testing.T) {
	fundingResp := `[
		{"time":1700000000000,"coin":"BTC","usds":"5.25","hash":"0x1","delta":{"type":"funding","coin":"BTC","usds":"5.25","fundingRate":"0.00015"}},
		{"time":1700028800000,"coin":"BTC","usds":"3.10","hash":"0x2","delta":{"type":"funding","coin":"BTC","usds":"3.10","fundingRate":"0.00012"}},
		{"time":1700014400000,"coin":"ETH","usds":"1.00","hash":"0x3","delta":{"type":"funding","coin":"ETH","usds":"1.00","fundingRate":"0.00020"}}
	]`

	fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fundingResp))
	}))
	defer srv.Close()

	from := time.UnixMilli(1699999000000)
	to := time.UnixMilli(1700100000000)

	t.Run("BTC only", func(t *testing.T) {
		rates, err := fa.History(context.Background(), domain.VenueHyperliquid, domain.Symbol("BTC/USD"), from, to)
		require.NoError(t, err)
		require.Len(t, rates, 2)
		assert.InDelta(t, 0.00015, rates[0].Rate, 1e-8)
		assert.InDelta(t, 0.00012, rates[1].Rate, 1e-8)
		assert.Equal(t, 1, rates[0].IntervalHours)
	})

	t.Run("ETH only", func(t *testing.T) {
		rates, err := fa.History(context.Background(), domain.VenueHyperliquid, domain.Symbol("ETH/USD"), from, to)
		require.NoError(t, err)
		require.Len(t, rates, 1)
		assert.InDelta(t, 0.00020, rates[0].Rate, 1e-8)
	})

	t.Run("no matches", func(t *testing.T) {
		rates, err := fa.History(context.Background(), domain.VenueHyperliquid, domain.Symbol("SOL/USD"), from, to)
		require.NoError(t, err)
		assert.Empty(t, rates)
	})
}

func TestNextFundingTime(t *testing.T) {
	next := nextFundingTime()
	now := time.Now().UTC()

	// Next funding should be in the future.
	assert.True(t, next.After(now) || next.Equal(now))

	// Hyperliquid uses 1-hour funding intervals, so next should be within 1h.
	assert.True(t, next.Sub(now) <= 1*time.Hour+time.Minute)

	// Should be exactly on the hour.
	assert.Equal(t, 0, next.Minute())
	assert.Equal(t, 0, next.Second())
}
