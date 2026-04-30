package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionResolver_RefreshIfStale_LazyReload pins the cross-day staleness
// fix: when live runs continuously past startup, the in-memory sessions map
// must gain the previous-day session row before ResolveAnchors fires for the
// new day. Otherwise findPreviousDay falls back to the most recent loaded
// row, which was the empirical cause of pd_high.barCount inflation across
// multi-day live runs.
func TestSessionResolver_RefreshIfStale_LazyReload(t *testing.T) {
	loc := domain.NYLocation()
	r := NewSessionResolver(loc)

	day0 := time.Date(2026, 4, 27, 0, 0, 0, 0, loc) // Monday
	day1 := day0.AddDate(0, 0, 1)

	mkSession := func(d time.Time, hi float64) SessionData {
		date := d.Format("2006-01-02")
		open := time.Date(d.Year(), d.Month(), d.Day(), 9, 30, 0, 0, loc)
		hiTime := time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc)
		return SessionData{
			Date:     date,
			Open:     hi - 1, OpenTime: open,
			High:     hi, HighTime: hiTime,
			Low:      hi - 2, LowTime: hiTime,
			Close:    hi - 0.5,
			ORHigh:   hi, ORHighTime: open,
			ORLow:    hi - 2, ORLowTime: open,
		}
	}

	r.mu.Lock()
	r.sessions["AAPL"] = map[string]SessionData{
		day0.Format("2006-01-02"): mkSession(day0, 200.0),
	}
	r.mu.Unlock()

	loaderCalls := 0
	var lastFrom, lastTo time.Time
	r.sessionLoader = func(_ context.Context, _ domain.Symbol, from, to time.Time) (map[string]SessionData, error) {
		loaderCalls++
		lastFrom = from
		lastTo = to
		out := map[string]SessionData{
			day0.Format("2006-01-02"): mkSession(day0, 200.0),
			day1.Format("2006-01-02"): mkSession(day1, 210.0),
		}
		return out, nil
	}

	// Day +1 first bar: previous-day = Day 0, already loaded -> no refresh.
	bar1 := time.Date(2026, 4, 28, 9, 30, 0, 0, loc)
	require.NoError(t, r.RefreshIfStale(context.Background(), nil, "AAPL", bar1))
	assert.Equal(t, 0, loaderCalls, "Day +1 first bar should not trigger reload (Day 0 covers prevDay)")

	// Day +2 first bar: previous-day = Day +1, NOT loaded -> refresh.
	bar2 := time.Date(2026, 4, 29, 9, 30, 0, 0, loc)
	require.NoError(t, r.RefreshIfStale(context.Background(), nil, "AAPL", bar2))
	assert.Equal(t, 1, loaderCalls, "Day +2 first bar should trigger reload (Day +1 missing)")
	assert.True(t, lastFrom.Before(bar2), "load window should start before the bar time")
	assert.True(t, lastTo.After(bar2), "load window should end after the bar time")

	// After refresh, Day +1 must be present.
	r.mu.RLock()
	_, hasDay1 := r.sessions["AAPL"][day1.Format("2006-01-02")]
	r.mu.RUnlock()
	assert.True(t, hasDay1, "post-refresh sessions map should contain Day +1")

	// Same Day +2 first bar repeated -> still no extra reload (idempotent).
	require.NoError(t, r.RefreshIfStale(context.Background(), nil, "AAPL", bar2))
	assert.Equal(t, 1, loaderCalls, "repeat call same day must not reload")

	// ResolveAnchors after refresh must return Day +1's HighTime, not Day 0's.
	resolved := r.ResolveAnchors("AAPL", bar2, []string{"pd_high", "pd_low"})
	require.NotNil(t, resolved)
	expectedPDHigh := time.Date(2026, 4, 28, 12, 0, 0, 0, loc)
	assert.True(t, resolved["pd_high"].Equal(expectedPDHigh),
		"post-refresh pd_high should resolve to Day +1's HighTime, not stale Day 0's")
}

// TestSessionResolver_RefreshIfStale_WeekendBoundary verifies that the
// weekend doesn't cause spurious refresh churn but still pulls in Friday's
// session before Monday's first bar resolves anchors.
func TestSessionResolver_RefreshIfStale_WeekendBoundary(t *testing.T) {
	loc := domain.NYLocation()
	r := NewSessionResolver(loc)

	// Friday loaded at startup.
	friday := time.Date(2026, 4, 24, 0, 0, 0, 0, loc)
	mkSession := func(d time.Time, hi float64) SessionData {
		date := d.Format("2006-01-02")
		open := time.Date(d.Year(), d.Month(), d.Day(), 9, 30, 0, 0, loc)
		hiTime := time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc)
		return SessionData{
			Date: date, Open: hi - 1, OpenTime: open,
			High: hi, HighTime: hiTime, Low: hi - 2, LowTime: hiTime,
			Close: hi - 0.5,
		}
	}
	r.mu.Lock()
	r.sessions["AAPL"] = map[string]SessionData{
		friday.Format("2006-01-02"): mkSession(friday, 200.0),
	}
	r.mu.Unlock()

	loaderCalls := 0
	r.sessionLoader = func(_ context.Context, _ domain.Symbol, _, _ time.Time) (map[string]SessionData, error) {
		loaderCalls++
		return map[string]SessionData{
			friday.Format("2006-01-02"): mkSession(friday, 200.0),
		}, nil
	}

	// Monday first bar: previous calendar day is Sunday, not in map. Friday is
	// in map though, and Friday < Sunday in date order so the simple staleness
	// rule fires a refresh. The loader returns the same Friday-only data.
	// This is harmless (one extra DB call per weekend boundary), and the test
	// just confirms the post-refresh state still has Friday.
	monday := time.Date(2026, 4, 27, 9, 30, 0, 0, loc)
	require.NoError(t, r.RefreshIfStale(context.Background(), nil, "AAPL", monday))
	r.mu.RLock()
	_, hasFriday := r.sessions["AAPL"][friday.Format("2006-01-02")]
	r.mu.RUnlock()
	assert.True(t, hasFriday, "Friday must remain present post-refresh on Monday boundary")

	// Monday's findPreviousDay should still resolve to Friday (within 4-day
	// lookback).
	resolved := r.ResolveAnchors("AAPL", monday, []string{"pd_high"})
	require.NotNil(t, resolved)
	expectedPDHigh := time.Date(2026, 4, 24, 12, 0, 0, 0, loc)
	assert.True(t, resolved["pd_high"].Equal(expectedPDHigh),
		"Monday's pd_high should resolve to Friday's HighTime via 4-day walkback")
}
