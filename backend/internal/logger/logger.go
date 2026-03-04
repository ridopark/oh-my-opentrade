// Package logger provides a thin zerolog wrapper for the omo-core service.
//
// Usage:
//
//	// At startup in main:
//	log := logger.New(logger.Config{Level: zerolog.InfoLevel, Pretty: false})
//
//	// Pass to components:
//	svc := mypackage.NewService(log.With().Str("component", "mypackage").Logger())
//
//	// In components, extract from context (for request-scoped correlation):
//	l := logger.FromCtx(ctx)
//	l.Info().Str("symbol", sym).Msg("setup detected")
package logger

import (
	"context"
	"os"
	"time"

	"github.com/rs/zerolog"
)

// ctxKey is the unexported context key for the logger.
type ctxKey struct{}

// Config controls logger initialization.
type Config struct {
	// Level is the minimum log level (e.g. zerolog.InfoLevel).
	Level zerolog.Level
	// Pretty enables human-readable console output (development mode).
	// In production, leave false for JSON output.
	Pretty bool
}

// New constructs a root zerolog.Logger from the given Config.
// Caller should attach a "service" or "component" field before passing it down.
func New(cfg Config) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano

	var base zerolog.Logger
	if cfg.Pretty {
		base = zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()
	} else {
		base = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}
	return base.Level(cfg.Level)
}

// WithCtx stores a logger into a context, returning a new context.
// Handlers and middleware should call this to attach a request-scoped logger.
func WithCtx(ctx context.Context, l zerolog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromCtx retrieves the logger stored in ctx.
// If no logger was stored, it returns a no-op logger to avoid nil panics.
func FromCtx(ctx context.Context) zerolog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(zerolog.Logger); ok {
		return l
	}
	return zerolog.Nop()
}
