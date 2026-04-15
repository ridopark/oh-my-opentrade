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
	"io"
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
	// Hooks are attached to the root logger.
	Hooks []zerolog.Hook
	// Writers are attached as additional sinks via MultiLevelWriter; each
	// receives the full serialized JSON event for every log line. Use
	// NewDiscordHook to route ErrorLevel events (with their structured
	// fields) to a NotifierPort; wire the notifier after services are
	// assembled via DiscordHook.SetNotifier.
	Writers []io.Writer
}

// New constructs a root zerolog.Logger from the given Config.
// Caller should attach a "service" or "component" field before passing it down.
func New(cfg Config) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano

	var primary io.Writer
	if cfg.Pretty {
		primary = zerolog.NewConsoleWriter()
	} else {
		primary = os.Stderr
	}
	writers := append([]io.Writer{primary}, cfg.Writers...)
	sink := primary
	if len(writers) > 1 {
		sink = zerolog.MultiLevelWriter(writers...)
	}
	base := zerolog.New(sink).With().Timestamp().Logger()
	l := base.Level(cfg.Level)
	for _, h := range cfg.Hooks {
		l = l.Hook(h)
	}
	return l
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
