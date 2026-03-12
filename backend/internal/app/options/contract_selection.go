package options

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type ContractSelectionService struct {
	constraints       domain.ContractSelectionConstraints
	regimeConstraints domain.RegimeConstraintsMap
	now               func() time.Time
}

func NewContractSelectionService(constraints domain.ContractSelectionConstraints, now func() time.Time) *ContractSelectionService {
	return &ContractSelectionService{
		constraints:       constraints,
		regimeConstraints: nil,
		now:               now,
	}
}

func NewContractSelectionServiceWithRegimes(
	defaults domain.ContractSelectionConstraints,
	regimes domain.RegimeConstraintsMap,
	now func() time.Time,
) *ContractSelectionService {
	return &ContractSelectionService{
		constraints:       defaults,
		regimeConstraints: regimes,
		now:               now,
	}
}

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
		if c, ok := s.regimeConstraints[domain.RegimeConstraintKey{Direction: direction, Regime: regime}]; ok {
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

		absDelta := math.Abs(snap.Delta)
		if absDelta < active.TargetDeltaLow || absDelta > active.TargetDeltaHigh {
			continue
		}

		if snap.OpenInterest < active.MinOpenInterest {
			continue
		}

		if snap.Ask > 0 {
			spreadPct := (snap.Ask - snap.Bid) / snap.Ask
			if spreadPct > active.MaxSpreadPct {
				continue
			}
		}

		if snap.IV > active.MaxIV {
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
