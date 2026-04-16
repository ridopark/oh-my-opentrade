package risk

import (
	"context"
	"fmt"
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// PortfolioHeat tracks aggregate risk across all open positions and refuses
// new entries that would push total heat above the configured maximum.
//
// Rationale: the per-trade risk check already caps each individual position
// at ~2% of NLV, but nothing today prevents ten such positions from
// accumulating to 20% aggregate. A correlated event (sector news, macro
// shock, flash crash) moves them all together and the aggregate drawdown
// is what actually wipes the account.
//
// Heat for a position = |entry_price - stop_loss| * quantity, in absolute
// dollar terms. The gate compares (Σ current heat + intent heat) / equity
// against MaxHeatPct. A MaxHeatPct of 0 is treated as "disabled" by the
// caller (the checker simply isn't wired in that case).
type PortfolioHeat struct {
	maxHeatPct   float64
	posSource    PositionSource
	equitySource EquitySource
	log          zerolog.Logger
}

// PositionSource returns the currently open monitored positions. The
// position monitor service implements this via ListPositions().
type PositionSource interface {
	ListPositions() []domain.MonitoredPosition
}

// EquitySource returns the current account equity (NLV) in account
// currency. Callers typically adapt an AccountPort or orchestrator
// equity snapshot into this interface.
type EquitySource interface {
	AccountEquity() float64
}

// NewPortfolioHeat constructs a PortfolioHeat guard. If maxHeatPct <= 0,
// the returned guard treats all Check calls as non-gating (passes) —
// this is the "disabled" mode matching the default config value.
func NewPortfolioHeat(maxHeatPct float64, posSource PositionSource, equitySource EquitySource, log zerolog.Logger) *PortfolioHeat {
	return &PortfolioHeat{
		maxHeatPct:   maxHeatPct,
		posSource:    posSource,
		equitySource: equitySource,
		log:          log.With().Str("component", "portfolio_heat").Logger(),
	}
}

// Check implements gate.PortfolioHeatChecker. It returns an error if
// accepting the intent would push aggregate portfolio heat above
// maxHeatPct as a fraction of account equity.
func (p *PortfolioHeat) Check(_ context.Context, intent domain.OrderIntent) error {
	if p.maxHeatPct <= 0 {
		return nil // disabled
	}
	if p.equitySource == nil {
		return fmt.Errorf("portfolio_heat: nil equity source")
	}
	equity := p.equitySource.AccountEquity()
	if equity <= 0 {
		return fmt.Errorf("portfolio_heat: invalid equity %.2f", equity)
	}

	currentHeat := p.currentHeat()
	newRisk := intentRisk(intent)
	projected := (currentHeat + newRisk) / equity

	if projected > p.maxHeatPct {
		return fmt.Errorf(
			"portfolio_heat: %.2f%% projected exceeds %.2f%% max (current=%.2f, new=%.2f, equity=%.2f)",
			projected*100, p.maxHeatPct*100, currentHeat, newRisk, equity,
		)
	}
	return nil
}

func (p *PortfolioHeat) currentHeat() float64 {
	if p.posSource == nil {
		return 0
	}
	var total float64
	for _, pos := range p.posSource.ListPositions() {
		total += positionRisk(pos)
	}
	return total
}

// positionRisk returns the dollar amount at risk between entry and stop.
// A position with no stop set contributes 0 — the heat view cannot reason
// about undefined-risk exposures, so we conservatively omit them rather
// than injecting a fabricated number.
func positionRisk(pos domain.MonitoredPosition) float64 {
	// MonitoredPosition stores per-rule params (not a top-level StopLoss
	// field), so we pull the stop from the initial "stop_loss" rule if
	// present. Callers that use a different exit rule layout should adapt
	// this helper accordingly.
	stop := stopLossFromRules(pos.InitialExitRules, pos.EntryPrice)
	if stop <= 0 {
		return 0
	}
	return math.Abs(pos.EntryPrice-stop) * pos.Quantity
}

// stopLossFromRules extracts a dollar-valued stop price from whichever
// initial exit rule carries one. MonitoredPosition currently has no
// top-level StopLoss field — stops live inside ExitRules as params.
// The helper covers the rule types that carry an absolute stop price;
// percentage-based stops (e.g. MAX_LOSS) can't be converted without
// the entry price, so we compute them from entry for those.
func stopLossFromRules(rules []domain.ExitRule, entry float64) float64 {
	for _, r := range rules {
		switch r.Type {
		case domain.ExitRuleTrailingStop, domain.ExitRuleVolatilityStop,
			domain.ExitRuleStepStop, domain.ExitRuleBreakevenStop,
			domain.ExitRuleSwingStop:
			if v, ok := r.Params["stop_price"]; ok && v > 0 {
				return v
			}
			if v, ok := r.Params["price"]; ok && v > 0 {
				return v
			}
		case domain.ExitRuleMaxLoss:
			// MAX_LOSS is expressed as a percentage drawdown from entry.
			if pct, ok := r.Params["max_loss_pct"]; ok && pct > 0 && entry > 0 {
				// Return implied stop price below entry (long side).
				return entry * (1 - pct/100.0)
			}
		}
	}
	return 0
}

func intentRisk(intent domain.OrderIntent) float64 {
	// Sprint 5: capped-risk combos. Debit spread risk = net debit; credit
	// spread risk = (strike width - credit received). Fall back to the
	// explicit MaxLossUSD when ComboRisk can't infer (e.g., missing strike
	// width on a non-vertical structure).
	if intent.IsCombo() {
		if r := domain.ComboRisk(intent); r > 0 {
			return r
		}
		return intent.MaxLossUSD
	}
	if intent.StopLoss <= 0 || intent.LimitPrice <= 0 {
		return 0
	}
	return math.Abs(intent.LimitPrice-intent.StopLoss) * intent.Quantity
}
