package strategy_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStrategyID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "orb_break_retest", false},
		{"valid single char", "a", false},
		{"valid with numbers", "momentum_v2", false},
		{"empty", "", true},
		{"starts with number", "1abc", true},
		{"starts with underscore", "_abc", true},
		{"uppercase", "ORB", true},
		{"has dash", "orb-break", true},
		{"has space", "orb break", true},
		{"too long", "a" + string(make([]byte, 64)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := strategy.NewStrategyID(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, strategy.StrategyID(""), id)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.input, id.String())
			}
		})
	}
}

func TestNewVersion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid semver", "1.2.0", false},
		{"valid with pre-release", "1.0.0-alpha.1", false},
		{"valid zero", "0.0.1", false},
		{"missing patch", "1.2", true},
		{"just major", "1", true},
		{"empty", "", true},
		{"no dots", "120", true},
		{"letters in version", "a.b.c", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := strategy.NewVersion(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.input, v.String())
			}
		})
	}
}

func TestNewSignalType(t *testing.T) {
	tests := []struct {
		input   string
		want    strategy.SignalType
		wantErr bool
	}{
		{"entry", strategy.SignalEntry, false},
		{"exit", strategy.SignalExit, false},
		{"adjust", strategy.SignalAdjust, false},
		{"flat", strategy.SignalFlat, false},
		{"invalid", strategy.SignalType(""), true},
		{"", strategy.SignalType(""), true},
		{"ENTRY", strategy.SignalType(""), true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			st, err := strategy.NewSignalType(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, st)
			}
		})
	}
}

func TestSignalTypeIsActionable(t *testing.T) {
	assert.True(t, strategy.SignalEntry.IsActionable())
	assert.True(t, strategy.SignalExit.IsActionable())
	assert.True(t, strategy.SignalAdjust.IsActionable())
	assert.False(t, strategy.SignalFlat.IsActionable())
}

func TestNewSide(t *testing.T) {
	tests := []struct {
		input   string
		want    strategy.Side
		wantErr bool
	}{
		{"buy", strategy.SideBuy, false},
		{"sell", strategy.SideSell, false},
		{"BUY", strategy.Side(""), true},
		{"", strategy.Side(""), true},
		{"hold", strategy.Side(""), true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			s, err := strategy.NewSide(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, s)
			}
		})
	}
}

func TestNewConflictPolicy(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"priority_wins", false},
		{"merge", false},
		{"vote", false},
		{"invalid", true},
		{"", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := strategy.NewConflictPolicy(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewHookEngine(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"builtin", false},
		{"yaegi", false},
		{"wasm", true},
		{"", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := strategy.NewHookEngine(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewInstanceID(t *testing.T) {
	id, err := strategy.NewInstanceID("orb_break_retest:1.0.0:AAPL")
	require.NoError(t, err)
	assert.Equal(t, "orb_break_retest:1.0.0:AAPL", id.String())

	_, err = strategy.NewInstanceID("")
	assert.Error(t, err)
}
