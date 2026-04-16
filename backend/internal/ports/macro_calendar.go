package ports

import (
	"context"
	"time"
)

// MacroEvent describes a scheduled macroeconomic release (FOMC rate
// decision, CPI, NFP, PPI, PCE, FOMC Minutes, etc.). The zero value is
// not a valid event; adapters are expected to populate at minimum ID,
// Name, ScheduledAt, and Impact.
type MacroEvent struct {
	// ID is a stable, source-specific identifier used for upsert.
	ID string
	// Name is a human-readable label such as "FOMC Rate Decision" or
	// "CPI". Downstream gates match on this string plus Impact.
	Name string
	// ScheduledAt is the wall-clock time (UTC) the release is
	// expected to go public.
	ScheduledAt time.Time
	// Impact is one of "high", "medium", "low". Empty is treated as
	// "medium" by gates.
	Impact string
	// Actual, Consensus, Previous are numeric release values once
	// available; nil while still pending.
	Actual    *float64
	Consensus *float64
	Previous  *float64
	// Released is true once the event has printed a value; gates use
	// this to stop blocking after the announcement has passed.
	Released bool
}

// MacroCalendarPort exposes a forward-looking view of macro events for
// both batch refreshes and the just-in-time gate check.
type MacroCalendarPort interface {
	// UpcomingEvents returns events whose ScheduledAt falls inside
	// [from, from+windowHours). Ordered ascending by ScheduledAt.
	UpcomingEvents(ctx context.Context, from time.Time, windowHours int) ([]MacroEvent, error)
	// EventsInWindow returns events whose ScheduledAt falls inside
	// [around-windowMinutes, around+windowMinutes]. Used by the
	// macro_event_gate at decision time.
	EventsInWindow(ctx context.Context, around time.Time, windowMinutes int) ([]MacroEvent, error)
}
