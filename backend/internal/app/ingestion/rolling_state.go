package ingestion

import (
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	defaultATRPeriod    = 14
	defaultVolSMAPeriod = 20
)

type RollingATR struct {
	period    int
	atr       float64
	prevClose float64
	count     int
	seeded    bool
	trSum     float64
}

func NewRollingATR(period int) *RollingATR {
	return &RollingATR{period: period}
}

func (r *RollingATR) Update(high, low, close float64) {
	if r.count == 0 {
		r.prevClose = close
		r.count++
		return
	}

	tr := trueRangeCalc(high, low, r.prevClose)
	r.count++

	if !r.seeded {
		r.trSum += tr
		if r.count >= r.period+1 {
			r.atr = r.trSum / float64(r.period)
			r.seeded = true
		}
	} else {
		r.atr = (r.atr*float64(r.period-1) + tr) / float64(r.period)
	}

	r.prevClose = close
}

func (r *RollingATR) Value() float64 {
	if !r.seeded {
		return 0
	}
	return r.atr
}

func (r *RollingATR) Seeded() bool { return r.seeded }

func (r *RollingATR) PrevClose() float64 { return r.prevClose }

func (r *RollingATR) Seed(bars []domain.MarketBar) {
	for _, b := range bars {
		r.Update(b.High, b.Low, b.Close)
	}
}

func trueRangeCalc(high, low, prevClose float64) float64 {
	hl := high - low
	hc := math.Abs(high - prevClose)
	lc := math.Abs(low - prevClose)
	m := hl
	if hc > m {
		m = hc
	}
	if lc > m {
		m = lc
	}
	return m
}

type RollingVolSMA struct {
	period  int
	volumes []float64
}

func NewRollingVolSMA(period int) *RollingVolSMA {
	return &RollingVolSMA{
		period:  period,
		volumes: make([]float64, 0, period),
	}
}

func (r *RollingVolSMA) Update(volume float64) {
	r.volumes = append(r.volumes, volume)
	if len(r.volumes) > r.period {
		r.volumes = r.volumes[1:]
	}
}

func (r *RollingVolSMA) Value() float64 {
	if len(r.volumes) < r.period {
		return 0
	}
	sum := 0.0
	for _, v := range r.volumes {
		sum += v
	}
	return sum / float64(len(r.volumes))
}

func (r *RollingVolSMA) Seeded() bool {
	return len(r.volumes) >= r.period
}

func (r *RollingVolSMA) Seed(bars []domain.MarketBar) {
	for _, b := range bars {
		r.Update(b.Volume)
	}
}
