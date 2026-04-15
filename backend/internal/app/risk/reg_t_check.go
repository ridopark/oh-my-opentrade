package risk

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// RegTCheck enforces the Federal Reserve Reg-T initial margin requirement
// on margin-eligible equity trades: 50% of trade notional must be covered
// by account effective buying power at submission time.
//
// Options are skipped — Reg-T for options is governed by a different rule
// (Reg-T options margin / portfolio margin) and Sprint 4.5 explicitly
// defers option-specific margin to a later sprint.
//
// Crypto is skipped as well — crypto is not margin-eligible at the
// supported venues and has its own NonMarginableBuyingPower source.
type RegTCheck struct {
	account ports.AccountPort
	log     zerolog.Logger
}

// NewRegTCheck constructs the check. A nil account port disables the
// check — Check returns nil in that case so the gate degrades to a
// pass-through rather than blocking everything.
func NewRegTCheck(account ports.AccountPort, log zerolog.Logger) *RegTCheck {
	return &RegTCheck{
		account: account,
		log:     log.With().Str("component", "reg_t").Logger(),
	}
}

// regTInitialMarginFraction is the Reg-T initial margin: 50% for both
// long and short margin-eligible equity (short has an additional 50% proceeds
// requirement but it nets against the 50% margin, so the buying-power
// consumption is the same 50% notional).
const regTInitialMarginFraction = 0.50

// Check implements gate.RegTChecker. Returns an error if the intent's
// required initial margin exceeds the current effective buying power.
func (r *RegTCheck) Check(ctx context.Context, intent domain.OrderIntent) error {
	if r.account == nil {
		return nil
	}
	if intent.Direction.IsExit() {
		return nil
	}
	if intent.AssetClass == domain.AssetClassCrypto {
		return nil
	}
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		return nil
	}
	if intent.LimitPrice <= 0 || intent.Quantity <= 0 {
		return nil // nothing to check — risk engine will reject separately
	}

	bp, err := r.account.GetAccountBuyingPower(ctx)
	if err != nil {
		return fmt.Errorf("reg_t: fetch buying power: %w", err)
	}

	notional := intent.LimitPrice * intent.Quantity
	required := notional * regTInitialMarginFraction

	if required > bp.EffectiveBuyingPower {
		return fmt.Errorf(
			"reg_t: required initial margin %.2f exceeds effective buying power %.2f (notional=%.2f, 50%% rule)",
			required, bp.EffectiveBuyingPower, notional,
		)
	}
	return nil
}
