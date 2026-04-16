package strategy

import "fmt"

// SkewRegime classifies the current IV surface into a regime that gates
// funding/basis strategies. High-fear regimes (steep put skew, elevated ATM
// IV, inverted term structure) signal risk-off and should reduce or halt
// carry exposure.
type SkewRegime int

const (
	// SkewRegimeNeutral indicates a balanced IV surface with no strong
	// directional skew signal. Carry strategies operate at normal sizing.
	SkewRegimeNeutral SkewRegime = iota

	// SkewRegimeFearful indicates steep put skew and/or elevated ATM IV
	// relative to the rolling median. Carry exposure should be reduced.
	SkewRegimeFearful

	// SkewRegimeGreedy indicates flat or call-skewed surface with suppressed
	// ATM IV. Full carry exposure is appropriate.
	SkewRegimeGreedy

	// SkewRegimeDislocated indicates an extreme term-structure inversion
	// (short-dated IV far exceeds long-dated). All carry should be halted.
	SkewRegimeDislocated
)

// String returns the human-readable regime name.
func (r SkewRegime) String() string {
	switch r {
	case SkewRegimeNeutral:
		return "neutral"
	case SkewRegimeFearful:
		return "fearful"
	case SkewRegimeGreedy:
		return "greedy"
	case SkewRegimeDislocated:
		return "dislocated"
	default:
		return fmt.Sprintf("unknown(%d)", int(r))
	}
}

// Thresholds for regime classification. These are exported so that callers
// can inspect them, but the classifier itself uses them as constants.
const (
	// RRFearThreshold: risk-reversal below this level (negative = put skew
	// exceeds call skew) triggers fearful classification. Expressed as a
	// fraction (e.g. -0.03 means 25d-put IV is 3pp above 25d-call IV).
	RRFearThreshold = -0.03

	// RRGreedThreshold: risk-reversal above this level triggers greedy.
	RRGreedThreshold = 0.01

	// ATMIVElevatedRatio: if current ATMIV7d exceeds the rolling median by
	// this factor, IV is considered elevated.
	ATMIVElevatedRatio = 1.20

	// ATMIVSuppressedRatio: if current ATMIV7d is below the rolling median
	// by this factor, IV is considered suppressed.
	ATMIVSuppressedRatio = 0.85

	// TermSlopeDislocatedThreshold: term slope below this level indicates
	// extreme inversion (short-dated IV >> long-dated).
	TermSlopeDislocatedThreshold = -0.10
)

// ClassifySkewRegime determines the IV regime from a surface snapshot and the
// 30-day rolling median of ATM IV. The function is stateless and
// deterministic: all state (rolling medians) must be computed externally.
//
// Priority order (first match wins):
//  1. Dislocated — term slope < -0.10 (extreme inversion)
//  2. Fearful — RR25d7d < -3% AND (ATMIV7d > 1.2x rolling median)
//  3. Greedy — RR25d7d > +1% AND (ATMIV7d < 0.85x rolling median)
//  4. Neutral — everything else
func ClassifySkewRegime(rr25d7d, atmIV7d, termSlope, rollingATMIVMedian float64) SkewRegime {
	// Guard: if rolling median is zero/negative (no history), fall back to
	// neutral to avoid division-by-zero or nonsensical ratios.
	if rollingATMIVMedian <= 0 {
		return SkewRegimeNeutral
	}

	// 1. Dislocated: extreme term-structure inversion.
	if termSlope < TermSlopeDislocatedThreshold {
		return SkewRegimeDislocated
	}

	ivRatio := atmIV7d / rollingATMIVMedian

	// 2. Fearful: steep put skew + elevated IV.
	if rr25d7d < RRFearThreshold && ivRatio > ATMIVElevatedRatio {
		return SkewRegimeFearful
	}

	// 3. Greedy: call-biased skew + suppressed IV.
	if rr25d7d > RRGreedThreshold && ivRatio < ATMIVSuppressedRatio {
		return SkewRegimeGreedy
	}

	return SkewRegimeNeutral
}
