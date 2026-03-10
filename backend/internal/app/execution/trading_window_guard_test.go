package execution_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTWGuard(now time.Time) *execution.TradingWindowGuard {
	return execution.NewTradingWindowGuardWithClock(func() time.Time { return now }, zerolog.Nop())
}

func TestTradingWindowGuard_RejectsOutsideHours(t *testing.T) {
	// 7:00 AM ET is before 08:00 window start
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 10, 7, 0, 0, 0, et) // Tuesday 7:00 AM ET
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"allowed_hours_start": "08:00",
		"allowed_hours_end":   "17:00",
		"allowed_hours_tz":    "America/New_York",
	}

	err := guard.Check(intent)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "trading_window")
	assert.Contains(t, err.Error(), "outside allowed window")
}

func TestTradingWindowGuard_AcceptsWithinHours(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 10, 10, 30, 0, 0, et) // Tuesday 10:30 AM ET
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"allowed_hours_start": "08:00",
		"allowed_hours_end":   "17:00",
		"allowed_hours_tz":    "America/New_York",
	}

	err := guard.Check(intent)
	assert.NoError(t, err)
}

func TestTradingWindowGuard_RejectsAfterEndHour(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 10, 17, 0, 0, 0, et) // Tuesday 17:00 ET (end boundary)
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"allowed_hours_start": "08:00",
		"allowed_hours_end":   "17:00",
		"allowed_hours_tz":    "America/New_York",
	}

	err := guard.Check(intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside allowed window")
}

func TestTradingWindowGuard_RejectsWeekend(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, et) // Saturday noon ET
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"skip_weekends":    "true",
		"allowed_hours_tz": "America/New_York",
	}

	err := guard.Check(intent)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "weekend")
}

func TestTradingWindowGuard_AllowsWeekendWhenNotSkipped(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, et) // Saturday noon ET
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"skip_weekends": "false",
	}

	err := guard.Check(intent)
	assert.NoError(t, err)
}

func TestTradingWindowGuard_NoOpWhenNoMeta(t *testing.T) {
	now := time.Date(2026, 3, 7, 3, 0, 0, 0, time.UTC) // Saturday 3 AM
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = nil

	err := guard.Check(intent)
	assert.NoError(t, err)
}

func TestTradingWindowGuard_NoOpWhenNoHoursConfigured(t *testing.T) {
	now := time.Date(2026, 3, 7, 3, 0, 0, 0, time.UTC)
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{}

	err := guard.Check(intent)
	assert.NoError(t, err)
}

func TestTradingWindowGuard_UsesUTCWhenNoTimezone(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) // Tuesday noon UTC
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"allowed_hours_start": "08:00",
		"allowed_hours_end":   "17:00",
	}

	err := guard.Check(intent)
	assert.NoError(t, err)
}

func TestTradingWindowGuard_AcceptsAtStartBoundary(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 10, 8, 0, 0, 0, et) // Tuesday 08:00 ET
	guard := makeTWGuard(now)

	intent := createTestOrderIntent(t)
	intent.Meta = map[string]string{
		"allowed_hours_start": "08:00",
		"allowed_hours_end":   "17:00",
		"allowed_hours_tz":    "America/New_York",
	}

	err := guard.Check(intent)
	assert.NoError(t, err)
}
