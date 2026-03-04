// Package middleware provides HTTP middleware for the omo-core HTTP server.
package middleware

import (
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/logger"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
// It also forwards http.Flusher so long-lived connections (SSE) continue to
// work through the middleware chain.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher by delegating to the underlying writer if it
// supports flushing.  This is required for SSE / streaming responses.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter so that type assertions
// propagate correctly through multiple wrapper layers.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// AccessLog returns an HTTP middleware that logs each request's method, path,
// status code, and latency as a structured zerolog event.
//
// It stores a request-scoped logger (enriched with method + path) in the
// request context so downstream handlers can call logger.FromCtx(r.Context())
// and add fields to the same log context.
//
// SSE connections (long-lived streams) are logged at connect time with
// status=0 and latency=N/A — the disconnect is tracked when ServeHTTP returns.
func AccessLog(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Build a request-scoped child logger and store in context.
			reqLog := log.With().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("remote", r.RemoteAddr).
				Logger()

			ctx := logger.WithCtx(r.Context(), reqLog)
			r = r.WithContext(ctx)

			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			latency := time.Since(start)

			evt := reqLog.Info()
			if rw.status >= 500 {
				evt = reqLog.Error()
			} else if rw.status >= 400 {
				evt = reqLog.Warn()
			}

			evt.
				Int("status", rw.status).
				Dur("latency", latency).
				Msg("http request")
		})
	}
}
