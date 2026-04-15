package strategy

import (
	"math"
	"time"
)

// CryptoInducement detects liquidity sweeps on crypto markets. It differs
// from the equities inducement detector in three material ways:
//
//  1. Crypto venues trade 24/7, so "session" HOD/LOD is anchored to
//     UTC-midnight rather than a cash-session open.
//  2. Crypto has no equity-style stop cluster at the prior-day HOD/LOD or
//     opening-range extremes. Instead the liquidity pools are (a) prior
//     ~1h swing high/low, (b) rolling UTC-session HOD/LOD, and (c)
//     round-number price grids ("magnet" levels — BTC $1,000 steps,
//     ETH $100, SOL $10).
//  3. Crypto tick velocity is much higher. Sweeps resolve in 1-2 5m bars,
//     not 3 — the default ReversalBars is 2.
//
// The detector is stateless from the caller's perspective: pass the current
// bar window and recent trade ticks, get back a single Result describing the
// most significant sweep observed on the most recent bar (or Detected=false).
// No strategy wiring happens here; that is the consumer's job.
type CryptoInducement struct {
	cfg CryptoInducementConfig
}

// CryptoInducementConfig controls sweep detection thresholds. All fields have
// crypto-calibrated defaults via NewCryptoInducement; callers may override
// individual fields.
type CryptoInducementConfig struct {
	// LookbackBars is the rolling window of 5m bars retained for HOD/LOD and
	// 1h swing reference computation. 60 bars = 5 hours at 5m, enough for
	// a mid-session HOD/LOD anchor without dragging in stale levels.
	LookbackBars int

	// ReversalBars is the number of bars the close must return inside the
	// swept level within. Crypto sweeps are fast — default 2 (10 min at 5m),
	// vs. equities default 3.
	ReversalBars int

	// SwingWindowBars defines the half-window size for detecting a prior
	// ~1h swing high/low. 12 bars = 1h at 5m; we scan for local extrema
	// where the candidate bar's high/low is the max/min across [+-window].
	SwingWindowBars int

	// BreachMinBPS is the minimum overshoot past a reference level to count
	// as a sweep (in basis points). Wicks that merely touch score 0.
	BreachMinBPS float64

	// BreachMaxBPS is the upper bound on overshoot. Moves larger than this
	// past the level are treated as real breakouts, not sweeps. Crypto
	// volatility is higher than equities, so default is 150 bps (vs. 80).
	BreachMaxBPS float64

	// VolumeMinRatio is the required ratio of the sweep bar's volume to the
	// trailing VolumeSMA over LookbackBars. 1.0 disables the filter.
	VolumeMinRatio float64

	// TakerFlowMinRatio is the required ratio of aggressive-opposing-side
	// taker volume to total taker volume across the supplied trades on the
	// sweep bar. For a bullish sweep (swept low, reversing up) we require
	// aggressive BUY taker flow >= ratio. 0.0 disables the filter.
	TakerFlowMinRatio float64

	// RoundIncrements maps symbol -> round-number grid spacing in price
	// units. Unlisted symbols skip round-number reference levels.
	RoundIncrements map[string]float64
}

// DefaultCryptoInducementConfig returns crypto-calibrated defaults. These
// are educated priors based on the equities detector shape scaled for
// crypto tick velocity; they must be validated on real historical data
// before being treated as ground truth.
func DefaultCryptoInducementConfig() CryptoInducementConfig {
	return CryptoInducementConfig{
		LookbackBars:      60,
		ReversalBars:      2,
		SwingWindowBars:   12,
		BreachMinBPS:      5,
		BreachMaxBPS:      150,
		VolumeMinRatio:    1.2,
		TakerFlowMinRatio: 0.0, // off by default; off-feed taker side may be missing
		RoundIncrements: map[string]float64{
			"BTC/USD":  1000,
			"BTC/USDT": 1000,
			"ETH/USD":  100,
			"ETH/USDT": 100,
			"SOL/USD":  10,
			"SOL/USDT": 10,
		},
	}
}

// NewCryptoInducement constructs a detector with the given config. A zero
// config is replaced with DefaultCryptoInducementConfig().
func NewCryptoInducement(cfg CryptoInducementConfig) *CryptoInducement {
	if cfg.LookbackBars == 0 && cfg.ReversalBars == 0 {
		cfg = DefaultCryptoInducementConfig()
	}
	if cfg.RoundIncrements == nil {
		cfg.RoundIncrements = map[string]float64{}
	}
	return &CryptoInducement{cfg: cfg}
}

// CryptoTrade is the minimal trade-tick shape the detector consumes.
// It intentionally decouples the detector from domain.MarketTrade to keep
// the strategy package free of outer-layer imports.
type CryptoTrade struct {
	Time      time.Time
	Price     float64
	Size      float64
	TakerSide string // "buy", "sell", or "" if unknown
}

