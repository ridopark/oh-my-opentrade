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

func (g *BuyingPowerGuard) Check(ctx context.Context, intent domain.OrderIntent) error {
	if intent.Direction.IsExit() {
		return nil
	}

	bp, err := g.account.GetAccountBuyingPower(ctx)
	if err != nil {
		g.log.Error().Err(err).Msg("buying power guard: failed to fetch buying power — allowing order through")
		return nil
	}

	orderCost := intent.LimitPrice * intent.Quantity

	if intent.AssetClass == domain.AssetClassCrypto {
		return g.checkCrypto(bp, orderCost)
	}
	return g.checkEquity(bp, orderCost)
}

func (g *BuyingPowerGuard) checkCrypto(bp ports.BuyingPower, orderCost float64) error {
	if orderCost > bp.NonMarginableBuyingPower {
		return fmt.Errorf("buying_power: crypto order cost $%.2f exceeds non-marginal buying power $%.2f",
			orderCost, bp.NonMarginableBuyingPower)
	}
	g.log.Debug().
		Float64("order_cost", orderCost).
		Float64("non_marginal_bp", bp.NonMarginableBuyingPower).
		Msg("buying power check passed (crypto)")
	return nil
}

func (g *BuyingPowerGuard) checkEquity(bp ports.BuyingPower, orderCost float64) error {
	if bp.PatternDayTrader {
		if bp.DayTradingBuyingPower > 0 {
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
