package ibkr

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// alpacaDataProvider lists the Alpaca adapter methods delegated by CompositeAdapter.
// Only REST-capable methods: historical bars, snapshots, options, universe.
// WebSocket/streaming methods (StreamBars, SubscribeOrderUpdates) are IBKR-only.
type alpacaDataProvider interface {
	GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error)
	GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error)
	GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error)
	ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error)
	Close() error
}

// CompositeAdapter satisfies the brokerAdapter mega-interface by routing:
//   - Live execution + streaming + account → IBKR
//   - Historical bars + snapshots + options + universe → Alpaca (REST only)
type CompositeAdapter struct {
	ibkr   *Adapter
	alpaca alpacaDataProvider
	log    zerolog.Logger
}

// NewCompositeAdapter creates a CompositeAdapter.
func NewCompositeAdapter(ibkrAdapter *Adapter, alpacaAdapter alpacaDataProvider, log zerolog.Logger) *CompositeAdapter {
	return &CompositeAdapter{
		ibkr:   ibkrAdapter,
		alpaca: alpacaAdapter,
		log:    log.With().Str("component", "ibkr_composite").Logger(),
	}
}

// Compile-time port assertions (cannot assert against brokerAdapter — it's in package main).
var (
	_ ports.BrokerPort            = (*CompositeAdapter)(nil)
	_ ports.OrderStreamPort       = (*CompositeAdapter)(nil)
	_ ports.MarketDataPort        = (*CompositeAdapter)(nil)
	_ ports.AccountPort           = (*CompositeAdapter)(nil)
	_ ports.SnapshotPort          = (*CompositeAdapter)(nil)
	_ ports.OptionsMarketDataPort = (*CompositeAdapter)(nil)
	_ ports.OptionsPricePort      = (*CompositeAdapter)(nil)
	_ ports.UniverseProviderPort  = (*CompositeAdapter)(nil)
)

// ── BrokerPort → IBKR ────────────────────────────────────────────────────────

func (c *CompositeAdapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	return c.ibkr.SubmitOrder(ctx, intent)
}

func (c *CompositeAdapter) CancelOrder(ctx context.Context, orderID string) error {
	return c.ibkr.CancelOrder(ctx, orderID)
}

func (c *CompositeAdapter) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
	return c.ibkr.CancelOpenOrders(ctx, symbol, side)
}

func (c *CompositeAdapter) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return c.ibkr.GetOrderStatus(ctx, orderID)
}

func (c *CompositeAdapter) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	return c.ibkr.GetPositions(ctx, tenantID, envMode)
}

func (c *CompositeAdapter) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
	return c.ibkr.GetPosition(ctx, symbol)
}

func (c *CompositeAdapter) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
	return c.ibkr.ClosePosition(ctx, symbol)
}

func (c *CompositeAdapter) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
	return c.ibkr.GetOrderDetails(ctx, orderID)
}

func (c *CompositeAdapter) CancelAllOpenOrders(ctx context.Context) (int, error) {
	return c.ibkr.CancelAllOpenOrders(ctx)
}

// ── OrderStreamPort → IBKR ───────────────────────────────────────────────────

func (c *CompositeAdapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	return c.ibkr.SubscribeOrderUpdates(ctx)
}

// ── MarketDataPort → IBKR (live) / Alpaca (historical) ───────────────────────

func (c *CompositeAdapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
	return c.ibkr.StreamBars(ctx, symbols, tf, handler)
}

func (c *CompositeAdapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return c.alpaca.GetHistoricalBars(ctx, symbol, tf, from, to)
}

// ── AccountPort → IBKR ───────────────────────────────────────────────────────

func (c *CompositeAdapter) GetAccountBuyingPower(ctx context.Context) (ports.BuyingPower, error) {
	return c.ibkr.GetAccountBuyingPower(ctx)
}

// ── SnapshotPort → Alpaca ────────────────────────────────────────────────────

func (c *CompositeAdapter) GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error) {
	return c.alpaca.GetSnapshots(ctx, symbols, asOf)
}

// ── OptionsMarketDataPort → Alpaca ───────────────────────────────────────────

func (c *CompositeAdapter) GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
	return c.alpaca.GetOptionChain(ctx, underlying, expiry, right)
}

// ── OptionsPricePort → Alpaca ────────────────────────────────────────────────

func (c *CompositeAdapter) GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	return c.alpaca.GetOptionPrices(ctx, symbols)
}

// ── UniverseProviderPort → Alpaca ────────────────────────────────────────────

func (c *CompositeAdapter) ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error) {
	return c.alpaca.ListTradeable(ctx, assetClass)
}

// ── Extra brokerAdapter methods (not in standard ports) ──────────────────────

// GetQuote returns bid/ask for a symbol via IBKR Snapshot.
func (c *CompositeAdapter) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	return c.ibkr.GetQuote(ctx, symbol)
}

// GetAccountEquity returns total account equity from IBKR.
func (c *CompositeAdapter) GetAccountEquity(ctx context.Context) (float64, error) {
	return c.ibkr.GetAccountEquity(ctx)
}

// SubscribeSymbols starts bar streaming for additional symbols via IBKR.
func (c *CompositeAdapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
	return c.ibkr.SubscribeSymbols(ctx, symbols)
}

// ── Lifecycle ─────────────────────────────────────────────────────────────────

// Close shuts down both IBKR and Alpaca adapters.
func (c *CompositeAdapter) Close() error {
	ibErr := c.ibkr.Close()
	alpErr := c.alpaca.Close()
	if ibErr != nil {
		return fmt.Errorf("ibkr composite close ibkr: %w", ibErr)
	}
	return alpErr
}
