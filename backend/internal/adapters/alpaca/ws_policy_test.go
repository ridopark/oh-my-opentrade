package alpaca

import (
	"errors"
	"fmt"
	"testing"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// BackoffPolicy tests
// ---------------------------------------------------------------------------

func TestBackoff_FirstAttempt_BoundedByInitial(t *testing.T) {
	p := BackoffPolicy{Initial: 500 * time.Millisecond, Multiplier: 1.8, Max: 15 * time.Second}
	for i := 0; i < 100; i++ {
		d := p.backoff(0)
		assert.GreaterOrEqual(t, d, time.Duration(0), "backoff should be non-negative")
		assert.Less(t, d, p.Initial, "first attempt full-jitter should be < Initial")
	}
}

func TestBackoff_GrowsExponentially(t *testing.T) {
	p := BackoffPolicy{Initial: 500 * time.Millisecond, Multiplier: 2.0, Max: 1 * time.Minute}

	// At attempt 3, base = 500ms * 2^3 = 4s. Full jitter ∈ [0, 4s).
	// Verify the average is roughly in the right ballpark.
	var total time.Duration
	const n = 1000
	for i := 0; i < n; i++ {
		total += p.backoff(3)
	}
	avg := total / time.Duration(n)
	// base = 4s, full jitter mean ≈ 2s. Allow wide range: 1s–3s.
	assert.Greater(t, avg, 500*time.Millisecond, "average should reflect exponential growth")
	assert.Less(t, avg, 4*time.Second, "average should be less than base (full jitter)")
}

func TestBackoff_CapsAtMax(t *testing.T) {
	p := BackoffPolicy{Initial: 500 * time.Millisecond, Multiplier: 2.0, Max: 2 * time.Second}
	for i := 0; i < 100; i++ {
		d := p.backoff(100) // very high attempt — should be capped
		assert.Less(t, d, p.Max, "backoff should never exceed Max")
		assert.GreaterOrEqual(t, d, time.Duration(0))
	}
}

func TestRTHPolicy_Bounds(t *testing.T) {
	for i := 0; i < 100; i++ {
		d := rthPolicy.backoff(i)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		assert.Less(t, d, rthPolicy.Max)
	}
}

func TestOffHoursPolicy_Bounds(t *testing.T) {
	for i := 0; i < 100; i++ {
		d := offHoursPolicy.backoff(i)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		assert.Less(t, d, offHoursPolicy.Max)
	}
}

// ---------------------------------------------------------------------------
// classifyError tests
// ---------------------------------------------------------------------------

func TestClassifyError_Nil(t *testing.T) {
	assert.Equal(t, ErrTransient, classifyError(nil))
}

func TestClassifyError_Ghost(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"string: connection limit exceeded", errors.New("connection limit exceeded")},
		{"string: 406 in message", errors.New("406 connection limit exceeded")},
		{"string: upstream 406", errors.New("upstream returned 406")},
		{"sdk: ErrConnectionLimitExceeded", alpacastream.ErrConnectionLimitExceeded},
		{"sdk: wrapped ErrConnectionLimitExceeded", fmt.Errorf("max reconnect limit has been reached, last error: %w", alpacastream.ErrConnectionLimitExceeded)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, ErrGhost, classifyError(tc.err))
		})
	}
}

func TestClassifyError_MaxReconnectLimit_WithoutSDKError_IsTransient(t *testing.T) {
	err := errors.New("max reconnect limit has been reached, last error: auth timeout")
	assert.Equal(t, ErrTransient, classifyError(err),
		"'max reconnect limit' without SDK ErrConnectionLimitExceeded should be transient, not ghost")
}

func TestClassifyError_Fatal(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"string: authentication failed", errors.New("authentication failed")},
		{"string: Authentication Failed", errors.New("Authentication Failed")},
		{"string: forbidden", errors.New("forbidden")},
		{"string: 401 unauthorized", errors.New("401 unauthorized")},
		{"string: 403 access denied", errors.New("403 access denied")},
		{"string: invalid api key", errors.New("invalid api key")},
		{"string: not authorized", errors.New("not authorized to access this resource")},
		{"sdk: ErrInvalidCredentials", alpacastream.ErrInvalidCredentials},
		{"sdk: ErrInsufficientSubscription", alpacastream.ErrInsufficientSubscription},
		{"sdk: ErrInsufficientScope", alpacastream.ErrInsufficientScope},
		{"sdk: wrapped ErrInvalidCredentials", fmt.Errorf("max reconnect limit: %w", alpacastream.ErrInvalidCredentials)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, ErrFatal, classifyError(tc.err))
		})
	}
}

