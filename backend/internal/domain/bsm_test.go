package domain

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBSMPrice(t *testing.T) {
	const (
		spot  = 150.0
		strike = 150.0
		dte7  = 7.0 / 365.25
		r     = 0.045
		sigma = 0.30
	)

	t.Run("ATM call price in expected range", func(t *testing.T) {
		price := BSMPrice(spot, strike, dte7, r, sigma, true)
		assert.Greater(t, price, 2.0, "ATM 7-DTE call should be > 2.0")
		assert.Less(t, price, 4.0, "ATM 7-DTE call should be < 4.0")
	})

	t.Run("ATM put-call parity", func(t *testing.T) {
		callPrice := BSMPrice(spot, strike, dte7, r, sigma, true)
		putPrice := BSMPrice(spot, strike, dte7, r, sigma, false)
		// Put-call parity: C - P = S - K*exp(-rT)
		lhs := callPrice - putPrice
		rhs := spot - strike*math.Exp(-r*dte7)
		assert.InDelta(t, rhs, lhs, 0.01, "put-call parity should hold")
	})

	t.Run("deep ITM call premium exceeds intrinsic", func(t *testing.T) {
		deepITMSpot := 160.0
		price := BSMPrice(deepITMSpot, strike, dte7, r, sigma, true)
		intrinsic := deepITMSpot - strike // 10.0
		assert.Greater(t, price, intrinsic, "deep ITM call should exceed intrinsic value")
	})

	t.Run("deep OTM call is small but positive", func(t *testing.T) {
		deepOTMSpot := 140.0
		price := BSMPrice(deepOTMSpot, strike, dte7, r, sigma, true)
		assert.Greater(t, price, 0.0, "deep OTM call should be positive")
		assert.Less(t, price, 1.0, "deep OTM call with 7 DTE should be small")
	})

	t.Run("zero time returns zero", func(t *testing.T) {
		price := BSMPrice(spot, strike, 0, r, sigma, true)
		assert.Equal(t, 0.0, price)
	})

	t.Run("zero sigma returns zero", func(t *testing.T) {
		price := BSMPrice(spot, strike, dte7, r, 0, true)
		assert.Equal(t, 0.0, price)
	})

	t.Run("zero spot returns zero", func(t *testing.T) {
		price := BSMPrice(0, strike, dte7, r, sigma, true)
		assert.Equal(t, 0.0, price)
	})

	t.Run("zero strike returns zero", func(t *testing.T) {
		price := BSMPrice(spot, 0, dte7, r, sigma, true)
		assert.Equal(t, 0.0, price)
	})

	t.Run("negative time returns zero", func(t *testing.T) {
		price := BSMPrice(spot, strike, -0.01, r, sigma, true)
		assert.Equal(t, 0.0, price)
	})

	t.Run("near-expiry ITM call is close to intrinsic", func(t *testing.T) {
		deepITMSpot := 160.0
		nearExpiry := 0.001 // ~8.7 hours
		price := BSMPrice(deepITMSpot, strike, nearExpiry, r, sigma, true)
		intrinsic := deepITMSpot - strike // 10.0
		assert.InDelta(t, intrinsic, price, 0.50, "near-expiry deep ITM should be close to intrinsic")
	})

	t.Run("near-expiry OTM call is near zero", func(t *testing.T) {
		otmSpot := 140.0
		nearExpiry := 0.001
		price := BSMPrice(otmSpot, strike, nearExpiry, r, sigma, true)
		assert.Less(t, price, 0.05, "near-expiry OTM call should be near zero")
		assert.GreaterOrEqual(t, price, 0.0)
	})

	t.Run("put gains value as spot decreases", func(t *testing.T) {
		putAtMoney := BSMPrice(150.0, strike, dte7, r, sigma, false)
		putBelow := BSMPrice(145.0, strike, dte7, r, sigma, false)
		putDeepBelow := BSMPrice(140.0, strike, dte7, r, sigma, false)

		assert.Greater(t, putBelow, putAtMoney, "put should gain as spot drops below strike")
		assert.Greater(t, putDeepBelow, putBelow, "put should gain more as spot drops further")
	})

	t.Run("call gains value as spot increases", func(t *testing.T) {
		callATM := BSMPrice(150.0, strike, dte7, r, sigma, true)
		callAbove := BSMPrice(155.0, strike, dte7, r, sigma, true)

		assert.Greater(t, callAbove, callATM, "call should gain as spot rises above strike")
	})
}

func TestNormCDF(t *testing.T) {
	t.Run("CDF at zero is 0.5", func(t *testing.T) {
		assert.InDelta(t, 0.5, normCDF(0), 1e-12)
	})

	t.Run("CDF at large positive is near 1", func(t *testing.T) {
		assert.InDelta(t, 1.0, normCDF(10), 1e-9)
	})

	t.Run("CDF at large negative is near 0", func(t *testing.T) {
		assert.InDelta(t, 0.0, normCDF(-10), 1e-9)
	})

	t.Run("symmetry: CDF(x) + CDF(-x) = 1", func(t *testing.T) {
		for _, x := range []float64{0.5, 1.0, 1.96, 2.33} {
			sum := normCDF(x) + normCDF(-x)
			assert.InDelta(t, 1.0, sum, 1e-12, "CDF(%.2f) + CDF(-%.2f) should equal 1", x, x)
		}
	})

	t.Run("known values", func(t *testing.T) {
		// N(1.96) ~ 0.975
		assert.InDelta(t, 0.975, normCDF(1.96), 0.001)
		// N(-1.96) ~ 0.025
		assert.InDelta(t, 0.025, normCDF(-1.96), 0.001)
	})
}