// CryptoInducementDirection classifies the swept side.
type CryptoInducementDirection int

const (
	CryptoInducementNone CryptoInducementDirection = iota
	// CryptoInducementBullish: a prior LOW was swept and price reversed up
	// (longs favored — stops below were taken, absorbed, reversed).
	CryptoInducementBullish
	// CryptoInducementBearish: a prior HIGH was swept and price reversed
	// down (shorts favored).
	CryptoInducementBearish
)

// CryptoInducementLevelType identifies which liquidity pool was swept.
type CryptoInducementLevelType string

const (
	CryptoInducementLevelHOD     CryptoInducementLevelType = "hod"
	CryptoInducementLevelLOD     CryptoInducementLevelType = "lod"
	CryptoInducementLevelSwing1h CryptoInducementLevelType = "swing_1h"
	CryptoInducementLevelRound   CryptoInducementLevelType = "round"
)

// CryptoInducementResult is the detector output for a given bar window.
type CryptoInducementResult struct {
	Detected       bool
	Direction      CryptoInducementDirection
	Timestamp      time.Time
	ReferenceLevel float64
	LevelType      CryptoInducementLevelType
	BreachBPS      float64
	VolumeRatio    float64
	TakerFlowRatio float64 // aggressive-opposing-side taker fraction on sweep bar
}

// Detect inspects the provided 5m bar window and trade ticks. The newest bar
// (bars[len-1]) is treated as the candidate sweep bar. Trades should be
// scoped to the candidate bar's time range; the detector does not filter
// them by time. Symbol is used for the round-number grid lookup.
//
// Returns the highest-conviction sweep found — same-bar reversal wins over
// multi-bar, larger breach wins ties. If no qualifying sweep is present
// Result.Detected will be false.
func (d *CryptoInducement) Detect(symbol string, bars []Bar, trades []CryptoTrade) CryptoInducementResult {
	if len(bars) < 2 {
		return CryptoInducementResult{}
	}
	// Trim to lookback window.
	if d.cfg.LookbackBars > 0 && len(bars) > d.cfg.LookbackBars {
		bars = bars[len(bars)-d.cfg.LookbackBars:]
	}

	curr := bars[len(bars)-1]
	prior := bars[:len(bars)-1]

	// Build reference levels from history (session HOD/LOD, 1h swings, round grid).
	levels := d.referenceLevels(symbol, curr, prior)
	if len(levels) == 0 {
		return CryptoInducementResult{}
	}

	// Volume ratio on the sweep bar.
	volRatio := d.volumeRatio(curr, prior)
	if d.cfg.VolumeMinRatio > 0 && volRatio < d.cfg.VolumeMinRatio {
		return CryptoInducementResult{}
	}

	// Taker-flow aggregation on the sweep bar.
	buyTaker, sellTaker := aggregateTakerFlow(trades)
	totalTaker := buyTaker + sellTaker

	best := CryptoInducementResult{}
	for _, lvl := range levels {
		res, ok := d.evaluateLevel(curr, lvl, buyTaker, sellTaker, totalTaker, volRatio)
		if !ok {
			continue
		}
		if !best.Detected || res.BreachBPS > best.BreachBPS {
			best = res
		}
	}
	return best
}

// referenceLevel is an internal candidate sweep target.
type referenceLevel struct {
	Price     float64
	Type      CryptoInducementLevelType
	IsHigh    bool // true => swept from above (bearish), false => from below (bullish)
}

func (d *CryptoInducement) referenceLevels(symbol string, curr Bar, prior []Bar) []referenceLevel {
	out := make([]referenceLevel, 0, 8)

	// (1) UTC-session HOD/LOD: rolling extrema since UTC-midnight of curr.Time.
	sessionHOD, sessionLOD, haveSession := sessionExtrema(curr, prior)
	if haveSession {
		out = append(out,
			referenceLevel{Price: sessionHOD, Type: CryptoInducementLevelHOD, IsHigh: true},
			referenceLevel{Price: sessionLOD, Type: CryptoInducementLevelLOD, IsHigh: false},
		)
	}

	// (2) 1h-ish swing highs/lows from prior bars (excludes curr).
	out = append(out, d.detectSwings(prior)...)

	// (3) Round-number grid nearest to curr price.
	if inc, ok := d.cfg.RoundIncrements[symbol]; ok && inc > 0 {
		// Nearest level above and below curr.Close.
		above := math.Ceil(curr.Close/inc) * inc
		below := math.Floor(curr.Close/inc) * inc
		// Avoid duplicate when close is exactly on the grid.
		if above > curr.Close {
			out = append(out, referenceLevel{Price: above, Type: CryptoInducementLevelRound, IsHigh: true})
		}
		if below < curr.Close {
			out = append(out, referenceLevel{Price: below, Type: CryptoInducementLevelRound, IsHigh: false})
		}
	}
	return out
}

