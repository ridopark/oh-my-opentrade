package options

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ContractSelectionConstraints holds all parameters for filtering and selecting
// an option contract from a chain.
type ContractSelectionConstraints struct {
	MinDTE          int     `toml:"min_dte"`
	MaxDTE          int     `toml:"max_dte"`
	TargetDeltaLow  float64 `toml:"target_delta_low"`
	TargetDeltaHigh float64 `toml:"target_delta_high"`
	MinOpenInterest int     `toml:"min_open_interest"`
	MaxSpreadPct    float64 `toml:"max_spread_pct"`
	MaxIV           float64 `toml:"max_iv"`
}

// ContractSelectionService selects the best option contract from a chain
// based on configured constraints and market regime.
type ContractSelectionService struct {
	constraints       ContractSelectionConstraints
	regimeConstraints RegimeConstraintsMap
	now               func() time.Time
}

// NewContractSelectionService creates a ContractSelectionService.
// now is injected for testability; pass time.Now in production.
func NewContractSelectionService(constraints ContractSelectionConstraints, now func() time.Time) *ContractSelectionService {
	return &ContractSelectionService{
		constraints:       constraints,
		regimeConstraints: nil,
		now:               now,
	}
}

func NewContractSelectionServiceWithRegimes(
	defaults ContractSelectionConstraints,
	regimes RegimeConstraintsMap,
	now func() time.Time,
) *ContractSelectionService {
	return &ContractSelectionService{
		constraints:       defaults,
		regimeConstraints: regimes,
		now:               now,
	}
}

// SelectBestContract selects the best option contract from the chain given
// a trading direction and market regime.
// Returns error if no contracts pass all filters, or if direction/regime is unsupported.
func (s *ContractSelectionService) SelectBestContract(
	direction domain.Direction,
	regime domain.RegimeType,
	chain []domain.OptionContractSnapshot,
) (domain.OptionContractSnapshot, error) {
	if direction != domain.DirectionLong && direction != domain.DirectionShort {
		return domain.OptionContractSnapshot{}, fmt.Errorf("unsupported direction: %s", direction)
	}
	if len(chain) == 0 {
		return domain.OptionContractSnapshot{}, errors.New("option chain is empty")
	}

	active := s.constraints
	if s.regimeConstraints != nil {
		if c, ok := s.regimeConstraints[RegimeConstraintKey{Direction: direction, Regime: regime}]; ok {
			active = c
		}
	}

	now := s.now()
	mid := (active.TargetDeltaLow + active.TargetDeltaHigh) / 2.0

	var best *domain.OptionContractSnapshot
	bestDist := math.MaxFloat64

	for i := range chain {
		snap := chain[i]
		dte := int(snap.OptionContract.Expiry.Sub(now).Hours() / 24)

		if dte < active.MinDTE || dte > active.MaxDTE {
			continue
		}

		absDelta := math.Abs(snap.Greeks.Delta)
		if absDelta < active.TargetDeltaLow || absDelta > active.TargetDeltaHigh {
			continue
		}

		if snap.OpenInterest < active.MinOpenInterest {
			continue
		}

		if snap.OptionQuote.Ask > 0 {
			spreadPct := (snap.OptionQuote.Ask - snap.OptionQuote.Bid) / snap.OptionQuote.Ask
			if spreadPct > active.MaxSpreadPct {
				continue
			}
		}

		if snap.Greeks.IV > active.MaxIV {
			continue
		}

		dist := math.Abs(absDelta - mid)
		if dist < bestDist {
			bestDist = dist
			c := snap
			best = &c
		}
	}

	if best == nil {
		return domain.OptionContractSnapshot{}, errors.New("no option contracts passed the selection filters")
	}
	return *best, nil
}
