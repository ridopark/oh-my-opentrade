// Package backtest provides a result collector and reporter for backtesting.
// The Collector subscribes to FillReceived and MarketBarReceived events,
// tracks trades and equity, then produces a Result with key metrics.
package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Config holds collector configuration.
type Config struct {
	InitialEquity  float64 // starting capital
	PeriodsPerYear float64 // for Sharpe annualization (default 252)
}

// TradeRecord captures a single completed trade.
type TradeRecord struct {
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"`
	Direction string    `json:"direction,omitempty"` // "LONG", "SHORT", or "CLOSE"
	Quantity  float64   `json:"quantity"`
	Price     float64   `json:"price"`
	FilledAt  time.Time `json:"filled_at"`
	Strategy  string    `json:"strategy,omitempty"`
	Rationale string    `json:"rationale,omitempty"` // exit reason (e.g. "exit_monitor:VOLATILITY_STOP:...")
	Regime        string `json:"regime,omitempty"`         // EMA regime: TREND / BALANCE / REVERSAL
	VIXBucket     string `json:"vix_bucket,omitempty"`     // LOW_VOL / NORMAL / HIGH_VOL
	MarketContext string  `json:"market_context,omitempty"` // composite: e.g. "NORMAL | NR7 | VWAP+"
	PnL           float64 `json:"pnl,omitempty"`
	Multiplier    float64 `json:"-"` // 100 for options, 1 for equity (internal use)
}

// Result holds the computed backtest metrics.
type Result struct {
	InitialEquity float64       `json:"initial_equity"`
	FinalEquity   float64       `json:"final_equity"`
	TotalReturn   float64       `json:"total_return_pct"`
	TotalPnL      float64       `json:"total_pnl"`
	TradeCount    int           `json:"trade_count"`
	WinCount      int           `json:"win_count"`
	LossCount     int           `json:"loss_count"`
	WinRate       float64       `json:"win_rate_pct"`
	MaxDrawdown   float64       `json:"max_drawdown_pct"`
	SharpeRatio   float64       `json:"sharpe_ratio"`
	ProfitFactor  float64       `json:"profit_factor"`
	AvgWin        float64       `json:"avg_win"`
	AvgLoss       float64       `json:"avg_loss"`
	LargestWin    float64       `json:"largest_win"`
	LargestLoss   float64       `json:"largest_loss"`
	Trades        []TradeRecord `json:"trades"`
}

// Collector aggregates fill and bar events to produce backtest metrics.
type Collector struct {
	cfg Config
	log zerolog.Logger

	mu          sync.Mutex
	cash        float64
	peakEquity  float64
	maxDrawdown float64
	trades      []TradeRecord
	equityCurve []float64 // equity sampled at each bar
	lastPrices  map[string]float64
	openBuys    map[string][]TradeRecord // symbol → open long fills (FIFO)
	openSells   map[string][]TradeRecord // symbol → open short fills (FIFO)
}

// NewCollector creates a Collector and subscribes to events on the bus.
func NewCollector(bus ports.EventBusPort, cfg Config, log zerolog.Logger) (*Collector, error) {
	if cfg.PeriodsPerYear == 0 {
		cfg.PeriodsPerYear = 252
	}
	c := &Collector{
		cfg:        cfg,
		log:        log.With().Str("component", "backtest_collector").Logger(),
		cash:       cfg.InitialEquity,
		peakEquity: cfg.InitialEquity,
		lastPrices: make(map[string]float64),
		openBuys:   make(map[string][]TradeRecord),
		openSells:  make(map[string][]TradeRecord),
	}

	ctx := context.Background()
	if err := bus.Subscribe(ctx, domain.EventFillReceived, c.onFill); err != nil {
		return nil, fmt.Errorf("backtest collector: failed to subscribe to FillReceived: %w", err)
	}
	if err := bus.Subscribe(ctx, domain.EventMarketBarReceived, c.onBar); err != nil {
		return nil, fmt.Errorf("backtest collector: failed to subscribe to MarketBarReceived: %w", err)
	}
	return c, nil
}