func TestClassifyError_Transient(t *testing.T) {
	tests := []struct {
		msg string
	}{
		{"connection reset by peer"},
		{"timeout exceeded"},
		{"EOF"},
		{"i/o timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.msg, func(t *testing.T) {
			assert.Equal(t, ErrTransient, classifyError(errors.New(tc.msg)))
		})
	}
}

// ---------------------------------------------------------------------------
// CircuitBreaker tests
// ---------------------------------------------------------------------------

func TestCircuitBreaker_StartsClosedAndAllows(t *testing.T) {
	cb := NewCircuitBreaker()
	ok, wait := cb.Allow()
	assert.True(t, ok)
	assert.Equal(t, time.Duration(0), wait)
	assert.Equal(t, CircuitClosed, cb.State())
}

func TestCircuitBreaker_TripsAfterConsecutiveFatals(t *testing.T) {
	cb := NewCircuitBreaker()
	// Default threshold = 5
	for i := 0; i < 4; i++ {
		cb.Record(ErrFatal)
		assert.Equal(t, CircuitClosed, cb.State(), "should still be closed after %d fatals", i+1)
	}
	// 5th fatal trips it
	state := cb.Record(ErrFatal)
	assert.Equal(t, CircuitOpen, state)
	assert.Equal(t, CircuitOpen, cb.State())
}

func TestCircuitBreaker_OpenBlocksAttempts(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < 5; i++ {
		cb.Record(ErrFatal)
	}
	require.Equal(t, CircuitOpen, cb.State())

	ok, wait := cb.Allow()
	assert.False(t, ok, "circuit open should block")
	assert.Greater(t, wait, time.Duration(0), "wait should be positive")
}

func TestCircuitBreaker_TransitionsToHalfOpen(t *testing.T) {
	cb := &CircuitBreaker{
		hardFailThreshold: 2,
		openDurationRTH:   1 * time.Millisecond,
		openDurationOff:   1 * time.Millisecond,
	}
	cb.Record(ErrFatal)
	cb.Record(ErrFatal)
	require.Equal(t, CircuitOpen, cb.State())

	// Wait for open duration to expire.
	time.Sleep(5 * time.Millisecond)

	ok, wait := cb.Allow()
	assert.True(t, ok, "should allow after open duration expires")
	assert.Equal(t, time.Duration(0), wait)
	assert.Equal(t, CircuitHalfOpen, cb.State())
}

func TestCircuitBreaker_ResetsOnNonFatal(t *testing.T) {
	cb := NewCircuitBreaker()
	// Build up failures.
	for i := 0; i < 3; i++ {
		cb.Record(ErrFatal)
	}
	assert.Equal(t, 3, cb.ConsecutiveFails())

	// One non-fatal resets everything.
	state := cb.Record(ErrTransient)
	assert.Equal(t, CircuitClosed, state)
	assert.Equal(t, 0, cb.ConsecutiveFails())
}

func TestCircuitBreaker_GhostResetsCounter(t *testing.T) {
	cb := NewCircuitBreaker()
	cb.Record(ErrFatal)
	cb.Record(ErrFatal)
	assert.Equal(t, 2, cb.ConsecutiveFails())

	// Ghost is non-fatal, so it resets.
	cb.Record(ErrGhost)
	assert.Equal(t, 0, cb.ConsecutiveFails())
	assert.Equal(t, CircuitClosed, cb.State())
}

func TestCalculateCryptoBackoff_RespectsMinFloor(t *testing.T) {
	logger := zerolog.Nop()
	for i := 0; i < 20; i++ {
		wait, _ := CalculateCryptoBackoff(ErrTransient, errors.New("reset"), time.Now(), i, i+1, logger)
		assert.GreaterOrEqual(t, wait, minCryptoBackoff,
			"crypto backoff at attempt %d should be >= %v", i, minCryptoBackoff)
	}
}

func TestCircuitState_String(t *testing.T) {
	assert.Equal(t, "closed", CircuitClosed.String())
	assert.Equal(t, "open", CircuitOpen.String())
	assert.Equal(t, "half_open", CircuitHalfOpen.String())
	assert.Equal(t, "unknown", CircuitState(99).String())
}
