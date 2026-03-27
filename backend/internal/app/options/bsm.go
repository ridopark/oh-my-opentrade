package options

import (
	"math"
)

// BSMPrice computes the Black-Scholes theoretical price for a European option.
//
// Parameters:
//   - s: underlying price
//   - k: strike price
//   - t: time to expiry in years (DTE / 365)
//   - r: risk-free rate (annualized, e.g. 0.05 for 5%)
//   - sigma: implied volatility (annualized, e.g. 0.20 for 20%)
//   - isCall: true for call, false for put
//
// Returns: theoretical option price, delta, gamma, theta (per day)
func BSMPrice(s, k, t, r, sigma float64, isCall bool) (price, delta, gamma, thetaPerDay float64) {
	if t <= 0 || sigma <= 0 || s <= 0 || k <= 0 {
		return 0, 0, 0, 0
	}

	sqrtT := math.Sqrt(t)
	d1 := (math.Log(s/k) + (r+0.5*sigma*sigma)*t) / (sigma * sqrtT)
	d2 := d1 - sigma*sqrtT

	nd1 := normCDF(d1)
	nd2 := normCDF(d2)
	npd1 := normPDF(d1)

	discountFactor := math.Exp(-r * t)

	if isCall {
		price = s*nd1 - k*discountFactor*nd2
		delta = nd1
	} else {
		price = k*discountFactor*(1-nd2) - s*(1-nd1)
		delta = nd1 - 1
	}

	// Gamma (same for calls and puts)
	gamma = npd1 / (s * sigma * sqrtT)

	// Theta per day (negative for long options — they lose value over time)
	thetaPerDay = (-(s * npd1 * sigma) / (2 * sqrtT) - r*k*discountFactor*nd2) / 365
	if !isCall {
		thetaPerDay = (-(s * npd1 * sigma) / (2 * sqrtT) + r*k*discountFactor*(1-nd2)) / 365
	}

	return price, delta, gamma, thetaPerDay
}

// BSMPriceAtTime computes the option price at a future time given the underlying moves.
// Useful for computing P&L during backtesting.
func BSMPriceAtTime(underlyingPrice, strike, dteYears, riskFreeRate, iv float64, isCall bool) float64 {
	price, _, _, _ := BSMPrice(underlyingPrice, strike, dteYears, riskFreeRate, iv, isCall)
	return price
}

// normCDF computes the standard normal cumulative distribution function.
func normCDF(x float64) float64 {
	return 0.5 * (1 + math.Erf(x/math.Sqrt2))
}

// normPDF computes the standard normal probability density function.
func normPDF(x float64) float64 {
	return math.Exp(-0.5*x*x) / math.Sqrt(2*math.Pi)
}
