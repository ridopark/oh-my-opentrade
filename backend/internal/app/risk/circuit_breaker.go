package risk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/rs/zerolog"
)

// DailyPnLSource provides the cumulative daily realized P&L.
// Implemented by perf.LedgerWriter for fast in-memory lookups.
type DailyPnLSource interface {
	GetDailyRealizedPnL(tenantID string, envMode domain.EnvMode) float64
}

// KillSwitchState represents the operational mode of the trading kill switch.
// See Sprint 4 Phase 4: a 3-state machine replacing the old binary SetGlobalHalt.
//
//	Active   — normal operation, all intents pass.
//	Reducing — new entries blocked, exits allowed (quiet shutdown).
//	Halted   — everything blocked (no entries, no exits).
type KillSwitchState int32

const (
	KillSwitchActive   KillSwitchState = 0
	KillSwitchReducing KillSwitchState = 1
	KillSwitchHalted   KillSwitchState = 2
)

// String returns the canonical uppercase label ("ACTIVE", "REDUCING", "HALTED").
func (s KillSwitchState) String() string {
	switch s {
	case KillSwitchActive:
		return "ACTIVE"
	case KillSwitchReducing:
		return "REDUCING"
	case KillSwitchHalted:
		return "HALTED"
	}
	return "UNKNOWN"
}

// ParseKillSwitchState maps a canonical label to its enum value. Case-insensitive.
// Returns an error for unknown labels so HTTP handlers can return 400.
func ParseKillSwitchState(s string) (KillSwitchState, error) {
	switch strings.ToUpper(s) {
	case "ACTIVE":
		return KillSwitchActive, nil
	case "REDUCING":
		return KillSwitchReducing, nil
	case "HALTED":
		return KillSwitchHalted, nil
	}
	return KillSwitchActive, fmt.Errorf("unknown kill switch state %q (want ACTIVE|REDUCING|HALTED)", s)
}

// KillSwitchTransition describes a single state change. Emitted on the
// optional transition sink so the persistence layer (kill_switch_events
// table) and any future event-bus subscribers can react.
type KillSwitchTransition struct {
	OldState KillSwitchState
	NewState KillSwitchState
	Reason   string
	Actor    string
	At       time.Time
}

// KillSwitchSink receives a notification for every state transition. The
// SQL-backed implementation persists to kill_switch_events; tests can
// supply an in-memory recorder.
type KillSwitchSink interface {
	RecordTransition(ctx context.Context, t KillSwitchTransition) error
}

// DailyLossBreaker is a circuit breaker that halts trading when cumulative
// daily losses exceed configured thresholds. It checks both percentage-based
// and absolute USD limits.
//
// Usage pattern mirrors execution.KillSwitch: check before broker submission.
// The embedded KillSwitchState is shared across all tripping sources
// (daily-loss trip, IBKR reconnect exhaustion, operator command) and is
// read by the kill_switch execution gate.
type DailyLossBreaker struct {
	maxLossPct float64 // e.g., 0.05 for 5%
	maxLossUSD float64 // absolute USD limit
	pnlSource  DailyPnLSource
	nowFunc    func() time.Time
	log        zerolog.Logger
	metrics    *metrics.Metrics
	globalHalt func() bool

	// state is the 3-state kill switch (Active/Reducing/Halted) read via
	// atomic.Int32 so the hot-path execution gate is lock-free.
	state atomic.Int32

	sink KillSwitchSink // optional persistence/event hook

	mu       sync.Mutex
	haltDate string // YYYY-MM-DD when halted; empty = not halted
}

