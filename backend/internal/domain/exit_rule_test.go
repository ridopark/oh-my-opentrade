package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateExitRules_RejectsTrailingGteMaxLoss(t *testing.T) {
	rules := []ExitRule{
		{Type: ExitRuleTrailingStop, Params: map[string]float64{"pct": 0.03}},
		{Type: ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.02}},
	}
	err := ValidateExitRules(rules)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TRAILING_STOP")
}

func TestValidateExitRules_RejectsTrailingEqualMaxLoss(t *testing.T) {
	rules := []ExitRule{
		{Type: ExitRuleTrailingStop, Params: map[string]float64{"pct": 0.02}},
		{Type: ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.02}},
	}
	err := ValidateExitRules(rules)
	require.Error(t, err)
}

func TestValidateExitRules_AcceptsTrailingLtMaxLoss(t *testing.T) {
	rules := []ExitRule{
		{Type: ExitRuleTrailingStop, Params: map[string]float64{"pct": 0.008}},
		{Type: ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.025}},
	}
	err := ValidateExitRules(rules)
	assert.NoError(t, err)
}

func TestValidateExitRules_AcceptsNoTrailingStop(t *testing.T) {
	rules := []ExitRule{
		{Type: ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.02}},
		{Type: ExitRuleEODFlatten, Params: map[string]float64{"minutes_before_close": 5}},
	}
	err := ValidateExitRules(rules)
	assert.NoError(t, err)
}

func TestValidateExitRules_AcceptsEmptyRules(t *testing.T) {
	err := ValidateExitRules(nil)
	assert.NoError(t, err)
}

func TestNewExitRuleType_AcceptsBreakevenStop(t *testing.T) {
	rt, err := NewExitRuleType("BREAKEVEN_STOP")
	require.NoError(t, err)
	assert.Equal(t, ExitRuleBreakevenStop, rt)
}

func TestNewExitRuleType_RejectsInvalid(t *testing.T) {
	_, err := NewExitRuleType("NONEXISTENT")
	require.Error(t, err)
}

func TestNewExitRuleType_AcceptsTieredPremiumStopDTE(t *testing.T) {
	rt, err := NewExitRuleType("TIERED_PREMIUM_STOP_DTE")
	require.NoError(t, err)
	assert.Equal(t, ExitRuleTieredPremiumStopDTE, rt)
}

func TestNewExitRuleType_AcceptsChandelierTrailUnderlying(t *testing.T) {
	rt, err := NewExitRuleType("CHANDELIER_TRAIL_UNDERLYING")
	require.NoError(t, err)
	assert.Equal(t, ExitRuleChandelierTrailUnderlying, rt)
}

func TestNewExitRuleType_AcceptsATRExtensionTimeStop(t *testing.T) {
	rt, err := NewExitRuleType("ATR_EXTENSION_TIME_STOP")
	require.NoError(t, err)
	assert.Equal(t, ExitRuleATRExtensionTimeStop, rt)
}

func TestExitRuleType_NewRules_RequirePrice(t *testing.T) {
	cases := []ExitRuleType{
		ExitRuleTieredPremiumStopDTE,
		ExitRuleChandelierTrailUnderlying,
		ExitRuleATRExtensionTimeStop,
	}
	for _, rt := range cases {
		t.Run(string(rt), func(t *testing.T) {
			assert.True(t, rt.RequiresPrice(), "%s should require a fresh price to evaluate", rt)
		})
	}
}

func TestWidestActiveStopPct(t *testing.T) {
	cases := []struct {
		name       string
		rules      []ExitRule
		wantPct    float64
		wantSource string
	}{
		{
			name:       "empty returns zero",
			rules:      nil,
			wantPct:    0,
			wantSource: "",
		},
		{
			name: "premium trail 10pct + max loss 20pct → 20pct",
			rules: []ExitRule{
				{Type: ExitRulePremiumTrail, Params: map[string]float64{"trail_pct": 0.10}},
				{Type: ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.20}},
			},
			wantPct:    0.20,
			wantSource: "MAX_LOSS.pct",
		},
		{
			name: "trailing stop 5pct + premium stop 30pct → 30pct",
			rules: []ExitRule{
				{Type: ExitRuleTrailingStop, Params: map[string]float64{"pct": 0.05}},
				{Type: ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.30}},
			},
			wantPct:    0.30,
			wantSource: "PREMIUM_STOP.threshold",
		},
		{
			name: "chandelier only",
			rules: []ExitRule{
				{Type: ExitRuleChandelierTrail, Params: map[string]float64{"giveback_pct": 0.15}},
			},
			wantPct:    0.15,
			wantSource: "CHANDELIER_TRAIL.giveback_pct",
		},
		{
			name: "rules without stop params are ignored",
			rules: []ExitRule{
				{Type: ExitRuleProfitTarget, Params: map[string]float64{"pct": 0.05}},
				{Type: ExitRuleMaxLoss, Params: map[string]float64{"pct": 0.12}},
			},
			wantPct:    0.12,
			wantSource: "MAX_LOSS.pct",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pct, source := WidestActiveStopPct(tc.rules)
			assert.InDelta(t, tc.wantPct, pct, 1e-9)
			assert.Equal(t, tc.wantSource, source)
		})
	}
}
