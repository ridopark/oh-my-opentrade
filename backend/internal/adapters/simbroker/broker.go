// Package simbroker provides a simulated broker adapter for backtesting.
// It implements ports.BrokerPort with configurable slippage and instant fills
// using the latest bar close price.
package simbroker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Config holds SimBroker configuration.
type Config struct {
	SlippageBPS     int64   // slippage in basis points (default 5 per PRD)
	InitialEquity   float64 // starting cash/equity for the simulated account (default 100000)
	DisableFillChan bool    // skip fillCh sends; set when syncFill handles fills directly
}

// simOrder tracks a submitted order and its fill details.
type simOrder struct {
	intent    domain.OrderIntent
	orderID   string
	fillPrice float64
	fillQty   float64
	filledAt  time.Time
	side      string
}

// position tracks aggregated position state for a symbol.
type position struct {
	symbol   domain.Symbol
	side     string // "buy" or "sell"
	quantity float64
	avgCost  float64
}

// Broker is a simulated broker for backtesting that implements ports.BrokerPort
// and ports.OrderStreamPort. It fills orders instantly at the last known bar
// close price with configurable slippage.
type Broker struct {
	slippageBPS       int64
	initialEquity     float64
	disableFillChan   bool
	historicalOptions ports.HistoricalOptionsPort
	log               zerolog.Logger

	mu        sync.RWMutex
	prices    map[domain.Symbol]float64
	barTimes  map[domain.Symbol]time.Time
	orders    map[string]*simOrder
	positions map[string]*position
	cash      float64
	orderSeq  atomic.Int64

	fillCh chan ports.OrderUpdate
}

// SetHistoricalOptions injects historical options data for realistic exit pricing.
func (b *Broker) SetHistoricalOptions(h ports.HistoricalOptionsPort) {
	b.historicalOptions = h
}

// New creates a new SimBroker with the given configuration.
func New(cfg Config, log zerolog.Logger) *Broker {
	if cfg.SlippageBPS == 0 {
		cfg.SlippageBPS = 5
	}
	equity := cfg.InitialEquity
	if equity == 0 {
		equity = 100_000
	}
	return &Broker{
		slippageBPS:     cfg.SlippageBPS,
		initialEquity:   equity,
		disableFillChan: cfg.DisableFillChan,
		log:             log.With().Str("component", "simbroker").Logger(),
		prices:          make(map[domain.Symbol]float64),
		barTimes:        make(map[domain.Symbol]time.Time),
		orders:          make(map[string]*simOrder),
		positions:       make(map[string]*position),
		cash:            equity,
		fillCh:          make(chan ports.OrderUpdate, 256),
	}
}

// UpdatePrice sets the latest close price for a symbol. Called by the replay loop
// before publishing each bar event so SimBroker has the current price for fills.
func (b *Broker) UpdatePrice(symbol domain.Symbol, price float64, barTime time.Time) {
	b.mu.Lock()
	b.prices[symbol] = price
	b.barTimes[symbol] = barTime
	b.mu.Unlock()
}

