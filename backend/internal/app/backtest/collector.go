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
	Symbol   string    `json:"symbol"`
	Side     string    `json:"side"`
	Quantity float64   `json:"quantity"`
	Price    float64   `json:"price"`
	FilledAt time.Time `json:"filled_at"`
	PnL      float64   `json:"pnl,omitempty"`
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
	quantity, _ := payload["quantity"].(float64)
	price, _ := payload["price"].(float64)
	filledAt, _ := payload["filled_at"].(time.Time)

	if symbol == "" || quantity == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	tr := TradeRecord{
		Symbol:   symbol,
		Side:     side,
		Quantity: quantity,
		Price:    price,
		FilledAt: filledAt,
	}

	switch side {
	case "buy":
		// Opening a long: record as open buy; deduct cash.
		c.openBuys[symbol] = append(c.openBuys[symbol], tr)
		c.cash -= quantity * price
	case "sell":
		// Closing a long (FIFO): match against open buys.
		opens := c.openBuys[symbol]
		remainQty := quantity
		var realizedPnL float64
		for len(opens) > 0 && remainQty > 0 {
			entry := opens[0]
			matchQty := math.Min(entry.Quantity, remainQty)
			pnl := matchQty * (price - entry.Price)
			realizedPnL += pnl

			entry.Quantity -= matchQty
			remainQty -= matchQty
			if entry.Quantity <= 0 {
				opens = opens[1:]
			} else {
				opens[0] = entry
			}
		}
		c.openBuys[symbol] = opens
		// Only credit cash for the matched quantity, not the full sell quantity.
		// Any excess (sell > open buys) is unmatched and should not affect cash.
		matchedQty := quantity - remainQty
		c.cash += matchedQty * price
		tr.PnL = realizedPnL
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
			equity += tr.Quantity * lastPrice
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
			finalEquity += tr.Quantity * lastPrice
		}
	}

	r := Result{
		InitialEquity: c.cfg.InitialEquity,
		FinalEquity:   finalEquity,
		TotalPnL:      finalEquity - c.cfg.InitialEquity,
		MaxDrawdown:   c.maxDrawdown * 100, // as percentage
		Trades:        c.trades,
	}

	if c.cfg.InitialEquity > 0 {
		r.TotalReturn = (finalEquity - c.cfg.InitialEquity) / c.cfg.InitialEquity * 100
	}

	// Compute win/loss stats from sell trades (realized P&L).
	var grossProfit, grossLoss float64
	for _, tr := range c.trades {
		if tr.Side != "sell" {
			continue
		}
		r.TradeCount++
		if tr.PnL > 0 {
			r.WinCount++
			grossProfit += tr.PnL
			if tr.PnL > r.LargestWin {
				r.LargestWin = tr.PnL
			}
		} else if tr.PnL < 0 {
			r.LossCount++
			grossLoss += math.Abs(tr.PnL)
			if math.Abs(tr.PnL) > math.Abs(r.LargestLoss) {
				r.LargestLoss = tr.PnL
			}
		}
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
			if t.Side == "sell" {
				fmt.Printf("  %s %s %s %.0f @ $%.2f  P&L: $%.2f\n",
					t.FilledAt.Format("2006-01-02 15:04"), t.Side, t.Symbol, t.Quantity, t.Price, t.PnL)
			} else {
				fmt.Printf("  %s %s %s %.0f @ $%.2f\n",
					t.FilledAt.Format("2006-01-02 15:04"), t.Side, t.Symbol, t.Quantity, t.Price)
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
