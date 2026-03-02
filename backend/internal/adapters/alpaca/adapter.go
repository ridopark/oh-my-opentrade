package alpaca

import (
	"context"
	"errors"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

const defaultRateLimit = 200

// Adapter implements both market data and broker interfaces for Alpaca.
type Adapter struct {
	rest *RESTClient
	ws   *WSClient
}

// NewAdapter creates a new Alpaca Adapter.
func NewAdapter(cfg config.AlpacaConfig) (*Adapter, error) {
	if cfg.APIKeyID == "" {
		return nil, errors.New("APIKeyID is required")
	}
	if cfg.APISecretKey == "" {
		return nil, errors.New("APISecretKey is required")
	}

	limiter := NewRateLimiter(defaultRateLimit)
	rest := NewRESTClient(cfg.BaseURL, cfg.APIKeyID, cfg.APISecretKey, limiter)
	ws := NewWSClient(cfg.DataURL, cfg.APIKeyID, cfg.APISecretKey)

	return &Adapter{
		rest: rest,
		ws:   ws,
	}, nil
}

// StreamBars starts streaming market data bars for the requested symbols.
func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler ports.BarHandler) error {
	return a.ws.StreamBars(ctx, symbols, timeframe, handler)
}

// GetHistoricalBars is a stub for fetching historical market bars.
func (a *Adapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}

// Close safely closes the adapter and underlying connections.
func (a *Adapter) Close() error {
	return a.ws.Close()
}

// SubmitOrder places an order through the Alpaca REST API.
func (a *Adapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
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
