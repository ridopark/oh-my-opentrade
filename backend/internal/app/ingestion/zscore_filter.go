package ingestion

import (
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

const defaultVolumeThreshold = 2.0

// Per-asset-class bar-over-bar deviation thresholds.
// Crypto 3%: catches Alpaca phantom wicks (2.8%) while allowing liquidation cascades.
// Equity 10%: aligned with LULD Tier 1/2 circuit breaker bands; covers halt resumes and open gaps.
const (
	DeviationCrypto  = 0.03
	DeviationEquity  = 0.10
	DeviationDefault = 0.10
)

type rollingWindow struct {
	prices    []float64
	volumes   []float64
	lastClose float64
	seeded    bool
}

type ZScoreFilter struct {
	windowSize       int
	priceThreshold   float64
	defaultDeviation float64
	symbolDeviation  map[domain.Symbol]float64
	windows          map[domain.Symbol]*rollingWindow
}

func NewZScoreFilter(windowSize int, priceThreshold float64) *ZScoreFilter {
	return &ZScoreFilter{
		windowSize:       windowSize,
		priceThreshold:   priceThreshold,
		defaultDeviation: DeviationDefault,
		symbolDeviation:  make(map[domain.Symbol]float64),
		windows:          make(map[domain.Symbol]*rollingWindow),
	}
}

func (f *ZScoreFilter) SetMaxDeviation(symbol domain.Symbol, maxDev float64) {
	f.symbolDeviation[symbol] = maxDev
}

func (f *ZScoreFilter) maxDeviationFor(symbol domain.Symbol) float64 {
	if dev, ok := f.symbolDeviation[symbol]; ok {
		return dev
	}
	return f.defaultDeviation
}

// Seed pre-fills the rolling window from historical bars, eliminating the warmup blind spot after restarts.
// Bars must be chronological (oldest first).
func (f *ZScoreFilter) Seed(symbol domain.Symbol, bars []domain.MarketBar) int {
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
	return len(window.prices)
}

func (f *ZScoreFilter) Check(bar domain.MarketBar) bool {
	window, exists := f.windows[bar.Symbol]
	if !exists {
		window = &rollingWindow{
			prices:  make([]float64, 0, f.windowSize),
			volumes: make([]float64, 0, f.windowSize),
		}
		f.windows[bar.Symbol] = window
	}

	// Layer 1: bar-over-bar deviation — works from bar #1, no warmup needed.
	if window.seeded {
		ref := window.lastClose
		if ref > 0 {
			for _, v := range []float64{bar.Open, bar.High, bar.Low, bar.Close} {
				if math.Abs(v-ref)/ref > f.maxDeviationFor(bar.Symbol) {
					return true
				}
			}
		}
	}

	// Layer 2: Z-score statistical filter — needs full window.
	if len(window.prices) < f.windowSize {
		window.prices = append(window.prices, bar.Close)
		window.volumes = append(window.volumes, bar.Volume)
		window.lastClose = bar.Close
		window.seeded = true
		return false
	}

	priceMean, priceStdDev := calculateStats(window.prices)
	volMean, volStdDev := calculateStats(window.volumes)

	closeZ := calculateZScore(bar.Close, priceMean, priceStdDev)
	highZ := calculateZScore(bar.High, priceMean, priceStdDev)
	lowZ := calculateZScore(bar.Low, priceMean, priceStdDev)
	volZ := calculateZScore(bar.Volume, volMean, volStdDev)

	priceAnomaly := closeZ > f.priceThreshold || highZ > f.priceThreshold || lowZ > f.priceThreshold
	suspect := priceAnomaly && volZ < defaultVolumeThreshold

	if !suspect {
		window.prices = append(window.prices[1:], bar.Close)
		window.volumes = append(window.volumes[1:], bar.Volume)
		window.lastClose = bar.Close
	}

	return suspect
}

func calculateStats(values []float64) (mean float64, stdDev float64) {
	if len(values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean = sum / float64(len(values))

	var varianceSum float64
	for _, v := range values {
		diff := v - mean
		varianceSum += diff * diff
	}
	variance := varianceSum / float64(len(values))
	stdDev = math.Sqrt(variance)
	return mean, stdDev
}

func calculateZScore(value, mean, stdDev float64) float64 {
	if stdDev == 0 {
		if value == mean {
			return 0.0
		}
		return math.Inf(1)
	}
	return math.Abs(value-mean) / stdDev
}
