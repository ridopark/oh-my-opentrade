//go:build integration

// Package integration provides end-to-end tests that validate the full crypto
// trading pipeline works alongside existing equity functionality.
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}

// newMockAlpacaServer returns an httptest.Server that serves:
//   - GET /v2/positions — returns one equity + one crypto position
//   - POST /v2/orders — accepts any order
//   - GET /v1beta3/crypto/us/bars — returns two BTC/USD bars
//   - GET /v2/stocks/* — returns two AAPL bars
func newMockAlpacaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Crypto historical bars
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta3/crypto/us/bars":
			resp := map[string]interface{}{
				"bars": map[string]interface{}{
					"BTC/USD": []map[string]interface{}{
						{"t": "2025-03-05T12:00:00Z", "o": 60000.0, "h": 61000.0, "l": 59500.0, "c": 60500.0, "v": 1.5},
						{"t": "2025-03-05T12:01:00Z", "o": 60500.0, "h": 60800.0, "l": 60200.0, "c": 60600.0, "v": 2.3},
					},
				},
				"next_page_token": nil,
			}
			json.NewEncoder(w).Encode(resp)

		// Equity historical bars
		case r.Method == http.MethodGet && len(r.URL.Path) > len("/v2/stocks/") && r.URL.Path != "/v2/positions":
			resp := map[string]interface{}{
				"bars": []map[string]interface{}{
					{"t": "2025-03-05T12:00:00Z", "o": 170.0, "h": 172.0, "l": 169.0, "c": 171.0, "v": 1000},
					{"t": "2025-03-05T12:01:00Z", "o": 171.0, "h": 173.0, "l": 170.0, "c": 172.0, "v": 1200},
				},
				"next_page_token": nil,
			}
			json.NewEncoder(w).Encode(resp)

		// Positions — returns one equity + one crypto position
		case r.Method == http.MethodGet && r.URL.Path == "/v2/positions":
			positions := []map[string]interface{}{
				{
					"symbol":          "AAPL",
					"qty":             "10",
					"side":            "long",
					"avg_entry_price": "170.50",
					"current_price":   "172.00",
					"asset_class":     "us_equity",
				},
				{
					"symbol":          "BTCUSD",
					"qty":             "0.5",
					"side":            "long",
					"avg_entry_price": "60000.00",
					"current_price":   "60500.00",
					"asset_class":     "crypto",
				},
			}
			json.NewEncoder(w).Encode(positions)

		// Submit order — accept any
		case r.Method == http.MethodPost && r.URL.Path == "/v2/orders":
			resp := map[string]string{
				"id":     "test-order-id-001",
				"status": "accepted",
			}
			json.NewEncoder(w).Encode(resp)

		default:
			http.NotFound(w, r)
		}
	}))
}

// ---------------------------------------------------------------------------
// E2E Tests
// ---------------------------------------------------------------------------

// TestCryptoE2E_ConfigLoad validates that a mixed equity/crypto config loads
// correctly and SymbolsByAssetClass returns the expected symbols.
func TestCryptoE2E_ConfigLoad(t *testing.T) {
	tmpDir := t.TempDir()

	envContent := `APCA_API_KEY_ID=test-key
APCA_API_SECRET_KEY=test-secret
TIMESCALEDB_PASSWORD=test-pass`
	writeFile(t, tmpDir, ".env", envContent)

	yamlContent := `alpaca:
  base_url: https://paper-api.alpaca.markets
  data_url: https://data.alpaca.markets
  paper_mode: true
  crypto_data_url: https://data.alpaca.markets
  crypto_feed: us

database:
  host: localhost
  port: 5432
  user: opentrade
  dbname: opentrade
  ssl_mode: disable
  max_pool_size: 10

trading:
  max_risk_percent: 2.0
  default_slippage_bps: 10
  kill_switch_max_stops: 3
  kill_switch_window: 2m
  kill_switch_halt_duration: 15m

symbols:
  timeframe: 1m
  groups:
    - name: equities
      asset_class: EQUITY
      timeframe: 1m
      symbols: [AAPL, MSFT, GOOGL]
    - name: crypto
      asset_class: CRYPTO
      timeframe: 1m
      symbols: [BTC/USD, ETH/USD]

server:
  port: 8080
  log_level: info`
	yamlPath := writeFile(t, tmpDir, "config.yaml", yamlContent)

	// Load config
	cfg, err := config.Load(filepath.Join(tmpDir, ".env"), yamlPath)
	require.NoError(t, err)

	// Normalize (populates flat Symbols list from groups)
	cfg.Symbols.Normalize()

	// Verify symbol groups
	equitySyms := cfg.Symbols.SymbolsByAssetClass("EQUITY")
	cryptoSyms := cfg.Symbols.SymbolsByAssetClass("CRYPTO")

	assert.Len(t, equitySyms, 3, "should have 3 equity symbols")
	assert.Len(t, cryptoSyms, 2, "should have 2 crypto symbols")
	assert.Contains(t, cryptoSyms, "BTC/USD")
	assert.Contains(t, cryptoSyms, "ETH/USD")

	// Verify flat list contains all symbols (backward compat)
	allSymbols := cfg.Symbols.AllSymbols()
	assert.Len(t, allSymbols, 5, "AllSymbols should return all 5 symbols")

	// Verify crypto config
	assert.Equal(t, "us", cfg.Alpaca.CryptoFeed)
	assert.NotEmpty(t, cfg.Alpaca.CryptoDataURL)
}

