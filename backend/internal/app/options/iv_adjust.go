package options

import "math"

// IVAdjustment holds the parameters needed to compute a dynamic IV adjustment
// for same-day option exits. All fields are optional — zero values disable
// the corresponding adjustment.
type IVAdjustment struct {
	// VIX-beta scaling: iv *= (vixNow / vixAtEntry) ^ VIXBeta
	VIXAtEntry float64
	VIXNow     float64
	VIXBeta    float64 // 0 = disabled; typical 0.6–1.2 for large caps

	// Time-of-day seasonality: deterministic IV multiplier based on
	// minutes since market open (9:30 ET). The U-shape pattern is well
	// documented in options microstructure literature.
	MinutesSinceOpen   int
	TODSeasonalEnabled bool

	// Earnings IV ramp: if the underlying is within DaysToEarnings of an
	// earnings date, IV ramps as sqrt(daysToEarnings). BaselineDTE is the
	// DTE at which the ramp starts (typically 5–10 trading days).
	DaysToEarnings      int // 0 or negative = no earnings nearby
	EarningsRampEnabled bool

	// Move-based IV crush: single-name spot-vol correlation that VIX-beta
	// (index-level) misses. When the underlying makes a directional move
	// since entry, ATM IV typically crushes — calls more than puts because
	// skew supports puts on down-moves. Modeled as
	//     iv *= max(MoveCrushFloor, 1 - k * |underlyingRetPct|)
	// with separate k for calls (stronger crush) vs puts (weaker crush).
	//
	// UnderlyingRetPct is the signed percent return since entry expressed
	// as a decimal (0.02 for +2%). The multiplier uses its absolute value;
	// both directions crush ATM IV. MoveCrushEnabled gates the whole path.
	MoveCrushEnabled bool
	MoveCrushCallK   float64 // typical 0.6; 0 disables for calls
	MoveCrushPutK    float64 // typical 0.4; 0 disables for puts
	MoveCrushFloor   float64 // min multiplier; typical 0.5
	UnderlyingRetPct float64 // signed return since entry, e.g. 0.02 for 2% up-move
	IsCall           bool    // true = use MoveCrushCallK, false = MoveCrushPutK
}

// AdjustIV applies all enabled IV adjustments and returns the modified IV.
// Adjustments are multiplicative and stack: iv * vixFactor * todFactor * earningsFactor.
func AdjustIV(baseIV float64, adj IVAdjustment) float64 {
	if baseIV <= 0 {
		return baseIV
	}

	iv := baseIV

	// 1. VIX-beta scaling
	if adj.VIXBeta > 0 && adj.VIXAtEntry > 0 && adj.VIXNow > 0 {
		ratio := adj.VIXNow / adj.VIXAtEntry
		iv *= math.Pow(ratio, adj.VIXBeta)
	}

	// 2. Time-of-day seasonality (U-shape)
	if adj.TODSeasonalEnabled {
		iv *= todSeasonalMultiplier(adj.MinutesSinceOpen)
	}

	// 3. Earnings IV ramp
	if adj.EarningsRampEnabled && adj.DaysToEarnings > 0 {
		iv *= earningsRampMultiplier(adj.DaysToEarnings)
	}

	// 4. Move-based IV crush (single-name spot-vol correlation)
	if adj.MoveCrushEnabled {
		iv *= moveCrushMultiplier(adj)
	}

	// Clamp to reasonable range
	if iv < 0.01 {
		iv = 0.01
	}
	if iv > 5.0 {
		iv = 5.0
	}

	return iv
}

// moveCrushMultiplier returns the IV multiplier from the move-based crush
// component. Uses MoveCrushCallK for calls and MoveCrushPutK for puts. Zero k
// for the relevant side disables the effect. Output is floored at
// MoveCrushFloor (or 0.1 if unset) so a large move can't collapse IV
// arbitrarily.
func moveCrushMultiplier(adj IVAdjustment) float64 {
	k := adj.MoveCrushPutK
	if adj.IsCall {
		k = adj.MoveCrushCallK
	}
	if k <= 0 {
		return 1.0
	}
	absRet := math.Abs(adj.UnderlyingRetPct)
	if absRet <= 0 {
		return 1.0
	}
	mult := 1.0 - k*absRet
	floor := adj.MoveCrushFloor
	if floor <= 0 {
		floor = 0.1
	}
	if mult < floor {
		mult = floor
	}
	return mult
}

// todSeasonalMultiplier returns a deterministic IV multiplier based on
// minutes since market open (9:30 ET). US equity session is 390 minutes.
//
// Empirical IV intraday pattern (U-shape):
//
//	9:30–10:00  (+4%)   — opening auction, high uncertainty
//	10:00–11:30 (-1.5%) — morning stabilization
//	11:30–14:00 (-2.5%) — midday doldrums
//	14:00–15:00 (-1%)   — early afternoon
//	15:00–16:00 (+1.5%) — closing auction approach
func todSeasonalMultiplier(minutesSinceOpen int) float64 {
	if minutesSinceOpen < 0 {
		minutesSinceOpen = 0
	}
	if minutesSinceOpen > 390 {
		minutesSinceOpen = 390
	}

	switch {
	case minutesSinceOpen <= 30: // 9:30–10:00
		return 1.04
	case minutesSinceOpen <= 120: // 10:00–11:30
		return 0.985
	case minutesSinceOpen <= 270: // 11:30–14:00
		return 0.975
	case minutesSinceOpen <= 330: // 14:00–15:00
		return 0.99
	default: // 15:00–16:00
		return 1.015
	}
}

// earningsRampMultiplier models IV increase as earnings approach.
// IV ramps approximately as 1 / sqrt(daysToEarnings) relative to a
// baseline of 5 trading days out. When earnings is further than 10 days,
// returns 1.0 (no adjustment).
//
// At 1 day out: ~2.24x baseline (sqrt(5)/sqrt(1))
// At 2 days:    ~1.58x
// At 3 days:    ~1.29x
// At 5 days:    ~1.00x (baseline)
// At 10 days:   ~0.71x — but we cap at 1.0 to avoid artificially
//
//	deflating IV for distant earnings.
func earningsRampMultiplier(daysToEarnings int) float64 {
	const baselineDays = 5.0

	if daysToEarnings <= 0 || daysToEarnings > 10 {
		return 1.0
	}

	// sqrt(baseline) / sqrt(days) gives the ramp ratio
	mult := math.Sqrt(baselineDays) / math.Sqrt(float64(daysToEarnings))

	// Cap: don't deflate IV for dates between baseline and max
	if mult < 1.0 {
		mult = 1.0
	}

	return mult
}
