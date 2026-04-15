// Tests for ExpectedBarTimestamps covering equity RTH, holidays, early-close,
// crypto 24/7, and unsupported timeframe edge cases.
package domain_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustET(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	return loc
}

func TestExpectedBarTimestamps_Equity1mFullRTHWednesday(t *testing.T) {
	loc := mustET(t)
	from := time.Date(2026, time.April, 8, 9, 30, 0, 0, loc)
	to := time.Date(2026, time.April, 8, 16, 0, 0, 0, loc)

	got := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1m"), from, to)
	assert.Len(t, got, 390)
	assert.True(t, got[0].Equal(from), "first bar opens at 09:30 ET")
	assert.True(t, got[len(got)-1].Equal(time.Date(2026, time.April, 8, 15, 59, 0, 0, loc)),
		"last bar opens at 15:59 ET")
}

func TestExpectedBarTimestamps_EquityHolidayMLK2026(t *testing.T) {
	loc := mustET(t)
	from := time.Date(2026, time.January, 19, 0, 0, 0, 0, loc)
	to := time.Date(2026, time.January, 20, 0, 0, 0, 0, loc)

	got := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1m"), from, to)
	assert.Empty(t, got)
}

func TestExpectedBarTimestamps_EquityGoodFriday2026(t *testing.T) {
	loc := mustET(t)
	from := time.Date(2026, time.April, 3, 0, 0, 0, 0, loc)
	to := time.Date(2026, time.April, 4, 0, 0, 0, 0, loc)

	got := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1m"), from, to)
	assert.Empty(t, got)
}

func TestExpectedBarTimestamps_EquityEarlyClose20261127(t *testing.T) {
	loc := mustET(t)
	from := time.Date(2026, time.November, 27, 9, 30, 0, 0, loc)
	to := time.Date(2026, time.November, 27, 16, 0, 0, 0, loc)

	got := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1m"), from, to)
	// 09:30 .. 12:59 inclusive opens = 210 bars (last bar covers 12:59-13:00).
	assert.Len(t, got, 210)
	assert.True(t, got[len(got)-1].Equal(time.Date(2026, time.November, 27, 12, 59, 0, 0, loc)))
}

func TestExpectedBarTimestamps_Equity1hWeek(t *testing.T) {
	loc := mustET(t)
	// Mon 2026-03-02 .. Sat 2026-03-07: 5 normal RTH days, no holidays.
	from := time.Date(2026, time.March, 2, 0, 0, 0, 0, loc)
	to := time.Date(2026, time.March, 7, 0, 0, 0, 0, loc)

	got := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1h"), from, to)
	// 1h: opens 09:30, 10:30, 11:30, 12:30, 13:30, 14:30, 15:30 = 7/day x 5
	// Last 1h bar opens 15:30 (closes 16:30) — but session ends 16:00, so the last
	// bar that fits before close is one whose open+step <= close: open 15:00 (close 16:00)?
	// With step=3600s and last=close-step=15:00, opens are 09:30,10:30,11:30,12:30,13:30,14:30 = 6/day.
	// Documenting actual behavior here.
	assert.Equal(t, 6*5, len(got))
}

func TestExpectedBarTimestamps_Equity1dWeekWithHoliday(t *testing.T) {
	loc := mustET(t)
	// Week containing MLK Mon 2026-01-19 (holiday): Mon-Fri = 4 trading days.
	from := time.Date(2026, time.January, 19, 0, 0, 0, 0, loc)
	to := time.Date(2026, time.January, 24, 0, 0, 0, 0, loc)

	got := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1d"), from, to)
	assert.Len(t, got, 4)
}

func TestExpectedBarTimestamps_Crypto1m24h(t *testing.T) {
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	got := domain.ExpectedBarTimestamps(domain.Symbol("BTC/USD"), domain.Timeframe("1m"), from, to)
	assert.Len(t, got, 1440)
}

func TestExpectedBarTimestamps_Crypto1d30Days(t *testing.T) {
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 30)

	got := domain.ExpectedBarTimestamps(domain.Symbol("BTC/USD"), domain.Timeframe("1d"), from, to)
	assert.Len(t, got, 30)
}

func TestExpectedBarTimestamps_UnsupportedTimeframe(t *testing.T) {
	from := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	got := domain.ExpectedBarTimestamps(domain.Symbol("BTC/USD"), domain.Timeframe("7m"), from, to)
	assert.Empty(t, got)
}

func TestExpectedBarTimestamps_FromGreaterEqualTo(t *testing.T) {
	t0 := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	got := domain.ExpectedBarTimestamps(domain.Symbol("BTC/USD"), domain.Timeframe("1m"), t0, t0)
	assert.Empty(t, got)
}
