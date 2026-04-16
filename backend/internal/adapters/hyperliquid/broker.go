package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Compile-time interface compliance checks.
var _ ports.BrokerPort = (*Broker)(nil)

// Broker implements ports.BrokerPort for the Hyperliquid perpetual exchange.
type Broker struct {
	client *Client
	rest   *RESTClient
	ws     *WSSubscriber
	log    zerolog.Logger

	// Position tracking: coin → tracked position state.
	posMu     sync.RWMutex
	positions map[string]*trackedPosition
}

type trackedPosition struct {
	Coin          string
	Qty           float64 // signed: positive=long, negative=short
	AvgEntryPx    float64
	UnrealizedPnl float64
}

// NewBroker creates a Hyperliquid BrokerPort implementation.
func NewBroker(cfg config.HyperliquidConfig, log zerolog.Logger) (*Broker, error) {
	client, err := NewClient(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid broker: create client: %w", err)
	}

	rest := NewRESTClient(client)
	ws := NewWSSubscriber(client.WSURL(), client.Address(), log)

	b := &Broker{
		client:    client,
		rest:      rest,
		ws:        ws,
		log:       log.With().Str("component", "hyperliquid_broker").Logger(),
		positions: make(map[string]*trackedPosition),
	}

	return b, nil
}

// Client returns the underlying shared client for use by other adapters
// (funding, open interest).
func (b *Broker) Client() *Client { return b.client }

// RESTClient returns the underlying REST client.
func (b *Broker) RESTClient() *RESTClient { return b.rest }

// WSSubscriber returns the underlying WebSocket subscriber.
func (b *Broker) WSSubscriber() *WSSubscriber { return b.ws }

// SubmitOrder submits an order to Hyperliquid via the /exchange endpoint.
// The intent is translated to the Hyperliquid order format with EIP-712
// signing.
func (b *Broker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	coin := symbolToCoin(intent.Symbol)
	assetID, err := b.client.ResolveAsset(ctx, coin)
	if err != nil {
		return "", fmt.Errorf("hyperliquid: submit order: %w", err)
	}

	isBuy := intent.Direction == domain.DirectionLong || intent.Direction == domain.DirectionCloseShort
	side := "B"
	if !isBuy {
		side = "A"
	}

	// Build the order. Market orders are simulated as aggressive limit orders
	// with a large slippage tolerance.
	orderType := buildOrderType(intent)
	limitPx := formatPrice(intent.LimitPrice)

	// For market-like orders, use a price 5% away from limit as a safety bound.
	if intent.OrderType == "market" {
		if isBuy {
			limitPx = formatPrice(intent.LimitPrice * 1.05)
		} else {
			limitPx = formatPrice(intent.LimitPrice * 0.95)
		}
	}

	order := hlOrder{
		Asset:     assetID,
		IsBuy:     isBuy,
		LimitPx:   limitPx,
		Sz:        formatSize(intent.Quantity),
		ReduceOnly: intent.Direction.IsExit(),
		OrderType: orderType,
	}

	action := map[string]any{
		"type":     "order",
		"orders":   []hlOrder{order},
		"grouping": "na",
	}

	nonce := time.Now().UnixMilli()
	raw, err := b.client.PostExchange(ctx, action, nonce)
	if err != nil {
		return "", fmt.Errorf("hyperliquid: submit order: %w", err)
	}

	// Parse the response to extract the order ID.
	oid, err := parseOrderResponse(raw)
	if err != nil {
		return "", fmt.Errorf("hyperliquid: parse order response: %w", err)
	}

	b.log.Info().
		Str("coin", coin).
		Str("side", side).
		Str("limit_px", limitPx).
		Str("sz", formatSize(intent.Quantity)).
		Str("oid", oid).
		Msg("hyperliquid: order submitted")

	return oid, nil
}

