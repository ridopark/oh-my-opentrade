package strategy

import (
	"time"
)

// TFI is the Trade Flow Imbalance indicator — a rolling (buy - sell) /
// (buy + sell) ratio in [-1, 1] over a time window of trade ticks. Used as
// a confluence / gating input for MFT crypto strategies where aggressor-side
// information is available from the venue feed.
//
// When TakerSide is present on ticks ("buy"/"sell"), trades are bucketed by
// aggressor. When TakerSide is empty, TFI optionally falls back to signing
// bar deltas: a positive (close-open)*volume contributes to buy, negative to
// sell. This is a lossy approximation and should only be used when tick-level
// data is genuinely unavailable.
//
// Not thread-safe; single-runner access assumed.
type TFI struct {
	cfg    TFIConfig
	events []tfiEvent
}

// TFIConfig controls window length and the bar-fallback behavior.
type TFIConfig struct {
	// WindowMinutes is the trailing time window over which imbalance is
	// computed. Default 15.
	WindowMinutes int

	// FallbackBarSign enables the lossy bar-sign fallback when a bar update
	// arrives with no taker-side tick data available.
	FallbackBarSign bool
}

// DefaultTFIConfig is the 15-minute window with bar-sign fallback enabled.
func DefaultTFIConfig() TFIConfig {
	return TFIConfig{
		WindowMinutes:   15,
		FallbackBarSign: true,
	}
}

// NewTFI constructs a TFI with the given config. Zero config is replaced with
// defaults.
func NewTFI(cfg TFIConfig) *TFI {
	if cfg.WindowMinutes <= 0 {
		cfg.WindowMinutes = 15
	}
	return &TFI{cfg: cfg}
}

// tfiEvent is a single signed-volume contribution to the window.
type tfiEvent struct {
	ts      time.Time
	buyVol  float64
	sellVol float64
}

// MarketTradeLike is the minimal shape TFI consumes from a trade tick. Defined
// locally to avoid pulling domain.MarketTrade into the strategy package.
type MarketTradeLike struct {
	Time      time.Time
	Size      float64
	TakerSide string // "buy", "sell", or "" if unknown
}

// UpdateTrade ingests a single trade tick. Unknown-side trades are ignored
// (use UpdateBar with FallbackBarSign for those).
func (t *TFI) UpdateTrade(trade MarketTradeLike) {
	if trade.Size <= 0 {
		return
	}
	switch trade.TakerSide {
	case "buy":
		t.events = append(t.events, tfiEvent{ts: trade.Time, buyVol: trade.Size})
	case "sell":
		t.events = append(t.events, tfiEvent{ts: trade.Time, sellVol: trade.Size})
	default:
		return
	}
	t.evictOlderThan(trade.Time)
}

// UpdateBar is the fallback path: it contributes sign(close-open)*volume to
// the appropriate bucket anchored at the bar's close time. Silently no-ops
// if FallbackBarSign is disabled.
func (t *TFI) UpdateBar(bar Bar) {
	if !t.cfg.FallbackBarSign {
		return
	}
	if bar.Volume <= 0 {
		return
	}
	delta := bar.Close - bar.Open
	ev := tfiEvent{ts: bar.Time}
	switch {
	case delta > 0:
		ev.buyVol = bar.Volume
	case delta < 0:
		ev.sellVol = bar.Volume
	default:
		return // flat bar contributes nothing
	}
	t.events = append(t.events, ev)
	t.evictOlderThan(bar.Time)
}

// Value returns the current imbalance in [-1, 1] and the event count in
// window. Returns (0, 0) when the window is empty.
func (t *TFI) Value() (tfi float64, samples int) {
	var buy, sell float64
	for _, e := range t.events {
		buy += e.buyVol
		sell += e.sellVol
	}
	total := buy + sell
	if total <= 0 {
		return 0, 0
	}
	return (buy - sell) / total, len(t.events)
}

// evictOlderThan drops events older than (now - window). now is passed in
// rather than read from wall-clock so the indicator remains deterministic
// against the data's own timeline (important for backtests).
func (t *TFI) evictOlderThan(now time.Time) {
	cutoff := now.Add(-time.Duration(t.cfg.WindowMinutes) * time.Minute)
	i := 0
	for ; i < len(t.events); i++ {
		if !t.events[i].ts.Before(cutoff) {
			break
		}
	}
	if i > 0 {
		t.events = append(t.events[:0], t.events[i:]...)
	}
}
