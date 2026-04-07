package gate

import (
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// IndexTideTracker maintains a running intraday VWAP for SPY and QQQ
// so that the market_tide gate can compare individual-stock trade
// direction against the broad-market trend.
type IndexTideTracker struct {
	mu            sync.RWMutex
	states        map[string]*indexState
	warmupMinutes int
	nyLoc         *time.Location
}

type indexState struct {
	cumPriceVol float64   // sum(typical_price * volume)
	cumVol      float64   // sum(volume)
	vwap        float64   // cumPriceVol / cumVol
	lastClose   float64   // most recent bar close
	sessionDate string    // "2026-04-07"
	sessionOpen time.Time // time of first bar in session
	barCount    int       // bars received this session
}

// NewIndexTideTracker creates a tracker that requires warmupMinutes bars
// before reporting a tide reading.
func NewIndexTideTracker(warmupMinutes int) *IndexTideTracker {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// Fallback: use UTC if the timezone database is missing.
		loc = time.UTC
	}
	return &IndexTideTracker{
		states: map[string]*indexState{
			"SPY": {},
			"QQQ": {},
		},
		warmupMinutes: warmupMinutes,
		nyLoc:         loc,
	}
}

// OnBar updates the running VWAP for SPY or QQQ. Call on every 1m bar
// for these symbols. Non-SPY/QQQ bars are silently ignored.
func (t *IndexTideTracker) OnBar(bar domain.MarketBar) {
	sym := string(bar.Symbol)
	if sym != "SPY" && sym != "QQQ" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	st := t.states[sym]

	// Reset on new session day.
	today := bar.Time.In(t.nyLoc).Format("2006-01-02")
	if st.sessionDate != today {
		st.cumPriceVol = 0
		st.cumVol = 0
		st.vwap = 0
		st.sessionDate = today
		st.sessionOpen = bar.Time
		st.barCount = 0
	}

	typicalPrice := (bar.High + bar.Low + bar.Close) / 3
	st.cumPriceVol += typicalPrice * bar.Volume
	st.cumVol += bar.Volume
	if st.cumVol > 0 {
		st.vwap = st.cumPriceVol / st.cumVol
	}
	st.lastClose = bar.Close
	st.barCount++
}

// GetTide returns the reference index's VWAP and last close for a given
// stock symbol. Returns ready=false if the symbol has no reference index
// or the warmup period is incomplete.
func (t *IndexTideTracker) GetTide(sym domain.Symbol) (vwap float64, lastClose float64, ready bool) {
	refIndex := ReferenceIndex(sym)
	if refIndex == "" {
		return 0, 0, false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	st, ok := t.states[refIndex]
	if !ok || st.barCount < t.warmupMinutes {
		return 0, 0, false
	}

	return st.vwap, st.lastClose, true
}

// ReferenceIndex returns "SPY" or "QQQ" based on the symbol's sector
// classification. Returns "" if the symbol should not be filtered
// (e.g., the symbol is itself an index ETF).
func ReferenceIndex(sym domain.Symbol) string {
	sector := domain.ClassifySector(sym)
	switch sector {
	case domain.SectorTech, domain.SectorSemis, domain.SectorSoftware,
		domain.SectorFintech, domain.SectorCryptoProxy, domain.SectorLevETF:
		if string(sym) == "QQQ" {
			return ""
		}
		return "QQQ"
	case domain.SectorETF:
		s := string(sym)
		if s == "SPY" || s == "QQQ" || s == "IWM" || s == "DIA" {
			return ""
		}
		return "SPY"
	default:
		if string(sym) == "SPY" {
			return ""
		}
		return "SPY"
	}
}
