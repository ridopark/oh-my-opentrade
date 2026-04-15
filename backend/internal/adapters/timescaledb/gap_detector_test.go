// Pure unit tests for the gap-coalescing logic. No DB.
package timescaledb

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mins(base time.Time, n ...int) []time.Time {
	out := make([]time.Time, 0, len(n))
	for _, m := range n {
		out = append(out, base.Add(time.Duration(m)*time.Minute))
	}
	return out
}

func TestCoalesceGaps_AllPresent(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	base := time.Date(2026, time.April, 8, 9, 30, 0, 0, loc)
	exp := mins(base, 0, 1, 2, 3, 4)
	act := mins(base, 0, 1, 2, 3, 4)

	got := coalesceGaps(domain.Symbol("AAPL"), domain.Timeframe("1m"), exp, act)
	assert.Empty(t, got)
}

func TestCoalesceGaps_SingleMissingMiddle(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	base := time.Date(2026, time.April, 8, 9, 30, 0, 0, loc)
	exp := mins(base, 0, 1, 2, 3, 4)
	act := mins(base, 0, 1, 3, 4)

	got := coalesceGaps(domain.Symbol("AAPL"), domain.Timeframe("1m"), exp, act)
	require.Len(t, got, 1)
	assert.True(t, got[0].Start.Equal(base.Add(2*time.Minute)))
	assert.True(t, got[0].End.Equal(base.Add(3*time.Minute)))
	assert.Equal(t, 5, got[0].ExpectedCount)
	assert.Equal(t, 4, got[0].ActualCount)
}

func TestCoalesceGaps_ThreeContiguousMissing(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	base := time.Date(2026, time.April, 8, 9, 30, 0, 0, loc)
	exp := mins(base, 0, 1, 2, 3, 4, 5, 6)
	act := mins(base, 0, 1, 5, 6)

	got := coalesceGaps(domain.Symbol("AAPL"), domain.Timeframe("1m"), exp, act)
	require.Len(t, got, 1)
	assert.True(t, got[0].Start.Equal(base.Add(2*time.Minute)))
	assert.True(t, got[0].End.Equal(base.Add(5*time.Minute)))
}

func TestCoalesceGaps_TwoDisjointGaps(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	base := time.Date(2026, time.April, 8, 9, 30, 0, 0, loc)
	exp := mins(base, 0, 1, 2, 3, 4, 5, 6, 7)
	act := mins(base, 0, 2, 3, 5, 7)

	got := coalesceGaps(domain.Symbol("AAPL"), domain.Timeframe("1m"), exp, act)
	require.Len(t, got, 3)
	assert.True(t, got[0].Start.Equal(base.Add(1*time.Minute)))
	assert.True(t, got[0].End.Equal(base.Add(2*time.Minute)))
	assert.True(t, got[1].Start.Equal(base.Add(4*time.Minute)))
	assert.True(t, got[1].End.Equal(base.Add(5*time.Minute)))
	assert.True(t, got[2].Start.Equal(base.Add(6*time.Minute)))
	assert.True(t, got[2].End.Equal(base.Add(7*time.Minute)))
}

func TestCoalesceGaps_HolidayInsideScanWindow(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	// Window spans Fri 2026-01-16 RTH .. Tue 2026-01-20 — MLK Mon 2026-01-19 closed.
	from := time.Date(2026, time.January, 16, 9, 30, 0, 0, loc)
	to := time.Date(2026, time.January, 20, 16, 0, 0, 0, loc)

	exp := domain.ExpectedBarTimestamps(domain.Symbol("AAPL"), domain.Timeframe("1m"), from, to)
	got := coalesceGaps(domain.Symbol("AAPL"), domain.Timeframe("1m"), exp, exp)
	assert.Empty(t, got, "no gaps when actual matches expected; holiday correctly excluded from expected")

	mlk := time.Date(2026, time.January, 19, 12, 0, 0, 0, loc)
	for _, ts := range exp {
		etDate := ts.In(loc)
		assert.False(t, etDate.Year() == mlk.Year() && etDate.Month() == mlk.Month() && etDate.Day() == mlk.Day(),
			"MLK day must not be in expected set")
	}
}

func TestCoalesceGaps_FirstAndLastMissing(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	base := time.Date(2026, time.April, 8, 9, 30, 0, 0, loc)
	exp := mins(base, 0, 1, 2, 3, 4)
	act := mins(base, 1, 2, 3)

	got := coalesceGaps(domain.Symbol("AAPL"), domain.Timeframe("1m"), exp, act)
	require.Len(t, got, 2)
	assert.True(t, got[0].Start.Equal(base))
	assert.True(t, got[0].End.Equal(base.Add(time.Minute)))
	assert.True(t, got[1].Start.Equal(base.Add(4*time.Minute)))
	assert.True(t, got[1].End.Equal(base.Add(5*time.Minute)))
}

func TestDiffSortedTimes_EmptyActual(t *testing.T) {
	base := time.Date(2026, time.April, 8, 9, 30, 0, 0, time.UTC)
	exp := mins(base, 0, 1, 2)
	got := diffSortedTimes(exp, nil)
	assert.Equal(t, exp, got)
}
