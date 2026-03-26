package monitor

import (
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	defaultEmaDivergenceThreshold = 0.003 // 0.3% — lowered from 1% to detect intraday trends on 5m bars
	rsiOverbought                 = 70.0
	rsiOversold                   = 30.0
	strengthScale                 = 60.0 // scaled up to match lower threshold (0.3% × 60 ≈ 0.18 vs old 1% × 20 = 0.20)
)

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

type RegimeDetector struct {
	states     map[string]*regimeState
	thresholds map[string]float64
}

type regimeState struct {
	confirmedRegime domain.RegimeType
	pendingRegime   domain.RegimeType
	pendingCount    int
}

func NewRegimeDetector() *RegimeDetector {
	return &RegimeDetector{
		states:     make(map[string]*regimeState),
		thresholds: make(map[string]float64),
	}
}

func (rd *RegimeDetector) RegisterDivergenceThreshold(symbol, timeframe string, threshold float64) {
	if threshold > 0 {
		rd.thresholds[symbol+":"+timeframe] = threshold
	}
}

// Detect analyzes a snapshot of indicators to determine the current
// market regime, and returns the regime along with a boolean indicating
// if the regime has changed since the last snapshot.
func (rd *RegimeDetector) Detect(snapshot domain.IndicatorSnapshot) (domain.MarketRegime, bool) {
	key := snapshot.Symbol.String() + ":" + snapshot.Timeframe.String()

	// Use EMA21/EMA50 for regime detection (wider lookback = less noise on 5m bars).
	// Falls back to configurable EMAFast/EMASlow if set (e.g. for per-strategy tuning).
	fast, slow := snapshot.EMA21, snapshot.EMA50
	if snapshot.EMAFast != 0 && snapshot.EMASlow != 0 {
		fast, slow = snapshot.EMAFast, snapshot.EMASlow
	}

	emaDiff := 0.0
	if slow != 0 {
		emaDiff = (fast - slow) / slow
	}
	absEmaDiff := math.Abs(emaDiff)

	threshold := defaultEmaDivergenceThreshold
	if t, ok := rd.thresholds[key]; ok {
		threshold = t
	}

	var regimeType domain.RegimeType
	strength := 0.0

	isReversal := false
	if snapshot.RSI > rsiOverbought && snapshot.StochK < snapshot.StochD {
		isReversal = true
	} else if snapshot.RSI < rsiOversold && snapshot.StochK > snapshot.StochD {
		isReversal = true
	}

	switch {
	case isReversal:
		regimeType = domain.RegimeReversal
		strength = math.Abs(snapshot.RSI-50.0) / 50.0
		strength = clamp(strength, 0, 1)
	case absEmaDiff > threshold:
		regimeType = domain.RegimeTrend
		strength = clamp(absEmaDiff*strengthScale, 0, 1)
	default:
		regimeType = domain.RegimeBalance
		strength = clamp(1.0-(absEmaDiff*strengthScale), 0, 1)
	}

	regime, _ := domain.NewMarketRegime(snapshot.Symbol, snapshot.Timeframe, regimeType, time.Now(), strength)

	const minAnchorBarsForRegime = 3
	st, exists := rd.states[key]
	if !exists {
		st = &regimeState{confirmedRegime: regimeType}
		rd.states[key] = st
		return regime, true
	}

	isAnchorTF := snapshot.Timeframe != "1m"
	changed := false

	if !isAnchorTF {

		if st.confirmedRegime != regimeType {
			st.confirmedRegime = regimeType
			st.pendingRegime = ""
			st.pendingCount = 0
			changed = true
		}
		return regime, changed
	}

	if st.confirmedRegime == regimeType {
		st.pendingRegime = ""
		st.pendingCount = 0
		return regime, false
	}

	if st.pendingRegime != regimeType {
		st.pendingRegime = regimeType
		st.pendingCount = 1
		return regime, false
	}

	st.pendingCount++
	if st.pendingCount >= minAnchorBarsForRegime {
		st.confirmedRegime = regimeType
		st.pendingRegime = ""
		st.pendingCount = 0
		changed = true
	}

	return regime, changed
}