// NewDailyLossBreaker creates a circuit breaker that trips when daily loss
// exceeds maxLossPct (as fraction, e.g. 0.05 = 5%) of equity or maxLossUSD in absolute terms.
func NewDailyLossBreaker(maxLossPct, maxLossUSD float64, pnlSource DailyPnLSource, nowFunc func() time.Time, log zerolog.Logger) *DailyLossBreaker {
	return &DailyLossBreaker{
		maxLossPct: maxLossPct,
		maxLossUSD: maxLossUSD,
		pnlSource:  pnlSource,
		nowFunc:    nowFunc,
		log:        log,
	}
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (d *DailyLossBreaker) SetMetrics(m *metrics.Metrics) {
	d.metrics = m
	// Initialize the gauge so Prometheus always exposes it (even when not tripped).
	m.Risk.CBActive.WithLabelValues("daily_loss").Set(0)
}

// SetSink wires a persistence/notification sink. Called at bootstrap.
func (d *DailyLossBreaker) SetSink(s KillSwitchSink) { d.sink = s }

// SetGlobalHalt is retained for backward compatibility with callers that
// still pass a boolean predicate (see orchestrator.IsGloballyHalted).
//
// Deprecated: prefer SetState(KillSwitchHalted, reason) for explicit
// transitions with audit trail. This method continues to work by
// consulting the predicate at Check() time.
func (d *DailyLossBreaker) SetGlobalHalt(isHalted func() bool) { d.globalHalt = isHalted }

// State returns the current kill switch state. Lock-free — safe to call
// from the hot-path execution gate.
func (d *DailyLossBreaker) State() KillSwitchState {
	return KillSwitchState(d.state.Load())
}

// SetState atomically transitions the kill switch to s. If s matches the
// current state it is a no-op (no log line, no sink emission) so repeated
// calls from different triggers don't spam the audit log.
//
// reason is a free-form short string ("daily loss threshold crossed",
// "ibkr reconnect exhausted", "operator requested"). actor is the
// operator identity when the transition came from the admin endpoint,
// or "system" for automatic transitions.
func (d *DailyLossBreaker) SetState(s KillSwitchState, reason, actor string) KillSwitchState {
	t := d.setStateCore(s, reason, actor)
	if t == nil {
		return s // no-op: old == new
	}
	if d.sink != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := d.sink.RecordTransition(ctx, *t); err != nil {
			d.log.Error().Err(err).Msg("kill switch: sink.RecordTransition failed")
		}
	}
	return t.OldState
}

// setStateCore performs the atomic state swap and synchronous log. Returns
// the transition record if a real transition occurred, nil on no-op.
// Callers decide whether to invoke the sink synchronously or asynchronously.
func (d *DailyLossBreaker) setStateCore(s KillSwitchState, reason, actor string) *KillSwitchTransition {
	old := KillSwitchState(d.state.Swap(int32(s)))
	if old == s {
		return nil
	}
	d.log.Warn().
		Str("old_state", old.String()).
		Str("new_state", s.String()).
		Str("reason", reason).
		Str("actor", actor).
		Msg("kill switch state transition")
	return &KillSwitchTransition{
		OldState: old,
		NewState: s,
		Reason:   reason,
		Actor:    actor,
		At:       d.now(),
	}
}

// sinkTransitionAsync persists a transition in a fire-and-forget goroutine.
// Used by transitionOnTrip (called under d.mu) to avoid holding the mutex
// during a potentially slow DB write.
func (d *DailyLossBreaker) sinkTransitionAsync(t KillSwitchTransition) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.sink.RecordTransition(ctx, t); err != nil {
		d.log.Error().Err(err).Msg("kill switch: async sink.RecordTransition failed")
	}
}

func (d *DailyLossBreaker) now() time.Time {
	if d.nowFunc != nil {
		return d.nowFunc()
	}
	return time.Now()
}

