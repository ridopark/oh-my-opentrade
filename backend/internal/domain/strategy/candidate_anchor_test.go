package strategy

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCandidateAnchor_Valid(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	ca, err := NewCandidateAnchor(ts, 185.50, AnchorSwingHigh, "5m", 5.0)
	require.NoError(t, err)

	expectedID := fmt.Sprintf("swing_high_5m_%d", ts.Unix())
	assert.Equal(t, expectedID, ca.ID)
	assert.Equal(t, ts, ca.Time)
	assert.Equal(t, 185.50, ca.Price)
	assert.Equal(t, AnchorSwingHigh, ca.Type)
	assert.Equal(t, "5m", ca.Timeframe)
	assert.Equal(t, 5.0, ca.Strength)
	assert.Equal(t, "algo", ca.Source)
	assert.Nil(t, ca.VolumeContext)
	assert.Equal(t, 0, ca.TouchCount)
}

func TestNewCandidateAnchor_AllTypes(t *testing.T) {
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	for _, at := range []CandidateAnchorType{AnchorSwingHigh, AnchorSwingLow, AnchorVolumeRotation, AnchorWeeklyOpen, AnchorSessionDerived} {
		ca, err := NewCandidateAnchor(ts, 100.0, at, "1h", 1.0)
		require.NoError(t, err, "type=%s", at)
		assert.Equal(t, at, ca.Type)
	}
}

func TestNewCandidateAnchor_ZeroTime(t *testing.T) {
	_, err := NewCandidateAnchor(time.Time{}, 100.0, AnchorSwingHigh, "5m", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "time must not be zero")
}

func TestNewCandidateAnchor_ZeroPrice(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	_, err := NewCandidateAnchor(ts, 0, AnchorSwingHigh, "5m", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "price must be positive")
}

func TestNewCandidateAnchor_NegativePrice(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	_, err := NewCandidateAnchor(ts, -10.0, AnchorSwingLow, "5m", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "price must be positive")
}

func TestNewCandidateAnchor_InvalidType(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	_, err := NewCandidateAnchor(ts, 100.0, "bogus_type", "5m", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid type")
}

func TestNewCandidateAnchor_EmptyTimeframe(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	_, err := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeframe must not be empty")
}

func TestNewCandidateAnchor_NegativeStrength(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	_, err := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", -1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "strength must be non-negative")
}

func TestCandidateAnchor_AnchorName(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	ca, err := NewCandidateAnchor(ts, 100.0, AnchorSwingLow, "1h", 3.0)
	require.NoError(t, err)
	assert.Equal(t, ca.ID, ca.AnchorName())
}

func TestCandidateAnchor_IsSwing(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)

	hi, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 1.0)
	assert.True(t, hi.IsSwing())

	lo, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingLow, "5m", 1.0)
	assert.True(t, lo.IsSwing())

	vr, _ := NewCandidateAnchor(ts, 100.0, AnchorVolumeRotation, "5m", 1.0)
	assert.False(t, vr.IsSwing())

	wo, _ := NewCandidateAnchor(ts, 100.0, AnchorWeeklyOpen, "5m", 1.0)
	assert.False(t, wo.IsSwing())
}

func TestCandidateAnchor_WithVolumeContext(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	ca, _ := NewCandidateAnchor(ts, 100.0, AnchorVolumeRotation, "5m", 2.0)
	assert.Nil(t, ca.VolumeContext)

	ctx := VolumeRotationContext{
		RotationBars:   20,
		AvgVolume:      50000,
		BreakoutVolume: 120000,
		PriceRange:     [2]float64{99.0, 101.0},
	}
	ca2 := ca.WithVolumeContext(ctx)
	require.NotNil(t, ca2.VolumeContext)
	assert.Equal(t, 20, ca2.VolumeContext.RotationBars)
	assert.Equal(t, 120000.0, ca2.VolumeContext.BreakoutVolume)
	assert.Nil(t, ca.VolumeContext, "original must be unmodified")
}

func TestCandidateAnchor_WithTouchCount(t *testing.T) {
	ts := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	ca, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 1.0)
	assert.Equal(t, 0, ca.TouchCount)

	ca2 := ca.WithTouchCount(3)
	assert.Equal(t, 3, ca2.TouchCount)
	assert.Equal(t, 0, ca.TouchCount, "original must be unmodified")
}

func TestCandidateAnchor_IDUniqueness(t *testing.T) {
	ts1 := time.Date(2026, 3, 17, 9, 30, 0, 0, time.UTC)
	ts2 := time.Date(2026, 3, 17, 9, 35, 0, 0, time.UTC)

	ca1, _ := NewCandidateAnchor(ts1, 100.0, AnchorSwingHigh, "5m", 1.0)
	ca2, _ := NewCandidateAnchor(ts2, 101.0, AnchorSwingHigh, "5m", 1.0)
	ca3, _ := NewCandidateAnchor(ts1, 100.0, AnchorSwingLow, "5m", 1.0)
	ca4, _ := NewCandidateAnchor(ts1, 100.0, AnchorSwingHigh, "1h", 1.0)

	assert.NotEqual(t, ca1.ID, ca2.ID, "different times")
	assert.NotEqual(t, ca1.ID, ca3.ID, "different types")
	assert.NotEqual(t, ca1.ID, ca4.ID, "different timeframes")
}
