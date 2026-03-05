package options

import "github.com/oh-my-opentrade/backend/internal/domain"

type OptionsConfig struct {
	Enabled         bool                                    `toml:"enabled"`
	Defaults        ContractSelectionConstraints            `toml:"defaults"`
	RegimeOverrides map[string]ContractSelectionConstraints `toml:"regime_overrides"`
}

func (c OptionsConfig) ToRegimeConstraintsMap() RegimeConstraintsMap {
	res := make(RegimeConstraintsMap)
	for _, dir := range []domain.Direction{domain.DirectionLong, domain.DirectionShort} {
		for _, reg := range []domain.RegimeType{domain.RegimeTrend, domain.RegimeBalance, domain.RegimeReversal} {
			res[RegimeConstraintKey{Direction: dir, Regime: reg}] = c.Defaults
		}
	}

	if len(c.RegimeOverrides) == 0 {
		return res
	}

	for regStr, override := range c.RegimeOverrides {
		reg, err := domain.NewRegimeType(regStr)
		if err != nil {
			continue
		}

		for _, dir := range []domain.Direction{domain.DirectionLong, domain.DirectionShort} {
			key := RegimeConstraintKey{Direction: dir, Regime: reg}
			base := res[key]
			res[key] = mergeConstraints(base, override)
		}
	}

	return res
}

func mergeConstraints(base, override ContractSelectionConstraints) ContractSelectionConstraints {
	out := base

	if override.MinDTE != 0 {
		out.MinDTE = override.MinDTE
	}
	if override.MaxDTE != 0 {
		out.MaxDTE = override.MaxDTE
	}
	if override.TargetDeltaLow != 0 {
		out.TargetDeltaLow = override.TargetDeltaLow
	}
	if override.TargetDeltaHigh != 0 {
		out.TargetDeltaHigh = override.TargetDeltaHigh
	}
	if override.MinOpenInterest != 0 {
		out.MinOpenInterest = override.MinOpenInterest
	}
	if override.MaxSpreadPct != 0 {
		out.MaxSpreadPct = override.MaxSpreadPct
	}
	if override.MaxIV != 0 {
		out.MaxIV = override.MaxIV
	}

	return out
}
