package deribit

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// buildTestChain creates a synthetic options chain for testing IV surface
// computation. It produces calls and puts across a range of strikes with
// known IVs and deltas to make assertions deterministic.
func buildTestChain(now time.Time, spot float64, expiryDaysOut int) ([]Instrument, []Ticker) {
	expiry := now.Add(time.Duration(expiryDaysOut) * 24 * time.Hour)

	type optSpec struct {
		strike   float64
		optType  string
		markIV   float64 // as fraction (0-1)
		delta    float64
	}

	specs := []optSpec{
		// OTM puts (negative delta, higher IV = put skew)
		{strike: spot * 0.85, optType: "put", markIV: 0.70, delta: -0.10},
		{strike: spot * 0.90, optType: "put", markIV: 0.62, delta: -0.25},
		{strike: spot * 0.95, optType: "put", markIV: 0.55, delta: -0.40},
		// ATM
		{strike: spot * 0.99, optType: "call", markIV: 0.50, delta: 0.52},
		{strike: spot * 1.01, optType: "call", markIV: 0.49, delta: 0.48},
		{strike: spot * 0.99, optType: "put", markIV: 0.50, delta: -0.48},
		{strike: spot * 1.01, optType: "put", markIV: 0.49, delta: -0.52},
		// OTM calls
		{strike: spot * 1.05, optType: "call", markIV: 0.47, delta: 0.35},
		{strike: spot * 1.10, optType: "call", markIV: 0.45, delta: 0.25},
		{strike: spot * 1.15, optType: "call", markIV: 0.44, delta: 0.15},
	}

	var instruments []Instrument
	var tickers []Ticker
	for i, s := range specs {
		name := fmt.Sprintf("TEST-%s-%s-%d-%c", expiry.Format("20060102"), s.optType, int(s.strike), rune('A'+i))
		instruments = append(instruments, Instrument{
			InstrumentName: name,
			BaseCurrency:   "BTC",
			QuoteCurrency:  "USD",
			Strike:         s.strike,
			Expiration:     expiry,
			OptionType:     s.optType,
			IsActive:       true,
		})
		tickers = append(tickers, Ticker{
			InstrumentName:  name,
			MarkIV:          s.markIV,
			BidIV:           s.markIV - 0.01,
			AskIV:           s.markIV + 0.01,
			Delta:           s.delta,
			UnderlyingPrice: spot,
			MarkPrice:       0.05,
			OpenInterest:    500,
		})
	}
	return instruments, tickers
}

func TestBuildIVSurface_ATMInterpolation(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	spot := 80000.0
	instruments, tickers := buildTestChain(now, spot, 7)

	surface := BuildIVSurface("BTC", now, instruments, tickers)

	// ATM IV should be interpolated between the 0.99*spot and 1.01*spot
	// strikes. At spot=80000: strikes 79200 and 80800. Both have IV near
	// 0.50/0.49. Expected: ~0.495 (weighted average, spot is centered).
	assert.InDelta(t, 0.495, surface.ATMIV7d, 0.01, "ATM IV 7d")
}

func TestBuildIVSurface_RiskReversal(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	spot := 80000.0
	instruments, tickers := buildTestChain(now, spot, 7)

	surface := BuildIVSurface("BTC", now, instruments, tickers)

	// 25d call IV = 0.45 (delta=0.25), 25d put IV = 0.62 (delta=-0.25)
	// RR = 0.45 - 0.62 = -0.17
	assert.InDelta(t, -0.17, surface.RR25d7d, 0.01, "risk reversal 7d")
}

func TestBuildIVSurface_Butterfly(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	spot := 80000.0
	instruments, tickers := buildTestChain(now, spot, 7)

	surface := BuildIVSurface("BTC", now, instruments, tickers)

	// BF = (25d_call_IV + 25d_put_IV) / 2 - ATM_IV
	// = (0.45 + 0.62) / 2 - 0.495 = 0.535 - 0.495 = 0.04
	assert.InDelta(t, 0.04, surface.BF25d7d, 0.02, "butterfly 7d")
}

func TestBuildIVSurface_TermStructure(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	spot := 80000.0

	// Build chains for two tenors: 7d and 30d.
	inst7, tick7 := buildTestChain(now, spot, 7)
	inst30, tick30 := buildTestChain(now, spot, 30)

	// Modify 30d chain to have slightly higher ATM IV (contango).
	for i := range tick30 {
		tick30[i].MarkIV *= 1.10 // 10% higher
	}

	// Combine both tenors.
	allInst := append(inst7, inst30...)
	allTick := append(tick7, tick30...)

	surface := BuildIVSurface("BTC", now, allInst, allTick)

	// Term slope should be positive (contango: 30d IV > 7d IV).
	assert.Greater(t, surface.TermSlope, 0.0, "term slope should be positive in contango")
	// ATMIV30d / ATMIV7d - 1 ~ 0.10
	assert.InDelta(t, 0.10, surface.TermSlope, 0.03, "term slope magnitude")
}

func TestBuildIVSurface_PutSkew(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	spot := 80000.0
	instruments, tickers := buildTestChain(now, spot, 7)

	surface := BuildIVSurface("BTC", now, instruments, tickers)

	// PutSkew = 25d_put_IV / ATM_IV - 1 = 0.62 / 0.495 - 1 ~ 0.2525
	assert.Greater(t, surface.PutSkew7d, 0.0, "put skew should be positive with put skew")
	assert.InDelta(t, 0.25, surface.PutSkew7d, 0.05, "put skew magnitude")
}

func TestBuildIVSurface_EmptyInput(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	surface := BuildIVSurface("BTC", now, nil, nil)

	assert.Equal(t, "BTC", surface.Asset)
	assert.Equal(t, 0.0, surface.ATMIV7d)
	assert.Equal(t, 0.0, surface.RR25d7d)
	assert.Equal(t, 0.0, surface.TermSlope)
}

func TestFindClosestExpiry(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	exp7 := now.Add(6 * 24 * time.Hour)
	exp14 := now.Add(15 * 24 * time.Hour)
	exp30 := now.Add(28 * 24 * time.Hour)

	options := []tickerWithMeta{
		{Ticker: Ticker{MarkIV: 0.50}, Strike: 80000, Expiration: exp7, OptionType: "call"},
		{Ticker: Ticker{MarkIV: 0.45}, Strike: 80000, Expiration: exp14, OptionType: "call"},
		{Ticker: Ticker{MarkIV: 0.55}, Strike: 80000, Expiration: exp30, OptionType: "call"},
	}

	t.Run("7d target picks closest", func(t *testing.T) {
		bucket := findClosestExpiry(options, now, 7)
		assert.Equal(t, exp7, bucket.expiry)
		assert.Len(t, bucket.calls, 1)
	})

	t.Run("30d target picks closest", func(t *testing.T) {
		bucket := findClosestExpiry(options, now, 30)
		assert.Equal(t, exp30, bucket.expiry)
	})
}

func TestInterpolateATMIV_ExactStrike(t *testing.T) {
	bucket := &tenorBucket{
		calls: []tickerWithMeta{
			{Ticker: Ticker{MarkIV: 0.50, UnderlyingPrice: 80000}, Strike: 80000},
		},
	}
	iv := interpolateATMIV(bucket)
	assert.InDelta(t, 0.50, iv, 0.001)
}

func TestInterpolateATMIV_EmptyBucket(t *testing.T) {
	bucket := &tenorBucket{}
	iv := interpolateATMIV(bucket)
	assert.Equal(t, 0.0, iv)
}
