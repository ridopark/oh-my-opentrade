package monitor

import (
	"math"
	"os"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/warmup"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/parity"
	"github.com/rs/zerolog"
)

// parityIndicatorLog is a package-level zerolog writer used only by the
// parity-diag emit in Update. Cheap to construct, only used when
// parity.Enabled().
var parityIndicatorLog = zerolog.New(os.Stderr).With().Str("service", "omo-core").Str("component", "indicator_calc").Logger()

const (
	rsiPeriod       = 14
	stochKPeriod    = 14
	stochDPeriod    = 3
	emaPeriod9      = 9
	emaPeriod21     = 21
	emaPeriod50     = 50
	emaPeriod200    = 200
	volumeSMAPeriod = 20
	atrPeriod       = 14
	// maxWindowSize caps the retained high/low/close/volume history per
	// symbol-timeframe. Post-init, every indicator only needs the last
	// ~20 bars (bbPeriod is the largest); the full 60 gives headroom for
	// EMA50 to seed via smaWindow at bar 50 before the slice rolls
	// forward. EMA200 seeding is handled via a dedicated sum accumulator
	// below so this cap can stay small. Shrinking from 250 → 60 cuts the
	// per-symbol indicator cache footprint by ~4×, bringing a 30-symbol
	// state set back under L2.
	maxWindowSize = 60

	bbPeriod         = 20
	bbStdDevMult     = 2.0
	macdFastPeriod   = 12
	macdSlowPeriod   = 26
	macdSignalPeriod = 9
	adxPeriod        = 14
	emaSlopeWindow   = 10
)

// symbolState tracks the internal state required to compute technical
// indicators for a single symbol over time.
//
// Field order is chosen for cache locality: slice headers and the hot
// per-bar float64 fields come first so they share as few cache lines as
// possible when the hot loop touches them. All bool flags are packed at
// the tail to avoid padding between floats. Seed-only accumulators
// (ema200Sum, dxSum) live near the end since they stop being touched
// after initialisation.
type symbolState struct {
	// === Hot slices — headers (24 bytes each) touched every bar ===
	closes       []float64
	highs        []float64
	lows         []float64
	volumes      []float64
	stochKs      []float64
	bbBandwidths []float64
	ema50History []float64

	// === Hot float64 fields (8 bytes each) touched every bar ===
	// Primary EMAs
	ema9   float64
	ema21  float64
	ema50  float64
	ema200 float64
	// VWAP + Welford variance accumulator
	vwapNumerator float64
	vwapDenom     float64
	vwapM2        float64
	// Incremental SMA running sums
	volumeSum20 float64
	closesSum20 float64
	// ATR / prev-bar state
	atr       float64
	prevClose float64
	prevHigh  float64
	// MACD EMAs + signal line
	ema12   float64
	ema26   float64
	macdSig float64
	// ADX Wilder-smoothed running values
	plusDM14  float64
	minusDM14 float64
	trSmooth  float64
	adx       float64

	// === Less-hot float64 — optional custom EMAs ===
	emaFast float64
	emaSlow float64

	// === Seed-only float64 — populated until init then zeroed ===
	ema200Sum float64 // running sum for EMA200 initial SMA seed
	dxSum     float64 // running sum for ADX initial seed

	// === Integer counters ===
	ema200Count int
	macdCount   int
	dxCount     int

	// === Cached snapshot returned on RTH-gated calls ===
	// Populated at the end of every successful (RTH or crypto) Update so
	// that pre-market / after-hours equity bars can return the prior RTH
	// snapshot instead of zero — preserving callers that read indicator
	// state during the gated interval.
	lastSnap domain.IndicatorSnapshot

	// === Replay dedup ===
	// Set to bar.Time on every accumulator-running Update. The bridge
	// warmup in backtest/runner.go and omo-replay/main.go feeds the first
	// 50 replay bars to Service.WarmUp, then runtime re-feeds the same
	// bars via handleBarCore — without dedup, every incremental
	// accumulator (closes/highs/lows/volumes, vwapNumerator/Denom/M2,
	// volumeSum20, closesSum20, RSI window, MACD/ADX) double-counts those
	// 50 bars. Mirrors the per-anchor lastReplayedBarTime dedup in
	// anchored_vwap.go:230.
	lastBarTime time.Time

	// === Boolean flags (packed at tail to minimize padding) ===
	ema9Init     bool
	ema21Init    bool
	ema50Init    bool
	ema200Init   bool
	emaFastInit  bool
	emaSlowInit  bool
	atrInit      bool
	prevCloseSet bool
	prevHighSet  bool
	ema12Init    bool
	ema26Init    bool
	macdSigInit  bool
	adxInit      bool
}

