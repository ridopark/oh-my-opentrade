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
	"strconv"
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
	Tags              map[string]string `json:"tags,omitempty"`
	PnL               float64 `json:"pnl,omitempty"`
	PremiumMFE        float64 `json:"premium_mfe_pct,omitempty"`
	PremiumMAE        float64 `json:"premium_mae_pct,omitempty"`
	MinutesToFirstProfit float64 `json:"minutes_to_first_profit,omitempty"`
	MinutesHeld          float64 `json:"minutes_held,omitempty"`
	Multiplier        float64 `json:"-"` // 100 for options, 1 for equity (internal use)
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
	lastPrices  map[string]float64
	openBuys    map[string][]TradeRecord // symbol → open long fills (FIFO)
	openSells   map[string][]TradeRecord // symbol → open short fills (FIFO)

	// Incremental mark-to-market: track position value per symbol so onBar
	// only recomputes the single symbol that changed, not all positions.
	posValue      map[string]float64 // symbol → current mark-to-market position value
	totalPosValue float64            // running sum of all posValue entries

	// Daily Sharpe: aggregate one return per trading day, annualize with sqrt(252).
	prevDayEquity float64    // equity at end of previous trading day
	currentDay    int        // year*10000 + month*100 + day (cheap date comparison)
	latestEquity  float64    // most recent equity snapshot (for end-of-day capture)
	returnSum     float64
	returnSumSq   float64
	returnCount   int
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
		posValue:   make(map[string]float64),
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
	premiumMFEStr, _ := payload["premium_mfe_pct"].(string)
	premiumMAEStr, _ := payload["premium_mae_pct"].(string)
	minutesToFirstProfitStr, _ := payload["minutes_to_first_profit"].(string)
	minutesHeldStr, _ := payload["minutes_held"].(string)
	signalTags, _ := payload["signal_tags"].(map[string]string)

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
		Tags:          signalTags,
	}

	// Parse MFE/MAE trade analytics from fill payload.
	if premiumMFEStr != "" {
		if v, err := strconv.ParseFloat(premiumMFEStr, 64); err == nil {
			tr.PremiumMFE = v
		}
	}
	if premiumMAEStr != "" {
		if v, err := strconv.ParseFloat(premiumMAEStr, 64); err == nil {
			tr.PremiumMAE = v
		}
	}
	if minutesToFirstProfitStr != "" {
		if v, err := strconv.ParseFloat(minutesToFirstProfitStr, 64); err == nil {
			tr.MinutesToFirstProfit = v
		}
	}
	if minutesHeldStr != "" {
		if v, err := strconv.ParseFloat(minutesHeldStr, 64); err == nil {
			tr.MinutesHeld = v
		}
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

	// Recompute posValue for this symbol after position change.
	c.recomputePosValue(symbol)

	return nil
}

// recomputePosValue recalculates the mark-to-market position value for a symbol.
// Called after fills (rare) to keep incremental equity tracking correct.
func (c *Collector) recomputePosValue(symbol string) {
	lastPrice := c.lastPrices[symbol]
	oldPV := c.posValue[symbol]
	var pv float64
	for _, tr := range c.openBuys[symbol] {
		price := lastPrice
		if price <= 0 {
			price = tr.Price
		}
		mult := tr.Multiplier
		if mult <= 0 {
			mult = 1
		}
		pv += tr.Quantity * price * mult
	}
	for _, tr := range c.openSells[symbol] {
		price := lastPrice
		if price <= 0 {
			price = tr.Price
		}
		mult := tr.Multiplier
		if mult <= 0 {
			mult = 1
		}
		pv -= tr.Quantity * price * mult
	}
	c.posValue[symbol] = pv
	c.totalPosValue += pv - oldPV
}

// onBar processes a MarketBarReceived event to track last prices and equity.
// Uses incremental mark-to-market: only recomputes position value for the
// bar's symbol rather than iterating all open positions on every bar.
func (c *Collector) onBar(_ context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	sym := string(bar.Symbol)
	c.lastPrices[sym] = bar.Close

	// Recompute position value only for this symbol.
	oldPV := c.posValue[sym]
	var newPV float64
	if opens := c.openBuys[sym]; len(opens) > 0 {
		for _, tr := range opens {
			mult := tr.Multiplier
			if mult <= 0 {
				mult = 1
			}
			newPV += tr.Quantity * bar.Close * mult
		}
	}
	if opens := c.openSells[sym]; len(opens) > 0 {
		for _, tr := range opens {
			mult := tr.Multiplier
			if mult <= 0 {
				mult = 1
			}
			newPV -= tr.Quantity * bar.Close * mult
		}
	}
	c.posValue[sym] = newPV

	// Compute equity: cash + sum(posValue). Use totalPosValue to avoid
	// iterating all symbols — we track the running total and apply deltas.
	c.totalPosValue += newPV - oldPV
	equity := c.cash + c.totalPosValue

	// Daily Sharpe: record one return per trading day (close-to-close).
	y, m, d := bar.Time.Date()
	barDay := y*10000 + int(m)*100 + d
	if c.currentDay == 0 {
		// First bar ever — just start tracking the day.
		c.currentDay = barDay
	} else if barDay != c.currentDay {
		// Day changed — compute daily return from previous day's close.
		if c.prevDayEquity > 0 {
			r := (c.latestEquity - c.prevDayEquity) / c.prevDayEquity
			c.returnSum += r
			c.returnSumSq += r * r
			c.returnCount++
		}
		c.prevDayEquity = c.latestEquity
		c.currentDay = barDay
	}
	c.latestEquity = equity

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

	// Final mark-to-market equity (already tracked incrementally).
	finalEquity := c.cash + c.totalPosValue

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

// computeSharpe calculates an annualized Sharpe ratio from daily returns.
// Flushes the last trading day before computing.
func (c *Collector) computeSharpe() float64 {
	// Flush the final trading day's return (close-to-close).
	if c.prevDayEquity > 0 && c.latestEquity > 0 {
		r := (c.latestEquity - c.prevDayEquity) / c.prevDayEquity
		c.returnSum += r
		c.returnSumSq += r * r
		c.returnCount++
	}

	n := float64(c.returnCount)
	if n < 2 {
		return 0
	}

	mean := c.returnSum / n
	// Var = E[X²] - E[X]², with Bessel's correction.
	variance := (c.returnSumSq - c.returnSum*c.returnSum/n) / (n - 1)
	if variance <= 0 {
		return 0
	}

	return (mean / math.Sqrt(variance)) * math.Sqrt(c.cfg.PeriodsPerYear)
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
