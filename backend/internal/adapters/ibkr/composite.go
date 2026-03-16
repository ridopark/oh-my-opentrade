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
	panic("CompositeAdapter.SubmitOrder: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) CancelOrder(ctx context.Context, orderID string) error {
	panic("CompositeAdapter.CancelOrder: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
	panic("CompositeAdapter.CancelOpenOrders: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	panic("CompositeAdapter.GetOrderStatus: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	panic("CompositeAdapter.GetPositions: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
	panic("CompositeAdapter.GetPosition: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
	panic("CompositeAdapter.ClosePosition: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
	panic("CompositeAdapter.GetOrderDetails: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) CancelAllOpenOrders(ctx context.Context) (int, error) {
	panic("CompositeAdapter.CancelAllOpenOrders: not implemented — implement in Task 4")
}

// ── OrderStreamPort → IBKR ───────────────────────────────────────────────────

func (c *CompositeAdapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	panic("CompositeAdapter.SubscribeOrderUpdates: not implemented — implement in Task 4")
}

// ── MarketDataPort → IBKR (live) / Alpaca (historical) ───────────────────────

func (c *CompositeAdapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
	panic("CompositeAdapter.StreamBars: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	panic("CompositeAdapter.GetHistoricalBars: not implemented — implement in Task 4")
}

// ── AccountPort → IBKR ───────────────────────────────────────────────────────

func (c *CompositeAdapter) GetAccountBuyingPower(ctx context.Context) (ports.BuyingPower, error) {
	panic("CompositeAdapter.GetAccountBuyingPower: not implemented — implement in Task 4")
}

// ── SnapshotPort → Alpaca ────────────────────────────────────────────────────

func (c *CompositeAdapter) GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error) {
	panic("CompositeAdapter.GetSnapshots: not implemented — implement in Task 4")
}

// ── OptionsMarketDataPort → Alpaca ───────────────────────────────────────────

func (c *CompositeAdapter) GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
	panic("CompositeAdapter.GetOptionChain: not implemented — implement in Task 4")
}

// ── OptionsPricePort → Alpaca ────────────────────────────────────────────────

func (c *CompositeAdapter) GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	panic("CompositeAdapter.GetOptionPrices: not implemented — implement in Task 4")
}

// ── UniverseProviderPort → Alpaca ────────────────────────────────────────────

func (c *CompositeAdapter) ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error) {
	panic("CompositeAdapter.ListTradeable: not implemented — implement in Task 4")
}

// ── Extra brokerAdapter methods (not in standard ports) → IBKR ───────────────

func (c *CompositeAdapter) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	panic("CompositeAdapter.GetQuote: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) GetAccountEquity(ctx context.Context) (float64, error) {
	panic("CompositeAdapter.GetAccountEquity: not implemented — implement in Task 4")
}

func (c *CompositeAdapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
	panic("CompositeAdapter.SubscribeSymbols: not implemented — implement in Task 4")
}

// ── Lifecycle ─────────────────────────────────────────────────────────────────

func (c *CompositeAdapter) Close() error {
	ibErr := c.ibkr.Close()
	alpErr := c.alpaca.Close()
	if ibErr != nil {
		return fmt.Errorf("ibkr composite close ibkr: %w", ibErr)
	}
	return alpErr
}
