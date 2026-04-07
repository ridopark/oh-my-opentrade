package backfill

import (
	"sort"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	dpExchange        = "D"
	largePrintMinNot  = 200_000.0
	dpBarTimeframe    = domain.Timeframe("5m")
	dpBucketInterval  = 5 * time.Minute
)

// dpWindow accumulates trade data for a single 5-minute window.
type dpWindow struct {
	dpVolume     float64
	dpNotional   float64 // sum of price*size for DP trades (for VWAP)
	dpTrades     int
	litVolume    float64
	totalVolume  float64
	buyVolume    float64
	sellVolume   float64
	largePrintVol   float64
	largePrintCount int
	maxPrintSize float64
}

// DPAggregator accumulates individual trade ticks into 5-minute dark pool bars.
type DPAggregator struct {
	symbol  domain.Symbol
	windows map[time.Time]*dpWindow
}

// NewDPAggregator creates a new aggregator for the given symbol.
func NewDPAggregator(symbol domain.Symbol) *DPAggregator {
	return &DPAggregator{
		symbol:  symbol,
		windows: make(map[time.Time]*dpWindow),
	}
}

// AddTrade processes a single trade tick, classifying it into the appropriate 5-minute window.
func (a *DPAggregator) AddTrade(t time.Time, exchange string, price, size float64) {
	bucket := t.Truncate(dpBucketInterval)
	w := a.windows[bucket]
	if w == nil {
		w = &dpWindow{}
		a.windows[bucket] = w
	}

	w.totalVolume += size

	if exchange == dpExchange {
		w.dpVolume += size
		w.dpNotional += price * size
		w.dpTrades++

		// Buy/sell classification: compare trade price to running DP VWAP for this window.
		// If no DP notional yet (first trade), default to buy.
		runningVWAP := 0.0
		if w.dpVolume > 0 {
			runningVWAP = w.dpNotional / w.dpVolume
		}
		if price >= runningVWAP {
			w.buyVolume += size
		} else {
			w.sellVolume += size
		}

		// Large print detection.
		notional := price * size
		if notional >= largePrintMinNot {
			w.largePrintVol += size
			w.largePrintCount++
		}

		// Track max print size.
		if size > w.maxPrintSize {
			w.maxPrintSize = size
		}
	} else {
		w.litVolume += size
	}
}

// Flush converts all accumulated windows into sorted DarkPoolBar slices and resets the aggregator.
func (a *DPAggregator) Flush() []domain.DarkPoolBar {
	if len(a.windows) == 0 {
		return nil
	}

	bars := make([]domain.DarkPoolBar, 0, len(a.windows))
	for t, w := range a.windows {
		dpvwap := 0.0
		if w.dpVolume > 0 {
			dpvwap = w.dpNotional / w.dpVolume
		}
		dpRatio := 0.0
		if w.totalVolume > 0 {
			dpRatio = w.dpVolume / w.totalVolume
		}

		bars = append(bars, domain.DarkPoolBar{
			Time:             t,
			Symbol:           a.symbol,
			Timeframe:        dpBarTimeframe,
			DPVolume:         w.dpVolume,
			DPTrades:         w.dpTrades,
			DPVWAP:           dpvwap,
			LitVolume:        w.litVolume,
			TotalVolume:      w.totalVolume,
			DPRatio:          dpRatio,
			BuyVolume:        w.buyVolume,
			SellVolume:       w.sellVolume,
			LargePrintVolume: w.largePrintVol,
			LargePrintCount:  w.largePrintCount,
			MaxPrintSize:     w.maxPrintSize,
		})
	}

	sort.Slice(bars, func(i, j int) bool {
		return bars[i].Time.Before(bars[j].Time)
	})

	// Reset windows for next use.
	a.windows = make(map[time.Time]*dpWindow)

	return bars
}
