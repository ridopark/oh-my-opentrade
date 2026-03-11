package alpaca

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewsClient_GetRecentNews_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1beta1/news", r.URL.Path)
		assert.Equal(t, "AAPL", r.URL.Query().Get("symbols"))
		assert.Equal(t, "5", r.URL.Query().Get("limit"))
		assert.Equal(t, "desc", r.URL.Query().Get("sort"))
		assert.Equal(t, "test-key", r.Header.Get("APCA-API-KEY-ID"))
		assert.Equal(t, "test-secret", r.Header.Get("APCA-API-SECRET-KEY"))

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"news": [
				{
					"id": 12345,
					"headline": "Apple Reports Record Revenue",
					"summary": "Apple Inc reported record Q1 revenue.",
					"source": "reuters",
					"symbols": ["AAPL"],
					"created_at": "2026-03-11T10:00:00Z",
					"updated_at": "2026-03-11T10:05:00Z",
					"url": "https://example.com/apple-revenue"
				},
				{
					"id": 12346,
					"headline": "Tech Sector Rallies",
					"summary": "Broad tech rally led by mega-caps.",
					"source": "bloomberg",
					"symbols": ["AAPL", "MSFT"],
					"created_at": "2026-03-11T09:30:00Z",
					"updated_at": "2026-03-11T09:30:00Z",
					"url": "https://example.com/tech-rally"
				}
			]
		}`))
	}))
	defer server.Close()

	nc := NewNewsClient(server.URL, "test-key", "test-secret", nil)
	items, err := nc.GetRecentNews(context.Background(), "AAPL", 4*time.Hour)
	require.NoError(t, err)
	require.Len(t, items, 2)

	assert.Equal(t, "12345", items[0].ID)
	assert.Equal(t, "Apple Reports Record Revenue", items[0].Headline)
	assert.Equal(t, "Apple Inc reported record Q1 revenue.", items[0].Summary)
	assert.Equal(t, "reuters", items[0].Source)
	assert.Equal(t, []string{"AAPL"}, items[0].Symbols)
	assert.Equal(t, "https://example.com/apple-revenue", items[0].URL)
	assert.False(t, items[0].CreatedAt.IsZero())

	assert.Equal(t, "12346", items[1].ID)
	assert.Equal(t, "bloomberg", items[1].Source)
}

func TestNewsClient_GetRecentNews_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"news": []}`))
	}))
	defer server.Close()

	nc := NewNewsClient(server.URL, "k", "s", nil)
	items, err := nc.GetRecentNews(context.Background(), "AAPL", 4*time.Hour)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestNewsClient_GetRecentNews_CryptoSymbol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "BTCUSD", r.URL.Query().Get("symbols"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"news": []}`))
	}))
	defer server.Close()

	nc := NewNewsClient(server.URL, "k", "s", nil)
	_, err := nc.GetRecentNews(context.Background(), "BTC/USD", 2*time.Hour)
	require.NoError(t, err)
}

func TestNewsClient_GetRecentNews_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	nc := NewNewsClient(server.URL, "k", "s", nil)
	items, err := nc.GetRecentNews(context.Background(), "AAPL", 4*time.Hour)
	assert.NoError(t, err)
	assert.Nil(t, items)
}

func TestNewsClient_GetRecentNews_Cache(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"news": [{"id": 1, "headline": "test", "source": "test", "symbols": ["AAPL"], "created_at": "2026-03-11T10:00:00Z", "updated_at": "2026-03-11T10:00:00Z"}]}`))
	}))
	defer server.Close()

	nc := NewNewsClient(server.URL, "k", "s", nil)

	items1, _ := nc.GetRecentNews(context.Background(), "AAPL", 4*time.Hour)
	items2, _ := nc.GetRecentNews(context.Background(), "AAPL", 4*time.Hour)

	assert.Equal(t, int32(1), callCount.Load())
	assert.Len(t, items1, 1)
	assert.Len(t, items2, 1)
}
