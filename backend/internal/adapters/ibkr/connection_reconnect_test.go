package ibkr

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeNotifier captures NotifySync messages for assertion in tests.
type fakeNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeNotifier) NotifySync(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
}

func (f *fakeNotifier) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.msgs))
	copy(out, f.msgs)
	return out
}

func newTestConn() *connection {
	return &connection{log: zerolog.Nop()}
}

// TestReconnectEscalation_FiresOnceAtWarningThreshold verifies that the
// Discord warning alert is emitted EXACTLY at reconnectEscalateAfter and not
// on any surrounding attempt. A repeated alert every tick would spam the
// on-call channel during an extended outage.
func TestReconnectEscalation_FiresOnceAtWarningThreshold(t *testing.T) {
	c := newTestConn()
	n := &fakeNotifier{}
	c.SetReconnectNotifier(n)

	dummyErr := errors.New("connection refused")

	// Simulate every attempt from 1 through reconnectFatalAfter-1 failing.
	// Only ONE message should be emitted, at reconnectEscalateAfter.
	for i := int64(1); i < reconnectFatalAfter; i++ {
		c.onReconnectFailure(i, dummyErr)
	}

	msgs := n.snapshot()
	require.Len(t, msgs, 1, "warning must fire exactly once across the entire pre-fatal window")
	assert.Contains(t, msgs[0], "IBKR disconnected")
	assert.Contains(t, msgs[0], "6 consecutive reconnect failures")
}

// TestReconnectEscalation_FiresFatalAtFatalThreshold verifies both the fatal
// Discord alert and the fatal-halt callback fire EXACTLY when the attempt
// counter hits reconnectFatalAfter.
func TestReconnectEscalation_FiresFatalAtFatalThreshold(t *testing.T) {
	c := newTestConn()
	n := &fakeNotifier{}
	c.SetReconnectNotifier(n)

	var haltMu sync.Mutex
	var haltReasons []string
	c.SetFatalHalt(func(reason string) {
		haltMu.Lock()
		defer haltMu.Unlock()
		haltReasons = append(haltReasons, reason)
	})

	dummyErr := errors.New("gateway unreachable")

	// Burn through every attempt up to and including the fatal threshold.
	for i := int64(1); i <= reconnectFatalAfter; i++ {
		c.onReconnectFailure(i, dummyErr)
	}

	// Extra post-fatal attempts must NOT re-fire the halt or the alerts —
	// otherwise the operator would receive an alert every 60s until recovery.
	for i := reconnectFatalAfter + 1; i <= reconnectFatalAfter+5; i++ {
		c.onReconnectFailure(i, dummyErr)
	}

	msgs := n.snapshot()
	// Expect exactly 2 messages total: one warning, one fatal.
	require.Len(t, msgs, 2)
	assert.True(t, strings.Contains(msgs[0], "IBKR disconnected"))
	assert.True(t, strings.Contains(msgs[1], "activating kill switch"))

	haltMu.Lock()
	defer haltMu.Unlock()
	require.Len(t, haltReasons, 1, "fatal halt must be called exactly once")
	assert.Contains(t, haltReasons[0], "ibkr reconnect exhausted")
}

// TestReconnectEscalation_CounterResetsOnSuccess verifies that a successful
// reconnect (a) zeros the counter so the next outage starts from scratch,
// and (b) emits a recovery notification only if the counter was non-zero.
func TestReconnectEscalation_CounterResetsOnSuccess(t *testing.T) {
	c := newTestConn()
	n := &fakeNotifier{}
	c.SetReconnectNotifier(n)

	// Prime the counter as if three attempts had already failed.
	c.reconnectAttempts.Store(3)

	c.onReconnectSuccess()

	assert.Equal(t, int64(0), c.reconnectAttempts.Load(), "counter must reset on success")
	msgs := n.snapshot()
	require.Len(t, msgs, 1, "recovery notification must fire after a recovered outage")
	assert.Contains(t, msgs[0], "IBKR reconnected after 3 attempts")
}

// TestReconnectEscalation_NoRecoveryNoticeOnCleanReconnect verifies we stay
// silent when the counter was already zero — e.g. the very first connect at
// process start, where there is no "outage" to recover from.
func TestReconnectEscalation_NoRecoveryNoticeOnCleanReconnect(t *testing.T) {
	c := newTestConn()
	n := &fakeNotifier{}
	c.SetReconnectNotifier(n)

	c.onReconnectSuccess()

	assert.Empty(t, n.snapshot(), "no notification when attempts=0")
}

// TestReconnectEscalation_NilCallbacksSafe ensures the escalation path is
// safe when neither a notifier nor a fatalHalt has been installed. This is
// the legacy behavior that must continue to work for tests and embedded
// adapters that never wire up a notify service.
func TestReconnectEscalation_NilCallbacksSafe(t *testing.T) {
	c := newTestConn()

	require.NotPanics(t, func() {
		for i := int64(1); i <= reconnectFatalAfter+2; i++ {
			c.onReconnectFailure(i, errors.New("boom"))
		}
		c.onReconnectSuccess()
	})
}
