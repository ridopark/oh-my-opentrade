package strategy

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEMARolling_Period9_Recurrence(t *testing.T) {
	period := 9
	e := NewEMARolling(period)
	prices := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}

	for i := 0; i < period; i++ {
		e.Update(prices[i])
	}
	assert.True(t, e.IsReady(), "EMA should be ready after period bars")

	seed := 0.0
	for i := 0; i < period; i++ {
		seed += prices[i]
	}
	seed /= float64(period)
	assert.InDelta(t, seed, e.Value(), 1e-12, "seeded value should equal simple mean of first period bars")

	alpha := 2.0 / (float64(period) + 1.0)
	want := seed
	for i := period; i < len(prices); i++ {
		want = alpha*prices[i] + (1-alpha)*want
		e.Update(prices[i])
		assert.InDelta(t, want, e.Value(), 1e-12)
	}
}

func TestEMARolling_WarmupNotReady(t *testing.T) {
	e := NewEMARolling(5)
	for i := 0; i < 4; i++ {
		e.Update(float64(i + 1))
		assert.False(t, e.IsReady(), "should not be ready before period bars seen")
	}
	e.Update(5)
	assert.True(t, e.IsReady())
}

func TestEMARolling_Period1(t *testing.T) {
	e := NewEMARolling(1)
	e.Update(42.0)
	assert.True(t, e.IsReady())
	assert.InDelta(t, 42.0, e.Value(), 1e-12)
	e.Update(50.0)
	assert.InDelta(t, 50.0, e.Value(), 1e-12, "alpha=1 means EMA tracks latest price")
}

func TestEMARolling_NaNAndInfSkipped(t *testing.T) {
	e := NewEMARolling(3)
	e.Update(10)
	e.Update(20)
	e.Update(30)
	v := e.Value()
	e.Update(math.NaN())
	assert.InDelta(t, v, e.Value(), 1e-12, "NaN updates should be skipped")
	e.Update(math.Inf(1))
	assert.InDelta(t, v, e.Value(), 1e-12, "Inf updates should be skipped")
}
