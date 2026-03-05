package options_test

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRegimeConstraints_HasAllRegimes(t *testing.T) {
	m := options.DefaultRegimeConstraints()

	for _, dir := range []domain.Direction{domain.DirectionLong, domain.DirectionShort} {
		_, ok := m[options.RegimeConstraintKey{Direction: dir, Regime: domain.RegimeTrend}]
		assert.True(t, ok, "missing %s/%s", dir, domain.RegimeTrend)

		_, ok = m[options.RegimeConstraintKey{Direction: dir, Regime: domain.RegimeBalance}]
		assert.True(t, ok, "missing %s/%s", dir, domain.RegimeBalance)

		_, ok = m[options.RegimeConstraintKey{Direction: dir, Regime: domain.RegimeReversal}]
		assert.True(t, ok, "missing %s/%s", dir, domain.RegimeReversal)
	}
}

func TestSelectBestContract_UsesRegimeSpecificConstraints(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	defaults := defaultConstraints()
	regimes := options.RegimeConstraintsMap{
		options.RegimeConstraintKey{Direction: domain.DirectionLong, Regime: domain.RegimeBalance}: {
			MinDTE:          defaults.MinDTE,
			MaxDTE:          defaults.MaxDTE,
			TargetDeltaLow:  0.30,
			TargetDeltaHigh: 0.31,
			MinOpenInterest: defaults.MinOpenInterest,
			MaxSpreadPct:    defaults.MaxSpreadPct,
			MaxIV:           defaults.MaxIV,
		},
	}
	svc := options.NewContractSelectionServiceWithRegimes(defaults, regimes, func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now),
		makeSnapshot("AAPL", 40, 200.0, 0.305, 3.0, 3.20, 0.30, 200, now),
	}

	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeBalance, chain)
	require.NoError(t, err)
	assert.InDelta(t, 0.305, best.Greeks.Delta, 1e-9)
}

func TestSelectBestContract_FallsBackToDefaults(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	defaults := defaultConstraints()
	regimes := options.RegimeConstraintsMap{
		options.RegimeConstraintKey{Direction: domain.DirectionLong, Regime: domain.RegimeTrend}: {
			MinDTE:          defaults.MinDTE,
			MaxDTE:          defaults.MaxDTE,
			TargetDeltaLow:  0.30,
			TargetDeltaHigh: 0.31,
			MinOpenInterest: defaults.MinOpenInterest,
			MaxSpreadPct:    defaults.MaxSpreadPct,
			MaxIV:           defaults.MaxIV,
		},
	}
	svc := options.NewContractSelectionServiceWithRegimes(defaults, regimes, func() time.Time { return now })

	chain := []domain.OptionContractSnapshot{
		makeSnapshot("AAPL", 40, 190.0, 0.48, 3.0, 3.20, 0.30, 200, now),
		makeSnapshot("AAPL", 40, 200.0, 0.305, 3.0, 3.20, 0.30, 200, now),
	}

	best, err := svc.SelectBestContract(domain.DirectionLong, domain.RegimeBalance, chain)
	require.NoError(t, err)
	assert.InDelta(t, 0.48, best.Greeks.Delta, 1e-9)
}

func TestOptionsConfig_ToRegimeConstraintsMap(t *testing.T) {
	tomlStr := `
[options]
enabled = true

[options.defaults]
min_dte = 35
max_dte = 45
target_delta_low = 0.40
target_delta_high = 0.55
min_open_interest = 100
max_spread_pct = 0.10
max_iv = 1.0

[options.regime_overrides.BALANCE]
target_delta_low = 0.30
target_delta_high = 0.45
min_dte = 30
max_dte = 50
`

	var raw struct {
		Options options.OptionsConfig `toml:"options"`
	}
	_, err := toml.Decode(tomlStr, &raw)
	require.NoError(t, err)

	m := raw.Options.ToRegimeConstraintsMap()

	keyLong := options.RegimeConstraintKey{Direction: domain.DirectionLong, Regime: domain.RegimeBalance}
	gotLong, ok := m[keyLong]
	require.True(t, ok)
	assert.Equal(t, 30, gotLong.MinDTE)
	assert.Equal(t, 50, gotLong.MaxDTE)
	assert.InDelta(t, 0.30, gotLong.TargetDeltaLow, 1e-9)
	assert.InDelta(t, 0.45, gotLong.TargetDeltaHigh, 1e-9)

	keyShort := options.RegimeConstraintKey{Direction: domain.DirectionShort, Regime: domain.RegimeBalance}
	gotShort, ok := m[keyShort]
	require.True(t, ok)
	assert.Equal(t, gotLong, gotShort, "overrides should apply to both directions")
}

func TestOptionsConfig_PartialOverrides(t *testing.T) {
	cfg := options.OptionsConfig{
		Enabled: true,
		Defaults: options.ContractSelectionConstraints{
			MinDTE:          35,
			MaxDTE:          45,
			TargetDeltaLow:  0.40,
			TargetDeltaHigh: 0.55,
			MinOpenInterest: 100,
			MaxSpreadPct:    0.10,
			MaxIV:           1.0,
		},
		RegimeOverrides: map[string]options.ContractSelectionConstraints{
			"BALANCE": {
				TargetDeltaLow:  0.30,
				TargetDeltaHigh: 0.45,
			},
		},
	}

	m := cfg.ToRegimeConstraintsMap()
	got := m[options.RegimeConstraintKey{Direction: domain.DirectionLong, Regime: domain.RegimeBalance}]

	assert.Equal(t, 35, got.MinDTE)
	assert.Equal(t, 45, got.MaxDTE)
	assert.Equal(t, 100, got.MinOpenInterest)
	assert.InDelta(t, 0.10, got.MaxSpreadPct, 1e-9)
	assert.InDelta(t, 1.0, got.MaxIV, 1e-9)
	assert.InDelta(t, 0.30, got.TargetDeltaLow, 1e-9)
	assert.InDelta(t, 0.45, got.TargetDeltaHigh, 1e-9)
}
