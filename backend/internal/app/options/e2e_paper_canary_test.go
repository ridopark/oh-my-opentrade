package options_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	alpacaadapter "github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_PaperCanary_LongCallOptionTrade(t *testing.T) {
	now := time.Date(2025, 3, 2, 12, 0, 0, 0, time.UTC)
	expiry := now.AddDate(0, 0, 40) // 40 DTE — within [35, 45] window

	occSymbol := domain.FormatOCCSymbol("AAPL", expiry, domain.OptionRightCall, 190.0)
	occ200 := domain.FormatOCCSymbol("AAPL", expiry, domain.OptionRightCall, 200.0)
	occ185 := domain.FormatOCCSymbol("AAPL", expiry, domain.OptionRightCall, 185.0)

	// Broker API: contract listing + order submission
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v2/options/contracts" {
			payload := map[string]interface{}{
				"option_contracts": []map[string]interface{}{
					{
						"symbol": occSymbol, "underlying_symbol": "AAPL",
						"expiration_date": expiry.Format("2006-01-02"), "strike_price": "190.00",
						"type": "call", "style": "american", "multiplier": "100",
						"open_interest": "500", "tradable": true, "status": "active",
					},
					{
						"symbol": occ200, "underlying_symbol": "AAPL",
						"expiration_date": expiry.Format("2006-01-02"), "strike_price": "200.00",
						"type": "call", "style": "american", "multiplier": "100",
						"open_interest": "300", "tradable": true, "status": "active",
					},
					{
						"symbol": occ185, "underlying_symbol": "AAPL",
						"expiration_date": expiry.Format("2006-01-02"), "strike_price": "185.00",
						"type": "call", "style": "american", "multiplier": "100",
						"open_interest": "50", "tradable": true, "status": "active",
					},
				},
				"next_page_token": nil,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(payload)
			return
		}

		if r.Method == http.MethodPost && r.URL.Path == "/v2/orders" {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, occSymbol, body["symbol"])
			assert.Equal(t, "buy", body["side"])
			assert.Equal(t, "limit", body["type"])
			assert.Equal(t, "day", body["time_in_force"])
			_, hasStopPrice := body["stop_price"]
			assert.False(t, hasStopPrice, "option orders must NOT include stop_price")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"paper-order-abc123"}`))
			return
		}

		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer brokerServer.Close()

	// Data API: option snapshots with greeks
	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta1/options/snapshots" {
			payload := map[string]interface{}{
				"snapshots": map[string]interface{}{
					occSymbol: map[string]interface{}{
						"greeks":            map[string]interface{}{"delta": 0.48, "gamma": 0.03, "theta": -0.10, "vega": 0.15, "rho": 0.02},
						"impliedVolatility": 0.30,
						"latestQuote":       map[string]interface{}{"bp": 3.20, "ap": 3.30, "c": 3.25},
						"openInterest":      500,
					},
					occ200: map[string]interface{}{
						"greeks":            map[string]interface{}{"delta": 0.35, "gamma": 0.02, "theta": -0.08, "vega": 0.12, "rho": 0.01},
						"impliedVolatility": 0.28,
						"latestQuote":       map[string]interface{}{"bp": 1.50, "ap": 1.60, "c": 1.55},
						"openInterest":      300,
					},
					occ185: map[string]interface{}{
						"greeks":            map[string]interface{}{"delta": 0.60, "gamma": 0.04, "theta": -0.12, "vega": 0.18, "rho": 0.03},
						"impliedVolatility": 0.32,
						"latestQuote":       map[string]interface{}{"bp": 5.00, "ap": 5.20, "c": 5.10},
						"openInterest":      50,
					},
				},
				"next_page_token": nil,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		http.Error(w, "unexpected data request", http.StatusInternalServerError)
	}))
	defer dataServer.Close()

	adapter, err := alpacaadapter.NewAdapter(config.AlpacaConfig{
		BaseURL:      brokerServer.URL,
		DataURL:      dataServer.URL,
		APIKeyID:     "test-key",
		APISecretKey: "test-secret",
	}, zerolog.Nop())
	require.NoError(t, err)

	ctx := context.Background()
	chain, err := adapter.GetOptionChain(ctx, domain.Symbol("AAPL"), expiry, domain.OptionRightCall)
	require.NoError(t, err)
	assert.Len(t, chain, 3, "adapter should parse all 3 contracts with snapshots")

	constraints := options.ContractSelectionConstraints{
		MinDTE:          35,
		MaxDTE:          45,
		TargetDeltaLow:  0.40,
		TargetDeltaHigh: 0.55,
		MinOpenInterest: 100,
		MaxSpreadPct:    0.10,
		MaxIV:           1.0,
	}
	selSvc := options.NewContractSelectionService(constraints, func() time.Time { return now })

	selected, err := selSvc.SelectBestContract(domain.DirectionLong, domain.RegimeTrend, chain)
	require.NoError(t, err)
	assert.Equal(t, domain.Symbol(occSymbol), selected.ContractSymbol,
		"should select the contract with delta closest to 0.475")

	instrument, err := domain.NewInstrument(domain.InstrumentTypeOption, occSymbol, "AAPL")
	require.NoError(t, err)

	contracts := 1.0
	premium := (selected.OptionQuote.Bid + selected.OptionQuote.Ask) / 2.0
	maxLoss := premium * 100 * contracts // $325

	intentID := uuid.New()
	intent, err := domain.NewOptionOrderIntent(
		intentID,
		"tenant-paper",
		domain.EnvModePaper,
		instrument,
		domain.DirectionLong,
		selected.OptionQuote.Ask,
		contracts,
		"options-canary",
		"E2E canary test",
		0.85,
		intentID.String(),
		maxLoss,
	)
	require.NoError(t, err)

	accountEquity := 20_000.0 // $325 / $20,000 = 1.625% < 2% limit

	riskEngine := execution.NewOptionsRiskEngine(
		0.02,
		100,
		0.10,
		1.0,
		35,
		func() time.Time { return now },
	)

	err = riskEngine.ValidateOptionIntent(intent, accountEquity)
	require.NoError(t, err, "risk engine should approve the intent")

	err = riskEngine.ValidateOptionLiquidity(selected)
	require.NoError(t, err, "liquidity check should pass")

	err = riskEngine.ValidateOptionExpiry(selected.OptionContract, 35)
	require.NoError(t, err, "expiry check should pass (DTE=40 >= minDTE=35)")

	orderID, err := adapter.SubmitOrder(ctx, intent)
	require.NoError(t, err)
	assert.Equal(t, "paper-order-abc123", orderID)
}

func TestE2E_PaperCanary_ShortAcceptedAtSelectionLayer(t *testing.T) {
	now := time.Date(2025, 3, 2, 12, 0, 0, 0, time.UTC)
	expiry := now.AddDate(0, 0, 40)

	chain := []domain.OptionContractSnapshot{
		{
			OptionContract: domain.OptionContract{
				ContractSymbol: domain.Symbol(domain.FormatOCCSymbol("AAPL", expiry, domain.OptionRightPut, 190.0)),
				Underlying:     "AAPL",
				Expiry:         expiry,
				Strike:         190.0,
				Right:          domain.OptionRightPut,
				Style:          domain.OptionStyleAmerican,
				Multiplier:     100,
			},
			Greeks:       domain.Greeks{Delta: -0.48, IV: 0.30},
			OptionQuote:  domain.OptionQuote{Bid: 3.20, Ask: 3.30},
			OpenInterest: 500,
		},
	}

	constraints := options.ContractSelectionConstraints{
		MinDTE:          35,
		MaxDTE:          45,
		TargetDeltaLow:  0.40,
		TargetDeltaHigh: 0.55,
		MinOpenInterest: 100,
		MaxSpreadPct:    0.10,
		MaxIV:           1.0,
	}
	selSvc := options.NewContractSelectionService(constraints, func() time.Time { return now })

	selected, err := selSvc.SelectBestContract(domain.DirectionShort, domain.RegimeTrend, chain)
	require.NoError(t, err)
	assert.Equal(t, domain.OptionRightPut, selected.OptionContract.Right)
	assert.InDelta(t, -0.48, selected.Greeks.Delta, 1e-9)
}

func TestE2E_PaperCanary_RiskRejectsOversizedTrade(t *testing.T) {
	now := time.Date(2025, 3, 2, 12, 0, 0, 0, time.UTC)
	expiry := now.AddDate(0, 0, 40)
	occSymbol := domain.FormatOCCSymbol("AAPL", expiry, domain.OptionRightCall, 190.0)

	instrument, _ := domain.NewInstrument(domain.InstrumentTypeOption, occSymbol, "AAPL")
	intentID := uuid.New()

	intent, err := domain.NewOptionOrderIntent(
		intentID,
		"tenant-paper",
		domain.EnvModePaper,
		instrument,
		domain.DirectionLong,
		3.30,
		10.0,
		"test",
		"oversized",
		0.85,
		intentID.String(),
		3250.0,
	)
	require.NoError(t, err)

	riskEngine := execution.NewOptionsRiskEngine(
		0.02,
		100,
		0.10,
		1.0,
		35,
		func() time.Time { return now },
	)

	err = riskEngine.ValidateOptionIntent(intent, 10_000.0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}
