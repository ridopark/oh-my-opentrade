package options

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// calendarDTE returns the difference between expiry and now in calendar days
// (number of ET midnights between them). Using hour-based arithmetic with
// integer truncation (int(expiry.Sub(now).Hours()/24)) rejects contracts that
// are exactly minDTE days away when the current wall-clock has advanced past
// midnight ET — e.g. Wed 13:25 ET vs Mon-expiry is 4.7 days → truncates to 4,
// failing a minDTE=5 filter even though the contract is "5 days out" by the
// conventional trading-day count.
func calendarDTE(expiry, now time.Time) int {
	loc := now.Location()
	expDay := time.Date(expiry.In(loc).Year(), expiry.In(loc).Month(), expiry.In(loc).Day(), 0, 0, 0, 0, loc)
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	days := int(math.Round(expDay.Sub(nowDay).Hours() / 24))
	if days < 0 {
		days = 0
	}
	return days
}

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
	targetDTE := (active.MinDTE + active.MaxDTE) / 2

	// Sort chain by DTE closeness to the midpoint of the acceptable range so
	// that when a downstream cap (e.g. the 250-contract Alpaca API limit) has
	// already truncated the returned chain, in-range candidates are iterated
	// first and a selection isn't starved because the remaining contracts sit
	// at an outlier expiry. Secondary key is delta-closeness to preserve the
	// existing strike-picking intent.
	sorted := make([]domain.OptionContractSnapshot, len(chain))
	copy(sorted, chain)
	sort.SliceStable(sorted, func(i, j int) bool {
		di := calendarDTE(sorted[i].Expiry, now)
		dj := calendarDTE(sorted[j].Expiry, now)
		return abs(di-targetDTE) < abs(dj-targetDTE)
	})
	chain = sorted

	var best *domain.OptionContractSnapshot
	bestDist := math.MaxFloat64

	for i := range chain {
		snap := chain[i]
		dte := calendarDTE(snap.Expiry, now)

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
			dte := calendarDTE(snap.Expiry, now)
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
