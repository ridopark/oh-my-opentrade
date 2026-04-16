package flow

import (
	"sync"
	"time"
)

// VenueTrade is a single trade tick from any crypto venue.
type VenueTrade struct {
	Venue     string    // "hyperliquid", "binance", "coinbase"
	Symbol    string    // normalized: "BTC/USD"
	Price     float64
	Size      float64
	TakerSide string   // "buy", "sell", ""
	Timestamp time.Time
}

// FlowScore is the aggregated flow signal for a symbol.
type FlowScore struct {
	Symbol string
	Timestamp time.Time

	// Per-window net flow: (buyVol - sellVol) / (buyVol + sellVol), in [-1, 1]
	NetFlow10s float64
	NetFlow60s float64

	// Per-window volume totals
	BuyVol10s  float64
	SellVol10s float64
	BuyVol60s  float64
	SellVol60s float64

	// Cross-venue confluence: number of venues with same-direction net flow
	VenueAgreement int // e.g. 3 means all 3 venues show net buy
	TotalVenues    int

	// Large print detection
	LargePrintCount   int     // prints > threshold in last 60s
	LargePrintNetFlow float64 // net flow from large prints only
}

// AggregatorConfig controls window sizes and memory limits.
type AggregatorConfig struct {
	Window10s       time.Duration // default 10s
	Window60s       time.Duration // default 60s
	LargePrintUSD   float64       // threshold for "large print" detection, default $50,000
	MaxTradesPerKey int           // cap per symbol-venue to prevent memory leak, default 10000
	EvictInterval   time.Duration // how often to garbage-collect old trades, default 5s
}

// DefaultAggregatorConfig returns sensible defaults.
func DefaultAggregatorConfig() AggregatorConfig {
	return AggregatorConfig{
		Window10s:       10 * time.Second,
		Window60s:       60 * time.Second,
		LargePrintUSD:   50_000,
		MaxTradesPerKey: 10_000,
		EvictInterval:   5 * time.Second,
	}
}

// Aggregator maintains rolling windows of trade flow across venues.
type Aggregator struct {
	cfg AggregatorConfig
	mu  sync.RWMutex
	// trades indexed by symbol -> venue -> time-ordered slice
	trades map[string]map[string][]VenueTrade
	// nowFn allows tests to inject a clock
	nowFn func() time.Time
}

// NewAggregator creates a new flow aggregator with the given config.
func NewAggregator(cfg AggregatorConfig) *Aggregator {
	if cfg.Window10s == 0 {
		cfg.Window10s = 10 * time.Second
	}
	if cfg.Window60s == 0 {
		cfg.Window60s = 60 * time.Second
	}
	if cfg.LargePrintUSD == 0 {
		cfg.LargePrintUSD = 50_000
	}
	if cfg.MaxTradesPerKey == 0 {
		cfg.MaxTradesPerKey = 10_000
	}
	if cfg.EvictInterval == 0 {
		cfg.EvictInterval = 5 * time.Second
	}
	return &Aggregator{
		cfg:    cfg,
		trades: make(map[string]map[string][]VenueTrade),
		nowFn:  time.Now,
	}
}

// Ingest adds a trade to the rolling buffer. Thread-safe.
func (a *Aggregator) Ingest(trade VenueTrade) {
	a.mu.Lock()
	defer a.mu.Unlock()

	venueMap, ok := a.trades[trade.Symbol]
	if !ok {
		venueMap = make(map[string][]VenueTrade)
		a.trades[trade.Symbol] = venueMap
	}

	trades := venueMap[trade.Venue]
	trades = append(trades, trade)

	// Cap per symbol-venue to prevent unbounded growth.
	if len(trades) > a.cfg.MaxTradesPerKey {
		// Drop the oldest 25% to amortize copy cost.
		drop := a.cfg.MaxTradesPerKey / 4
		trades = trades[drop:]
	}

	venueMap[trade.Venue] = trades
}

// Score computes the current flow score for a symbol from buffered trades.
func (a *Aggregator) Score(symbol string) FlowScore {
	a.mu.RLock()
	defer a.mu.RUnlock()

	now := a.nowFn()
	fs := FlowScore{
		Symbol:    symbol,
		Timestamp: now,
	}

	venueMap, ok := a.trades[symbol]
	if !ok {
		return fs
	}

	// Single pass per venue: accumulate global totals and per-venue 60s sums
	// together, eliminating the flatten allocation and avoiding a second
	// full scan. VenueAgreement is resolved in a cheap O(venues) pass over
	// the per-venue sums collected here.
	cutoff10 := now.Add(-a.cfg.Window10s)
	cutoff60 := now.Add(-a.cfg.Window60s)
	var lpBuy, lpSell float64

	type venueSums struct{ buy, sell float64 }
	perVenue := make([]venueSums, 0, len(venueMap))

	fs.TotalVenues = len(venueMap)
	for _, trades := range venueMap {
		var vs venueSums
		for _, t := range trades {
			if t.Timestamp.Before(cutoff60) {
				continue
			}
			usd := t.Price * t.Size
			switch t.TakerSide {
			case "buy":
				fs.BuyVol60s += usd
				vs.buy += usd
			case "sell":
				fs.SellVol60s += usd
				vs.sell += usd
			}
			if usd >= a.cfg.LargePrintUSD {
				fs.LargePrintCount++
				switch t.TakerSide {
				case "buy":
					lpBuy += usd
				case "sell":
					lpSell += usd
				}
			}
			if !t.Timestamp.Before(cutoff10) {
				switch t.TakerSide {
				case "buy":
					fs.BuyVol10s += usd
				case "sell":
					fs.SellVol10s += usd
				}
			}
		}
		perVenue = append(perVenue, vs)
	}

	fs.NetFlow10s = safeNetFlow(fs.BuyVol10s, fs.SellVol10s)
	fs.NetFlow60s = safeNetFlow(fs.BuyVol60s, fs.SellVol60s)
	fs.LargePrintNetFlow = safeNetFlow(lpBuy, lpSell)

	// Cross-venue confluence: O(venues) pass over pre-computed sums.
	overallDir := direction(fs.NetFlow60s)
	for _, vs := range perVenue {
		vDir := direction(safeNetFlow(vs.buy, vs.sell))
		if vDir != 0 && vDir == overallDir {
			fs.VenueAgreement++
		}
	}

	return fs
}

// Evict removes trades older than the largest window + a buffer.
// Should be called periodically (e.g. via a goroutine on EvictInterval).
func (a *Aggregator) Evict() {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Keep trades within the largest window plus 10% buffer.
	maxWindow := a.cfg.Window60s
	if a.cfg.Window10s > maxWindow {
		maxWindow = a.cfg.Window10s
	}
	cutoff := a.nowFn().Add(-(maxWindow + maxWindow/10))

	for sym, venueMap := range a.trades {
		for venue, trades := range venueMap {
			// Binary search for the cutoff point since trades are time-ordered.
			idx := 0
			for idx < len(trades) && trades[idx].Timestamp.Before(cutoff) {
				idx++
			}
			if idx == len(trades) {
				delete(venueMap, venue)
			} else if idx > 0 {
				venueMap[venue] = trades[idx:]
			}
		}
		if len(venueMap) == 0 {
			delete(a.trades, sym)
		}
	}
}

// safeNetFlow computes (buy - sell) / (buy + sell), returning 0 when total is 0.
func safeNetFlow(buy, sell float64) float64 {
	total := buy + sell
	if total == 0 {
		return 0
	}
	return (buy - sell) / total
}

// direction returns +1 for positive, -1 for negative, 0 for zero.
func direction(v float64) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}
