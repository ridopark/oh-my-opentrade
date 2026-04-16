package simbroker

import (
	"fmt"
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// FeeContext describes a single fill the broker is about to record. All
// schedules receive the same shape so swapping implementations is a pure
// wiring change — no caller-side branching per venue.
type FeeContext struct {
	Symbol    string
	Venue     domain.Venue // execution venue; empty for legacy equity paths
	IsOption  bool
	Side      string  // "BUY" | "SELL"
	Qty       float64 // equities: shares; options: contracts
	Notional  float64 // FillPrice * Qty * multiplier (caller computes)
	FillPrice float64
	OrderType string  // "limit", "market", "stop_limit"; empty falls back to adapter default
}

// Fees is the itemized cost breakdown returned by a FeeSchedule. Total is
// populated by Compute() so callers don't have to re-sum.
type Fees struct {
	Commission float64
	Regulatory float64 // SEC + TAF + FINRA ORF
	Exchange   float64
	Total      float64
}

// FeeSchedule is the venue-specific cost-of-business plug. Returning a zero
// Fees struct is legal (see NoFees) — callers must not assume non-zero.
type FeeSchedule interface {
	Name() string
	Compute(ctx FeeContext) Fees
}

// --- Alpaca equity --------------------------------------------------------

// AlpacaEquityFees models the 2026 Alpaca US equities retail schedule:
// zero commission, SEC Section 31 fee on sells, FINRA TAF on sells, and a
// small FINRA ORF pass-through on sells. Rates are expressed as floats so
// we can bump them without touching code paths.
type AlpacaEquityFees struct {
	SECRate     float64 // fraction of sell notional; default 0.000008 ($8 per $1M)
	TAFPerShare float64 // per-share; default 0.000166
	TAFMin      float64 // per-order floor; default 0.01
	TAFMax      float64 // per-order cap; default 8.30
	FINRAORF    float64 // per-share ORF pass-through on sells; default 0.0000029
}

// Name returns the canonical identifier used in config.
func (AlpacaEquityFees) Name() string { return "alpaca_equity" }

// Compute returns the per-fill cost; buys pay nothing under the current
// retail schedule.
func (f AlpacaEquityFees) Compute(ctx FeeContext) Fees {
	if ctx.IsOption {
		// Defensive: caller shouldn't route options here, but return zeros
		// rather than silently apply equity math.
		return Fees{}
	}
	if !isSell(ctx.Side) {
		return Fees{}
	}
	secRate := f.SECRate
	if secRate == 0 {
		secRate = 0.000008
	}
	taf := f.TAFPerShare
	if taf == 0 {
		taf = 0.000166
	}
	tafMin := f.TAFMin
	if tafMin == 0 {
		tafMin = 0.01
	}
	tafMax := f.TAFMax
	if tafMax == 0 {
		tafMax = 8.30
	}
	orf := f.FINRAORF
	if orf == 0 {
		orf = 0.0000029
	}

	sec := math.Abs(ctx.Notional) * secRate
	tafCost := taf * ctx.Qty
	if tafCost < tafMin {
		tafCost = tafMin
	}
	if tafCost > tafMax {
		tafCost = tafMax
	}
	orfCost := orf * ctx.Qty

	fees := Fees{
		Commission: 0,
		Regulatory: sec + tafCost + orfCost,
		Exchange:   0,
	}
	fees.Total = fees.Commission + fees.Regulatory + fees.Exchange
	return fees
}

// --- IBKR tiered options --------------------------------------------------

// IBKRTieredOptionsFees models IBKR's tiered US options schedule: $0.65 per
// contract base commission, a ~$0.04/contract OCC/exchange pass-through, and
// the SEC Section 31 fee on sells only.
type IBKRTieredOptionsFees struct {
	CommissionPerContract float64 // default 0.65
	CommissionMin         float64 // per-order floor; default 1.00
	ExchangePerContract   float64 // default 0.04
	SECRate               float64 // default 0.000008
}

// Name returns the canonical identifier used in config.
func (IBKRTieredOptionsFees) Name() string { return "ibkr_options" }

// Compute returns the per-fill cost for options; equity orders get zero fees
// (caller should have routed them to the equity schedule).
func (f IBKRTieredOptionsFees) Compute(ctx FeeContext) Fees {
	if !ctx.IsOption {
		return Fees{}
	}
	commPer := f.CommissionPerContract
	if commPer == 0 {
		commPer = 0.65
	}
	commMin := f.CommissionMin
	if commMin == 0 {
		commMin = 1.00
	}
	exchPer := f.ExchangePerContract
	if exchPer == 0 {
		exchPer = 0.04
	}
	secRate := f.SECRate
	if secRate == 0 {
		secRate = 0.000008
	}

	commission := commPer * ctx.Qty
	if commission < commMin {
		commission = commMin
	}
	exchange := exchPer * ctx.Qty

	var regulatory float64
	if isSell(ctx.Side) {
		regulatory = math.Abs(ctx.Notional) * secRate
	}

	fees := Fees{
		Commission: commission,
		Regulatory: regulatory,
		Exchange:   exchange,
	}
	fees.Total = fees.Commission + fees.Regulatory + fees.Exchange
	return fees
}

// --- NoFees ---------------------------------------------------------------

// NoFees is a zero-cost schedule used by unit tests and as the fallback when
// a caller opts out of realistic fee accounting.
type NoFees struct{}

// Name returns the canonical identifier used in config.
func (NoFees) Name() string { return "none" }

// Compute returns a zero-valued Fees struct regardless of input.
func (NoFees) Compute(_ FeeContext) Fees { return Fees{} }

// --- helpers --------------------------------------------------------------

func isSell(side string) bool {
	return side == "SELL" || side == "sell"
}

// FeeScheduleByName maps a config string to a FeeSchedule. Returns an error
// for unknown names so operators don't silently get zero fees.
func FeeScheduleByName(name string) (FeeSchedule, error) {
	switch name {
	case "", "none":
		return NoFees{}, nil
	case "alpaca_equity":
		return AlpacaEquityFees{}, nil
	case "ibkr_options":
		return IBKRTieredOptionsFees{}, nil
	case "crypto_venue":
		return DefaultCryptoFees(), nil
	default:
		return nil, fmt.Errorf("simbroker: unknown fee_schedule %q", name)
	}
}
