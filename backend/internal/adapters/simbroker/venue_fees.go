package simbroker

import (
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// VenueFeeRate holds maker/taker fee rates for a single venue.
type VenueFeeRate struct {
	MakerBPS float64 // maker fee in basis points
	TakerBPS float64 // taker fee in basis points
}

// CryptoVenueFees implements FeeSchedule with venue-specific maker/taker rates.
// Market orders are classified as taker; limit orders as maker.
type CryptoVenueFees struct {
	venues     map[domain.Venue]VenueFeeRate
	defaultFee VenueFeeRate // applied when venue is not in the map
}

// Name returns the canonical identifier used in config.
func (CryptoVenueFees) Name() string { return "crypto_venue" }

// Compute returns the per-fill cost using the venue-specific maker/taker rate.
// Market orders (OrderType empty or "market") pay the taker rate; limit orders
// pay the maker rate.
func (f CryptoVenueFees) Compute(ctx FeeContext) Fees {
	rate, ok := f.venues[ctx.Venue]
	if !ok {
		rate = f.defaultFee
	}

	bps := rate.TakerBPS // default: taker
	if isLimitOrder(ctx.OrderType) {
		bps = rate.MakerBPS
	}

	commission := ctx.Notional * bps / 10000.0
	fees := Fees{
		Commission: commission,
		Exchange:   0,
		Regulatory: 0,
	}
	fees.Total = fees.Commission
	return fees
}

// DefaultCryptoFees returns a CryptoVenueFees populated with industry-standard
// 2026 retail rate tiers. Unknown venues fall back to 5/10 bps maker/taker.
func DefaultCryptoFees() *CryptoVenueFees {
	return &CryptoVenueFees{
		venues: map[domain.Venue]VenueFeeRate{
			domain.VenueHyperliquid: {MakerBPS: 1.0, TakerBPS: 3.5},
			domain.VenueBinanceFut:  {MakerBPS: 2.0, TakerBPS: 5.0},
			domain.VenueBybit:       {MakerBPS: 1.0, TakerBPS: 6.0},
			domain.VenueCoinbase:    {MakerBPS: 6.0, TakerBPS: 8.0},
		},
		defaultFee: VenueFeeRate{MakerBPS: 5.0, TakerBPS: 10.0},
	}
}

// NewCryptoVenueFees creates a CryptoVenueFees with custom venue rates and a
// fallback default. Pass nil venues to get only the default rate.
func NewCryptoVenueFees(venues map[domain.Venue]VenueFeeRate, defaultFee VenueFeeRate) *CryptoVenueFees {
	if venues == nil {
		venues = make(map[domain.Venue]VenueFeeRate)
	}
	return &CryptoVenueFees{venues: venues, defaultFee: defaultFee}
}

// isLimitOrder returns true for order types that rest on the book (maker).
func isLimitOrder(orderType string) bool {
	return orderType == "limit" || orderType == "LMT"
}
