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

		// Skip OI filter when the source doesn't supply OI (DoltHub rows
		// arrive with 0). Liquidity is gated by MaxSpreadPct below.
		if snap.OpenInterest > 0 && snap.OpenInterest < active.MinOpenInterest {
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
		// Debug: log why all contracts were filtered out
		var dteReject, deltaReject, oiReject, spreadReject, ivReject int
		for i := range chain {
			snap := chain[i]
			dte := int(snap.OptionContract.Expiry.Sub(now).Hours() / 24)
			absDelta := math.Abs(snap.Delta)
			if dte < active.MinDTE || dte > active.MaxDTE {
				dteReject++
				continue
			}
			if absDelta < active.TargetDeltaLow || absDelta > active.TargetDeltaHigh {
				deltaReject++
				continue
			}
			if snap.OpenInterest > 0 && snap.OpenInterest < active.MinOpenInterest {
				oiReject++
				continue
			}
			if snap.Ask > 0 {
				spreadPct := (snap.Ask - snap.Bid) / snap.Ask
				if spreadPct > active.MaxSpreadPct {
					spreadReject++
					continue
				}
			}
			if snap.IV > active.MaxIV {
				ivReject++
				continue
			}
		}
		return domain.OptionContractSnapshot{}, fmt.Errorf(
			"no contracts passed filters (chain=%d dte_reject=%d delta_reject=%d oi_reject=%d spread_reject=%d iv_reject=%d constraints={dte:%d-%d delta:%.2f-%.2f oi:%d spread:%.2f iv:%.2f})",
			len(chain), dteReject, deltaReject, oiReject, spreadReject, ivReject,
			active.MinDTE, active.MaxDTE, active.TargetDeltaLow, active.TargetDeltaHigh,
			active.MinOpenInterest, active.MaxSpreadPct, active.MaxIV,
		)
	}
	return *best, nil
}
