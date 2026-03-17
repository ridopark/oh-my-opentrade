package ingestion

import (
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type FilterStatus int

const (
	FilterPass FilterStatus = iota
	FilterRepaired
	FilterRejected
)

func (s FilterStatus) String() string {
	switch s {
	case FilterPass:
		return "pass"
	case FilterRepaired:
		return "repaired"
	case FilterRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

const (
	wickBodyRatio    = 3.0
	volumeGateRatio  = 1.2
	tradeCountFloor  = 5
	atrRangeMultiple = 2.5
	atrEnvelopeMult  = 3.5
	atrClampMult     = 0.5
)

type AdaptiveFilter struct {
	windowSize       int
	priceThreshold   float64
	defaultDeviation float64
	symbolDeviation  map[domain.Symbol]float64
	windows          map[domain.Symbol]*rollingWindow
	atrState         map[domain.Symbol]*RollingATR
	volState         map[domain.Symbol]*RollingVolSMA
	passthrough      bool
}

func NewAdaptiveFilter(windowSize int, priceThreshold float64) *AdaptiveFilter {
	return &AdaptiveFilter{
		windowSize:       windowSize,
		priceThreshold:   priceThreshold,
		defaultDeviation: DeviationDefault,
		symbolDeviation:  make(map[domain.Symbol]float64),
		windows:          make(map[domain.Symbol]*rollingWindow),
		atrState:         make(map[domain.Symbol]*RollingATR),
		volState:         make(map[domain.Symbol]*RollingVolSMA),
	}
}

func (f *AdaptiveFilter) SetMaxDeviation(symbol domain.Symbol, maxDev float64) {
	f.symbolDeviation[symbol] = maxDev
}

func (f *AdaptiveFilter) maxDeviationFor(symbol domain.Symbol) float64 {
	if dev, ok := f.symbolDeviation[symbol]; ok {
		return dev
	}
	return f.defaultDeviation
}

func (f *AdaptiveFilter) Seed(symbol domain.Symbol, bars []domain.MarketBar) int {
	if len(bars) == 0 {
		return 0
	}

	window := &rollingWindow{
		prices:  make([]float64, 0, f.windowSize),
		volumes: make([]float64, 0, f.windowSize),
	}
	start := 0
	if len(bars) > f.windowSize {
		start = len(bars) - f.windowSize
	}
	for _, bar := range bars[start:] {
		window.prices = append(window.prices, bar.Close)
		window.volumes = append(window.volumes, bar.Volume)
	}
	window.lastClose = bars[len(bars)-1].Close
	window.seeded = true
	f.windows[symbol] = window

	atr := NewRollingATR(defaultATRPeriod)
	atr.Seed(bars)
	f.atrState[symbol] = atr

	vol := NewRollingVolSMA(defaultVolSMAPeriod)
	vol.Seed(bars)
	f.volState[symbol] = vol

	return len(window.prices)
}

func (f *AdaptiveFilter) ensureState(symbol domain.Symbol) (*rollingWindow, *RollingATR, *RollingVolSMA) {
	w, ok := f.windows[symbol]
	if !ok {
		w = &rollingWindow{
			prices:  make([]float64, 0, f.windowSize),
			volumes: make([]float64, 0, f.windowSize),
		}
		f.windows[symbol] = w
	}
	a, ok := f.atrState[symbol]
	if !ok {
		a = NewRollingATR(defaultATRPeriod)
		f.atrState[symbol] = a
	}
	v, ok := f.volState[symbol]
	if !ok {
		v = NewRollingVolSMA(defaultVolSMAPeriod)
		f.volState[symbol] = v
	}
	return w, a, v
}

// RepairGate identifies which gate triggered the repair.
type RepairGate string

const (
	GateTradeCount RepairGate = "tradecount"
	GateWick       RepairGate = "wick"
	GateATR        RepairGate = "atr_envelope"
	GateZScore     RepairGate = "zscore"
	GateDeviation  RepairGate = "deviation"
)

type FilterResult struct {
	Bar    domain.MarketBar
	Status FilterStatus
	Gate   RepairGate
}

func (f *AdaptiveFilter) SetPassthrough(v bool) { f.passthrough = v }

func (f *AdaptiveFilter) Process(bar domain.MarketBar) FilterResult {
	if f.passthrough {
		return FilterResult{Bar: bar, Status: FilterPass}
	}
	w, atr, vol := f.ensureState(bar.Symbol)
	atrSeeded := atr.Seeded()
	atrVal := atr.Value()
	volSMA := vol.Value()
	prevClose := atr.PrevClose()

	result := FilterResult{Bar: bar, Status: FilterPass}

	// Gate 0: legacy bar-over-bar deviation (always active, serves as warmup fallback)
	if w.seeded && prevClose > 0 {
		maxDev := f.maxDeviationFor(bar.Symbol)
		for _, v := range []float64{bar.Open, bar.High, bar.Low, bar.Close} {
			if math.Abs(v-prevClose)/prevClose > maxDev {
				if !atrSeeded {
					result.Status = FilterRejected
					result.Gate = GateDeviation
					return f.finalize(result, w, atr, vol)
				}
				break
			}
		}
	}

	// Gate 1: TradeCount (cheapest check)
	if bar.TradeCount > 0 && bar.TradeCount < tradeCountFloor {
		if atrSeeded && atrVal > 0 && (bar.High-bar.Low) > atrRangeMultiple*atrVal {
			result = f.repairBar(result, atr)
			result.Gate = GateTradeCount
			return f.finalize(result, w, atr, vol)
		}
	}

	highVolumeMove := volSMA > 0 && bar.Volume > 2*volSMA && bar.TradeCount > 20

	// Gate 2: Wick-to-Body ratio (skip for doji candles where body ≈ 0)
	if atrSeeded && volSMA > 0 {
		body := math.Abs(bar.Close - bar.Open)
		minBody := atrVal * 0.01
		if body > minBody {
			upperWick := bar.High - math.Max(bar.Open, bar.Close)
			lowerWick := math.Min(bar.Open, bar.Close) - bar.Low

			wickAnomaly := upperWick > wickBodyRatio*body || lowerWick > wickBodyRatio*body
			lowVolume := bar.Volume < volumeGateRatio*volSMA

			if wickAnomaly && lowVolume {
				result = f.repairBar(result, atr)
				result.Gate = GateWick
				return f.finalize(result, w, atr, vol)
			}
		}
	}

	// Gate 3: ATR envelope (skip if volume+trades indicate legitimate move)
	if atrSeeded && atrVal > 0 && prevClose > 0 && !highVolumeMove {
		ceiling := prevClose + atrEnvelopeMult*atrVal
		floor := prevClose - atrEnvelopeMult*atrVal
		if bar.High > ceiling || bar.Low < floor {
			result = f.repairBar(result, atr)
			result.Gate = GateATR
			return f.finalize(result, w, atr, vol)
		}
	}

	// Gate 4: Z-score catch-all (same as original Layer 2)
	if len(w.prices) >= f.windowSize {
		priceMean, priceStdDev := calculateStats(w.prices)
		volMean, volStdDev := calculateStats(w.volumes)

		closeZ := calculateZScore(bar.Close, priceMean, priceStdDev)
		highZ := calculateZScore(bar.High, priceMean, priceStdDev)
		lowZ := calculateZScore(bar.Low, priceMean, priceStdDev)
		volZ := calculateZScore(bar.Volume, volMean, volStdDev)

		priceAnomaly := closeZ > f.priceThreshold || highZ > f.priceThreshold || lowZ > f.priceThreshold
		if priceAnomaly && volZ < defaultVolumeThreshold {
			result.Status = FilterRejected
			result.Gate = GateZScore
			return f.finalize(result, w, atr, vol)
		}
	}

	return f.finalize(result, w, atr, vol)
}

func (f *AdaptiveFilter) repairBar(r FilterResult, atr *RollingATR) FilterResult {
	bar := r.Bar
	bar.OriginalHigh = bar.High
	bar.OriginalLow = bar.Low
	bar.Repaired = true

	atrVal := atr.Value()
	bodyHigh := math.Max(bar.Open, bar.Close)
	bodyLow := math.Min(bar.Open, bar.Close)

	bar.High = bodyHigh + atrClampMult*atrVal
	bar.Low = bodyLow - atrClampMult*atrVal

	if bar.High < bar.Close {
		bar.High = bar.Close
	}
	if bar.Low > bar.Close {
		bar.Low = bar.Close
	}
	if bar.High < bar.Low {
		bar.High = bar.Low
	}

	r.Bar = bar
	r.Status = FilterRepaired
	return r
}

func (f *AdaptiveFilter) finalize(r FilterResult, w *rollingWindow, atr *RollingATR, vol *RollingVolSMA) FilterResult {
	bar := r.Bar

	if r.Status != FilterRejected {
		atr.Update(bar.High, bar.Low, bar.Close)
		vol.Update(bar.Volume)
		if len(w.prices) < f.windowSize {
			w.prices = append(w.prices, bar.Close)
			w.volumes = append(w.volumes, bar.Volume)
		} else {
			w.prices = append(w.prices[1:], bar.Close)
			w.volumes = append(w.volumes[1:], bar.Volume)
		}
		w.lastClose = bar.Close
		w.seeded = true
	}

	return r
}
