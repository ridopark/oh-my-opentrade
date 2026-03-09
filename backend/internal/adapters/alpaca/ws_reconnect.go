package alpaca

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// ReconnectConfig holds shared reconnection parameters.
type ReconnectConfig struct {
	MaxConsecutiveFails int
	Logger              zerolog.Logger
}

// ReconnectState tracks the state of a reconnection attempt.
type ReconnectState struct {
	Attempt          int
	ConsecutiveFails int
	ConnectedAt      time.Time
	LastConnErr      error
	LastErrClass     ErrorClass
	WasStaleReset    bool
	ShouldContinue   bool
	WaitDuration     time.Duration
}

// CheckCircuitBreaker checks if the circuit breaker allows a reconnection attempt.
// Returns (allowed, waitDuration).
func CheckCircuitBreaker(tracker *feedTracker, logger zerolog.Logger) (bool, time.Duration) {
	ok, wait := tracker.cb.Allow()
	if !ok {
		tracker.setState("circuit_open")
		logger.Warn().Dur("wait", wait).Msg("circuit breaker open — waiting before retry")
	}
	return ok, wait
}

// CheckMaxConsecutiveFails checks if we've exceeded the max consecutive failures limit.
func CheckMaxConsecutiveFails(consecutiveFails int) bool {
	return consecutiveFails >= maxConsecutiveFailsBeforeError
}

// IncrementAttempt increments the attempt counter and logs the reconnection.
// Returns true if this is a reconnection attempt (attempt > 1).
func IncrementAttempt(attempt *int, consecutiveFails int, symbols []string, logger zerolog.Logger) bool {
	*attempt++
	if *attempt > 1 {
		logger.Info().
			Int("attempt", *attempt).
			Int("consecutive_fails", consecutiveFails).
			Strs("symbols", symbols).
			Msg("reconnecting to Alpaca WebSocket stream")
		return true
	}
	return false
}

// HandleStaleReset processes a stale feed watchdog-triggered reconnect.
// Returns true if this was a stale reset.
func HandleStaleReset(connCtx, streamCtx context.Context, connErr error, tracker *feedTracker, logger zerolog.Logger) bool {
	wasStaleReset := connCtx.Err() != nil && streamCtx.Err() == nil && connErr == nil
	if wasStaleReset {
		tracker.incStaleReset()
		logger.Warn().Msg("stale feed watchdog triggered reconnect")
	}
	return wasStaleReset
}

// ClassifyAndRecordError classifies the error and records it in the circuit breaker.
// Returns the error class.
func ClassifyAndRecordError(connErr error, tracker *feedTracker) ErrorClass {
	tracker.recordError(connErr)
	errClass := classifyError(connErr)
	tracker.cb.Record(errClass)
	return errClass
}

// CalculateBackoff determines the wait duration before the next reconnection attempt.
// For stale resets, uses aggressive backoff (ErrTransient).
// For normal transient errors, uses policy-based backoff.
// Returns (waitDuration, shouldContinue).
func CalculateBackoff(
	errClass ErrorClass,
	wasStaleReset bool,
	connErr error,
	connectedAt time.Time,
	consecutiveFails int,
	attempt int,
	logger zerolog.Logger,
) (time.Duration, bool) {
	// Stale reset: use aggressive backoff
	if wasStaleReset {
		errClass = ErrTransient
	}

	// Fatal error: circuit breaker handles backoff
	if errClass == ErrFatal {
		logger.Error().Err(connErr).Int("attempt", attempt).
			Msg("Alpaca stream fatal error (auth/permission) — circuit breaker will gate retries")
		return 0, false // Don't wait here; circuit breaker will handle it
	}

	// Normal transient error: policy-based backoff
	policy := selectPolicy()
	wait := policy.backoff(consecutiveFails - 1)
	if connErr != nil {
		logger.Error().Err(connErr).Int("attempt", attempt).Dur("retry_in", wait).
			Msg("Alpaca stream disconnected with error, reconnecting")
	} else {
		logger.Warn().Int("attempt", attempt).Dur("retry_in", wait).
			Msg("Alpaca stream clean close during core market hours, reconnecting")
	}
	return wait, true
}

// CalculateCryptoBackoff determines the wait duration for crypto reconnection.
// Simpler than equity: always uses policy-based backoff, resets counter after 30s.
// Returns (waitDuration, resetCounter).
func CalculateCryptoBackoff(
	errClass ErrorClass,
	connErr error,
	connectedAt time.Time,
	consecutiveFails int,
	attempt int,
	logger zerolog.Logger,
) (time.Duration, bool) {
	// Fatal error: circuit breaker handles backoff
	if errClass == ErrFatal {
		logger.Error().Err(connErr).Int("attempt", attempt).
			Msg("crypto stream fatal error — circuit breaker will gate retries")
		return 0, false // Don't wait here; circuit breaker will handle it
	}

	// Check if we've been connected long enough to reset the counter
	resetCounter := time.Since(connectedAt) > 30*time.Second

	policy := selectPolicy()
	wait := policy.backoff(consecutiveFails)
	logger.Warn().Err(connErr).Int("attempt", attempt).Dur("retry_in", wait).
		Msg("crypto stream disconnected, reconnecting")

	return wait, resetCounter
}

// WaitForRetry waits for the specified duration or until context is cancelled.
// Returns true if context was cancelled (shutdown), false if wait completed.
func WaitForRetry(ctx context.Context, wait time.Duration) bool {
	select {
	case <-time.After(wait):
		return false // wait completed
	case <-ctx.Done():
		return true // context cancelled
	}
}

// LogReconnectionFailure logs a reconnection failure with context.
func LogReconnectionFailure(attempt int, consecutiveFails int, logger zerolog.Logger) {
	logger.Warn().
		Int("attempt", attempt).
		Int("consecutive_fails", consecutiveFails).
		Msg("reconnection attempt failed")
}

// LogReconnectionSuccess logs a successful reconnection.
func LogReconnectionSuccess(attempt int, logger zerolog.Logger) {
	logger.Info().
		Int("attempt", attempt).
		Msg("successfully reconnected to Alpaca WebSocket stream")
}

// LogMaxFailuresExceeded logs when max consecutive failures is exceeded.
func LogMaxFailuresExceeded(consecutiveFails int, logger zerolog.Logger) {
	logger.Error().
		Int("consecutive_fails", consecutiveFails).
		Msg("max consecutive connection failures exceeded — giving up")
}
