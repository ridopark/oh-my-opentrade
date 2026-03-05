package risk_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPnLSource is a test double implementing risk.DailyPnLSource.
type stubPnLSource struct {
	pnl float64
}

func (s *stubPnLSource) GetDailyRealizedPnL(_ string, _ domain.EnvMode) float64 {
	return s.pnl
}

func newTestBreaker(maxPct, maxUSD float64, pnlSource risk.DailyPnLSource, now func() time.Time) *risk.DailyLossBreaker {
	log := zerolog.Nop()
	return risk.NewDailyLossBreaker(maxPct, maxUSD, pnlSource, now, log)
}

func TestDailyLossBreaker_AllowsPositivePnL(t *testing.T) {
	src := &stubPnLSource{pnl: 500.0} // profit
	breaker := newTestBreaker(0.05, 5000, src, time.Now)

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	assert.NoError(t, err)
	assert.False(t, breaker.IsHalted())
}

func TestDailyLossBreaker_AllowsZeroPnL(t *testing.T) {
	src := &stubPnLSource{pnl: 0}
	breaker := newTestBreaker(0.05, 5000, src, time.Now)

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	assert.NoError(t, err)
	assert.False(t, breaker.IsHalted())
}

func TestDailyLossBreaker_AllowsSmallLoss(t *testing.T) {
	src := &stubPnLSource{pnl: -100.0} // small loss: 0.1% of 100k, well under 5%
	breaker := newTestBreaker(0.05, 5000, src, time.Now)

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	assert.NoError(t, err)
	assert.False(t, breaker.IsHalted())
}

func TestDailyLossBreaker_TripsOnPercentageLimit(t *testing.T) {
	// 5% of 100k = 5000. Loss of 5000 should trip.
	src := &stubPnLSource{pnl: -5000.0}
	breaker := newTestBreaker(0.05, 10000, src, time.Now) // USD limit higher so % triggers first

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "daily loss circuit breaker")
	assert.Contains(t, err.Error(), "exceeds max")
	assert.True(t, breaker.IsHalted())
}

func TestDailyLossBreaker_TripsOnAbsoluteUSDLimit(t *testing.T) {
	// USD limit 3000. Loss of 3000 should trip even though % limit (5% of 100k = 5000) isn't hit.
	src := &stubPnLSource{pnl: -3000.0}
	breaker := newTestBreaker(0.05, 3000, src, time.Now)

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "daily loss circuit breaker")
	assert.Contains(t, err.Error(), "max $")
	assert.True(t, breaker.IsHalted())
}

func TestDailyLossBreaker_StaysHaltedAfterTrip(t *testing.T) {
	src := &stubPnLSource{pnl: -6000.0}
	breaker := newTestBreaker(0.05, 5000, src, time.Now)

	// First check trips it
	_ = breaker.Check("tenant1", domain.EnvModePaper, 100000)
	require.True(t, breaker.IsHalted())

	// Even if PnL improves, halt remains for the day
	src.pnl = 0
	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	require.Error(t, err) // still halted
	assert.True(t, breaker.IsHalted())
}

func TestDailyLossBreaker_ResetsOnNewDay(t *testing.T) {
	now := time.Date(2025, 3, 10, 14, 0, 0, 0, time.UTC)
	nowFunc := func() time.Time { return now }
	src := &stubPnLSource{pnl: -6000.0}
	breaker := newTestBreaker(0.05, 5000, src, nowFunc)

	// Trip the breaker
	_ = breaker.Check("tenant1", domain.EnvModePaper, 100000)
	require.True(t, breaker.IsHalted())

	// Advance to next day
	now = time.Date(2025, 3, 11, 9, 30, 0, 0, time.UTC)
	src.pnl = 0 // new day, no losses yet

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	assert.NoError(t, err)
	assert.False(t, breaker.IsHalted())
}

func TestDailyLossBreaker_ManualReset(t *testing.T) {
	src := &stubPnLSource{pnl: -6000.0}
	breaker := newTestBreaker(0.05, 5000, src, time.Now)

	_ = breaker.Check("tenant1", domain.EnvModePaper, 100000)
	require.True(t, breaker.IsHalted())

	breaker.Reset()

	assert.False(t, breaker.IsHalted())
}

func TestDailyLossBreaker_ZeroLimitsDisabled(t *testing.T) {
	// If both limits are 0, breaker should never trip
	src := &stubPnLSource{pnl: -999999.0}
	breaker := newTestBreaker(0, 0, src, time.Now)

	err := breaker.Check("tenant1", domain.EnvModePaper, 100000)

	assert.NoError(t, err)
	assert.False(t, breaker.IsHalted())
}
