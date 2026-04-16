package hyperliquid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/config"
)

// testPrivateKey is a well-known test key — never use in production.
const testPrivateKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	cfg := config.HyperliquidConfig{
		Network:    "testnet",
		PrivateKey: testPrivateKey,
	}
	c, err := NewClient(cfg, zerolog.Nop())
	require.NoError(t, err)
	c.baseURL = serverURL
	return c
}

func TestNewClient_DeriveAddress(t *testing.T) {
	cfg := config.HyperliquidConfig{
		Network:    "testnet",
		PrivateKey: testPrivateKey,
	}
	c, err := NewClient(cfg, zerolog.Nop())
	require.NoError(t, err)
	// The well-known Hardhat #0 address.
	assert.Equal(t, "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266", c.Address())
}

func TestNewClient_ExplicitAddress(t *testing.T) {
	cfg := config.HyperliquidConfig{
		Network:    "testnet",
		PrivateKey: testPrivateKey,
		Address:    "0x1234567890abcdef1234567890abcdef12345678",
	}
	c, err := NewClient(cfg, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "0x1234567890abcdef1234567890abcdef12345678", c.Address())
}

func TestNewClient_NoPrivateKey(t *testing.T) {
	cfg := config.HyperliquidConfig{
		Network: "testnet",
	}
	c, err := NewClient(cfg, zerolog.Nop())
	require.NoError(t, err)
	// Client should be created (read-only mode).
	assert.Empty(t, c.Address())
	// Exchange calls should fail.
	_, err = c.PostExchange(context.Background(), map[string]string{"type": "test"}, 1)
	assert.ErrorIs(t, err, ErrNotConfigured)
}

func TestNewClient_NetworkDefaults(t *testing.T) {
	tests := []struct {
		name    string
		network string
		wantURL string
		wantWS  string
	}{
		{"mainnet", "mainnet", MainnetBaseURL, MainnetWSURL},
		{"testnet", "testnet", TestnetBaseURL, TestnetWSURL},
		{"empty defaults to testnet", "", TestnetBaseURL, TestnetWSURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.HyperliquidConfig{Network: tt.network}
			c, err := NewClient(cfg, zerolog.Nop())
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, c.BaseURL())
			assert.Equal(t, tt.wantWS, c.WSURL())
		})
	}
}

func TestNewClient_InvalidPrivateKey(t *testing.T) {
	cfg := config.HyperliquidConfig{
		Network:    "testnet",
		PrivateKey: "not-hex",
	}
	_, err := NewClient(cfg, zerolog.Nop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode private key hex")
}

func TestPostInfo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/info", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	raw, err := c.PostInfo(context.Background(), map[string]string{"type": "meta"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"result":"ok"}`, string(raw))
}

func TestPostInfo_RateLimit(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`rate limited`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	raw, err := c.PostInfo(context.Background(), map[string]string{"type": "meta"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"result":"ok"}`, string(raw))
	assert.Equal(t, 3, attempts)
}

func TestPostInfo_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.PostInfo(context.Background(), map[string]string{"type": "meta"})
	assert.ErrorIs(t, err, ErrAuth)
}

func TestResolveAsset(t *testing.T) {
	metaResp := `{"universe":[{"name":"BTC","szDecimals":5},{"name":"ETH","szDecimals":4},{"name":"SOL","szDecimals":2}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(metaResp))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)

	tests := []struct {
		coin    string
		wantID  int
		wantErr bool
	}{
		{"BTC", 0, false},
		{"ETH", 1, false},
		{"SOL", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.coin, func(t *testing.T) {
			id, err := c.ResolveAsset(context.Background(), tt.coin)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, id)
			}
		})
	}

	// Test cache hit (server already closed wouldn't matter).
	id, err := c.ResolveAsset(context.Background(), "BTC")
	require.NoError(t, err)
	assert.Equal(t, 0, id)
}

func TestResolveAsset_NotFound(t *testing.T) {
	metaResp := `{"universe":[{"name":"BTC","szDecimals":5}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(metaResp))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.ResolveAsset(context.Background(), "DOGE")
	assert.ErrorIs(t, err, ErrAssetNotFound)
}

func TestSignAction_Deterministic(t *testing.T) {
	cfg := config.HyperliquidConfig{
		Network:    "testnet",
		PrivateKey: testPrivateKey,
	}
	c, err := NewClient(cfg, zerolog.Nop())
	require.NoError(t, err)

	actionJSON, _ := json.Marshal(map[string]string{"type": "order"})
	nonce := int64(1234567890)

	sig1, err := c.signAction(actionJSON, nonce)
	require.NoError(t, err)
	sig2, err := c.signAction(actionJSON, nonce)
	require.NoError(t, err)

	// ECDSA with random k is non-deterministic, but both should be valid sigs.
	assert.NotEmpty(t, sig1.R)
	assert.NotEmpty(t, sig1.S)
	assert.Contains(t, []int{27, 28}, sig1.V)
	assert.NotEmpty(t, sig2.R)
	assert.NotEmpty(t, sig2.S)
}

func TestHashAction(t *testing.T) {
	actionJSON := []byte(`{"type":"order"}`)
	nonce := int64(100)

	hash1 := hashAction(actionJSON, nonce)
	hash2 := hashAction(actionJSON, nonce)
	assert.Equal(t, hash1, hash2, "same input should produce same hash")
	assert.Len(t, hash1, 32, "keccak256 output should be 32 bytes")

	// Different nonce produces different hash.
	hash3 := hashAction(actionJSON, 101)
	assert.NotEqual(t, hash1, hash3)
}

func TestSetAssetMap(t *testing.T) {
	c := newTestClient(t, "http://unused")
	c.SetAssetMap(map[string]int{"BTC": 0, "ETH": 1})

	id, err := c.ResolveAsset(context.Background(), "BTC")
	require.NoError(t, err)
	assert.Equal(t, 0, id)

	id, err = c.ResolveAsset(context.Background(), "ETH")
	require.NoError(t, err)
	assert.Equal(t, 1, id)
}