// CancelOrder cancels an order on Hyperliquid by its OID.
func (b *Broker) CancelOrder(ctx context.Context, orderID string) error {
	oid, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return fmt.Errorf("hyperliquid: parse order id %q: %w", orderID, err)
	}

	// We need the asset ID for the cancel. Try to find it from open orders,
	// or fall back to cancel-by-oid which requires iterating positions.
	// For simplicity, use the cancel-by-cloid approach if available, but
	// Hyperliquid requires asset+oid for cancels.
	//
	// Without knowing the asset, we query open orders first.
	orders, err := b.rest.GetOpenOrders(ctx, b.client.Address())
	if err != nil {
		return fmt.Errorf("hyperliquid: get open orders for cancel: %w", err)
	}

	var assetID int
	found := false
	for _, o := range orders {
		if o.OID == oid {
			assetID, err = b.client.ResolveAsset(ctx, o.Coin)
			if err != nil {
				return fmt.Errorf("hyperliquid: resolve asset for cancel: %w", err)
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: oid %d", ErrOrderNotFound, oid)
	}

	action := map[string]any{
		"type": "cancel",
		"cancels": []map[string]any{
			{"asset": assetID, "oid": oid},
		},
	}

	nonce := time.Now().UnixMilli()
	_, err = b.client.PostExchange(ctx, action, nonce)
	if err != nil {
		return fmt.Errorf("hyperliquid: cancel order: %w", err)
	}

	b.log.Info().Int64("oid", oid).Int("asset", assetID).
		Msg("hyperliquid: order canceled")
	return nil
}

// CancelOpenOrders cancels all open orders for a given symbol and side.
func (b *Broker) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
	coin := symbolToCoin(symbol)
	orders, err := b.rest.GetOpenOrders(ctx, b.client.Address())
	if err != nil {
		return 0, fmt.Errorf("hyperliquid: cancel open orders: %w", err)
	}

	var cancels []map[string]any
	for _, o := range orders {
		if o.Coin != coin {
			continue
		}
		orderSide := hlSideToDomain(o.Side)
		if side != "" && orderSide != side {
			continue
		}
		assetID, err := b.client.ResolveAsset(ctx, o.Coin)
		if err != nil {
			continue
		}
		cancels = append(cancels, map[string]any{"asset": assetID, "oid": o.OID})
	}

	if len(cancels) == 0 {
		return 0, nil
	}

	action := map[string]any{
		"type":    "cancel",
		"cancels": cancels,
	}
	nonce := time.Now().UnixMilli()
	_, err = b.client.PostExchange(ctx, action, nonce)
	if err != nil {
		return 0, fmt.Errorf("hyperliquid: batch cancel: %w", err)
	}
	return len(cancels), nil
}

// GetOrderStatus returns the status of an order (not directly supported by
// Hyperliquid info API in a simple way; we check open orders).
func (b *Broker) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	oid, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("hyperliquid: parse order id: %w", err)
	}
	orders, err := b.rest.GetOpenOrders(ctx, b.client.Address())
	if err != nil {
		return "", fmt.Errorf("hyperliquid: get order status: %w", err)
	}
	for _, o := range orders {
		if o.OID == oid {
			return "open", nil
		}
	}
	// If not found in open orders, it was filled, canceled, or expired.
	return "closed", nil
}

// GetPositions returns all open positions from the clearinghouse state.
func (b *Broker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	state, err := b.rest.GetAccountState(ctx, b.client.Address())
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get positions: %w", err)
	}

	var trades []domain.Trade
	for _, ap := range state.Positions {
		p := ap.Position
		if p.Szi == 0 {
			continue
		}
		side := "buy"
		if p.Szi < 0 {
			side = "sell"
		}
		sym := coinToSymbol(p.Coin)
		t := domain.Trade{
			Time:       time.Now(),
			TenantID:   tenantID,
			EnvMode:    envMode,
			Symbol:     sym,
			Side:       side,
			Quantity:   math.Abs(p.Szi),
			Price:      p.EntryPx,
			Status:     "open",
			AssetClass: domain.AssetClassCrypto,
			Venue:      domain.VenueHyperliquid,
		}
		trades = append(trades, t)

		// Update position cache.
		b.posMu.Lock()
		b.positions[p.Coin] = &trackedPosition{
			Coin:          p.Coin,
			Qty:           p.Szi,
			AvgEntryPx:    p.EntryPx,
			UnrealizedPnl: p.UnrealizedPnl,
		}
		b.posMu.Unlock()
	}
	return trades, nil
}

// GetPosition returns the current quantity held for a single symbol.
func (b *Broker) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
	coin := symbolToCoin(symbol)

	// Check cache first.
	b.posMu.RLock()
	if tp, ok := b.positions[coin]; ok {
		b.posMu.RUnlock()
		return tp.Qty, nil
	}
	b.posMu.RUnlock()

	// Cache miss: query the API.
	state, err := b.rest.GetAccountState(ctx, b.client.Address())
	if err != nil {
		return 0, fmt.Errorf("hyperliquid: get position: %w", err)
	}
	for _, ap := range state.Positions {
		if ap.Position.Coin == coin {
			return ap.Position.Szi, nil
		}
	}
	return 0, nil
}