// TestCryptoE2E_HistoricalBars validates that the adapter correctly fetches
// crypto historical bars via the crypto REST endpoint.
func TestCryptoE2E_HistoricalBars(t *testing.T) {
	mockServer := newMockAlpacaServer(t)
	defer mockServer.Close()

	cfg := config.AlpacaConfig{
		APIKeyID:      "test-key",
		APISecretKey:  "test-secret",
		BaseURL:       mockServer.URL,
		DataURL:       mockServer.URL,
		PaperMode:     true,
		CryptoDataURL: mockServer.URL,
		CryptoFeed:    "us",
	}

	adapter, err := alpaca.NewAdapter(cfg, zerolog.Nop())
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	sym, err := domain.NewSymbol("BTC/USD")
	require.NoError(t, err)
	tf, err := domain.NewTimeframe("1m")
	require.NoError(t, err)

	from := time.Date(2025, 3, 5, 12, 0, 0, 0, time.UTC)
	to := time.Date(2025, 3, 5, 12, 5, 0, 0, time.UTC)

	bars, err := adapter.GetHistoricalBars(ctx, sym, tf, from, to)
	require.NoError(t, err)
	assert.Len(t, bars, 2, "should return 2 crypto bars")
	assert.Equal(t, 60500.0, bars[0].Close)
	assert.Equal(t, 60600.0, bars[1].Close)
}

// TestCryptoE2E_EquityHistoricalBars validates that equity bar fetching still
// works correctly alongside crypto (no regression).
func TestCryptoE2E_EquityHistoricalBars(t *testing.T) {
	mockServer := newMockAlpacaServer(t)
	defer mockServer.Close()

	cfg := config.AlpacaConfig{
		APIKeyID:      "test-key",
		APISecretKey:  "test-secret",
		BaseURL:       mockServer.URL,
		DataURL:       mockServer.URL,
		PaperMode:     true,
		CryptoDataURL: mockServer.URL,
		CryptoFeed:    "us",
	}

	adapter, err := alpaca.NewAdapter(cfg, zerolog.Nop())
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	tf, err := domain.NewTimeframe("1m")
	require.NoError(t, err)

	from := time.Date(2025, 3, 5, 12, 0, 0, 0, time.UTC)
	to := time.Date(2025, 3, 5, 12, 5, 0, 0, time.UTC)

	bars, err := adapter.GetHistoricalBars(ctx, sym, tf, from, to)
	require.NoError(t, err)
	assert.Len(t, bars, 2, "should return 2 equity bars")
	assert.Equal(t, 171.0, bars[0].Close)
}

// TestCryptoE2E_OrderSubmit validates that:
//   - Crypto LONG orders are accepted
//   - Crypto SHORT orders are rejected
//   - Equity orders still work (regression check)
func TestCryptoE2E_OrderSubmit(t *testing.T) {
	mockServer := newMockAlpacaServer(t)
	defer mockServer.Close()

	cfg := config.AlpacaConfig{
		APIKeyID:      "test-key",
		APISecretKey:  "test-secret",
		BaseURL:       mockServer.URL,
		DataURL:       mockServer.URL,
		PaperMode:     true,
		CryptoDataURL: mockServer.URL,
		CryptoFeed:    "us",
	}

	adapter, err := alpaca.NewAdapter(cfg, zerolog.Nop())
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	btcSym, _ := domain.NewSymbol("BTC/USD")
	aaplSym, _ := domain.NewSymbol("AAPL")

	t.Run("crypto long order accepted", func(t *testing.T) {
		intent := domain.OrderIntent{
			Symbol:     btcSym,
			Direction:  domain.DirectionLong,
			Quantity:   0.1,
			AssetClass: domain.AssetClassCrypto,
		}
		orderID, err := adapter.SubmitOrder(ctx, intent)
		require.NoError(t, err)
		assert.NotEmpty(t, orderID, "should return order ID for crypto long")
	})

	t.Run("crypto short order rejected", func(t *testing.T) {
		intent := domain.OrderIntent{
			Symbol:     btcSym,
			Direction:  domain.DirectionShort,
			Quantity:   0.1,
			AssetClass: domain.AssetClassCrypto,
		}
		_, err := adapter.SubmitOrder(ctx, intent)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "short selling")
	})

	t.Run("equity order still works", func(t *testing.T) {
		intent := domain.OrderIntent{
			Symbol:     aaplSym,
			Direction:  domain.DirectionLong,
			Quantity:   10,
			AssetClass: domain.AssetClassEquity,
		}
		orderID, err := adapter.SubmitOrder(ctx, intent)
		require.NoError(t, err)
		assert.NotEmpty(t, orderID, "equity order should still be accepted")
	})
}