// Check evaluates whether trading should be halted for the given tenant.
// It returns an error if the daily loss circuit breaker is tripped.
// accountEquity is the current account equity used for percentage calculation.
func (d *DailyLossBreaker) Check(tenantID string, envMode domain.EnvMode, accountEquity float64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	today := d.nowFunc().UTC().Format("2006-01-02")
	if d.globalHalt != nil && d.globalHalt() {
		d.haltDate = today
		if d.metrics != nil {
			d.metrics.Risk.CBTripsTotal.WithLabelValues("global_halt").Inc()
			d.metrics.Risk.CBActive.WithLabelValues("daily_loss").Set(1)
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "tripped").Inc()
		}
		return fmt.Errorf("daily loss circuit breaker: global halt engaged")
	}

	// Reset halt if it's a new day.
	if d.haltDate != "" && d.haltDate != today {
		d.haltDate = ""
	}

	// If already halted today, reject immediately.
	if d.haltDate == today {
		if d.metrics != nil {
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "halted").Inc()
		}
		return fmt.Errorf("daily loss circuit breaker: trading halted for %s on %s", tenantID, today)
	}

	// Get cumulative realized P&L for today.
	dailyPnL := d.pnlSource.GetDailyRealizedPnL(tenantID, envMode)

	// Only check for losses (negative P&L).
	if dailyPnL >= 0 {
		if d.metrics != nil {
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "pass").Inc()
		}
		return nil
	}

	loss := -dailyPnL // positive number representing loss

	// Check absolute USD limit.
	if d.maxLossUSD > 0 && loss >= d.maxLossUSD {
		d.haltDate = today
		d.log.Warn().
			Float64("daily_loss", loss).
			Float64("max_loss_usd", d.maxLossUSD).
			Str("tenant_id", tenantID).
			Msg("daily loss circuit breaker tripped: absolute USD limit exceeded")
		if d.metrics != nil {
			d.metrics.Risk.CBTripsTotal.WithLabelValues("usd_limit").Inc()
			d.metrics.Risk.CBActive.WithLabelValues("daily_loss").Set(1)
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "tripped").Inc()
		}
		// Daily-loss trip transitions into REDUCING so operators can still close positions.
		d.transitionOnTrip(KillSwitchReducing, "daily loss USD limit exceeded")
		return fmt.Errorf("daily loss circuit breaker: loss $%.2f exceeds max $%.2f", loss, d.maxLossUSD)
	}

	// Check percentage limit.
	if d.maxLossPct > 0 && accountEquity > 0 {
		lossPct := loss / accountEquity
		if lossPct >= d.maxLossPct {
			d.haltDate = today
			d.log.Warn().
				Float64("daily_loss", loss).
				Float64("loss_pct", lossPct*100).
				Float64("max_loss_pct", d.maxLossPct*100).
				Float64("account_equity", accountEquity).
				Str("tenant_id", tenantID).
				Msg("daily loss circuit breaker tripped: percentage limit exceeded")
			if d.metrics != nil {
				d.metrics.Risk.CBTripsTotal.WithLabelValues("pct_limit").Inc()
				d.metrics.Risk.CBActive.WithLabelValues("daily_loss").Set(1)
				d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "tripped").Inc()
			}
			d.transitionOnTrip(KillSwitchReducing, "daily loss percentage limit exceeded")
			return fmt.Errorf("daily loss circuit breaker: loss %.2f%% exceeds max %.2f%%", lossPct*100, d.maxLossPct*100)
		}
	}

	if d.metrics != nil {
		d.metrics.Risk.CBActive.WithLabelValues("daily_loss").Set(0)
		d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "pass").Inc()
	}
	return nil
}

// Inspect reports whether the daily-loss breaker would trip for the given
// tenant without mutating any state. It is the read-only twin of Check:
// no haltDate write, no transitionOnTrip, no trip counters. Used by the
// proposal handler to probe risk gates without flipping the kill switch.
func (d *DailyLossBreaker) Inspect(tenantID string, envMode domain.EnvMode, accountEquity float64) (lossUSD, lossPct float64, tripped bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	today := d.nowFunc().UTC().Format("2006-01-02")
	if d.globalHalt != nil && d.globalHalt() {
		return 0, 0, true, nil
	}
	if d.haltDate == today {
		return 0, 0, true, nil
	}

	dailyPnL := d.pnlSource.GetDailyRealizedPnL(tenantID, envMode)
	if dailyPnL >= 0 {
		return 0, 0, false, nil
	}

	loss := -dailyPnL
	var pct float64
	if accountEquity > 0 {
		pct = loss / accountEquity
	}

	if d.maxLossUSD > 0 && loss >= d.maxLossUSD {
		return loss, pct, true, nil
	}
	if d.maxLossPct > 0 && accountEquity > 0 && pct >= d.maxLossPct {
		return loss, pct, true, nil
	}
	return loss, pct, false, nil
}

// MaxLossUSD returns the configured absolute USD daily-loss cap.
func (d *DailyLossBreaker) MaxLossUSD() float64 { return d.maxLossUSD }

// MaxLossPct returns the configured fractional daily-loss cap (e.g. 0.05 = 5%).
func (d *DailyLossBreaker) MaxLossPct() float64 { return d.maxLossPct }

// transitionOnTrip bumps the kill switch state on an automatic trip.
// Once HALTED, we do not downgrade to REDUCING — escalation is monotonic
// except by operator command.
//
// Called with d.mu held (from Check), so the sink call is dispatched
// asynchronously to avoid blocking concurrent Check() callers during a
// potentially slow DB write.
func (d *DailyLossBreaker) transitionOnTrip(s KillSwitchState, reason string) {
	cur := d.State()
	if cur >= s {
		return
	}
	t := d.setStateCore(s, reason, "system")
	if t != nil && d.sink != nil {
		go d.sinkTransitionAsync(*t)
	}
}

// IsHalted reports whether trading is currently halted for today.
func (d *DailyLossBreaker) IsHalted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	today := d.nowFunc().UTC().Format("2006-01-02")
	return d.haltDate == today
}

// Reset clears the halt state and kill switch state. Useful for testing.
func (d *DailyLossBreaker) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.haltDate = ""
	d.state.Store(int32(KillSwitchActive))
}
