package alpaca

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// feedTracker tests
// ---------------------------------------------------------------------------

func TestFeedTracker_InitialState(t *testing.T) {
	ft := newFeedTracker()
	snap := ft.Snapshot()
	assert.False(t, snap.Connected)
	assert.Equal(t, "stopped", snap.State)
	assert.Equal(t, 0, snap.ReconnectCount)
	assert.Equal(t, 0, snap.StaleResets)
	assert.Equal(t, 0, snap.GhostWindowCount)
	assert.Equal(t, "closed", snap.CircuitState)
	assert.Empty(t, snap.LastError)
}

func TestFeedTracker_SetConnected(t *testing.T) {
	ft := newFeedTracker()
	ft.setConnected(true)
	assert.True(t, ft.Snapshot().Connected)
	ft.setConnected(false)
	assert.False(t, ft.Snapshot().Connected)
}

func TestFeedTracker_SetState(t *testing.T) {
	ft := newFeedTracker()
	ft.setState("streaming")
	assert.Equal(t, "streaming", ft.Snapshot().State)
	ft.setState("ghost_probe")
	assert.Equal(t, "ghost_probe", ft.Snapshot().State)
}

func TestFeedTracker_RecordBar(t *testing.T) {
	ft := newFeedTracker()
	snap := ft.Snapshot()
	// Before any bar, LastBarAge is 0 (lastBarAt is zero).
	assert.Equal(t, time.Duration(0), snap.LastBarAge)

	ft.recordBar()
	snap = ft.Snapshot()
	// After recording a bar, age should be very small.
	assert.Less(t, snap.LastBarAge, 1*time.Second)
}

func TestFeedTracker_RecordError(t *testing.T) {
	ft := newFeedTracker()
	ft.recordError(nil) // nil should not crash
	assert.Empty(t, ft.Snapshot().LastError)

	ft.recordError(assert.AnError)
	assert.Contains(t, ft.Snapshot().LastError, "assert.AnError")
}

func TestFeedTracker_Counters(t *testing.T) {
	ft := newFeedTracker()

	ft.incReconnect()
	ft.incReconnect()
	assert.Equal(t, 2, ft.Snapshot().ReconnectCount)

	ft.incStaleReset()
	assert.Equal(t, 1, ft.Snapshot().StaleResets)

	ft.incGhostWindow()
	ft.incGhostWindow()
	ft.incGhostWindow()
	assert.Equal(t, 3, ft.Snapshot().GhostWindowCount)
}

// ---------------------------------------------------------------------------
// FeedHealth.IsHealthy tests
// ---------------------------------------------------------------------------

// Note: IsHealthy depends on isCoreMarketHours() which checks real wall clock.
// We test the invariant properties instead:
//   - Off-hours → always healthy (regardless of connected/age)
//   - RTH + connected + fresh bar → healthy
//   - RTH + disconnected → unhealthy
//   - RTH + connected + stale bar → unhealthy

func TestFeedHealth_IsHealthy_OffHours_AlwaysTrue(t *testing.T) {
	if isCoreMarketHours() {
		t.Skip("skipping off-hours test during RTH")
	}
	fh := FeedHealth{
		Connected:  false,
		State:      "stopped",
		LastBarAge: 999 * time.Hour,
	}
	assert.True(t, fh.IsHealthy(), "off-hours should always be healthy")
}

func TestFeedHealth_IsHealthy_RTH_Connected_Fresh(t *testing.T) {
	if !isCoreMarketHours() {
		t.Skip("skipping RTH test outside market hours")
	}
	fh := FeedHealth{
		Connected:  true,
		State:      "streaming",
		LastBarAge: 10 * time.Second,
	}
	assert.True(t, fh.IsHealthy())
}

func TestFeedHealth_IsHealthy_RTH_Disconnected(t *testing.T) {
	if !isCoreMarketHours() {
		t.Skip("skipping RTH test outside market hours")
	}
	fh := FeedHealth{
		Connected:  false,
		State:      "reconnecting",
		LastBarAge: 5 * time.Second,
	}
	assert.False(t, fh.IsHealthy())
}

func TestFeedHealth_IsHealthy_RTH_Stale(t *testing.T) {
	if !isCoreMarketHours() {
		t.Skip("skipping RTH test outside market hours")
	}
	fh := FeedHealth{
		Connected:  true,
		State:      "streaming",
		LastBarAge: 120 * time.Second, // exceeds 90s threshold
	}
	assert.False(t, fh.IsHealthy())
}

func TestFeedTracker_Snapshot_CircuitStateReflected(t *testing.T) {
	ft := newFeedTracker()
	// Trip the circuit breaker.
	for i := 0; i < 5; i++ {
		ft.cb.Record(ErrFatal)
	}
	snap := ft.Snapshot()
	assert.Equal(t, "open", snap.CircuitState)
}
