package execution

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// BuyingPowerGuard pre-checks buying power before broker submission.
// When Alpaca's paper account reports daytrading_buying_power=0 (a known bug),
// it falls back to effective_buying_power for equity entry orders.
//
// This guard is only active when explicitly enabled via the DTBP_FALLBACK env var.
type BuyingPowerGuard struct {
	account ports.AccountPort
	log     zerolog.Logger
}

// NewBuyingPowerGuard creates a BuyingPowerGuard backed by the given AccountPort.
func NewBuyingPowerGuard(account ports.AccountPort, log zerolog.Logger) *BuyingPowerGuard {
	return &BuyingPowerGuard{account: account, log: log}
}

// Check verifies that the account has sufficient buying power for an equity entry order.
//
// Logic:
//  1. Skip check entirely for crypto orders (PDT rules don't apply) and exit orders.
//  2. Fetch account buying power from the broker.
//  3. If the account is PDT and DTBP > 0, check order cost against DTBP.
//  4. If the account is PDT and DTBP == 0 (Alpaca bug), fall back to effective_buying_power.
//  5. If the account is not PDT, check order cost against effective_buying_power.
func (g *BuyingPowerGuard) Check(ctx context.Context, intent domain.OrderIntent) error {
	// Skip crypto — PDT rules don't apply.
	if intent.AssetClass == domain.AssetClassCrypto {
		return nil
	}
	// Skip exit orders — closing reduces exposure.
	if intent.Direction.IsExit() {
		return nil
	}

	bp, err := g.account.GetAccountBuyingPower(ctx)
	if err != nil {
		g.log.Error().Err(err).Msg("buying power guard: failed to fetch buying power — allowing order through")
		return nil // fail-open: don't block orders if we can't fetch account info
	}

	orderCost := intent.LimitPrice * intent.Quantity

	if bp.PatternDayTrader {
		if bp.DayTradingBuyingPower > 0 {
			// Normal PDT account — check DTBP.
			if orderCost > bp.DayTradingBuyingPower {
				return fmt.Errorf("buying_power: order cost $%.2f exceeds day trading buying power $%.2f",
					orderCost, bp.DayTradingBuyingPower)
			}
			g.log.Debug().
				Float64("order_cost", orderCost).
				Float64("dtbp", bp.DayTradingBuyingPower).
				Msg("buying power check passed (DTBP)")
			return nil
		}

		// DTBP == 0 (Alpaca paper bug) — fall back to effective buying power.
		g.log.Warn().
			Float64("dtbp", bp.DayTradingBuyingPower).
			Float64("effective_bp", bp.EffectiveBuyingPower).
			Msg("DTBP is zero (Alpaca paper bug) — falling back to effective buying power")

		if orderCost > bp.EffectiveBuyingPower {
			return fmt.Errorf("buying_power: order cost $%.2f exceeds effective buying power $%.2f (DTBP fallback)",
				orderCost, bp.EffectiveBuyingPower)
		}
		g.log.Debug().
			Float64("order_cost", orderCost).
			Float64("effective_bp", bp.EffectiveBuyingPower).
			Msg("buying power check passed (effective BP fallback)")
		return nil
	}

	// Non-PDT account — check effective buying power.
	if orderCost > bp.EffectiveBuyingPower {
		return fmt.Errorf("buying_power: order cost $%.2f exceeds effective buying power $%.2f",
			orderCost, bp.EffectiveBuyingPower)
	}
	g.log.Debug().
		Float64("order_cost", orderCost).
		Float64("effective_bp", bp.EffectiveBuyingPower).
		Msg("buying power check passed (effective BP)")
	return nil
}