// SubmitOrder fills the order immediately at the current bar close ± slippage.
// Returns a generated order ID. If no price is available for the symbol,
// the order is rejected with an error.
func (b *Broker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	isOption := intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption

	priceSymbol := intent.Symbol
	if isOption && intent.Instrument.UnderlyingSymbol != "" {
		priceSymbol = intent.Instrument.UnderlyingSymbol
	}

	lastPrice, ok := b.prices[priceSymbol]
	if (!ok || lastPrice <= 0) && !isOption {
		if intent.Direction.IsExit() {
			if pos, posOk := b.positions[string(intent.Symbol)]; posOk && pos.avgCost > 0 {
				lastPrice = pos.avgCost
				ok = true
			}
		}
		if !ok || lastPrice <= 0 {
			return "", fmt.Errorf("simbroker: no price available for %s — cannot fill order", priceSymbol)
		}
	}

	barTime := b.barTimes[priceSymbol]

	var fillPrice float64
	var side string
	if isOption {
		switch {
		case intent.Direction.IsExit():
			// Compute BSM exit price using current underlying price
			fillPrice = b.computeOptionExitPrice(intent, lastPrice, barTime)
			if fillPrice <= 0 {
				fillPrice = 0.01
			}
			side = "sell"
		default:
			if intent.LimitPrice <= 0 {
				return "", fmt.Errorf("simbroker: options entry has no limit price for %s", intent.Symbol)
			}
			fillPrice = intent.LimitPrice
			side = "buy"
		}
	} else {
		slippage := lastPrice * float64(b.slippageBPS) / 10000.0
		switch intent.Direction {
		case domain.DirectionLong:
			side = "buy"
			fillPrice = lastPrice + slippage
		case domain.DirectionShort:
			side = "sell"
			fillPrice = lastPrice - slippage
		default:
			// Exit: determine side from existing position.
			// Use limit price when set (strategy-managed stops/targets).
			if pos, ok := b.positions[string(intent.Symbol)]; ok && pos.side == "sell" {
				side = "buy"
				if intent.LimitPrice > 0 {
					fillPrice = intent.LimitPrice + slippage
				} else {
					fillPrice = lastPrice + slippage
				}
			} else {
				side = "sell"
				if intent.LimitPrice > 0 {
					fillPrice = intent.LimitPrice - slippage
				} else {
					fillPrice = lastPrice - slippage
				}
			}
		}
	}

	// Generate order ID.
	seq := b.orderSeq.Add(1)
	orderID := fmt.Sprintf("sim-%d", seq)

	// Record the order.
	b.orders[orderID] = &simOrder{
		intent:    intent,
		orderID:   orderID,
		fillPrice: fillPrice,
		fillQty:   intent.Quantity,
		filledAt:  barTime,
		side:      side,
	}

	// Update position tracking.
	posKey := string(intent.Symbol)
	pos, exists := b.positions[posKey]
	if !exists {
		pos = &position{symbol: intent.Symbol}
		b.positions[posKey] = pos
	}

	switch side {
	case "buy":
		b.cash -= fillPrice * intent.Quantity
		switch {
		case pos.quantity == 0:
			pos.side = "buy"
			pos.avgCost = fillPrice
			pos.quantity = intent.Quantity
		case pos.side == "buy":
			totalCost := pos.avgCost*pos.quantity + fillPrice*intent.Quantity
			pos.quantity += intent.Quantity
			pos.avgCost = totalCost / pos.quantity
		default:
			pos.quantity -= intent.Quantity
			if pos.quantity <= 0 {
				pos.quantity = -pos.quantity
				pos.side = "buy"
				pos.avgCost = fillPrice
			}
		}
	case "sell":
		b.cash += fillPrice * intent.Quantity
		switch {
		case pos.quantity == 0:
			pos.side = "sell"
			pos.avgCost = fillPrice
			pos.quantity = intent.Quantity
		case pos.side == "sell":
			totalCost := pos.avgCost*pos.quantity + fillPrice*intent.Quantity
			pos.quantity += intent.Quantity
			pos.avgCost = totalCost / pos.quantity
		default:
			pos.quantity -= intent.Quantity
			if pos.quantity <= 0 {
				pos.quantity = -pos.quantity
				pos.side = "sell"
				pos.avgCost = fillPrice
			}
		}
	}

	b.log.Debug().
		Str("order_id", orderID).
		Str("symbol", string(intent.Symbol)).
		Str("side", side).
		Float64("fill_price", fillPrice).
		Float64("last_price", lastPrice).
		Float64("quantity", intent.Quantity).
		Int64("slippage_bps", b.slippageBPS).
		Msg("order filled")

	// Non-blocking send to fill channel for OrderStreamPort consumers.
	// Skipped when DisableFillChan is set (syncFill mode handles fills inline).
	if !b.disableFillChan {
		select {
		case b.fillCh <- ports.OrderUpdate{
			BrokerOrderID:  orderID,
			Event:          "fill",
			Qty:            intent.Quantity,
			Price:          fillPrice,
			FilledQty:      intent.Quantity,
			FilledAvgPrice: fillPrice,
			FilledAt:       barTime,
		}:
		default:
		}
	}

	return orderID, nil
}