// TestCryptoE2E_PositionNormalization validates that positions from Alpaca have
// their crypto symbols normalized (BTCUSD → BTC/USD) and asset class set.
func TestCryptoE2E_PositionNormalization(t *testing.T) {
	mockServer := newMockAlpacaServer(t)
	defer mockServer.Close()

	cfg := config.AlpacaConfig{
		APIKeyID:      "test-key",
		APISecretKey:  "test-secret",
		BaseURL:       mockServer.URL,
		DataURL:       mockServer.URL,
		PaperMode:     true,
		CryptoDataURL: mockServer.URL,
		CryptoFeed:    "us",
	}

	adapter, err := alpaca.NewAdapter(cfg, zerolog.Nop())
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	trades, err := adapter.GetPositions(ctx, "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	require.Len(t, trades, 2, "should return both equity and crypto positions")

	// Find the crypto position
	var cryptoTrade, equityTrade *domain.Trade
	for i := range trades {
		if trades[i].Symbol.IsCryptoSymbol() {
			cryptoTrade = &trades[i]
		} else {
			equityTrade = &trades[i]
		}
	}

	require.NotNil(t, cryptoTrade, "should have a crypto position")
	require.NotNil(t, equityTrade, "should have an equity position")

	// Crypto position should have normalized symbol (BTC/USD, not BTCUSD)
	assert.Equal(t, domain.Symbol("BTC/USD"), cryptoTrade.Symbol,
		"crypto symbol should be normalized to slash format")
	assert.Equal(t, domain.AssetClassCrypto, cryptoTrade.AssetClass,
		"crypto position should have CRYPTO asset class")
	assert.Equal(t, 0.5, cryptoTrade.Quantity)

	// Equity position should remain unchanged
	assert.Equal(t, domain.Symbol("AAPL"), equityTrade.Symbol)
	assert.Equal(t, domain.AssetClassEquity, equityTrade.AssetClass)
	assert.Equal(t, 10.0, equityTrade.Quantity)
}

// TestCryptoE2E_DomainLogic validates pure domain logic for crypto support:
// symbol normalization, asset class helpers, and calendar behavior.
func TestCryptoE2E_DomainLogic(t *testing.T) {
	t.Run("symbol normalization round-trip", func(t *testing.T) {
		// Slash → compact → slash
		sym, err := domain.NewSymbol("BTC/USD")
		require.NoError(t, err)
		assert.True(t, sym.IsCryptoSymbol())

		compact := sym.ToNoSlashFormat()
		assert.Equal(t, domain.Symbol("BTCUSD"), compact)
		assert.False(t, compact.IsCryptoSymbol())

		slashed := compact.ToSlashFormat()
		assert.Equal(t, domain.Symbol("BTC/USD"), slashed)
		assert.True(t, slashed.IsCryptoSymbol())
	})

	t.Run("asset class properties", func(t *testing.T) {
		assert.True(t, domain.AssetClassCrypto.Is24x7(), "crypto should be 24x7")
		assert.False(t, domain.AssetClassCrypto.SupportsShort(), "crypto should not support short")
		assert.False(t, domain.AssetClassEquity.Is24x7(), "equity should not be 24x7")
		assert.True(t, domain.AssetClassEquity.SupportsShort(), "equity should support short")
	})

	t.Run("trading calendar", func(t *testing.T) {
		cryptoCal := domain.CalendarFor(domain.AssetClassCrypto)
		nyseCal := domain.CalendarFor(domain.AssetClassEquity)

		// Crypto is always open, even on Saturday
		saturday := time.Date(2025, 3, 8, 12, 0, 0, 0, time.UTC) // Saturday
		assert.True(t, cryptoCal.IsOpen(saturday), "crypto should be open on Saturday")
		assert.False(t, nyseCal.IsOpen(saturday), "NYSE should not be open on Saturday")

		// Crypto is always open — verify a weekday too
		wedNight := time.Date(2025, 3, 5, 3, 0, 0, 0, time.UTC) // 3 AM UTC Wednesday
		assert.True(t, cryptoCal.IsOpen(wedNight), "crypto should be open at 3 AM UTC")
	})

	t.Run("equity symbol detection", func(t *testing.T) {
		aaplSym, _ := domain.NewSymbol("AAPL")
		assert.False(t, aaplSym.IsCryptoSymbol())

		spySym, _ := domain.NewSymbol("SPY")
		assert.False(t, spySym.IsCryptoSymbol())
	})
}
