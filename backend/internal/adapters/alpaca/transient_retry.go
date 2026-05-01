package alpaca

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/rs/zerolog"
)

// transientRetryBackoffs is the per-retry wait schedule for retryTransient.
// Total worst case ~1.3s plus per-call timeout: well under the strategy
// pending-entry budget (5min) and the typical 10s HTTP timeout.
var transientRetryBackoffs = []time.Duration{
	100 * time.Millisecond,
	300 * time.Millisecond,
	900 * time.Millisecond,
}

// isTransientError reports whether err looks like a transient network failure
// worth retrying. Context-canceled is explicitly excluded: the caller gave up.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// isTransientStatus reports whether an HTTP response status code is the kind
// that often clears on retry: bad gateway, service unavailable, gateway
// timeout. 4xx is excluded; no retry helps a malformed request.
func isTransientStatus(code int) bool {
	return code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// retryTransient invokes fn up to len(transientRetryBackoffs)+1 attempts.
// On a transient network error or transient HTTP status (502/503/504) it
// sleeps the configured backoff with +/-10% jitter and retries. Non-transient
// outcomes return immediately. ctx cancellation aborts the wait.
//
// Callers receive the final attempt's response/error. retryTransient drains
// and closes the body of any retried response so the socket is released.
func retryTransient(
	ctx context.Context,
	log zerolog.Logger,
	op string,
	fn func() (*http.Response, error),
) (*http.Response, error) {
	maxAttempts := len(transientRetryBackoffs) + 1

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := fn()

		switch {
		case err == nil && (resp == nil || !isTransientStatus(resp.StatusCode)):
			return resp, nil
		case attempt == maxAttempts:
			return resp, err
		case err != nil && !isTransientError(err):
			return resp, err
		}

		if resp != nil && resp.Body != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		wait := transientRetryBackoffs[attempt-1]
		jitter := time.Duration((randUnitFloat64()*2 - 1) * 0.10 * float64(wait))
		sleep := wait + jitter
		if sleep < 0 {
			sleep = wait
		}

		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		evt := log.Info().
			Str("event", "transient_retry").
			Str("op", op).
			Int("attempt", attempt).
			Int("max_attempts", maxAttempts).
			Dur("wait", sleep).
			Int("status", statusCode)
		if err != nil {
			evt = evt.AnErr("last_err", err)
		}
		evt.Msg("alpaca: transient failure - retrying")

		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			if !t.Stop() {
				<-t.C
			}
			return nil, ctx.Err()
		case <-t.C:
		}
	}

	return nil, nil
}
