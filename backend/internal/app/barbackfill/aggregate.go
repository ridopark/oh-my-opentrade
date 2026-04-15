// Package barbackfill provides shared helpers for reconstructing higher-timeframe
// bars from a run of 1m bars during backfill. Both omo-core's warmup gap-fill
// and omo-data's intraday refresh use this to avoid divergent aggregation
// behavior (notably equity pre-market anchor handling).
package barbackfill

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

var htfTFs = []domain.Timeframe{"5m", "15m", "1h"}

// AggregateHTF returns 5m/15m/1h bars aggregated from 1m bars.
// Crypto symbols use a UTC clock-aligned aggregator; equity symbols use
// today's NYSE 9:30 ET open, falling back to the previous RTH session
// start if the first input bar predates today's open.
func AggregateHTF(sym domain.Symbol, bars1m []domain.MarketBar, now time.Time) []domain.MarketBar {
	if len(bars1m) == 0 {
		return nil
	}
	first := bars1m[0].Time
	var out []domain.MarketBar

	for _, tf := range htfTFs {
		var agg *domain.BarAggregator
		var err error
		if sym.IsCryptoSymbol() {
			agg, err = domain.NewClockAlignedAggregator(sym, tf)
		} else {
			loc, lerr := time.LoadLocation("America/New_York")
			if lerr != nil {
				loc = time.FixedZone("EST", -5*3600)
			}
			nowET := now.In(loc)
			anchor := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 9, 30, 0, 0, loc)
			if first.Before(anchor) {
				prevStart, _ := domain.PreviousRTHSession(now)
				anchor = prevStart
			}
			agg, err = domain.NewBarAggregator(sym, tf, anchor)
		}
		if err != nil || agg == nil {
			continue
		}
		for _, b := range bars1m {
			if closed, ok := agg.Push(b); ok {
				out = append(out, closed)
			}
		}
	}
	return out
}
