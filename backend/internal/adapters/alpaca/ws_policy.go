package alpaca

import (
	"errors"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
)

// ---------------------------------------------------------------------------
// Reconnect policy: exponential backoff + jitter, RTH vs off-hours
// ---------------------------------------------------------------------------

// BackoffPolicy defines backoff parameters for reconnect attempts.
type BackoffPolicy struct {
	Initial    time.Duration // first backoff sleep
	Multiplier float64       // growth factor per attempt
	Max        time.Duration // ceiling
}

// rthPolicy is aggressive — reconnect fast during market hours.
var rthPolicy = BackoffPolicy{
	Initial:    500 * time.Millisecond,
	Multiplier: 1.8,
	Max:        15 * time.Second,
}

// offHoursPolicy is relaxed — conserve resources outside RTH.
var offHoursPolicy = BackoffPolicy{
	Initial:    5 * time.Second,
	Multiplier: 1.8,
	Max:        2 * time.Minute,
}

// selectPolicy returns the appropriate backoff policy for the current time.
func selectPolicy() BackoffPolicy {
	if isCoreMarketHours() {
		return rthPolicy
	}
	return offHoursPolicy
}

// backoff returns a sleep duration with full jitter: rand(0, base)
// where base = min(max, initial * multiplier^attempt).
func (p BackoffPolicy) backoff(attempt int) time.Duration {
	base := float64(p.Initial) * math.Pow(p.Multiplier, float64(attempt))
	if base > float64(p.Max) {
		base = float64(p.Max)
	}
	// Full jitter: uniform random in [0, base)
	return time.Duration(rand.Float64() * base) //nolint:gosec
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

// ErrorClass categorizes a connect/stream error.
type ErrorClass int

const (
	// ErrTransient is a retryable network/server error.
	ErrTransient ErrorClass = iota
	// ErrGhost is a 406 / connection-limit / ghost-session error.
	ErrGhost
	// ErrFatal is a non-recoverable error (auth, permission, invalid request).
	ErrFatal
)

// classifyError inspects an error and returns its class.
// It first checks SDK typed errors (via errors.Is) for reliable classification,
// then falls back to string matching for non-SDK errors.
// connErr may be nil (clean close) — caller handles nil separately.
func classifyError(connErr error) ErrorClass {
	if connErr == nil {
		return ErrTransient
	}

	// Prefer SDK typed errors — these survive fmt.Errorf("%w") wrapping.
	if errors.Is(connErr, alpacastream.ErrConnectionLimitExceeded) {
		return ErrGhost
	}
	if errors.Is(connErr, alpacastream.ErrInvalidCredentials) ||
		errors.Is(connErr, alpacastream.ErrInsufficientSubscription) ||
		errors.Is(connErr, alpacastream.ErrInsufficientScope) {
		return ErrFatal
	}

	// Fall back to string matching for errors not wrapped with SDK types
	// (e.g., connection failures, timeouts, EOF).
	msg := connErr.Error()
	lower := strings.ToLower(msg)

	// Ghost-session / connection-limit (string fallback).
	if strings.Contains(msg, "connection limit exceeded") ||
		strings.Contains(msg, "406") {
		return ErrGhost
	}

	// Fatal: auth / permission / invalid.
	if strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "401") ||
		strings.Contains(lower, "403") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "not authorized") {
		return ErrFatal
	}

	return ErrTransient
}

// ---------------------------------------------------------------------------
// Circuit breaker (minimal, per-connection)
// ---------------------------------------------------------------------------

// CircuitState represents the circuit breaker state.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal — allow attempts
	CircuitOpen                         // tripped — block until openUntil
	CircuitHalfOpen                     // trial — allow one attempt
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// CircuitBreaker prevents tight reconnect loops on fatal errors.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            CircuitState
	consecutiveFails int
	openUntil        time.Time

	// Thresholds.
	hardFailThreshold int           // consecutive fatal errors to trip
	openDurationRTH   time.Duration // how long to stay open during RTH
	openDurationOff   time.Duration // how long to stay open off-hours
}

// NewCircuitBreaker creates a CircuitBreaker with sensible defaults.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		hardFailThreshold: 5,
		openDurationRTH:   2 * time.Minute,
		openDurationOff:   10 * time.Minute,
	}
}

// Record records a connect attempt result. Returns the new state.
func (cb *CircuitBreaker) Record(ec ErrorClass) CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if ec == ErrFatal {
		cb.consecutiveFails++
		if cb.consecutiveFails >= cb.hardFailThreshold {
			dur := cb.openDurationOff
			if isCoreMarketHours() {
				dur = cb.openDurationRTH
			}
			cb.state = CircuitOpen
			cb.openUntil = time.Now().Add(dur)
		}
		return cb.state
	}

	// Any non-fatal result resets the counter.
	cb.consecutiveFails = 0
	cb.state = CircuitClosed
	return cb.state
}

// Allow checks whether a reconnect attempt is allowed.
// If the circuit is open, it returns the remaining wait duration.
func (cb *CircuitBreaker) Allow() (bool, time.Duration) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true, 0
	case CircuitHalfOpen:
		return true, 0
	case CircuitOpen:
		remaining := time.Until(cb.openUntil)
		if remaining <= 0 {
			cb.state = CircuitHalfOpen
			return true, 0
		}
		return false, remaining
	}
	return true, 0
}

// State returns the current circuit breaker state (thread-safe).
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ConsecutiveFails returns the current consecutive fatal failure count.
func (cb *CircuitBreaker) ConsecutiveFails() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.consecutiveFails
}
