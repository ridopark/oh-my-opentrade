package execution

import (
	"context"
	"fmt"
	"strconv"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// SpreadGuard rejects entry orders when the current bid-ask spread exceeds
// a configurable threshold (in basis points). The threshold is read from
// intent.Meta["max_spread_bps"]; if absent, the guard is a no-op.
//
// This is distinct from SlippageGuard, which checks price drift from the
// intended limit price. SpreadGuard checks market microstructure quality.
type SpreadGuard struct {
	provider QuoteProvider
	log      zerolog.Logger
}

func NewSpreadGuard(quoteProvider QuoteProvider, log zerolog.Logger) *SpreadGuard {
	return &SpreadGuard{provider: quoteProvider, log: log}
}

func (g *SpreadGuard) Check(ctx context.Context, intent domain.OrderIntent) error {
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		return nil
	}
	raw, ok := intent.Meta["max_spread_bps"]
	if !ok || raw == "" {
		return nil // not configured — allow
	}

	maxBPS, err := strconv.ParseFloat(raw, 64)
	if err != nil || maxBPS <= 0 {
		return nil // invalid config — allow (don't block trading on bad config)
	}

	bid, ask, err := g.provider.GetQuote(ctx, intent.Symbol)
	if err != nil {
		g.log.Warn().Err(err).Str("symbol", string(intent.Symbol)).
			Msg("spread guard: quote fetch failed — allowing order through")
		return nil
	}
	if bid <= 0 || ask <= 0 {
		return fmt.Errorf("spread_guard: zero bid/ask for %s (bid=%.2f, ask=%.2f)", intent.Symbol, bid, ask)
	}

	mid := (bid + ask) / 2
	spreadBPS := ((ask - bid) / mid) * 10000

	if spreadBPS > maxBPS {
		return fmt.Errorf("spread_guard: %s spread %.1f bps exceeds max %.1f bps (bid=%.4f, ask=%.4f)",
			intent.Symbol, spreadBPS, maxBPS, bid, ask)
	}

	g.log.Debug().
		Str("symbol", string(intent.Symbol)).
		Float64("spread_bps", spreadBPS).
		Float64("max_spread_bps", maxBPS).
		Msg("spread guard passed")

	return nil
}
