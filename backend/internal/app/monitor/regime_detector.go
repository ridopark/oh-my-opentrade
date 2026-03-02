package monitor

import (
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	emaDivergenceThreshold = 0.01 // 1% EMA divergence for TREND detection
	rsiOverbought          = 70.0
	rsiOversold            = 30.0
	strengthScale          = 20.0 // multiplier for normalizing EMA diff to strength
)

// clamp restricts a value to a given minimum and maximum range.
func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// RegimeDetector maintains state and detects market regime shifts
// based on incoming technical indicator snapshots.
type RegimeDetector struct {
	lastRegimes map[string]domain.RegimeType
}

// NewRegimeDetector creates a new RegimeDetector.
func NewRegimeDetector() *RegimeDetector {
	return &RegimeDetector{
		lastRegimes: make(map[string]domain.RegimeType),
	}
}

// Detect analyzes a snapshot of indicators to determine the current
// market regime, and returns the regime along with a boolean indicating
// if the regime has changed since the last snapshot.
func (rd *RegimeDetector) Detect(snapshot domain.IndicatorSnapshot) (domain.MarketRegime, bool) {
	symStr := snapshot.Symbol.String()

	emaDiff := 0.0
	if snapshot.EMA21 != 0 {
		emaDiff = (snapshot.EMA9 - snapshot.EMA21) / snapshot.EMA21
	}
	absEmaDiff := math.Abs(emaDiff)

	regimeType := domain.RegimeBalance
	strength := 0.0

	// 1. REVERSAL check
	isReversal := false
	if snapshot.RSI > rsiOverbought && snapshot.StochK < snapshot.StochD {
		isReversal = true
	} else if snapshot.RSI < rsiOversold && snapshot.StochK > snapshot.StochD {
		isReversal = true
	}

	if isReversal {
		regimeType = domain.RegimeReversal
		strength = math.Abs(snapshot.RSI-50.0) / 50.0
		strength = clamp(strength, 0, 1)
	} else if absEmaDiff > emaDivergenceThreshold {
		// 2. TREND check
		regimeType = domain.RegimeTrend
		strength = clamp(absEmaDiff*strengthScale, 0, 1)
	} else {
		// 3. BALANCE (default)
		regimeType = domain.RegimeBalance
		strength = clamp(1.0-(absEmaDiff*strengthScale), 0, 1)
	}

	regime, _ := domain.NewMarketRegime(snapshot.Symbol, snapshot.Timeframe, regimeType, time.Now(), strength)

	lastType, exists := rd.lastRegimes[symStr]
	changed := !exists || lastType != regimeType

	if changed {
		rd.lastRegimes[symStr] = regimeType
	}

	return regime, changed
}