// onFill processes a FillReceived event.
func (c *Collector) onFill(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}

	symbol, _ := payload["symbol"].(string)
	side, _ := payload["side"].(string)
	direction, _ := payload["direction"].(string)
	quantity, _ := payload["quantity"].(float64)
	price, _ := payload["price"].(float64)
	filledAt, _ := payload["filled_at"].(time.Time)
	strategy, _ := payload["strategy"].(string)
	rationale, _ := payload["rationale"].(string)
	regime, _ := payload["regime"].(string)
	vixBucket, _ := payload["vix_bucket"].(string)
	marketContext, _ := payload["market_context"].(string)
	instrumentType, _ := payload["instrument_type"].(string)

	// Options contracts have a 100x multiplier
	multiplier := 1.0
	if instrumentType == "OPTION" {
		multiplier = 100.0
	}

	if symbol == "" || quantity == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Map direction to position direction label.
	posDir := ""
	switch domain.Direction(direction) {
	case domain.DirectionLong:
		posDir = "LONG"
	case domain.DirectionShort:
		posDir = "SHORT"
	case domain.DirectionCloseLong:
		posDir = "CLOSE_LONG"
	case domain.DirectionCloseShort:
		posDir = "CLOSE_SHORT"
	}

	tr := TradeRecord{
		Symbol:    symbol,
		Side:      strings.ToLower(side),
		Direction: posDir,
		Quantity:  quantity,
		Price:     price,
		FilledAt:  filledAt,
		Strategy:  strategy,
		Rationale:     rationale,
		Regime:        regime,
		VIXBucket:     vixBucket,
		MarketContext: marketContext,
	}

	// Use direction to classify entries vs exits.
	switch domain.Direction(direction) {
	case domain.DirectionLong:
		// Long entry: buy to open.
		tr.Multiplier = multiplier
		c.openBuys[symbol] = append(c.openBuys[symbol], tr)
		c.cash -= quantity * price * multiplier

	case domain.DirectionShort:
		// Short entry: sell to open.
		tr.Multiplier = multiplier
		c.openSells[symbol] = append(c.openSells[symbol], tr)
		c.cash += quantity * price * multiplier

	case domain.DirectionCloseLong, domain.DirectionCloseShort:
		// Exit: close whichever position is open (long or short).
		if opens := c.openBuys[symbol]; len(opens) > 0 {
			// Closing a long: PnL = (exit - entry) × qty × multiplier.
			remainQty := quantity
			var realizedPnL float64
			entryMult := 1.0
			for len(opens) > 0 && remainQty > 0 {
				entry := opens[0]
				if entry.Multiplier > 0 {
					entryMult = entry.Multiplier
				}
				matchQty := math.Min(entry.Quantity, remainQty)
				realizedPnL += matchQty * (price - entry.Price) * entryMult
				entry.Quantity -= matchQty
				remainQty -= matchQty
				if entry.Quantity <= 0 {
					opens = opens[1:]
				} else {
					opens[0] = entry
				}
			}
			c.openBuys[symbol] = opens
			c.cash += (quantity - remainQty) * price * entryMult
			tr.PnL = realizedPnL
		} else if opens := c.openSells[symbol]; len(opens) > 0 {
			// Closing a short: PnL = (entry - exit) × qty × multiplier.
			remainQty := quantity
			var realizedPnL float64
			entryMult := 1.0
			for len(opens) > 0 && remainQty > 0 {
				entry := opens[0]
				if entry.Multiplier > 0 {
					entryMult = entry.Multiplier
				}
				matchQty := math.Min(entry.Quantity, remainQty)
				realizedPnL += matchQty * (entry.Price - price) * entryMult
				entry.Quantity -= matchQty
				remainQty -= matchQty
				if entry.Quantity <= 0 {
					opens = opens[1:]
				} else {
					opens[0] = entry
				}
			}
			c.openSells[symbol] = opens
			c.cash -= (quantity - remainQty) * price * entryMult
			tr.PnL = realizedPnL
		}

	default:
		// Legacy (no direction): fall back to side-based matching.
		switch strings.ToLower(side) {
		case "buy":
			c.openBuys[symbol] = append(c.openBuys[symbol], tr)
			c.cash -= quantity * price
		case "sell":
			opens := c.openBuys[symbol]
			remainQty := quantity
			var realizedPnL float64
			for len(opens) > 0 && remainQty > 0 {
				entry := opens[0]
				matchQty := math.Min(entry.Quantity, remainQty)
				realizedPnL += matchQty * (price - entry.Price)
				entry.Quantity -= matchQty
				remainQty -= matchQty
				if entry.Quantity <= 0 {
					opens = opens[1:]
				} else {
					opens[0] = entry
				}
			}
			c.openBuys[symbol] = opens
			c.cash += (quantity - remainQty) * price
			tr.PnL = realizedPnL
		}
	}

	c.trades = append(c.trades, tr)
	return nil
}

