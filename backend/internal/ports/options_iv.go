package ports

import (
	"context"
	"time"
)

// IVSurface represents a snapshot of an options implied-volatility surface
// for a crypto asset. It captures ATM levels, 25-delta risk reversals,
// butterflies, and term structure metrics across two standard tenors (7d
// and 30d). These metrics gate carry-trade exposure via the skew-regime
// classifier in domain/strategy.
type IVSurface struct {
	Asset     string
	Timestamp time.Time
	ATMIV7d   float64 // ATM implied vol for ~7-day tenor
	ATMIV30d  float64 // ATM implied vol for ~30-day tenor
	RR25d7d   float64 // 25-delta risk reversal, 7d tenor
	RR25d30d  float64 // 25-delta risk reversal, 30d tenor
	BF25d7d   float64 // 25-delta butterfly, 7d tenor
	BF25d30d  float64 // 25-delta butterfly, 30d tenor
	TermSlope float64 // ATMIV30d / ATMIV7d - 1 (contango > 0, backwardation < 0)
	PutSkew7d float64 // 25d put IV / ATM IV - 1
}

// OptionsIVPort provides access to crypto options IV surface data. This is a
// read-only port: implementations fetch live surface snapshots from derivatives
// venues (e.g. Deribit) and expose derived metrics used by the skew-regime
// classifier.
type OptionsIVPort interface {
	// Surface returns the full IV surface snapshot for the given crypto asset.
	Surface(ctx context.Context, asset string) (IVSurface, error)

	// SkewRR returns the 25-delta risk reversal for the given asset and
	// tenor string ("7d" or "30d").
	SkewRR(ctx context.Context, asset string, tenor string) (float64, error)

	// TermSlope returns the term-structure slope (ATMIV30d / ATMIV7d - 1)
	// for the given asset.
	TermSlope(ctx context.Context, asset string) (float64, error)
}
