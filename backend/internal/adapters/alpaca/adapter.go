package alpaca

import (
	"context"
	"errors"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const defaultRateLimit = 200

// Adapter implements both market data and broker interfaces for Alpaca.
type Adapter struct {
	rest    *RESTClient
	ws      *WSClient
	dataURL string
	log     zerolog.Logger
}

// NewAdapter creates a new Alpaca Adapter.
func NewAdapter(cfg config.AlpacaConfig, log zerolog.Logger) (*Adapter, error) {
	if cfg.APIKeyID == "" {
		return nil, errors.New("APIKeyID is required")
	}
	if cfg.APISecretKey == "" {
		return nil, errors.New("APISecretKey is required")
	}

	limiter := NewRateLimiter(defaultRateLimit)
	restLog := log.With().Str("client", "rest").Logger()
	rest := NewRESTClient(cfg.BaseURL, cfg.APIKeyID, cfg.APISecretKey, limiter, restLog)

	dataURL := cfg.DataURL
	if dataURL == "" {
		dataURL = "https://data.alpaca.markets"
	}

	// Wire the REST fetcher into the WS client so it can poll during ghost windows.
	fetcher := func(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
		return rest.GetHistoricalBars(ctx, dataURL, symbol, timeframe, from, to)
	}
	ws := NewWSClient(cfg.DataURL, cfg.APIKeyID, cfg.APISecretKey, cfg.Feed, fetcher)



	return &Adapter{
		rest:    rest,
		ws:      ws,
		dataURL: dataURL,
		log:     log,
	}, nil
}

// StreamBars starts streaming market data bars for the requested symbols.
func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler ports.BarHandler) error {
	return a.ws.StreamBars(ctx, symbols, timeframe, handler)
}

// GetHistoricalBars fetches historical OHLCV bars from the Alpaca data API.
func (a *Adapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return a.rest.GetHistoricalBars(ctx, a.dataURL, symbol, timeframe, from, to)
}

// Close safely closes the adapter and underlying connections.
func (a *Adapter) Close() error {
	return a.ws.Close()
}

// SubmitOrder places an order through the Alpaca REST API.
// If the intent carries an options instrument, it dispatches to SubmitOptionOrder.
func (a *Adapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		return a.rest.SubmitOptionOrder(ctx, intent)
	}
	return a.rest.SubmitOrder(ctx, intent)
}

// CancelOrder requests cancellation of an existing Alpaca order.
func (a *Adapter) CancelOrder(ctx context.Context, orderID string) error {
	return a.rest.CancelOrder(ctx, orderID)
}

// GetOrderStatus fetches the current status of an Alpaca order.
func (a *Adapter) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return a.rest.GetOrderStatus(ctx, orderID)
}

// GetPositions retrieves currently open positions from Alpaca.
func (a *Adapter) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	return a.rest.GetPositions(ctx, tenantID, envMode)
}

// GetQuote fetches the latest bid and ask prices for a symbol.
func (a *Adapter) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	return a.rest.GetQuote(ctx, symbol)
}

// GetOptionChain fetches option contract snapshots for the given underlying, expiry, and right.
func (a *Adapter) GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
	return a.rest.GetOptionChain(ctx, underlying, expiry, right)
}
// GetAccountEquity fetches the current paper account equity from Alpaca.
func (a *Adapter) GetAccountEquity(ctx context.Context) (float64, error) {
	return a.rest.GetAccountEquity(ctx)
}
