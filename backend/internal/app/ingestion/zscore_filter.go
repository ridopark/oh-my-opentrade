package ingestion

import (
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

const defaultVolumeThreshold = 2.0 // secondary threshold for volume

// rollingWindow stores values for calculating mean and stddev.
type rollingWindow struct {
	prices  []float64
	volumes []float64
}

// ZScoreFilter maintains rolling windows per symbol for anomaly detection.
type ZScoreFilter struct {
	windowSize     int
	priceThreshold float64 // Default: 4.0 (4 sigma)
	windows        map[domain.Symbol]*rollingWindow
}

// NewZScoreFilter initializes a new ZScoreFilter.
func NewZScoreFilter(windowSize int, priceThreshold float64) *ZScoreFilter {
	return &ZScoreFilter{
		windowSize:     windowSize,
		priceThreshold: priceThreshold,
		windows:        make(map[domain.Symbol]*rollingWindow),
	}
}

// Check computes the Z-score for the new bar and returns true if it is suspect.
func (f *ZScoreFilter) Check(bar domain.MarketBar) bool {
	window, exists := f.windows[bar.Symbol]
	if !exists {
		window = &rollingWindow{
			prices:  make([]float64, 0, f.windowSize),
			volumes: make([]float64, 0, f.windowSize),
		}
		f.windows[bar.Symbol] = window
	}

	// Not enough data
	if len(window.prices) < f.windowSize {
		window.prices = append(window.prices, bar.Close)
		window.volumes = append(window.volumes, bar.Volume)
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

	// Add to rolling window, dropping the oldest value
	// We might choose to NOT add anomalous data, but the prompt
	// says "The filter maintains a rolling window of closing prices per symbol. For each new bar: 1. Compute ... 2. Calculate... 3. If ... reject".
	// Let's NOT poison the window if it's suspect. If it passes, add it.
	// Actually, wait, it says "For each new bar: 1. Compute mean...". This means we compute using the EXISTING window.
	// It doesn't explicitly mention whether to add anomalies. Let's add them regardless or skip.
	// Often anomalies are skipped to avoid window poisoning. Let's skip them.
	if !suspect {
		window.prices = append(window.prices[1:], bar.Close)
		window.volumes = append(window.volumes[1:], bar.Volume)
	}

	return suspect
}

// calculateStats calculates the mean and standard deviation of a slice.
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

// calculateZScore computes the z-score of a value given mean and stddev.
func calculateZScore(value, mean, stdDev float64) float64 {
	if stdDev == 0 {
		if value == mean {
			return 0.0
		}
		// If stddev is 0 but value is different, it's an infinite z-score (large anomaly).
		return math.Inf(1)
	}
	return math.Abs(value-mean) / stdDev
}