// CancelOrder is a no-op for SimBroker since all orders fill instantly.
func (b *Broker) CancelOrder(_ context.Context, orderID string) error {
	b.mu.RLock()
	_, exists := b.orders[orderID]
	b.mu.RUnlock()
	if !exists {
		return fmt.Errorf("simbroker: order %s not found", orderID)
	}
	return nil
}

// GetOrderStatus always returns "filled" for known orders since SimBroker fills instantly.
func (b *Broker) GetOrderStatus(_ context.Context, orderID string) (string, error) {
	b.mu.RLock()
	_, exists := b.orders[orderID]
	b.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("simbroker: order %s not found", orderID)
	}
	return "filled", nil
}

// GetPositions returns the current simulated positions as domain.Trade slices.
func (b *Broker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	trades := make([]domain.Trade, 0, len(b.positions))
	for _, pos := range b.positions {
		if pos.quantity <= 0 {
			continue
		}
		trades = append(trades, domain.Trade{
			Symbol:   pos.symbol,
			Side:     pos.side,
			Quantity: pos.quantity,
			Price:    pos.avgCost,
			Status:   "open",
		})
	}
	return trades, nil
}

func (b *Broker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}

func (b *Broker) CancelAllOpenOrders(_ context.Context) (int, error) {
	return 0, nil
}

func (b *Broker) GetPosition(_ context.Context, symbol domain.Symbol) (float64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pos, ok := b.positions[string(symbol)]
	if !ok || pos.quantity <= 0 {
		return 0, nil
	}
	return pos.quantity, nil
}

func (b *Broker) ClosePosition(_ context.Context, _ domain.Symbol) (string, error) {
	return "", nil
}

func (b *Broker) GetOrderDetails(_ context.Context, orderID string) (ports.OrderDetails, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ord, ok := b.orders[orderID]
	if !ok {
		return ports.OrderDetails{}, fmt.Errorf("simbroker: order %s: %w", orderID, ports.ErrOrderNotFound)
	}
	return ports.OrderDetails{
		Status:         "filled",
		FilledQty:      ord.fillQty,
		FilledAvgPrice: ord.fillPrice,
		FilledAt:       ord.filledAt,
	}, nil
}

// GetFillPrice returns the fill price for a given order ID. Used by the backtest
// collector to access actual fill details without relying on status string parsing.
func (b *Broker) GetFillPrice(orderID string) (float64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ord, ok := b.orders[orderID]
	if !ok {
		return 0, false
	}
	return ord.fillPrice, true
}

// Stats returns summary statistics about the SimBroker's activity.
func (b *Broker) Stats() (totalOrders int, symbolsTraded int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.orders), len(b.positions)
}

// GetPrice returns the last known price for a symbol. Used by the passthrough
// QuoteProvider in backtest mode so the SlippageGuard can check bid/ask.
func (b *Broker) GetPrice(symbol domain.Symbol) (float64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	p, ok := b.prices[symbol]
	return p, ok
}

func (b *Broker) GetQuote(_ context.Context, symbol domain.Symbol) (bid float64, ask float64, err error) {
	b.mu.RLock()
	lastPrice, ok := b.prices[symbol]
	b.mu.RUnlock()
	if !ok {
		return 0, 0, fmt.Errorf("simbroker: no price available for %s", symbol)
	}
	spreadHalf := lastPrice * float64(b.slippageBPS) / 10000.0 / 2.0
	return lastPrice - spreadHalf, lastPrice + spreadHalf, nil
}

func (b *Broker) GetAccountBuyingPower(_ context.Context) (ports.BuyingPower, error) {
	b.mu.RLock()
	cash := b.cash
	b.mu.RUnlock()
	return ports.BuyingPower{
		DayTradingBuyingPower:    cash,
		EffectiveBuyingPower:     cash,
		NonMarginableBuyingPower: cash,
	}, nil
}

func (b *Broker) GetAccountEquity(_ context.Context) (float64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	equity := b.cash
	for _, pos := range b.positions {
		if pos.quantity <= 0 {
			continue
		}
		currentPrice, ok := b.prices[pos.symbol]
		if !ok {
			currentPrice = pos.avgCost
		}
		switch pos.side {
		case "buy":
			equity += currentPrice * pos.quantity
		case "sell":
			equity += (2*pos.avgCost - currentPrice) * pos.quantity
		}
	}
	return equity, nil
}

