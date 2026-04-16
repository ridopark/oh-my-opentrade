package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Get_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v5/market/tickers", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[]}}`))
	}))
	defer srv.Close()

	c := NewClientWithHTTP(srv.URL, srv.Client(), zerolog.Nop())
	resp, err := c.get(context.Background(), "/v5/market/tickers")
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 0, resp.RetCode)
}

func TestClient_Get_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"retCode":10001,"retMsg":"params error","result":null}`))
	}))
	defer srv.Close()

	c := NewClientWithHTTP(srv.URL, srv.Client(), zerolog.Nop())
	_, err := c.get(context.Background(), "/v5/market/tickers")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api error")
}

func TestClient_Get_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := NewClientWithHTTP(srv.URL, srv.Client(), zerolog.Nop())
	_, err := c.get(context.Background(), "/v5/market/tickers")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidResponse)
}

func TestClient_Get_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClientWithHTTP(srv.URL, srv.Client(), zerolog.Nop())
	_, err := c.get(context.Background(), "/v5/market/tickers")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 500")
}

func TestToBybitSymbol(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"BTC/USD", "BTCUSDT", false},
		{"ETH/USD", "ETHUSDT", false},
		{"SOL/USD", "SOLUSDT", false},
		{"DOGE/USD", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := toBybitSymbol(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrSymbolNotFound)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
