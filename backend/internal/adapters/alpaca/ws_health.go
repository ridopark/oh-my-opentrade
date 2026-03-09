package alpaca

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Feed health snapshot
// ---------------------------------------------------------------------------

// FeedHealth is a point-in-time snapshot of WebSocket feed status.
// Exported so main.go can build a HealthChecker from it.
type FeedHealth struct {
	Connected        bool          `json:"connected"`
	State            string        `json:"state"`           // "streaming", "reconnecting", "ghost_probe", "circuit_open", "stopped"
	LastBarAge       time.Duration `json:"last_bar_age_ms"` // since last bar
	LastError        string        `json:"last_error,omitempty"`
	ReconnectCount   int           `json:"reconnect_count"` // total reconnects this session
	CircuitState     string        `json:"circuit_state"`   // "closed", "open", "half_open"
	StaleResets      int           `json:"stale_resets"`    // times watchdog forced reconnect
	GhostWindowCount int           `json:"ghost_window_count"`

	PipelineLastBarAge time.Duration `json:"pipeline_last_bar_age_ms"`
	PipelineHealthy    bool          `json:"pipeline_healthy"`
}

// IsHealthy returns true if the feed is usable.
// During RTH: must be connected and not stale.
// Off-hours: always healthy (feed is expected to be idle).
func (fh FeedHealth) IsHealthy() bool {
	if !isCoreMarketHours() {
		return true
	}
	// During RTH: must be connected + streaming + not stale.
	if !fh.Connected {
		return false
	}
	// 90s = 1.5× bar interval (1m bars); generous for IEX.
	const staleThresholdRTH = 90 * time.Second
	return fh.LastBarAge < staleThresholdRTH
}

// feedTracker tracks mutable health state inside WSClient.
// All fields are protected by mu.
type feedTracker struct {
	mu               sync.Mutex
	connected        bool
	state            string
	lastBarAt        time.Time
	lastErr          string
	reconnectCount   int
	staleResets      int
	ghostWindowCount int
	staleAlertSent   bool
	cb               *CircuitBreaker
}

func newFeedTracker() *feedTracker {
	return &feedTracker{
		state: "stopped",
		cb:    NewCircuitBreaker(),
	}
}

func (ft *feedTracker) setConnected(v bool) {
	ft.mu.Lock()
	ft.connected = v
	ft.mu.Unlock()
}

func (ft *feedTracker) setState(s string) {
	ft.mu.Lock()
	ft.state = s
	ft.mu.Unlock()
}

func (ft *feedTracker) recordBar() {
	ft.mu.Lock()
	ft.lastBarAt = time.Now()
	ft.mu.Unlock()
}

func (ft *feedTracker) resetBarTimer() {
	ft.mu.Lock()
	ft.lastBarAt = time.Now()
	ft.mu.Unlock()
}

func (ft *feedTracker) recordError(err error) {
	ft.mu.Lock()
	if err != nil {
		ft.lastErr = err.Error()
	}
	ft.mu.Unlock()
}

func (ft *feedTracker) incReconnect() {
	ft.mu.Lock()
	ft.reconnectCount++
	ft.mu.Unlock()
}

func (ft *feedTracker) incStaleReset() {
	ft.mu.Lock()
	ft.staleResets++
	ft.mu.Unlock()
}

func (ft *feedTracker) incGhostWindow() {
	ft.mu.Lock()
	ft.ghostWindowCount++
	ft.mu.Unlock()
}

func (ft *feedTracker) tryMarkStaleAlert() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.staleAlertSent {
		return false
	}
	ft.staleAlertSent = true
	return true
}

func (ft *feedTracker) clearStaleAlert() {
	ft.mu.Lock()
	ft.staleAlertSent = false
	ft.mu.Unlock()
}

// Snapshot returns a point-in-time FeedHealth.
func (ft *feedTracker) Snapshot() FeedHealth {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var barAge time.Duration
	if !ft.lastBarAt.IsZero() {
		barAge = time.Since(ft.lastBarAt)
	}
	return FeedHealth{
		Connected:        ft.connected,
		State:            ft.state,
		LastBarAge:       barAge,
		LastError:        ft.lastErr,
		ReconnectCount:   ft.reconnectCount,
		CircuitState:     ft.cb.State().String(),
		StaleResets:      ft.staleResets,
		GhostWindowCount: ft.ghostWindowCount,
	}
}