// sessionExtrema returns HOD/LOD for the UTC session containing curr. Only
// prior bars whose UTC date matches curr's UTC date are considered.
func sessionExtrema(curr Bar, prior []Bar) (hod, lod float64, ok bool) {
	y, m, day := curr.Time.UTC().Date()
	hod, lod = math.Inf(-1), math.Inf(1)
	found := false
	for _, b := range prior {
		by, bm, bday := b.Time.UTC().Date()
		if by != y || bm != m || bday != day {
			continue
		}
		found = true
		if b.High > hod {
			hod = b.High
		}
		if b.Low < lod {
			lod = b.Low
		}
	}
	if !found {
		return 0, 0, false
	}
	return hod, lod, true
}

// detectSwings scans prior bars for local extrema: a bar is a swing HIGH if
// its high strictly exceeds all bars within +-SwingWindowBars, likewise LOW.
// We skip bars within SwingWindowBars of the edges since the window is
// incomplete there.
func (d *CryptoInducement) detectSwings(prior []Bar) []referenceLevel {
	w := d.cfg.SwingWindowBars
	if w <= 0 || len(prior) < 2*w+1 {
		return nil
	}
	out := []referenceLevel{}
	for i := w; i < len(prior)-w; i++ {
		bar := prior[i]
		isHigh, isLow := true, true
		for j := i - w; j <= i+w; j++ {
			if j == i {
				continue
			}
			if prior[j].High >= bar.High {
				isHigh = false
			}
			if prior[j].Low <= bar.Low {
				isLow = false
			}
			if !isHigh && !isLow {
				break
			}
		}
		if isHigh {
			out = append(out, referenceLevel{Price: bar.High, Type: CryptoInducementLevelSwing1h, IsHigh: true})
		}
		if isLow {
			out = append(out, referenceLevel{Price: bar.Low, Type: CryptoInducementLevelSwing1h, IsHigh: false})
		}
	}
	return out
}

func (d *CryptoInducement) volumeRatio(curr Bar, prior []Bar) float64 {
	if len(prior) == 0 {
		return 0
	}
	sum := 0.0
	for _, b := range prior {
		sum += b.Volume
	}
	sma := sum / float64(len(prior))
	if sma <= 0 {
		return 0
	}
	return curr.Volume / sma
}

func aggregateTakerFlow(trades []CryptoTrade) (buy, sell float64) {
	for _, t := range trades {
		switch t.TakerSide {
		case "buy":
			buy += t.Size
		case "sell":
			sell += t.Size
		}
	}
	return buy, sell
}

// evaluateLevel checks whether curr bar swept lvl and reversed same-bar.
// Multi-bar reversal is left to the caller (stateful orchestration).
func (d *CryptoInducement) evaluateLevel(
	curr Bar,
	lvl referenceLevel,
	buyTaker, sellTaker, totalTaker float64,
	volRatio float64,
) (CryptoInducementResult, bool) {
	if lvl.Price <= 0 {
		return CryptoInducementResult{}, false
	}

	var breachBPS float64
	var direction CryptoInducementDirection
	if lvl.IsHigh {
		// Bearish sweep: high overshot lvl, close reverted below.
		if !(curr.High > lvl.Price && curr.Close < lvl.Price) {
			return CryptoInducementResult{}, false
		}
		breachBPS = (curr.High - lvl.Price) / lvl.Price * 10000.0
		direction = CryptoInducementBearish
	} else {
		// Bullish sweep: low undershot lvl, close reverted above.
		if !(curr.Low < lvl.Price && curr.Close > lvl.Price) {
			return CryptoInducementResult{}, false
		}
		breachBPS = (lvl.Price - curr.Low) / lvl.Price * 10000.0
		direction = CryptoInducementBullish
	}

	if breachBPS < d.cfg.BreachMinBPS || breachBPS > d.cfg.BreachMaxBPS {
		return CryptoInducementResult{}, false
	}

	// Taker-flow filter (optional): for a bullish sweep, require aggressive
	// BUY flow to dominate; for bearish, aggressive SELL. Skipped when
	// ratio is 0 or there is no taker-side data on the bar.
	takerRatio := 0.0
	if totalTaker > 0 {
		if direction == CryptoInducementBullish {
			takerRatio = buyTaker / totalTaker
		} else {
			takerRatio = sellTaker / totalTaker
		}
		if d.cfg.TakerFlowMinRatio > 0 && takerRatio < d.cfg.TakerFlowMinRatio {
			return CryptoInducementResult{}, false
		}
	}

	return CryptoInducementResult{
		Detected:       true,
		Direction:      direction,
		Timestamp:      curr.Time,
		ReferenceLevel: lvl.Price,
		LevelType:      lvl.Type,
		BreachBPS:      breachBPS,
		VolumeRatio:    volRatio,
		TakerFlowRatio: takerRatio,
	}, true
}
