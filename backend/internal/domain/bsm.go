package domain

import "math"

// BSMPrice computes the Black-Scholes-Merton theoretical price for a European
// option. This is a pure-math function with no external dependencies, suitable
// for the domain layer.
//
// Parameters:
//   - s: underlying (spot) price
//   - k: strike price
//   - t: time to expiry in years (e.g. 7 / 365.25)
//   - r: risk-free rate (annualized, e.g. 0.045)
//   - sigma: implied volatility (annualized, e.g. 0.30)
//   - isCall: true for call, false for put
//
// Returns the theoretical option price. Returns 0 if any input is non-positive
// or time to expiry is <= 0.
func BSMPrice(s, k, t, r, sigma float64, isCall bool) float64 {
	if t <= 0 || sigma <= 0 || s <= 0 || k <= 0 {
		return 0
	}

	sqrtT := math.Sqrt(t)
	d1 := (math.Log(s/k) + (r+0.5*sigma*sigma)*t) / (sigma * sqrtT)
	d2 := d1 - sigma*sqrtT

	nd1 := normCDF(d1)
	nd2 := normCDF(d2)
	discountFactor := math.Exp(-r * t)

	if isCall {
		return s*nd1 - k*discountFactor*nd2
	}
	return k*discountFactor*(1-nd2) - s*(1-nd1)
}

// normCDF computes the standard normal cumulative distribution function.
func normCDF(x float64) float64 {
	return 0.5 * (1 + math.Erf(x/math.Sqrt2))
}
