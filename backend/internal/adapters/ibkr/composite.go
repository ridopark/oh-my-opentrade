package ibkr

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

var ErrIBKRNotConnected = fmt.Errorf("IBKR: %w", ports.ErrBrokerNotAvailable)

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
	mu     sync.RWMutex
	ibkr   *Adapter
	alpaca alpacaDataProvider
	log    zerolog.Logger
}

// NewCompositeAdapter creates a CompositeAdapter. ibkrAdapter may be nil —
// broker operations will return ErrIBKRNotConnected until SetIBKR is called.
func NewCompositeAdapter(ibkrAdapter *Adapter, alpacaAdapter alpacaDataProvider, log zerolog.Logger) *CompositeAdapter {
	return &CompositeAdapter{
		ibkr:   ibkrAdapter,
		alpaca: alpacaAdapter,
		log:    log.With().Str("component", "ibkr_composite").Logger(),
	}
}

// SetIBKR hot-swaps the IBKR adapter after a deferred connection.
func (c *CompositeAdapter) SetIBKR(a *Adapter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ibkr = a
	c.log.Info().Msg("IBKR adapter connected (hot-swap)")
}

func (c *CompositeAdapter) getIBKR() (*Adapter, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ibkr == nil {
		return nil, ErrIBKRNotConnected
	}
	return c.ibkr, nil
}

// IsIBKRConnected reports whether the IBKR adapter is set and connected.
func (c *CompositeAdapter) IsIBKRConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ibkr != nil && c.ibkr.IsConnected()
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
	a, err := c.getIBKR()
	if err != nil {
		return "", err
	}
	return a.SubmitOrder(ctx, intent)
}

func (c *CompositeAdapter) CancelOrder(ctx context.Context, orderID string) error {
	a, err := c.getIBKR()
	if err != nil {
		return err
	}
	return a.CancelOrder(ctx, orderID)
}

func (c *CompositeAdapter) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
	a, err := c.getIBKR()
	if err != nil {
		return 0, err
	}
	return a.CancelOpenOrders(ctx, symbol, side)
}

func (c *CompositeAdapter) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	a, err := c.getIBKR()
	if err != nil {
		return "", err
	}
	return a.GetOrderStatus(ctx, orderID)
}

func (c *CompositeAdapter) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	a, err := c.getIBKR()
	if err != nil {
		return nil, err
	}
	return a.GetPositions(ctx, tenantID, envMode)
}

func (c *CompositeAdapter) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
	a, err := c.getIBKR()
	if err != nil {
		return 0, err
	}
	return a.GetPosition(ctx, symbol)
}

func (c *CompositeAdapter) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
	a, err := c.getIBKR()
	if err != nil {
		return "", err
	}
	return a.ClosePosition(ctx, symbol)
}

func (c *CompositeAdapter) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
	a, err := c.getIBKR()
	if err != nil {
		return ports.OrderDetails{}, err
	}
	return a.GetOrderDetails(ctx, orderID)
}

func (c *CompositeAdapter) CancelAllOpenOrders(ctx context.Context) (int, error) {
	a, err := c.getIBKR()
	if err != nil {
		return 0, err
	}
	return a.CancelAllOpenOrders(ctx)
}

func (c *CompositeAdapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	a, err := c.getIBKR()
	if err != nil {
		return nil, err
	}
	return a.SubscribeOrderUpdates(ctx)
}

// ── MarketDataPort → IBKR (live) / Alpaca (historical) ───────────────────────

func (c *CompositeAdapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
	a, err := c.getIBKR()
	if err != nil {
		return err
	}
	return a.StreamBars(ctx, symbols, tf, handler)
}

func (c *CompositeAdapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return c.alpaca.GetHistoricalBars(ctx, symbol, tf, from, to)
}

func (c *CompositeAdapter) GetAccountBuyingPower(ctx context.Context) (ports.BuyingPower, error) {
	a, err := c.getIBKR()
	if err != nil {
		return ports.BuyingPower{}, err
	}
	return a.GetAccountBuyingPower(ctx)
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

func (c *CompositeAdapter) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	a, err := c.getIBKR()
	if err != nil {
		return 0, 0, err
	}
	return a.GetQuote(ctx, symbol)
}

func (c *CompositeAdapter) GetAccountEquity(ctx context.Context) (float64, error) {
	a, err := c.getIBKR()
	if err != nil {
		return 0, err
	}
	return a.GetAccountEquity(ctx)
}

func (c *CompositeAdapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
	a, err := c.getIBKR()
	if err != nil {
		return err
	}
	return a.SubscribeSymbols(ctx, symbols)
}

func (c *CompositeAdapter) Close() error {
	c.mu.RLock()
	ib := c.ibkr
	c.mu.RUnlock()
	var ibErr error
	if ib != nil {
		ibErr = ib.Close()
	}
	alpErr := c.alpaca.Close()
	if ibErr != nil {
		return fmt.Errorf("ibkr composite close: %w", ibErr)
	}
	return alpErr
}
