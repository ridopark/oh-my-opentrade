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