// onBar processes a MarketBarReceived event to track last prices and equity curve.
func (c *Collector) onBar(_ context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastPrices[string(bar.Symbol)] = bar.Close

	// Mark-to-market equity.
	equity := c.cash
	for sym, opens := range c.openBuys {
		lastPrice := c.lastPrices[sym]
		for _, tr := range opens {
			price := lastPrice
			if price <= 0 {
				price = tr.Price // fallback to entry price
			}
			equity += tr.Quantity * price
		}
	}
	for sym, opens := range c.openSells {
		lastPrice := c.lastPrices[sym]
		for _, tr := range opens {
			price := lastPrice
			if price <= 0 {
				price = tr.Price
			}
			// Short obligation: subtract current cost to cover.
			// Cash already includes sale proceeds (qty * entryPrice),
			// so we subtract the current buyback cost (qty * currentPrice).
			equity -= tr.Quantity * price
		}
	}
	c.equityCurve = append(c.equityCurve, equity)

	if equity > c.peakEquity {
		c.peakEquity = equity
	}
	if c.peakEquity > 0 {
		dd := (c.peakEquity - equity) / c.peakEquity
		if dd > c.maxDrawdown {
			c.maxDrawdown = dd
		}
	}

	return nil
}

// Result computes and returns the final backtest metrics.
func (c *Collector) Result() Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Final mark-to-market equity.
	finalEquity := c.cash
	for sym, opens := range c.openBuys {
		lastPrice := c.lastPrices[sym]
		for _, tr := range opens {
			price := lastPrice
			if price <= 0 {
				price = tr.Price
			}
			finalEquity += tr.Quantity * price
		}
	}
	for sym, opens := range c.openSells {
		lastPrice := c.lastPrices[sym]
		for _, tr := range opens {
			price := lastPrice
			if price <= 0 {
				price = tr.Price
			}
			// Short obligation: subtract current cost to cover.
			finalEquity -= tr.Quantity * price
		}
	}

	// Compute win/loss stats from trades with realized P&L (exits).
	var grossProfit, grossLoss float64
	var tradeCount, winCount, lossCount int
	var largestWin, largestLoss float64
	for _, tr := range c.trades {
		if tr.PnL == 0 {
			continue
		}
		tradeCount++
		if tr.PnL > 0 {
			winCount++
			grossProfit += tr.PnL
			if tr.PnL > largestWin {
				largestWin = tr.PnL
			}
		} else if tr.PnL < 0 {
			lossCount++
			grossLoss += math.Abs(tr.PnL)
			if math.Abs(tr.PnL) > math.Abs(largestLoss) {
				largestLoss = tr.PnL
			}
		}
	}

	realizedPnL := grossProfit - grossLoss

	r := Result{
		InitialEquity: c.cfg.InitialEquity,
		FinalEquity:   finalEquity,
		TotalPnL:      realizedPnL,
		MaxDrawdown:   c.maxDrawdown * 100,
		Trades:        c.trades,
		TradeCount:    tradeCount,
		WinCount:      winCount,
		LossCount:     lossCount,
		LargestWin:    largestWin,
		LargestLoss:   largestLoss,
	}

	if c.cfg.InitialEquity > 0 {
		r.TotalReturn = realizedPnL / c.cfg.InitialEquity * 100
	}

	if r.TradeCount > 0 {
		r.WinRate = float64(r.WinCount) / float64(r.TradeCount) * 100
	}
	if r.WinCount > 0 {
		r.AvgWin = grossProfit / float64(r.WinCount)
	}
	if r.LossCount > 0 {
		r.AvgLoss = grossLoss / float64(r.LossCount)
	}
	if grossLoss > 0 {
		r.ProfitFactor = grossProfit / grossLoss
	}

	// Sharpe ratio from equity curve returns.
	r.SharpeRatio = c.computeSharpe()

	return r
}

