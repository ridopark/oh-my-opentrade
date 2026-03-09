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
	for i := 0; i < 5; i++ {
		ft.cb.Record(ErrFatal)
	}
	snap := ft.Snapshot()
	assert.Equal(t, "open", snap.CircuitState)
}

func TestIsPipelineDeadlocked(t *testing.T) {
	tests := []struct {
		name       string
		networkAge time.Duration
		pipeAge    time.Duration
		want       bool
	}{
		{"both_healthy", 2 * time.Second, 5 * time.Second, false},
		{"network_healthy_pipeline_stale", 3 * time.Second, 45 * time.Second, true},
		{"network_stale_pipeline_stale", 2 * time.Minute, 2 * time.Minute, false},
		{"network_borderline_pipeline_stale", 10 * time.Second, 45 * time.Second, false},
		{"network_healthy_pipeline_borderline", 3 * time.Second, 30 * time.Second, false},
		{"network_healthy_pipeline_just_over", 3 * time.Second, 31 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsPipelineDeadlocked(tt.networkAge, tt.pipeAge)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFeedHealth_PipelineFields_Default(t *testing.T) {
	ft := newFeedTracker()
	snap := ft.Snapshot()
	assert.Equal(t, time.Duration(0), snap.PipelineLastBarAge)
	assert.False(t, snap.PipelineHealthy)
}
