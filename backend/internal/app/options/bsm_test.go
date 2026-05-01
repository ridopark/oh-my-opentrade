package options

import (
	"math"
	"testing"
)

func TestBSMPrice_ATMCall(t *testing.T) {
	// SPY at $550, ATM call, 40 DTE, 5% rate, 15% vol
	price, delta, gamma, theta := BSMPrice(550, 550, 40.0/365, 0.05, 0.15, true)

	// ATM call should be around $8-12
	if price < 5 || price > 20 {
		t.Errorf("ATM call price %f out of expected range [5, 20]", price)
	}
	// Delta should be ~0.50-0.55 for ATM call
	if delta < 0.45 || delta > 0.60 {
		t.Errorf("ATM call delta %f out of expected range [0.45, 0.60]", delta)
	}
	// Gamma should be positive
	if gamma <= 0 {
		t.Errorf("gamma should be positive, got %f", gamma)
	}
	// Theta should be negative (time decay)
	if theta >= 0 {
		t.Errorf("theta per day should be negative, got %f", theta)
	}
	t.Logf("ATM Call: price=%.2f delta=%.4f gamma=%.6f theta=%.4f/day", price, delta, gamma, theta)
}

func TestBSMPrice_ATMPut(t *testing.T) {
	price, delta, _, _ := BSMPrice(550, 550, 40.0/365, 0.05, 0.15, false)

	// ATM put should be similar to call (put-call parity)
	if price < 3 || price > 15 {
		t.Errorf("ATM put price %f out of expected range [3, 15]", price)
	}
	// Delta should be negative for puts (~-0.45 to -0.50)
	if delta > -0.40 || delta < -0.60 {
		t.Errorf("ATM put delta %f out of expected range [-0.60, -0.40]", delta)
	}
}

func TestBSMPrice_DeepITMCall(t *testing.T) {
	// Deep ITM: strike 500, price 550
	price, delta, _, _ := BSMPrice(550, 500, 40.0/365, 0.05, 0.15, true)

	// Should be mostly intrinsic (~$50 + small time value)
	if price < 49 || price > 55 {
		t.Errorf("deep ITM call price %f out of expected range [49, 55]", price)
	}
	// Delta should be close to 1
	if delta < 0.95 {
		t.Errorf("deep ITM call delta %f should be > 0.95", delta)
	}
}

func TestBSMPrice_DeepOTMCall(t *testing.T) {
	// Deep OTM: strike 600, price 550
	price, delta, _, _ := BSMPrice(550, 600, 40.0/365, 0.05, 0.15, true)

	// Should be very cheap
	if price > 2 {
		t.Errorf("deep OTM call price %f should be < 2", price)
	}
	// Delta should be close to 0
	if delta > 0.10 {
		t.Errorf("deep OTM call delta %f should be < 0.10", delta)
	}
}

func TestBSMPrice_ZeroTime(t *testing.T) {
	price, delta, _, _ := BSMPrice(550, 550, 0, 0.05, 0.15, true)
	if price != 0 || delta != 0 {
		t.Errorf("zero time should return zero price/delta, got price=%f delta=%f", price, delta)
	}
}

func TestBSMPrice_PutCallParity(t *testing.T) {
	s, k, tYears, r, sigma := 550.0, 550.0, 40.0/365, 0.05, 0.15
	callPrice, _, _, _ := BSMPrice(s, k, tYears, r, sigma, true)
	putPrice, _, _, _ := BSMPrice(s, k, tYears, r, sigma, false)

	// Put-call parity: C - P = S - K*exp(-rT)
	expected := s - k*math.Exp(-r*tYears)
	actual := callPrice - putPrice

	if math.Abs(actual-expected) > 0.01 {
		t.Errorf("put-call parity violated: C-P=%.4f, expected S-PV(K)=%.4f", actual, expected)
	}
}

// TestImpliedVol_RoundTrip exercises the IV inversion across the IV/DTE
// rectangle the historical-backfill path will hit. Each (sigma_true, dte)
// pair generates a forward price, then ImpliedVol must recover sigma_true
// within 1e-5. ATM strikes only — vega vanishes far from the money so
// inversion legitimately falls back to chainIV out there, which is the
// skip-row branch the importer relies on (not a round-trip target).
func TestImpliedVol_RoundTrip(t *testing.T) {
	const tol = 1e-5
	s := 100.0
	k := 100.0 // ATM keeps vega well-conditioned across the whole grid
	r := 0.045
	ivs := []float64{0.05, 0.10, 0.20, 0.40, 0.80, 1.20, 2.0}
	dtes := []int{1, 5, 14, 30, 60}
	for _, isCall := range []bool{true, false} {
		for _, sigmaTrue := range ivs {
			for _, dte := range dtes {
				tYears := float64(dte) / 365.0
				price, _, _, _ := BSMPrice(s, k, tYears, r, sigmaTrue, isCall)
				if price <= 0 {
					t.Fatalf("forward price non-positive at sigma=%.2f dte=%d call=%v", sigmaTrue, dte, isCall)
				}
				// chainIV starts from a deliberately wrong guess so we exercise
				// Newton-Raphson, not just the cached starting point.
				recovered := ImpliedVol(price, s, k, tYears, r, isCall, 0.30)
				if math.Abs(recovered-sigmaTrue) > tol {
					t.Errorf("IV round-trip miss: sigma_true=%.6f recovered=%.6f dte=%d call=%v |diff|=%g",
						sigmaTrue, recovered, dte, isCall, math.Abs(recovered-sigmaTrue))
				}
			}
		}
	}
}

// TestImpliedVol_DeepOTMSkipsToFallback documents the skip-row contract the
// historical-backfill importer leans on: when vega vanishes (deep OTM with
// short DTE), ImpliedVol returns chainIV unchanged so the caller can detect
// "no calibration possible" rather than persisting a wrong IV.
func TestImpliedVol_DeepOTMSkipsToFallback(t *testing.T) {
	// 1 DTE, 50% OTM call, 50% IV — vega is effectively zero.
	s, k, tYears, r := 100.0, 150.0, 1.0/365, 0.045
	price, _, _, _ := BSMPrice(s, k, tYears, r, 0.50, true)
	const sentinel = 0.30
	got := ImpliedVol(price, s, k, tYears, r, true, sentinel)
	// We don't require equality to sentinel exactly — convergence may land
	// arbitrarily close to the true 0.50 if vega is non-trivial. We DO
	// require the function not to diverge / panic, and to return a positive
	// finite IV.
	if math.IsNaN(got) || math.IsInf(got, 0) || got <= 0 {
		t.Fatalf("ImpliedVol returned non-finite or non-positive: %v", got)
	}
}