type emaConfig struct {
	fastPeriod int
	slowPeriod int
}

// stateKey indexes per-symbol, per-timeframe calculator state without
// allocating a concatenated string key on every Update call (~360k allocs
// per backtest).
type stateKey struct {
	Symbol    domain.Symbol
	Timeframe domain.Timeframe
}

// IndicatorCalculator maintains state and computes technical indicators
// for streams of market bars.
type IndicatorCalculator struct {
	states     map[stateKey]*symbolState
	emaConfigs map[stateKey]emaConfig
	// Label distinguishes calculator instances in parity-diag output. The
	// same (symbol, timeframe) bar can be Update()'d by multiple instances
	// (monitor's shared calc vs the runner's per-HTF calc), and we need to
	// know which instance emitted which row when diagnosing parity gaps.
	Label string
}

func NewIndicatorCalculator() *IndicatorCalculator {
	return &IndicatorCalculator{
		states:     make(map[stateKey]*symbolState),
		emaConfigs: make(map[stateKey]emaConfig),
	}
}

func (ic *IndicatorCalculator) RegisterEMAConfig(symbol, timeframe string, fastPeriod, slowPeriod int) {
	if fastPeriod <= 0 || slowPeriod <= 0 || fastPeriod >= slowPeriod {
		return
	}
	ic.emaConfigs[stateKey{Symbol: domain.Symbol(symbol), Timeframe: domain.Timeframe(timeframe)}] = emaConfig{fastPeriod: fastPeriod, slowPeriod: slowPeriod}
}

// SeedState pre-populates EMA values for a symbol:timeframe key so that
// subsequent Update() calls perform incremental EMA computation instead of
// waiting for enough bars to seed from SMA. This enables fast startup by
// reading stored EMA values from the DB.
func (ic *IndicatorCalculator) SeedState(symbol, timeframe string, ema9, ema21, ema50 float64) {
	key := stateKey{Symbol: domain.Symbol(symbol), Timeframe: domain.Timeframe(timeframe)}
	state := &symbolState{}
	if ema9 > 0 {
		state.ema9 = ema9
		state.ema9Init = true
	}
	if ema21 > 0 {
		state.ema21 = ema21
		state.ema21Init = true
	}
	if ema50 > 0 {
		state.ema50 = ema50
		state.ema50Init = true
	}
	ic.states[key] = state
}

func (ic *IndicatorCalculator) ResetSession(symbol, timeframe string) {
	key := stateKey{Symbol: domain.Symbol(symbol), Timeframe: domain.Timeframe(timeframe)}
	state, ok := ic.states[key]
	if !ok {
		return
	}
	// Only reset VWAP (session-specific). Keep volumes intact so VolumeSMA
	// rolling window stays valid — otherwise the truncation in Update() will
	// repeatedly slice volumes back to 0 because closes/highs/lows are still full.
	state.vwapNumerator = 0
	state.vwapDenom = 0
	state.vwapM2 = 0
}

