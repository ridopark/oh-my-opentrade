package positionmonitor

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// Constants for spread-aware option exit pricing.
//
// exitQuoteMaxAge: a quote older than this is treated as unusable because
// the book may have moved since the snapshot was taken. Keep short — the
// point of quote-aware pricing is to reflect the current bid/ask.
//
// exitBlownSpreadRatio: when spread/mid exceeds this, the contract is
// effectively illiquid or the feed is wide because the market is making
// one. Pricing "just below mid" in that regime either leaves us stuck
// (still far above bid) or routes through a nonsense mid. Fall back to
// the retry-escalation path instead.
//
// exitBpsCap: hard ceiling on how far below mid we will price, expressed
// as a fraction of mid (500 bps). Protects against pathologically wide
// but not-quite-blown spreads where k*spread would exceed a reasonable
// discount off mid.
//
// exitTickOption: one-penny tick. Many liquid US equity options tick in
// pennies; the bid+tick floor is a "never price AT the bid" guard, not
// a strict exchange-tick-conformance guard.
const (
	exitQuoteMaxAge      = 5 * time.Second
	exitBlownSpreadRatio = 0.25
	exitBpsCap           = 0.05
	exitTickOption       = 0.01
)

// kForDTE returns the spread-aggression factor k for a given days-to-expiry.
// Wider k = give up more of the spread. Short-dated options (0-4 DTE) are
// the most time-sensitive and get priced more aggressively to fill; longer-
// dated contracts have room to wait for a better print.
func kForDTE(dte int) float64 {
	switch {
	case dte >= 14:
		return 0.25
	case dte >= 5:
		return 0.35
	default:
		return 0.45
	}
}

// buildExitLimitPrice computes an option exit limit price using the live
// bid/ask spread, with aggression scaled by DTE.
//
// Formula for a SELL (closing long — every option exit in this system is
// a SELL regardless of short/long thesis because the contract position is
// always long the option):
//
//	spread = ask - bid
//	target = mid - min(k*spread, bpsCap*mid)
//	limit  = max(bid + tick, target)
//
// Rejects the quote (returns usable=false) when:
//   - quote.Timestamp older than exitQuoteMaxAge (stale)
//   - spread/mid > exitBlownSpreadRatio (blown-out spread — caller falls back)
//   - BidSize == 0 (no bid to hit)
//   - bid <= 0 or ask <= 0 (broken quote)
//
// When usable=false, callers MUST fall back to the mid-based formula so
// the exit still goes out — this is a safety-critical path and silently
// skipping the exit is worse than a slightly-off limit.
//
// Short/buy direction is supported for symmetry, though current call sites
// only exit long options via SELL.
func buildExitLimitPrice(quote domain.OptionQuote, now time.Time, dte int, isShort bool) (price float64, usable bool) {
	if quote.Bid <= 0 || quote.Ask <= 0 {
		return 0, false
	}
	if quote.BidSize == 0 {
		return 0, false
	}
	if !quote.Timestamp.IsZero() && now.Sub(quote.Timestamp) > exitQuoteMaxAge {
		return 0, false
	}
	mid := (quote.Bid + quote.Ask) / 2.0
	if mid <= 0 {
		return 0, false
	}
	spread := quote.Ask - quote.Bid
	if spread < 0 {
		return 0, false
	}
	if spread/mid > exitBlownSpreadRatio {
		return 0, false
	}

	k := kForDTE(dte)
	discount := k * spread
	if cap := exitBpsCap * mid; discount > cap {
		discount = cap
	}

	if isShort {
		// Closing a short contract (BUY): pay a premium above mid, floored
		// by ask - tick so we never cross through the offer. Symmetric
		// counterpart to the long-exit path.
		target := mid + discount
		ceil := quote.Ask - exitTickOption
		if target > ceil {
			target = ceil
		}
		if target < mid {
			target = mid
		}
		return target, true
	}

	target := mid - discount
	floor := quote.Bid + exitTickOption
	if target < floor {
		target = floor
	}
	return target, true
}

// dteFromExpiry computes whole days-to-expiry from the position's option
// expiry time relative to now. Returns 0 for already-expired contracts.
func dteFromExpiry(expiry, now time.Time) int {
	if expiry.IsZero() {
		return 0
	}
	d := int(expiry.Sub(now).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}
