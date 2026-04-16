package thetadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_NoAPIKey_ReturnsNil(t *testing.T) {
	c, err := NewClient(Config{}, zerolog.Nop())
	require.NoError(t, err)
	assert.Nil(t, c)
}

func TestNilClient_ReturnsNotConfigured(t *testing.T) {
	var c *Client
	_, err := c.Quote(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, ports.ErrOptionDataNotConfigured)
	_, err = c.Greeks(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, ports.ErrOptionDataNotConfigured)
}

func TestQuote_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/snapshot/option/quote", r.URL.Path)
		assert.Equal(t, "AAPL", r.URL.Query().Get("root"))
		assert.Equal(t, "20260417", r.URL.Query().Get("exp"))
		assert.Equal(t, "150000", r.URL.Query().Get("strike"))
		assert.Equal(t, "C", r.URL.Query().Get("right"))
		assert.Equal(t, "Bearer testkey", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[[1700000000000,5,1.20,7,1.25,1.22]]}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{APIKey: "testkey", BaseURL: srv.URL, RateLimitPerSec: 100}, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()
	c.SetHTTPClient(srv.Client())

	expiry := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	q, err := c.Quote(context.Background(), "AAPL", expiry, 150, "C")
	require.NoError(t, err)
	assert.Equal(t, "AAPL", q.Symbol)
	assert.Equal(t, 1.20, q.Bid)
	assert.Equal(t, 1.25, q.Ask)
	assert.Equal(t, 1.22, q.Last)
	assert.Equal(t, 5, q.BidSize)
	assert.Equal(t, 7, q.AskSize)
	assert.Equal(t, "C", q.Right)
}

func TestGreeks_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/snapshot/option/greeks", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[[1700000000000,0.32,0.55,0.04,-0.08,0.18,0.05,150.5]]}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{APIKey: "k", BaseURL: srv.URL, RateLimitPerSec: 100}, zerolog.Nop())
	require.NoError(t, err)
	defer c.Close()
	c.SetHTTPClient(srv.Client())

	g, err := c.Greeks(context.Background(), "AAPL", time.Now(), 150, "C")
	require.NoError(t, err)
	assert.InDelta(t, 0.32, g.IV, 1e-9)
	assert.InDelta(t, 0.55, g.Delta, 1e-9)
	assert.InDelta(t, 150.5, g.UnderlyingPrice, 1e-9)
}

func TestQuote_AuthErrorMapsToNotConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := NewClient(Config{APIKey: "k", BaseURL: srv.URL, RateLimitPerSec: 100}, zerolog.Nop())
	require.NoError(t, err)
	defer c.Close()
	c.SetHTTPClient(srv.Client())

	_, err = c.Quote(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, ports.ErrOptionDataNotConfigured)
}