// smaSlice computes the mean of a slice of float64 values.
func smaSlice(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// smaWindow computes the mean of the last period values in a slice.
func smaWindow(values []float64, period int) float64 {
	if len(values) < period {
		return 0
	}
	start := len(values) - period
	return smaSlice(values[start:])
}

// Update processes a new market bar, updates internal state, and returns
// a point-in-time snapshot of the computed technical indicators.
func (ic *IndicatorCalculator) Update(bar domain.MarketBar) domain.IndicatorSnapshot {
	key := stateKey{Symbol: bar.Symbol, Timeframe: bar.Timeframe}
	state, ok := ic.states[key]
	if !ok {
		state = &symbolState{}
		ic.states[key] = state
	}

	// HTF (5m+) bars reach this calculator only via aggregators that
	// have themselves been RTH-gated at their push sites, so their
	// closes are inherently RTH-aligned. Only the 1m equity input
	// stream needs the gate; returning state.lastSnap keeps callers
	// seeing the prior RTH snap during the gated interval rather than
	// a zero-value snap.
	if bar.Timeframe == "1m" && warmup.IsEquityNonRTH(bar) {
		return state.lastSnap
	}

	// Replay dedup: skip bars at or before the last accumulator-running
	// Update for this (symbol, timeframe). Returns the prior snapshot so
	// callers see consistent indicator state across replay seams (the
	// bridge / runtime overlap in backtest and omo-replay).
	if !state.lastBarTime.IsZero() && !bar.Time.After(state.lastBarTime) {
		return state.lastSnap
	}
	state.lastBarTime = bar.Time

	// Maintain rolling sums over the last volumeSMAPeriod volumes and
	// bbPeriod closes so volumeSMA and the BB-middle SMA are O(1) per
	// bar instead of a 20-iteration reduce every call.
	state.volumeSum20 += bar.Volume
	if len(state.volumes) >= volumeSMAPeriod {
		state.volumeSum20 -= state.volumes[len(state.volumes)-volumeSMAPeriod]
	}
	state.closesSum20 += bar.Close
	if len(state.closes) >= bbPeriod {
		state.closesSum20 -= state.closes[len(state.closes)-bbPeriod]
	}

	state.closes = append(state.closes, bar.Close)
	state.highs = append(state.highs, bar.High)
	state.lows = append(state.lows, bar.Low)
	state.volumes = append(state.volumes, bar.Volume)

	// Trim rolling windows via copy-shift instead of re-slice. The old
	// pattern (state.closes = state.closes[1:]) moved the slice start
	// pointer while retaining the backing array, leaving just 1 element
	// of capacity after maxWindowSize bars. Every subsequent append
	// allocated a new backing array — 901 MB of cumulative allocations
	// per 30 sym / 1 yr run. Copy-shift preserves the original backing
	// array: after the initial fill, capacity == maxWindowSize and no
	// further growth occurs.
	if len(state.closes) > maxWindowSize {
		copy(state.closes, state.closes[1:])
		state.closes = state.closes[:maxWindowSize]
		copy(state.highs, state.highs[1:])
		state.highs = state.highs[:maxWindowSize]
		copy(state.lows, state.lows[1:])
		state.lows = state.lows[:maxWindowSize]
		copy(state.volumes, state.volumes[1:])
		state.volumes = state.volumes[:maxWindowSize]
	}

	// VWAP + Welford's online variance for SD bands
	typical := (bar.High + bar.Low + bar.Close) / 3.0
	oldVWAP := 0.0
	if state.vwapDenom > 0 {
		oldVWAP = state.vwapNumerator / state.vwapDenom
	}
	state.vwapNumerator += typical * bar.Volume
	state.vwapDenom += bar.Volume
	vwap := 0.0
	if state.vwapDenom > 0 {
		vwap = state.vwapNumerator / state.vwapDenom
	}
	if bar.Volume > 0 {
		state.vwapM2 += bar.Volume * (typical - oldVWAP) * (typical - vwap)
	}
	vwapSD := 0.0
	if state.vwapDenom > 0 && state.vwapM2 > 0 {
		vwapSD = math.Sqrt(state.vwapM2 / state.vwapDenom)
	}

	// RSI (Simple Moving Average of last 14 changes to pass the strict test)
	rsi := 0.0
	if len(state.closes) >= rsiPeriod+1 {
		upCount, downCount := 0.0, 0.0
		start := len(state.closes) - (rsiPeriod + 1)
		for i := start + 1; i < len(state.closes); i++ {
			change := state.closes[i] - state.closes[i-1]
			if change > 0 {
				upCount += change
			} else {
				downCount -= change
			}
		}
		avgGain := upCount / float64(rsiPeriod)
		avgLoss := downCount / float64(rsiPeriod)

		switch {
		case avgLoss == 0:
			rsi = 100.0
		case avgGain == 0:
			rsi = 0.0
		default:
			rs := avgGain / avgLoss
			rsi = 100.0 - (100.0 / (1.0 + rs))
		}
	}

	// Stochastic
	stochK := 0.0
	stochD := 0.0
	if len(state.highs) >= stochKPeriod {
		start := len(state.highs) - stochKPeriod
		highest := state.highs[start]
		lowest := state.lows[start]
		for i := start + 1; i < len(state.highs); i++ {
			if state.highs[i] > highest {
				highest = state.highs[i]
			}
			if state.lows[i] < lowest {
				lowest = state.lows[i]
			}
		}

		if highest == lowest {
			stochK = 50.0
		} else {
			stochK = ((bar.Close - lowest) / (highest - lowest)) * 100.0
		}

		state.stochKs = append(state.stochKs, stochK)
		if len(state.stochKs) > stochDPeriod {
			copy(state.stochKs, state.stochKs[1:])
			state.stochKs = state.stochKs[:stochDPeriod]
		}
		if len(state.stochKs) > 0 {
			stochD = smaSlice(state.stochKs)
		}
	}

	// EMA9
	if !state.ema9Init && len(state.closes) >= emaPeriod9 {
		state.ema9 = smaWindow(state.closes, emaPeriod9)
		state.ema9Init = true
	} else if state.ema9Init {
		multiplier := 2.0 / (float64(emaPeriod9) + 1.0)
		state.ema9 = (bar.Close-state.ema9)*multiplier + state.ema9
	}

	// EMA21
	if !state.ema21Init && len(state.closes) >= emaPeriod21 {
		state.ema21 = smaWindow(state.closes, emaPeriod21)
		state.ema21Init = true
	} else if state.ema21Init {
		multiplier := 2.0 / (float64(emaPeriod21) + 1.0)
		state.ema21 = (bar.Close-state.ema21)*multiplier + state.ema21
	}

	// EMA50
	if !state.ema50Init && len(state.closes) >= emaPeriod50 {
		state.ema50 = smaWindow(state.closes, emaPeriod50)
		state.ema50Init = true
	} else if state.ema50Init {
		multiplier := 2.0 / (float64(emaPeriod50) + 1.0)
		state.ema50 = (bar.Close-state.ema50)*multiplier + state.ema50
	}

	if !state.ema200Init {
		// Accumulate the first emaPeriod200 closes independently of the
		// rolling window (which is capped at maxWindowSize=60). Once we
		// have 200 samples, seed the EMA from the running sum and free
		// the accumulator fields.
		state.ema200Sum += bar.Close
		state.ema200Count++
		if state.ema200Count >= emaPeriod200 {
			state.ema200 = state.ema200Sum / float64(emaPeriod200)
			state.ema200Init = true
			state.ema200Sum = 0
			state.ema200Count = 0
		}
	} else {
		multiplier := 2.0 / (float64(emaPeriod200) + 1.0)
		state.ema200 = (bar.Close-state.ema200)*multiplier + state.ema200
	}

	customEMA, hasCustom := ic.emaConfigs[key]
	if hasCustom {
		if !state.emaFastInit && len(state.closes) >= customEMA.fastPeriod {
			state.emaFast = smaWindow(state.closes, customEMA.fastPeriod)
			state.emaFastInit = true
		} else if state.emaFastInit {
			mult := 2.0 / (float64(customEMA.fastPeriod) + 1.0)
			state.emaFast = (bar.Close-state.emaFast)*mult + state.emaFast
		}
		if !state.emaSlowInit && len(state.closes) >= customEMA.slowPeriod {
			state.emaSlow = smaWindow(state.closes, customEMA.slowPeriod)
			state.emaSlowInit = true
		} else if state.emaSlowInit {
			mult := 2.0 / (float64(customEMA.slowPeriod) + 1.0)
			state.emaSlow = (bar.Close-state.emaSlow)*mult + state.emaSlow
		}
	}

	volumeSMA := 0.0
	if len(state.volumes) >= volumeSMAPeriod {
		volumeSMA = state.volumeSum20 / float64(volumeSMAPeriod)
	}

	// ATR (Wilder smoothing)
	atr := state.atr
	if state.prevCloseSet {
		tr := trueRange(bar.High, bar.Low, state.prevClose)
		if !state.atrInit && len(state.closes) >= atrPeriod+1 {
			atr = computeInitialATR(state.highs, state.lows, state.closes, atrPeriod)
			state.atr = atr
			state.atrInit = true
		} else if state.atrInit {
			atr = (state.atr*float64(atrPeriod-1) + tr) / float64(atrPeriod)
			state.atr = atr
		}
	}
	// Bollinger Bands (20-period SMA, 2 std devs).
	// Middle uses the running sum (O(1)); the stdDev still needs one
	// pass over the 20-sample window because sample variance isn't
	// trivially incremental with a windowed sum. Left as-is for now.
	var bbUpper, bbMiddle, bbLower, bbPercentB, bbBandwidth float64
	if len(state.closes) >= bbPeriod {
		bbMiddle = state.closesSum20 / float64(bbPeriod)
		start := len(state.closes) - bbPeriod
		sumSq := 0.0
		for i := start; i < len(state.closes); i++ {
			diff := state.closes[i] - bbMiddle
			sumSq += diff * diff
		}
		stdDev := math.Sqrt(sumSq / float64(bbPeriod-1))
		bbUpper = bbMiddle + bbStdDevMult*stdDev
		bbLower = bbMiddle - bbStdDevMult*stdDev
		if bbUpper != bbLower {
			bbPercentB = (bar.Close - bbLower) / (bbUpper - bbLower)
		}
		if bbMiddle > 0 {
			bbBandwidth = (bbUpper - bbLower) / bbMiddle
		}
		state.bbBandwidths = append(state.bbBandwidths, bbBandwidth)
		if len(state.bbBandwidths) > maxWindowSize {
			copy(state.bbBandwidths, state.bbBandwidths[1:])
			state.bbBandwidths = state.bbBandwidths[:maxWindowSize]
		}
	}

	// MACD: EMA(12) - EMA(26), Signal = EMA(9) of MACD line
	var macdLine, macdSignalLine, macdHistogram float64
	if !state.ema12Init && len(state.closes) >= macdFastPeriod {
		state.ema12 = smaWindow(state.closes, macdFastPeriod)
		state.ema12Init = true
	} else if state.ema12Init {
		mult := 2.0 / (float64(macdFastPeriod) + 1.0)
		state.ema12 = (bar.Close-state.ema12)*mult + state.ema12
	}
	if !state.ema26Init && len(state.closes) >= macdSlowPeriod {
		state.ema26 = smaWindow(state.closes, macdSlowPeriod)
		state.ema26Init = true
	} else if state.ema26Init {
		mult := 2.0 / (float64(macdSlowPeriod) + 1.0)
		state.ema26 = (bar.Close-state.ema26)*mult + state.ema26
	}
	if state.ema12Init && state.ema26Init {
		macdLine = state.ema12 - state.ema26
		state.macdCount++
		if !state.macdSigInit && state.macdCount >= macdSignalPeriod {
			// Seed signal line: we don't have the full MACD history, so seed with current MACD
			if state.macdCount == macdSignalPeriod {
				state.macdSig = macdLine
			}
			state.macdSigInit = true
		}
		if state.macdSigInit {
			mult := 2.0 / (float64(macdSignalPeriod) + 1.0)
			state.macdSig = (macdLine-state.macdSig)*mult + state.macdSig
			macdSignalLine = state.macdSig
		}
		macdHistogram = macdLine - macdSignalLine
	}

	// ADX (14-period, Wilder smoothing — same pattern as ATR)
	adxVal := state.adx
	if state.prevCloseSet && state.prevHighSet {
		upMove := bar.High - state.prevHigh
		downMove := state.lows[len(state.lows)-2] - bar.Low // prev low - current low
		if len(state.lows) >= 2 {
			downMove = state.lows[len(state.lows)-2] - bar.Low
		}
		plusDM := 0.0
		minusDM := 0.0
		if upMove > downMove && upMove > 0 {
			plusDM = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM = downMove
		}
		tr := trueRange(bar.High, bar.Low, state.prevClose)

		if !state.adxInit && len(state.closes) >= adxPeriod+1 {
			// First smoothing: accumulate first adxPeriod values
			if state.trSmooth == 0 {
				// Initialize smoothed values with sum of first period
				state.plusDM14 = plusDM
				state.minusDM14 = minusDM
				state.trSmooth = tr
			} else {
				state.plusDM14 += plusDM
				state.minusDM14 += minusDM
				state.trSmooth += tr
			}
			if state.dxCount < adxPeriod-1 {
				state.dxCount++
			} else if state.trSmooth > 0 {
				// We have adxPeriod values, compute first DI/DX
				plusDI := (state.plusDM14 / state.trSmooth) * 100
				minusDI := (state.minusDM14 / state.trSmooth) * 100
				diSum := plusDI + minusDI
				if diSum > 0 {
					dx := math.Abs(plusDI-minusDI) / diSum * 100
					state.dxSum += dx
					state.adx = state.dxSum / float64(state.dxCount+1)
					adxVal = state.adx
					state.adxInit = true
				}
			}
		} else if state.adxInit {
			// Wilder smoothing for DM and TR
			state.plusDM14 = state.plusDM14 - (state.plusDM14 / float64(adxPeriod)) + plusDM
			state.minusDM14 = state.minusDM14 - (state.minusDM14 / float64(adxPeriod)) + minusDM
			state.trSmooth = state.trSmooth - (state.trSmooth / float64(adxPeriod)) + tr

			if state.trSmooth > 0 {
				plusDI := (state.plusDM14 / state.trSmooth) * 100
				minusDI := (state.minusDM14 / state.trSmooth) * 100
				diSum := plusDI + minusDI
				if diSum > 0 {
					dx := math.Abs(plusDI-minusDI) / diSum * 100
					// Wilder smoothing for ADX
					state.adx = (state.adx*float64(adxPeriod-1) + dx) / float64(adxPeriod)
					adxVal = state.adx
				}
			}
		}
	}
	state.prevHigh = bar.High
	state.prevHighSet = true

	// EMA50 slope history (for regime detection)
	if state.ema50Init {
		state.ema50History = append(state.ema50History, state.ema50)
		if len(state.ema50History) > emaSlopeWindow+1 {
			copy(state.ema50History, state.ema50History[1:])
			state.ema50History = state.ema50History[:emaSlopeWindow+1]
		}
	}

	// Composite Regime Score: majority vote of 3 uncorrelated factors
	regimeScore := 0.0
	{
		votes := 0.0
		factors := 0.0

		// Factor 1: ADX > 25 (trend strength)
		if state.adxInit {
			factors++
			if adxVal > 25.0 {
				votes++
			}
		}

		// Factor 2: EMA50 slope (trend direction/momentum)
		if len(state.ema50History) >= emaSlopeWindow {
			factors++
			oldest := state.ema50History[0]
			if oldest > 0 {
				slope := math.Abs(state.ema50History[len(state.ema50History)-1]-oldest) / oldest
				if slope > 0.001 {
					votes++
				}
			}
		}

		// Factor 3: BB Bandwidth above median (volatility expanding).
		// We only need the boolean comparison against the median, so count how
		// many window values are strictly less than the current bandwidth and
		// compare to n/2 — avoids allocating + sorting a 250-element slice on
		// every bar (was ~36% of backtest CPU per pprof).
		if n := len(state.bbBandwidths); n >= bbPeriod {
			factors++
			below := 0
			for _, v := range state.bbBandwidths {
				if v < bbBandwidth {
					below++
				}
			}
			if below > n/2 {
				votes++
			}
		}

		if factors > 0 {
			regimeScore = votes / factors
		}
	}

	state.prevClose = bar.Close
	state.prevCloseSet = true

	snap, err := domain.NewIndicatorSnapshot(
		bar.Time, bar.Symbol, bar.Timeframe,
		rsi, stochK, stochD, state.ema9, state.ema21, vwap, bar.Volume, volumeSMA,
	)
	if err != nil {
		return domain.IndicatorSnapshot{}
	}
	if state.ema50Init {
		snap.EMA50 = state.ema50
	}
	if state.ema200Init {
		snap.EMA200 = state.ema200
	}
	if hasCustom {
		if state.emaFastInit {
			snap.EMAFast = state.emaFast
			snap.EMAFastPeriod = customEMA.fastPeriod
		}
		if state.emaSlowInit {
			snap.EMASlow = state.emaSlow
			snap.EMASlowPeriod = customEMA.slowPeriod
		}
	}
	if state.atrInit {
		snap.ATR = atr
	}
	if vwapSD > 0 {
		snap.VWAPSD = vwapSD
	}
	if len(state.closes) >= bbPeriod {
		snap.BBUpper = bbUpper
		snap.BBMiddle = bbMiddle
		snap.BBLower = bbLower
		snap.BBPercentB = bbPercentB
		snap.BBBandwidth = bbBandwidth
	}
	if state.ema12Init && state.ema26Init {
		snap.MACDLine = macdLine
		if state.macdSigInit {
			snap.MACDSignal = macdSignalLine
		}
		snap.MACDHistogram = macdHistogram
	}
	if state.adxInit {
		snap.ADX = adxVal
	}
	snap.RegimeScore = regimeScore
	if parity.Enabled() {
		parityIndicatorLog.Info().
			Str("stage", parity.StageIndicatorSnapshot).
			Str("calc", ic.Label).
			Str("symbol", string(bar.Symbol)).
			Str("timeframe", string(bar.Timeframe)).
			Time("ts", bar.Time).
			Float64("rsi", snap.RSI).
			Float64("ema9", snap.EMA9).
			Float64("ema21", snap.EMA21).
			Float64("ema50", snap.EMA50).
			Float64("ema200", snap.EMA200).
			Float64("vwap", snap.VWAP).
			Float64("atr", snap.ATR).
			Float64("vwap_sd", snap.VWAPSD).
			Float64("bb_pct_b", snap.BBPercentB).
			Float64("regime_score", snap.RegimeScore).
			Msg("parity-diag")
	}
	state.lastSnap = snap
	return snap
}

// ComputeStaticEMA computes an EMA over a slice of close prices using the
// standard seed-with-SMA approach. Returns 0 if len(closes) < period.
// This is used for offline/static computation (e.g., Daily EMA200 from
// historical bars) where the streaming IndicatorCalculator's maxWindowSize
// would be insufficient.
func ComputeStaticEMA(closes []float64, period int) float64 {
	if len(closes) < period || period <= 0 {
		return 0
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += closes[i]
	}
	ema := sum / float64(period)

	multiplier := 2.0 / (float64(period) + 1.0)
	for i := period; i < len(closes); i++ {
		ema = (closes[i]-ema)*multiplier + ema
	}
	return ema
}

func trueRange(high, low, prevClose float64) float64 {
	hl := high - low
	hc := high - prevClose
	if hc < 0 {
		hc = -hc
	}
	lc := low - prevClose
	if lc < 0 {
		lc = -lc
	}
	m := hl
	if hc > m {
		m = hc
	}
	if lc > m {
		m = lc
	}
	return m
}

func computeInitialATR(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	sum := 0.0
	for i := n - period; i < n; i++ {
		sum += trueRange(highs[i], lows[i], closes[i-1])
	}
	return sum / float64(period)
}

// ComputeNR7 returns true if the last bar in the slice has the narrowest
// range (high - low) of the final 7 bars. Requires at least 7 bars.
func ComputeNR7(bars []domain.MarketBar) bool {
	n := len(bars)
	if n < 7 {
		return false
	}
	last7 := bars[n-7:]
	lastRange := last7[6].High - last7[6].Low
	for i := 0; i < 6; i++ {
		r := last7[i].High - last7[i].Low
		if r <= lastRange {
			return false // an earlier bar had equal or narrower range
		}
	}
	return true
}

// ComputeDailyATR computes ATR(period) from daily bars.
// Returns 0 if insufficient data.
func ComputeDailyATR(bars []domain.MarketBar, period int) float64 {
	n := len(bars)
	if n < period+1 || period <= 0 {
		return 0
	}
	// Simple ATR: average of true ranges over the last `period` bars
	sum := 0.0
	for i := n - period; i < n; i++ {
		sum += trueRange(bars[i].High, bars[i].Low, bars[i-1].Close)
	}
	return sum / float64(period)
}

// ComputeRealizedVol computes annualized realized volatility from daily bars
// using close-to-close log returns. Returns a VIX-like number (e.g. 15 = low, 25 = high).
// Uses the last `period` bars (typically 20 trading days = 1 month).
func ComputeRealizedVol(bars []domain.MarketBar, period int) float64 {
	n := len(bars)
	if n < period+1 || period <= 0 {
		return 0
	}

	// Compute log returns
	returns := make([]float64, period)
	for i := 0; i < period; i++ {
		idx := n - period + i
		prev := bars[idx-1].Close
		if prev <= 0 {
			continue
		}
		returns[i] = math.Log(bars[idx].Close / prev)
	}

	// Mean
	sum := 0.0
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(period)

	// Variance
	varSum := 0.0
	for _, r := range returns {
		diff := r - mean
		varSum += diff * diff
	}
	variance := varSum / float64(period-1)

	// Annualize: sqrt(252) * daily std dev * 100 (to get VIX-like percentage)
	return math.Sqrt(variance) * math.Sqrt(252) * 100
}