// ClosePosition liquidates any remaining position for a symbol.
func (b *Broker) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
	qty, err := b.GetPosition(ctx, symbol)
	if err != nil {
		return "", err
	}
	if qty == 0 {
		return "", nil
	}

	// Build a market-like closing order in the opposite direction.
	dir := domain.DirectionCloseLong
	if qty < 0 {
		dir = domain.DirectionCloseShort
	}

	// We need a price reference. Use the mark price from metaAndAssetCtxs.
	coin := symbolToCoin(symbol)
	oi, err := b.rest.GetOpenInterest(ctx, coin)
	if err != nil {
		return "", fmt.Errorf("hyperliquid: close position: get mark price: %w", err)
	}

	intent := domain.OrderIntent{
		Symbol:    symbol,
		Direction: dir,
		Quantity:  math.Abs(qty),
		LimitPrice: oi.MarkPrice,
		OrderType: "market",
		Venue:     domain.VenueHyperliquid,
	}

	return b.SubmitOrder(ctx, intent)
}

// GetOrderDetails returns full order details. Hyperliquid does not have a
// single-order query endpoint; this is approximated from open orders.
func (b *Broker) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
	oid, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return ports.OrderDetails{}, fmt.Errorf("hyperliquid: parse order id: %w", err)
	}
	orders, err := b.rest.GetOpenOrders(ctx, b.client.Address())
	if err != nil {
		return ports.OrderDetails{}, fmt.Errorf("hyperliquid: get order details: %w", err)
	}
	for _, o := range orders {
		if o.OID == oid {
			return ports.OrderDetails{
				BrokerOrderID: orderID,
				Status:        "open",
				Symbol:        o.Coin,
				Side:          hlSideToDomain(o.Side),
				Qty:           parseFloat(o.Sz),
			}, nil
		}
	}
	return ports.OrderDetails{}, fmt.Errorf("%w: oid %s", ports.ErrOrderNotFound, orderID)
}

// CancelAllOpenOrders cancels every open order on the account.
func (b *Broker) CancelAllOpenOrders(ctx context.Context) (int, error) {
	orders, err := b.rest.GetOpenOrders(ctx, b.client.Address())
	if err != nil {
		return 0, fmt.Errorf("hyperliquid: cancel all open orders: %w", err)
	}
	if len(orders) == 0 {
		return 0, nil
	}

	var cancels []map[string]any
	for _, o := range orders {
		assetID, err := b.client.ResolveAsset(ctx, o.Coin)
		if err != nil {
			b.log.Warn().Err(err).Str("coin", o.Coin).Msg("skip cancel: cannot resolve asset")
			continue
		}
		cancels = append(cancels, map[string]any{"asset": assetID, "oid": o.OID})
	}
	if len(cancels) == 0 {
		return 0, nil
	}

	action := map[string]any{
		"type":    "cancel",
		"cancels": cancels,
	}
	nonce := time.Now().UnixMilli()
	_, err = b.client.PostExchange(ctx, action, nonce)
	if err != nil {
		return 0, fmt.Errorf("hyperliquid: cancel all: %w", err)
	}
	return len(cancels), nil
}

// GetOpenOrders returns the broker's view of every working order.
func (b *Broker) GetOpenOrders(ctx context.Context) ([]ports.OpenOrder, error) {
	orders, err := b.rest.GetOpenOrders(ctx, b.client.Address())
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: get open orders: %w", err)
	}

	result := make([]ports.OpenOrder, 0, len(orders))
	for _, o := range orders {
		result = append(result, ports.OpenOrder{
			BrokerOrderID: strconv.FormatInt(o.OID, 10),
			Symbol:        o.Coin,
			Side:          hlSideToDomain(o.Side),
			Quantity:      parseFloat(o.Sz),
			OrderType:     "limit",
			LimitPrice:    parseFloat(o.LimitPx),
			Status:        "open",
			CreatedAt:     time.UnixMilli(o.Timestamp),
		})
	}
	return result, nil
}

// ──────────────────────────────────────────────────────────────────────────
// Internal types and helpers
// ──────────────────────────────────────────────────────────────────────────

