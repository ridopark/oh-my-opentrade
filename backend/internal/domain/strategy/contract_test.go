package strategy_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSignal_Valid(t *testing.T) {
	instanceID, _ := strategy.NewInstanceID("orb:1.0.0:AAPL")
	sig, err := strategy.NewSignal(
		instanceID,
		"AAPL",
		strategy.SignalEntry,
		strategy.SideBuy,
		0.85,
		map[string]string{"reason": "breakout_retest"},
	)
	require.NoError(t, err)
	assert.Equal(t, instanceID, sig.StrategyInstanceID)
	assert.Equal(t, "AAPL", sig.Symbol)
	assert.Equal(t, strategy.SignalEntry, sig.Type)
	assert.Equal(t, strategy.SideBuy, sig.Side)
	assert.Equal(t, 0.85, sig.Strength)
	assert.Equal(t, "breakout_retest", sig.Tags["reason"])
}

func TestNewSignal_NilTags(t *testing.T) {
	instanceID, _ := strategy.NewInstanceID("orb:1.0.0:AAPL")
	sig, err := strategy.NewSignal(instanceID, "AAPL", strategy.SignalFlat, strategy.SideBuy, 0.5, nil)
	require.NoError(t, err)
	assert.NotNil(t, sig.Tags) // nil tags should be initialized to empty map
}

func TestNewSignal_InvalidStrength(t *testing.T) {
	instanceID, _ := strategy.NewInstanceID("orb:1.0.0:AAPL")

	_, err := strategy.NewSignal(instanceID, "AAPL", strategy.SignalEntry, strategy.SideBuy, -0.1, nil)
	assert.ErrorIs(t, err, strategy.ErrInvalidStrength)

	_, err = strategy.NewSignal(instanceID, "AAPL", strategy.SignalEntry, strategy.SideBuy, 1.1, nil)
	assert.ErrorIs(t, err, strategy.ErrInvalidStrength)
}

func TestNewSignal_EmptySymbol(t *testing.T) {
	instanceID, _ := strategy.NewInstanceID("orb:1.0.0:AAPL")
	_, err := strategy.NewSignal(instanceID, "", strategy.SignalEntry, strategy.SideBuy, 0.5, nil)
	assert.ErrorIs(t, err, strategy.ErrEmptySymbol)
}

func TestNewSignal_BoundaryStrength(t *testing.T) {
	instanceID, _ := strategy.NewInstanceID("test:1.0.0:SPY")

	// Exactly 0 and 1 should be valid
	sig, err := strategy.NewSignal(instanceID, "SPY", strategy.SignalEntry, strategy.SideBuy, 0.0, nil)
	require.NoError(t, err)
	assert.Equal(t, 0.0, sig.Strength)

	sig, err = strategy.NewSignal(instanceID, "SPY", strategy.SignalEntry, strategy.SideBuy, 1.0, nil)
	require.NoError(t, err)
	assert.Equal(t, 1.0, sig.Strength)
}

func TestBar_FieldAccess(t *testing.T) {
	now := time.Now()
	bar := strategy.Bar{
		Time:   now,
		Open:   100.0,
		High:   105.0,
		Low:    99.0,
		Close:  103.0,
		Volume: 1000.0,
	}
	assert.Equal(t, now, bar.Time)
	assert.Equal(t, 100.0, bar.Open)
	assert.Equal(t, 105.0, bar.High)
	assert.Equal(t, 99.0, bar.Low)
	assert.Equal(t, 103.0, bar.Close)
	assert.Equal(t, 1000.0, bar.Volume)
}

func TestMeta_FieldAccess(t *testing.T) {
	id, _ := strategy.NewStrategyID("orb_break_retest")
	ver, _ := strategy.NewVersion("1.0.0")
	meta := strategy.Meta{
		ID:          id,
		Version:     ver,
		Name:        "ORB Break & Retest",
		Description: "Opening Range Breakout strategy",
		Author:      "system",
	}
	assert.Equal(t, "orb_break_retest", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
	assert.Equal(t, "ORB Break & Retest", meta.Name)
	assert.Equal(t, "system", meta.Author)
}

func TestIndicatorData_FieldAccess(t *testing.T) {
	ind := strategy.IndicatorData{
		RSI:       55.0,
		StochK:    70.0,
		StochD:    65.0,
		EMA9:      150.0,
		EMA21:     148.0,
		VWAP:      149.5,
		Volume:    5000.0,
		VolumeSMA: 4500.0,
	}
	assert.Equal(t, 55.0, ind.RSI)
	assert.Equal(t, 70.0, ind.StochK)
	assert.Equal(t, 150.0, ind.EMA9)
}
