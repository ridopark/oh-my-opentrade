package risk

import (
	"context"
	"fmt"
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// DirectionalBias caps net long-minus-short notional exposure as a fraction
// of account equity.
//
// Rationale: sector_exposure catches single-sector concentration and
// portfolio_heat catches aggregate per-trade risk, but neither constrains
// net directional bias. An account can accidentally (or stubbornly) go
// 100% long at a market top or 100% short into a squeeze while every
// individual position passes the per-symbol and per-sector checks. The
// existing exposure gate caps per-symbol concentration but is silent on
// the aggregate long-short net.
//
// net = Σ long_notional − Σ short_notional across open non-option
// positions plus the proposed intent. The gate rejects the intent only
// when |net|/equity exceeds MaxBiasPct AND the intent pushes |net|
// further from neutral. Bias-reducing intents always pass.
//
// Design choices:
//   - Option positions are skipped in the notional sum (same as
//     sector_exposure): options delta-notional accounting is deferred.
//   - Exits bypass the gate at the gate layer (see exec_directional_bias.go).
//   - A MaxBiasPct of 0 is treated as "disabled" so the Check is a no-op.
type DirectionalBias struct {
	maxBiasPct   float64
	posSource    PositionSource
	equitySource EquitySource
	log          zerolog.Logger
}

// NewDirectionalBias constructs a DirectionalBias guard. If maxBiasPct <= 0
// the returned guard treats all Check calls as non-gating (disabled).
func NewDirectionalBias(maxBiasPct float64, posSource PositionSource, equitySource EquitySource, log zerolog.Logger) *DirectionalBias {
	return &DirectionalBias{
		maxBiasPct:   maxBiasPct,
		posSource:    posSource,
		equitySource: equitySource,
		log:          log.With().Str("component", "directional_bias").Logger(),
	}
}

// Check implements gate.DirectionalBiasChecker. Returns a descriptive error
// when the intent would push |net bias| above MaxBiasPct AND the new
// |bias| is greater than the current |bias| (i.e., only bias-increasing
// intents are gated; bias-reducing intents always pass).
func (d *DirectionalBias) Check(_ context.Context, intent domain.OrderIntent) error {
	if d.maxBiasPct <= 0 {
		return nil
	}
	if d.equitySource == nil {
		return fmt.Errorf("directional_bias: nil equity source")
	}
	equity := d.equitySource.AccountEquity()
	if equity <= 0 {
		return fmt.Errorf("directional_bias: invalid equity %.2f", equity)
	}

	var currentNet float64
	if d.posSource != nil {
		for _, pos := range d.posSource.ListPositions() {
			// Sprint 4 defers options delta-notional accounting; skip options
			// just as sector_exposure does.
			if pos.InstrumentType == domain.InstrumentTypeOption {
				continue
			}
			n := positionNotional(pos)
			if pos.IsShort() {
				currentNet -= n
			} else {
				currentNet += n
			}
		}
	}

	delta := intentSignedNotional(intent)
	projectedNet := currentNet + delta

	currentBias := math.Abs(currentNet) / equity
	projectedBias := math.Abs(projectedNet) / equity

	// Bias-reducing (or neutral) intents always pass — we only block
	// intents that push the account further from neutral.
	if projectedBias <= currentBias {
		return nil
	}
	if projectedBias > d.maxBiasPct {
		side := "long"
		if projectedNet < 0 {
			side = "short"
		}
		return fmt.Errorf(
			"directional_bias: net %s %.2f%% projected exceeds %.2f%% max (current=%.2f%%, equity=%.2f)",
			side, projectedBias*100, d.maxBiasPct*100, currentBias*100, equity,
		)
	}
	return nil
}

// intentSignedNotional returns the signed dollar notional the intent would
// add to the net-directional sum. Long adds positive, short adds negative.
// Single-leg option intents contribute 0 (delta-notional deferred).
// Sprint 5 combos contribute signed capped-risk: bullish structures
// (vertical_call_debit, vertical_put_credit) add positive; bearish ones
// (vertical_put_debit, vertical_call_credit) subtract. This keeps the
// directional-bias gate honest about net delta exposure.
func intentSignedNotional(intent domain.OrderIntent) float64 {
	if intent.IsCombo() {
		n := domain.ComboRisk(intent)
		if n <= 0 {
			n = intent.MaxLossUSD
		}
		switch intent.ComboType {
		case domain.ComboTypeVerticalCallDebit, domain.ComboTypeVerticalPutCredit:
			return n
		case domain.ComboTypeVerticalPutDebit, domain.ComboTypeVerticalCallCredit:
			return -n
		}
		return 0
	}
	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		return 0
	}
	n := math.Abs(intent.LimitPrice * intent.Quantity)
	if intent.Direction == domain.DirectionShort {
		return -n
	}
	return n
}