func (b *Broker) SubscribeOrderUpdates(_ context.Context) (<-chan ports.OrderUpdate, error) {
	return b.fillCh, nil
}

// computeOptionExitPrice computes the BSM price for an options exit using the
// current underlying price and the entry metadata (strike, DTE, IV, right).
// Applies intraday IV decay and bid-ask spread for realistic pricing.
func (b *Broker) computeOptionExitPrice(intent domain.OrderIntent, underlyingPrice float64, barTime time.Time) float64 {
	if intent.Meta == nil {
		return 0
	}

	strikeStr := intent.Meta["strike"]
	expiryStr := intent.Meta["expiry"]
	rightStr := intent.Meta["option_right"]

	if strikeStr == "" || expiryStr == "" {
		if pos, ok := b.positions[string(intent.Symbol)]; ok && pos.avgCost > 0 {
			return pos.avgCost
		}
		return 0
	}

	var strike float64
	_, _ = fmt.Sscanf(strikeStr, "%f", &strike)

	expiry, err := time.Parse("2006-01-02", expiryStr)
	if err != nil {
		return 0
	}

	underlying := intent.Meta["underlying"]
	if underlying == "" {
		underlying = string(domain.UnderlyingFromOCC(intent.Symbol))
	}

	// Multi-day holds: use historical bid from DoltHub (different daily snapshot).
	entryDateStr := intent.Meta["entry_date"]
	exitDate := barTime.Format("2006-01-02")
	isMultiDay := entryDateStr != "" && entryDateStr != exitDate

	if isMultiDay && b.historicalOptions != nil {
		right := domain.OptionRightCall
		if rightStr == "PUT" {
			right = domain.OptionRightPut
		}
		row, err := b.historicalOptions.GetHistoricalContract(
			context.Background(), domain.Symbol(underlying), barTime,
			strike, expiry, right)
		if err == nil && row != nil && row.Bid > 0 {
			return row.Bid
		}
	}

	// Same-day exits: delta approximation using historical data.
	// exit_premium ≈ entry_premium + delta × (underlying_now - underlying_at_entry) - half_spread
	// This is simpler and more accurate than BSM for intraday holds.
	var entryPremium, delta float64
	_, _ = fmt.Sscanf(intent.Meta["premium"], "%f", &entryPremium)
	_, _ = fmt.Sscanf(intent.Meta["delta_at_entry"], "%f", &delta)

	if entryPremium <= 0 {
		if pos, ok := b.positions[string(intent.Symbol)]; ok && pos.avgCost > 0 {
			entryPremium = pos.avgCost
		}
	}
	if entryPremium <= 0 || delta == 0 {
		return entryPremium // can't compute, return entry (breakeven)
	}

	// For puts, delta is negative — the formula naturally works:
	// underlying drops → (underlyingPrice - entryUnderlying) < 0 → negative * negative delta = positive P&L
	var entryUnderlying float64
	_, _ = fmt.Sscanf(intent.Meta["entry_underlying"], "%f", &entryUnderlying)
	if entryUnderlying <= 0 {
		// Fallback: use strike as proxy (valid for ~0.50 delta)
		entryUnderlying = strike
	}

	underlyingMove := underlyingPrice - entryUnderlying
	premiumChange := delta * underlyingMove
	exitPremium := entryPremium + premiumChange

	// Apply half-spread cost (selling at bid) — tiered by premium level
	var spreadPct float64
	switch {
	case entryPremium >= 10.0:
		spreadPct = 0.003 // 0.3% for expensive options (deep ITM, liquid)
	case entryPremium >= 5.0:
		spreadPct = 0.005 // 0.5% for mid-range options
	case entryPremium >= 2.0:
		spreadPct = 0.008 // 0.8% for cheaper options
	default:
		spreadPct = 0.015 // 1.5% for very cheap / illiquid options
	}
	spread := entryPremium * spreadPct
	exitPremium -= spread

	if exitPremium < 0.01 {
		exitPremium = 0.01
	}

	return exitPremium
}
