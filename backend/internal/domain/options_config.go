package domain

// OptionsConfig holds the options trading configuration for a strategy.
// Parsed from the [options] TOML block in strategy spec files.
type OptionsConfig struct {
	Enabled         bool                                    `toml:"enabled"`
	Defaults        ContractSelectionConstraints            `toml:"defaults"`
	RegimeOverrides map[string]ContractSelectionConstraints `toml:"regime_overrides"`
	MaxContracts    int                                     `toml:"max_contracts"`    // cap contract count to limit spread drag (0 = use default 15)
	LimitSpreadPct  *float64                                `toml:"limit_spread_pct"`  // limit = mid + X * spread (default 0.6 = 60%)
	LimitBufferBPS  *int                                    `toml:"limit_buffer_bps"`  // fallback when spread=0: ask * (1 + bps/10000) (default 300 = 3%)
	StaleCancelSecs *int                                    `toml:"stale_cancel_secs"` // cancel unfilled option orders after N seconds (default 60)
	PaperFillAtAsk  bool                                    `toml:"paper_fill_at_ask"` // in paper-pinned BTO path, fill at min(live_ask, cap) instead of always at cap (default false)
}

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

// RegimeConstraintKey identifies a unique combination of direction and regime
// for looking up constraint overrides.
type RegimeConstraintKey struct {
	Direction Direction
	Regime    RegimeType
}

// RegimeConstraintsMap maps direction+regime pairs to their constraint overrides.
type RegimeConstraintsMap map[RegimeConstraintKey]ContractSelectionConstraints

// ToRegimeConstraintsMap builds a RegimeConstraintsMap from the OptionsConfig.
// It starts with defaults for all direction×regime combinations, then merges
// any regime-specific overrides on top.
func (c OptionsConfig) ToRegimeConstraintsMap() RegimeConstraintsMap {
	res := make(RegimeConstraintsMap)
	for _, dir := range []Direction{DirectionLong, DirectionShort} {
		for _, reg := range []RegimeType{RegimeTrend, RegimeTrendUp, RegimeTrendDown, RegimeBalance, RegimeReversal} {
			res[RegimeConstraintKey{Direction: dir, Regime: reg}] = c.Defaults
		}
	}

	if len(c.RegimeOverrides) == 0 {
		return res
	}

	for regStr, override := range c.RegimeOverrides {
		reg, err := NewRegimeType(regStr)
		if err != nil {
			continue
		}

		for _, dir := range []Direction{DirectionLong, DirectionShort} {
			key := RegimeConstraintKey{Direction: dir, Regime: reg}
			base := res[key]
			res[key] = mergeConstraints(base, override)
		}
	}

	return res
}

// DefaultRegimeConstraints returns production defaults for option contract
// selection, keyed by direction and regime.
func DefaultRegimeConstraints() RegimeConstraintsMap {
	byRegime := map[RegimeType]ContractSelectionConstraints{
		RegimeTrend: {
			MinDTE:          35,
			MaxDTE:          45,
			TargetDeltaLow:  0.40,
			TargetDeltaHigh: 0.55,
		},
		RegimeTrendUp: {
			MinDTE:          35,
			MaxDTE:          45,
			TargetDeltaLow:  0.40,
			TargetDeltaHigh: 0.55,
		},
		RegimeTrendDown: {
			MinDTE:          35,
			MaxDTE:          45,
			TargetDeltaLow:  0.40,
			TargetDeltaHigh: 0.55,
		},
		RegimeBalance: {
			MinDTE:          30,
			MaxDTE:          50,
			TargetDeltaLow:  0.30,
			TargetDeltaHigh: 0.45,
		},
		RegimeReversal: {
			MinDTE:          40,
			MaxDTE:          55,
			TargetDeltaLow:  0.35,
			TargetDeltaHigh: 0.50,
		},
	}

	out := make(RegimeConstraintsMap)
	for _, dir := range []Direction{DirectionLong, DirectionShort} {
		for reg, c := range byRegime {
			out[RegimeConstraintKey{Direction: dir, Regime: reg}] = c
		}
	}

	return out
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
