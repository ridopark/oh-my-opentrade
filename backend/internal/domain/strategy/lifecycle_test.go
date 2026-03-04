package strategy_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLifecycleState(t *testing.T) {
	validStates := []string{
		"Draft", "BacktestReady", "PaperActive",
		"LiveActive", "Deactivated", "Archived",
	}
	for _, s := range validStates {
		t.Run("valid_"+s, func(t *testing.T) {
			ls, err := strategy.NewLifecycleState(s)
			require.NoError(t, err)
			assert.Equal(t, s, ls.String())
		})
	}

	invalidStates := []string{"", "draft", "DRAFT", "Active", "Running"}
	for _, s := range invalidStates {
		t.Run("invalid_"+s, func(t *testing.T) {
			_, err := strategy.NewLifecycleState(s)
			assert.Error(t, err)
		})
	}
}

func TestLifecycleTransitions_HappyPath(t *testing.T) {
	// Full promotion path: Draft → BacktestReady → PaperActive → LiveActive → Deactivated → Archived
	transitions := []struct {
		from strategy.LifecycleState
		to   strategy.LifecycleState
	}{
		{strategy.LifecycleDraft, strategy.LifecycleBacktestReady},
		{strategy.LifecycleBacktestReady, strategy.LifecyclePaperActive},
		{strategy.LifecyclePaperActive, strategy.LifecycleLiveActive},
		{strategy.LifecycleLiveActive, strategy.LifecycleDeactivated},
		{strategy.LifecycleDeactivated, strategy.LifecycleArchived},
	}
	for _, tt := range transitions {
		t.Run(tt.from.String()+"→"+tt.to.String(), func(t *testing.T) {
			assert.True(t, tt.from.CanTransitionTo(tt.to))
			assert.NoError(t, strategy.ValidateTransition(tt.from, tt.to))
		})
	}
}

func TestLifecycleTransitions_Reactivation(t *testing.T) {
	// Deactivated → PaperActive (reactivation)
	assert.True(t, strategy.LifecycleDeactivated.CanTransitionTo(strategy.LifecyclePaperActive))
	assert.NoError(t, strategy.ValidateTransition(strategy.LifecycleDeactivated, strategy.LifecyclePaperActive))
}

func TestLifecycleTransitions_SkipTrading(t *testing.T) {
	// BacktestReady → Deactivated (skip trading phases)
	assert.True(t, strategy.LifecycleBacktestReady.CanTransitionTo(strategy.LifecycleDeactivated))
}

func TestLifecycleTransitions_PaperDeactivate(t *testing.T) {
	// PaperActive → Deactivated (deactivate from paper)
	assert.True(t, strategy.LifecyclePaperActive.CanTransitionTo(strategy.LifecycleDeactivated))
}

func TestLifecycleTransitions_Invalid(t *testing.T) {
	invalidTransitions := []struct {
		from strategy.LifecycleState
		to   strategy.LifecycleState
		name string
	}{
		{strategy.LifecycleDraft, strategy.LifecycleLiveActive, "skip to live"},
		{strategy.LifecycleDraft, strategy.LifecyclePaperActive, "skip to paper"},
		{strategy.LifecycleArchived, strategy.LifecycleDraft, "archived is terminal"},
		{strategy.LifecycleArchived, strategy.LifecycleDeactivated, "archived to deactivated"},
		{strategy.LifecycleLiveActive, strategy.LifecyclePaperActive, "demote live to paper"},
		{strategy.LifecycleLiveActive, strategy.LifecycleBacktestReady, "demote to backtest"},
		{strategy.LifecycleLiveActive, strategy.LifecycleArchived, "skip deactivation"},
	}
	for _, tt := range invalidTransitions {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, tt.from.CanTransitionTo(tt.to))
			err := strategy.ValidateTransition(tt.from, tt.to)
			assert.Error(t, err)
			assert.ErrorIs(t, err, strategy.ErrInvalidTransition)
		})
	}
}

func TestValidateTransition_SameState(t *testing.T) {
	states := []strategy.LifecycleState{
		strategy.LifecycleDraft,
		strategy.LifecycleBacktestReady,
		strategy.LifecyclePaperActive,
		strategy.LifecycleLiveActive,
		strategy.LifecycleDeactivated,
		strategy.LifecycleArchived,
	}
	for _, s := range states {
		t.Run(s.String(), func(t *testing.T) {
			err := strategy.ValidateTransition(s, s)
			assert.Error(t, err)
			assert.ErrorIs(t, err, strategy.ErrAlreadyInState)
		})
	}
}

func TestLifecycleState_IsActive(t *testing.T) {
	assert.False(t, strategy.LifecycleDraft.IsActive())
	assert.False(t, strategy.LifecycleBacktestReady.IsActive())
	assert.True(t, strategy.LifecyclePaperActive.IsActive())
	assert.True(t, strategy.LifecycleLiveActive.IsActive())
	assert.False(t, strategy.LifecycleDeactivated.IsActive())
	assert.False(t, strategy.LifecycleArchived.IsActive())
}

func TestLifecycleState_IsTerminal(t *testing.T) {
	assert.False(t, strategy.LifecycleDraft.IsTerminal())
	assert.False(t, strategy.LifecycleDeactivated.IsTerminal())
	assert.True(t, strategy.LifecycleArchived.IsTerminal())
}

func TestAllowedTransitions(t *testing.T) {
	// Draft can only go to BacktestReady
	allowed := strategy.LifecycleDraft.AllowedTransitions()
	assert.Len(t, allowed, 1)
	assert.Contains(t, allowed, strategy.LifecycleBacktestReady)

	// Deactivated can go to PaperActive or Archived
	allowed = strategy.LifecycleDeactivated.AllowedTransitions()
	assert.Len(t, allowed, 2)
	assert.Contains(t, allowed, strategy.LifecyclePaperActive)
	assert.Contains(t, allowed, strategy.LifecycleArchived)

	// Archived has no transitions
	allowed = strategy.LifecycleArchived.AllowedTransitions()
	assert.Empty(t, allowed)
}
