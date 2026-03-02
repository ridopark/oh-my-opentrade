package execution_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKillSwitch_AllowsFirstStop(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act
	err := ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	// Assert
	assert.NoError(t, err)
	assert.False(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")))
}

func TestKillSwitch_AllowsSecondStop(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	now = now.Add(time.Minute)
	err := ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	// Assert
	assert.NoError(t, err)
	assert.False(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")))
}

func TestKillSwitch_TriggersOnThirdStopIn2Minutes(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	now = now.Add(30 * time.Second)
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	now = now.Add(30 * time.Second) // total 1 min since start
	err := ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	// Assert
	require.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "kill switch engaged")
	}
	assert.True(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")))
}

func TestKillSwitch_ResetsAfterWindowExpires(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	now = now.Add(30 * time.Second)
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	// Wait for window to expire (> 2 mins from first stop)
	now = now.Add(2 * time.Minute)
	err := ks.RecordStop("tenant1", domain.Symbol("BTCUSD")) // This should be considered the 1st/2nd stop in new window

	// Assert
	assert.NoError(t, err)
	assert.False(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")))
}

func TestKillSwitch_TracksPerTenantPerSymbol(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	_ = ks.RecordStop("tenant2", domain.Symbol("BTCUSD")) // Different tenant
	_ = ks.RecordStop("tenant1", domain.Symbol("ETHUSD")) // Different symbol

	err := ks.RecordStop("tenant2", domain.Symbol("BTCUSD")) // tenant2 only has 2 stops now

	// Assert
	assert.NoError(t, err)
	assert.False(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD"))) // tenant1 only has 2 stops
	assert.False(t, ks.IsHalted("tenant2", domain.Symbol("BTCUSD")))
	assert.False(t, ks.IsHalted("tenant1", domain.Symbol("ETHUSD")))
}

func TestKillSwitch_HaltDuration15Minutes(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act - trigger kill switch
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	// Assert immediately halted
	assert.True(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")))

	// Move forward 14 minutes
	now = now.Add(14 * time.Minute)
	assert.True(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")), "Should still be halted at 14m")
}

func TestKillSwitch_HaltExpires(t *testing.T) {
	// Arrange
	now := time.Now()
	nowFunc := func() time.Time { return now }
	ks := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	// Act - trigger kill switch
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))
	_ = ks.RecordStop("tenant1", domain.Symbol("BTCUSD"))

	// Move forward > 15 minutes
	now = now.Add(16 * time.Minute)

	// Assert
	assert.False(t, ks.IsHalted("tenant1", domain.Symbol("BTCUSD")), "Halt should expire after 15m")
}
