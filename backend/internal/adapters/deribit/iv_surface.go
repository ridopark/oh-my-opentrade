package deribit

import (
	"math"
	"sort"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// tenorBucket groups instruments by the target tenor they are closest to.
type tenorBucket struct {
	targetDays int
	expiry     time.Time
	calls      []tickerWithMeta
	puts       []tickerWithMeta
}

// tickerWithMeta combines a Ticker with the Instrument metadata needed for
// IV surface construction (strike, expiry, option type).
type tickerWithMeta struct {
	Ticker
	Strike     float64
	Expiration time.Time
	OptionType string // "call" or "put"
}

// BuildIVSurface constructs an IV surface from a batch of instruments and
// tickers. It groups options by the nearest tenor bucket (7d, 30d),
// interpolates ATM IV, finds 25-delta options, and computes risk reversals,
// butterflies, and term structure metrics.
func BuildIVSurface(asset string, now time.Time, instruments []Instrument, tickers []Ticker) ports.IVSurface {
	// Build a map from instrument name to Instrument for metadata lookup.
	instMap := make(map[string]Instrument, len(instruments))
	for _, inst := range instruments {
		instMap[inst.InstrumentName] = inst
	}

	// Combine tickers with instrument metadata.
	var combined []tickerWithMeta
	for _, tk := range tickers {
		inst, ok := instMap[tk.InstrumentName]
		if !ok {
			continue
		}
		combined = append(combined, tickerWithMeta{
			Ticker:     tk,
			Strike:     inst.Strike,
			Expiration: inst.Expiration,
			OptionType: inst.OptionType,
		})
	}

	// Find the closest expiry to each target tenor.
	bucket7d := findClosestExpiry(combined, now, 7)
	bucket30d := findClosestExpiry(combined, now, 30)

	atmIV7d := interpolateATMIV(bucket7d)
	atmIV30d := interpolateATMIV(bucket30d)

	rr7d, putIV7d := compute25DeltaMetrics(bucket7d)
	rr30d, _ := compute25DeltaMetrics(bucket30d)

	bf7d := computeButterfly(bucket7d, atmIV7d)
	bf30d := computeButterfly(bucket30d, atmIV30d)

	var termSlope float64
	if atmIV7d > 0 {
		termSlope = atmIV30d/atmIV7d - 1
	}

	var putSkew7d float64
	if atmIV7d > 0 && putIV7d > 0 {
		putSkew7d = putIV7d/atmIV7d - 1
	}

	return ports.IVSurface{
		Asset:     asset,
		Timestamp: now,
		ATMIV7d:   atmIV7d,
		ATMIV30d:  atmIV30d,
		RR25d7d:   rr7d,
		RR25d30d:  rr30d,
		BF25d7d:   bf7d,
		BF25d30d:  bf30d,
		TermSlope: termSlope,
		PutSkew7d: putSkew7d,
	}
}

// findClosestExpiry selects the expiry date closest to (now + targetDays)
// and returns all options at that expiry, split into calls and puts.
func findClosestExpiry(options []tickerWithMeta, now time.Time, targetDays int) *tenorBucket {
	if len(options) == 0 {
		return &tenorBucket{targetDays: targetDays}
	}

	target := now.Add(time.Duration(targetDays) * 24 * time.Hour)

	// Collect distinct expiries.
	expirySet := make(map[time.Time]struct{})
	for _, o := range options {
		expirySet[o.Expiration] = struct{}{}
	}

	// Find the expiry closest to target.
	var bestExpiry time.Time
	bestDist := math.MaxFloat64
	for exp := range expirySet {
		dist := math.Abs(exp.Sub(target).Seconds())
		if dist < bestDist {
			bestDist = dist
			bestExpiry = exp
		}
	}

	bucket := &tenorBucket{
		targetDays: targetDays,
		expiry:     bestExpiry,
	}

	for _, o := range options {
		if !o.Expiration.Equal(bestExpiry) {
			continue
		}
		switch o.OptionType {
		case "call":
			bucket.calls = append(bucket.calls, o)
		case "put":
			bucket.puts = append(bucket.puts, o)
		}
	}

	return bucket
}

// interpolateATMIV computes ATM IV by interpolating between the two strikes
// closest to the underlying price. Uses the call chain for the ATM
// interpolation (calls and puts have the same ATM IV by put-call parity at
// the money).
func interpolateATMIV(bucket *tenorBucket) float64 {
	options := bucket.calls
	if len(options) == 0 {
		options = bucket.puts
	}
	if len(options) == 0 {
		return 0
	}

	// Use the underlying price from the first ticker (all should agree).
	spot := options[0].UnderlyingPrice
	if spot <= 0 {
		return 0
	}

	// Sort by strike.
	sort.Slice(options, func(i, j int) bool {
		return options[i].Strike < options[j].Strike
	})

	// Find the two strikes that bracket the spot price.
	var below, above *tickerWithMeta
	for i := range options {
		if options[i].Strike <= spot {
			below = &options[i]
		}
		if options[i].Strike >= spot && above == nil {
			above = &options[i]
		}
	}

	if below == nil && above == nil {
		return 0
	}
	if below == nil {
		return above.MarkIV
	}
	if above == nil {
		return below.MarkIV
	}
	if below.Strike == above.Strike {
		return below.MarkIV
	}

	// Linear interpolation weighted by distance to spot.
	totalDist := above.Strike - below.Strike
	weightAbove := (spot - below.Strike) / totalDist
	weightBelow := 1 - weightAbove

	return below.MarkIV*weightBelow + above.MarkIV*weightAbove
}

// compute25DeltaMetrics finds the options closest to +0.25 delta (calls)
// and -0.25 delta (puts), then returns the risk reversal (call IV - put IV)
// and the absolute 25d put IV.
func compute25DeltaMetrics(bucket *tenorBucket) (rr float64, putIV float64) {
	callIV := find25DeltaIV(bucket.calls, 0.25)
	putIV = find25DeltaIV(bucket.puts, -0.25)

	if callIV == 0 || putIV == 0 {
		return 0, putIV
	}
	return callIV - putIV, putIV
}

// find25DeltaIV finds the option closest to the target delta and returns its
// mark IV. For calls targetDelta should be +0.25, for puts -0.25.
func find25DeltaIV(options []tickerWithMeta, targetDelta float64) float64 {
	if len(options) == 0 {
		return 0
	}

	var best *tickerWithMeta
	bestDist := math.MaxFloat64
	for i := range options {
		dist := math.Abs(options[i].Delta - targetDelta)
		if dist < bestDist {
			bestDist = dist
			best = &options[i]
		}
	}
	if best == nil {
		return 0
	}
	return best.MarkIV
}

// computeButterfly computes the 25-delta butterfly spread:
// BF = (25d_call_IV + 25d_put_IV) / 2 - ATM_IV
func computeButterfly(bucket *tenorBucket, atmIV float64) float64 {
	callIV := find25DeltaIV(bucket.calls, 0.25)
	putIV := find25DeltaIV(bucket.puts, -0.25)

	if callIV == 0 || putIV == 0 || atmIV == 0 {
		return 0
	}
	return (callIV+putIV)/2 - atmIV
}