// hlOrder is the Hyperliquid order format for the exchange endpoint.
type hlOrder struct {
	Asset      int         `json:"a"`
	IsBuy      bool        `json:"b"`
	LimitPx    string      `json:"p"`
	Sz         string      `json:"s"`
	ReduceOnly bool        `json:"r"`
	OrderType  hlOrderType `json:"t"`
}

// hlOrderType encodes the order type and time-in-force.
type hlOrderType struct {
	Limit *hlLimit `json:"limit,omitempty"`
	Trigger *hlTrigger `json:"trigger,omitempty"`
}

type hlLimit struct {
	Tif string `json:"tif"` // "Gtc", "Ioc", "Alo"
}

type hlTrigger struct {
	IsMarket  bool   `json:"isMarket"`
	TriggerPx string `json:"triggerPx"`
	TpSl      string `json:"tpsl"` // "tp" or "sl"
}

// buildOrderType translates intent fields to Hyperliquid's order type.
func buildOrderType(intent domain.OrderIntent) hlOrderType {
	switch intent.OrderType {
	case "market":
		// Market orders are simulated as IOC limit orders.
		return hlOrderType{Limit: &hlLimit{Tif: "Ioc"}}
	default:
		// Default to GTC limit.
		tif := "Gtc"
		switch intent.TimeInForce {
		case "ioc":
			tif = "Ioc"
		case "alo":
			tif = "Alo"
		}
		return hlOrderType{Limit: &hlLimit{Tif: tif}}
	}
}

// parseOrderResponse extracts the order ID from the exchange response.
func parseOrderResponse(raw []byte) (string, error) {
	// Hyperliquid returns: {"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":123}}]}}}
	var resp struct {
		Status   string `json:"status"`
		Response struct {
			Type string `json:"type"`
			Data struct {
				Statuses []struct {
					Resting *struct {
						OID int64 `json:"oid"`
					} `json:"resting,omitempty"`
					Filled *struct {
						OID int64 `json:"oid"`
					} `json:"filled,omitempty"`
					Error string `json:"error,omitempty"`
				} `json:"statuses"`
			} `json:"data"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("hyperliquid: unmarshal order response: %w", err)
	}
	if resp.Status != "ok" {
		return "", fmt.Errorf("%w: status=%s", ErrInvalidResponse, resp.Status)
	}
	for _, s := range resp.Response.Data.Statuses {
		if s.Error != "" {
			if s.Error == "insufficient margin" || s.Error == "Insufficient margin" {
				return "", fmt.Errorf("%w: %s", ErrInsufficientMargin, s.Error)
			}
			return "", fmt.Errorf("%w: %s", ErrInvalidResponse, s.Error)
		}
		if s.Resting != nil {
			return strconv.FormatInt(s.Resting.OID, 10), nil
		}
		if s.Filled != nil {
			return strconv.FormatInt(s.Filled.OID, 10), nil
		}
	}
	return "", fmt.Errorf("%w: no order ID in response", ErrInvalidResponse)
}

// symbolToCoin converts a domain.Symbol like "BTC/USD" to Hyperliquid's coin
// name "BTC". Hyperliquid perps are denominated in USD implicitly.
func symbolToCoin(s domain.Symbol) string {
	str := string(s)
	// Remove /USD, /USDT, -PERP suffixes
	for _, suffix := range []string{"/USD", "/USDT", "-PERP", "-USD"} {
		if idx := len(str) - len(suffix); idx > 0 && str[idx:] == suffix {
			return str[:idx]
		}
	}
	return str
}

// coinToSymbol converts Hyperliquid's coin name "BTC" back to domain.Symbol
// "BTC/USD".
func coinToSymbol(coin string) domain.Symbol {
	return domain.Symbol(coin + "/USD")
}

// hlSideToDomain converts Hyperliquid's "A"/"B" side codes to "sell"/"buy".
func hlSideToDomain(side string) string {
	switch side {
	case "B":
		return "buy"
	case "A":
		return "sell"
	default:
		return side
	}
}

// formatPrice formats a float64 price as a string with appropriate precision.
func formatPrice(p float64) string {
	if p >= 1.0 {
		return strconv.FormatFloat(p, 'f', 2, 64)
	}
	return strconv.FormatFloat(p, 'f', 6, 64)
}

// formatSize formats a quantity for the Hyperliquid API.
func formatSize(sz float64) string {
	return strconv.FormatFloat(sz, 'f', -1, 64)
}
