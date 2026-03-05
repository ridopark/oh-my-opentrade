package options

import "github.com/oh-my-opentrade/backend/internal/domain"

type RegimeConstraintKey struct {
	Direction domain.Direction
	Regime    domain.RegimeType
}

type RegimeConstraintsMap map[RegimeConstraintKey]ContractSelectionConstraints

func DefaultRegimeConstraints() RegimeConstraintsMap {
	byRegime := map[domain.RegimeType]ContractSelectionConstraints{
		domain.RegimeTrend: {
			MinDTE:          35,
			MaxDTE:          45,
			TargetDeltaLow:  0.40,
			TargetDeltaHigh: 0.55,
		},
		domain.RegimeBalance: {
			MinDTE:          30,
			MaxDTE:          50,
			TargetDeltaLow:  0.30,
			TargetDeltaHigh: 0.45,
		},
		domain.RegimeReversal: {
			MinDTE:          40,
			MaxDTE:          55,
			TargetDeltaLow:  0.35,
			TargetDeltaHigh: 0.50,
		},
	}

	out := make(RegimeConstraintsMap)
	for _, dir := range []domain.Direction{domain.DirectionLong, domain.DirectionShort} {
		for reg, c := range byRegime {
			out[RegimeConstraintKey{Direction: dir, Regime: reg}] = c
		}
	}

	return out
}