// computeSharpe calculates an annualized Sharpe ratio from the equity curve.
func (c *Collector) computeSharpe() float64 {
	if len(c.equityCurve) < 2 {
		return 0
	}

	returns := make([]float64, 0, len(c.equityCurve)-1)
	for i := 1; i < len(c.equityCurve); i++ {
		if c.equityCurve[i-1] == 0 {
			continue
		}
		r := (c.equityCurve[i] - c.equityCurve[i-1]) / c.equityCurve[i-1]
		returns = append(returns, r)
	}

	if len(returns) < 2 {
		return 0
	}

	// Mean return.
	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	// Standard deviation.
	var sumSq float64
	for _, r := range returns {
		d := r - mean
		sumSq += d * d
	}
	stdDev := math.Sqrt(sumSq / float64(len(returns)-1))

	if stdDev == 0 {
		return 0
	}

	// Annualize.
	return (mean / stdDev) * math.Sqrt(c.cfg.PeriodsPerYear)
}

// PrintReport writes a human-readable report to stdout.
func (r *Result) PrintReport() {
	fmt.Println("\n=== BACKTEST RESULTS ===")
	fmt.Printf("Initial Equity:   $%.2f\n", r.InitialEquity)
	fmt.Printf("Final Equity:     $%.2f\n", r.FinalEquity)
	fmt.Printf("Total P&L:        $%.2f (%.2f%%)\n", r.TotalPnL, r.TotalReturn)
	fmt.Printf("Trade Count:      %d\n", r.TradeCount)
	fmt.Printf("Win Rate:         %.1f%% (%d wins / %d losses)\n", r.WinRate, r.WinCount, r.LossCount)
	fmt.Printf("Max Drawdown:     %.2f%%\n", r.MaxDrawdown)
	fmt.Printf("Sharpe Ratio:     %.3f\n", r.SharpeRatio)
	fmt.Printf("Profit Factor:    %.2f\n", r.ProfitFactor)
	fmt.Printf("Avg Win:          $%.2f\n", r.AvgWin)
	fmt.Printf("Avg Loss:         $%.2f\n", r.AvgLoss)
	fmt.Printf("Largest Win:      $%.2f\n", r.LargestWin)
	fmt.Printf("Largest Loss:     $%.2f\n", r.LargestLoss)

	if r.TradeCount > 0 {
		fmt.Println("\n--- Trade Log ---")
		// Sort trades by time.
		sorted := make([]TradeRecord, len(r.Trades))
		copy(sorted, r.Trades)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].FilledAt.Before(sorted[j].FilledAt) })
		for _, t := range sorted {
			stratName := t.Strategy
			if stratName == "" {
				stratName = "unknown"
			}
			if t.Side == "sell" {
				fmt.Printf("  %s [%s] %s %s %.0f @ $%.2f  P&L: $%.2f\n",
					t.FilledAt.Format("2006-01-02 15:04"), stratName, t.Side, t.Symbol, t.Quantity, t.Price, t.PnL)
			} else {
				fmt.Printf("  %s [%s] %s %s %.0f @ $%.2f\n",
					t.FilledAt.Format("2006-01-02 15:04"), stratName, t.Side, t.Symbol, t.Quantity, t.Price)
			}
		}
	}
}

// WriteJSON writes the result to a JSON file.
func (r *Result) WriteJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("backtest: failed to marshal result: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
