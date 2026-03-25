package backtest

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// BarAggregator accumulates 1m bars into a higher timeframe for backtest replay.
// Add returns true when the aggregated period closes.
type BarAggregator struct {
	period         time.Duration
	count          int
	barsInPeriod   int
	lastClosedTime int64
	pending        bool
}

// NewBarAggregator creates an aggregator for the given timeframe string (e.g. "5m", "15m").
func NewBarAggregator(tf string) *BarAggregator {
	d := parseTFDuration(tf)
	barsNeeded := int(d / time.Minute)
	if barsNeeded < 1 {
		barsNeeded = 1
	}
	return &BarAggregator{
		period:       d,
		barsInPeriod: barsNeeded,
	}
}

// Add ingests a 1m bar. Returns true when the aggregated period boundary is reached.
func (a *BarAggregator) Add(bar domain.MarketBar) bool {
	a.count++
	a.pending = true
	if a.count >= a.barsInPeriod {
		a.count = 0
		a.pending = false
		a.lastClosedTime = bar.Time.Unix()
		return true
	}
	return false
}

// HasPending returns true if there are un-flushed bars.
func (a *BarAggregator) HasPending() bool {
	return a.pending
}

// LastClosedTime returns the unix timestamp of the most recent closed period.
func (a *BarAggregator) LastClosedTime() int64 {
	return a.lastClosedTime
}

func parseTFDuration(tf string) time.Duration {
	switch tf {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return time.Minute
	}
}
