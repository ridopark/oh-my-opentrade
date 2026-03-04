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
	MinDTE          int     // minimum days to expiry
	MaxDTE          int     // maximum days to expiry
	TargetDeltaLow  float64 // minimum abs(delta)
	TargetDeltaHigh float64 // maximum abs(delta)
	MinOpenInterest int     // minimum open interest
	MaxSpreadPct    float64 // max (ask-bid)/ask
	MaxIV           float64 // max implied volatility (e.g. 1.0 = 100%)
}

// ContractSelectionService selects the best option contract from a chain
// based on configured constraints and market regime.
type ContractSelectionService struct {
	constraints ContractSelectionConstraints
	now         func() time.Time
}

// NewContractSelectionService creates a ContractSelectionService.
// now is injected for testability; pass time.Now in production.
func NewContractSelectionService(constraints ContractSelectionConstraints, now func() time.Time) *ContractSelectionService {
	return &ContractSelectionService{
		constraints: constraints,
		now:         now,
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
	if direction == domain.DirectionShort {
		return domain.OptionContractSnapshot{}, errors.New("options MVP does not support short direction")
	}
	if regime != domain.RegimeTrend {
		return domain.OptionContractSnapshot{}, fmt.Errorf("options not supported for this regime: %s", regime)
	}
	if len(chain) == 0 {
		return domain.OptionContractSnapshot{}, errors.New("option chain is empty")
	}

	now := s.now()
	mid := (s.constraints.TargetDeltaLow + s.constraints.TargetDeltaHigh) / 2.0

	var best *domain.OptionContractSnapshot
	bestDist := math.MaxFloat64

	for i := range chain {
		snap := chain[i]
		dte := int(snap.OptionContract.Expiry.Sub(now).Hours() / 24)

		if dte < s.constraints.MinDTE || dte > s.constraints.MaxDTE {
			continue
		}

		absDelta := math.Abs(snap.Greeks.Delta)
		if absDelta < s.constraints.TargetDeltaLow || absDelta > s.constraints.TargetDeltaHigh {
			continue
		}

		if snap.OpenInterest < s.constraints.MinOpenInterest {
			continue
		}

		if snap.OptionQuote.Ask > 0 {
			spreadPct := (snap.OptionQuote.Ask - snap.OptionQuote.Bid) / snap.OptionQuote.Ask
			if spreadPct > s.constraints.MaxSpreadPct {
				continue
			}
		}

		if snap.Greeks.IV > s.constraints.MaxIV {
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
