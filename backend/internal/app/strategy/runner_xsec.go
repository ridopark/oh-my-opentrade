package strategy

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// XSecRunner buffers per-symbol bars and dispatches them as a batch
// to CrossSectionalStrategy.OnCrossSectionalBar once all symbols in
// the universe have reported a bar at the current timestamp.
type XSecRunner struct {
	strategy domstrategy.CrossSectionalStrategy
	universe map[string]bool                         // expected symbols
	log      *slog.Logger

	// Live mode: grace period to handle WS jitter.
	graceWindow time.Duration // e.g. 500ms for live, 0 for backtest

	// mu protects buffer and state which are accessed from both the
	// OnBar caller goroutine and grace-window AfterFunc timer goroutines.
	mu     sync.Mutex
	buffer map[string]map[string]domstrategy.Bar // tsKey → symbol → bar
	state  domstrategy.State

	// timers tracks pending grace-window timers keyed by tsKey.
	// Only used when graceWindow > 0.
	timersMu sync.Mutex
	timers   map[string]*time.Timer

	// pendingSignals collects signals from grace-window flushes that
	// happen asynchronously. The caller must drain them.
	pendingMu      sync.Mutex
	pendingSignals []domstrategy.Signal
}

// XSecRunnerConfig configures the cross-sectional runner.
type XSecRunnerConfig struct {
	GraceWindow time.Duration // 0 = strict (backtest), >0 = jitter tolerance (live)
}

// NewXSecRunner creates a cross-sectional runner for the given strategy.
func NewXSecRunner(strategy domstrategy.CrossSectionalStrategy, cfg XSecRunnerConfig, log *slog.Logger) *XSecRunner {
	if log == nil {
		log = slog.Default()
	}

	universe := make(map[string]bool, len(strategy.Universe()))
	for _, sym := range strategy.Universe() {
		universe[sym] = true
	}

	return &XSecRunner{
		strategy:    strategy,
		universe:    universe,
		buffer:      make(map[string]map[string]domstrategy.Bar),
		log:         log.With("component", "xsec_runner"),
		graceWindow: cfg.GraceWindow,
		timers:      make(map[string]*time.Timer),
	}
}

// SetState sets the runner's current strategy state (e.g. after Init).
func (r *XSecRunner) SetState(st domstrategy.State) {
	r.mu.Lock()
	r.state = st
	r.mu.Unlock()
}

// State returns the runner's current strategy state.
func (r *XSecRunner) State() domstrategy.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// OnBar buffers a bar for the given symbol. When all universe symbols have
// reported for this timestamp (or the grace window expires in live mode),
// it calls OnCrossSectionalBar and returns the resulting signals.
func (r *XSecRunner) OnBar(ctx domstrategy.Context, symbol string, bar domstrategy.Bar) ([]domstrategy.Signal, error) {
	if !r.universe[symbol] {
		return nil, fmt.Errorf("xsec_runner: symbol %q not in universe", symbol)
	}

	key := r.tsKey(bar.Time)

	r.mu.Lock()
	if r.buffer[key] == nil {
		r.buffer[key] = make(map[string]domstrategy.Bar, len(r.universe))
	}
	r.buffer[key][symbol] = bar
	complete := r.isComplete(key)

	// Evict stale timestamps to prevent unbounded buffer growth from
	// data gaps where a symbol never reports for a given timestamp.
	const maxBufferedTimestamps = 10
	if len(r.buffer) > maxBufferedTimestamps {
		cutoff := bar.Time.Add(-5 * time.Minute)
		for k := range r.buffer {
			if ts, err := time.Parse(time.RFC3339, k); err == nil && ts.Before(cutoff) {
				delete(r.buffer, k)
			}
		}
	}
	r.mu.Unlock()

	if complete {
		return r.flush(ctx, key)
	}

	// In live mode with grace window, start a timer on first bar for this ts.
	if r.graceWindow > 0 {
		r.timersMu.Lock()
		if _, exists := r.timers[key]; !exists {
			tsKey := key
			r.timers[tsKey] = time.AfterFunc(r.graceWindow, func() {
				r.timersMu.Lock()
				delete(r.timers, tsKey)
				r.timersMu.Unlock()

				signals, err := r.flush(ctx, tsKey)
				if err != nil {
					r.log.Error("xsec_runner: grace flush failed", "ts_key", tsKey, "error", err)
					return
				}
				if len(signals) > 0 {
					r.pendingMu.Lock()
					r.pendingSignals = append(r.pendingSignals, signals...)
					r.pendingMu.Unlock()
				}
			})
		}
		r.timersMu.Unlock()
	}

	return nil, nil
}

// DrainPendingSignals returns and clears any signals produced by grace-window
// timer flushes. Only relevant in live mode (GraceWindow > 0).
func (r *XSecRunner) DrainPendingSignals() []domstrategy.Signal {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	out := r.pendingSignals
	r.pendingSignals = nil
	return out
}

// tsKey rounds a timestamp to the minute for grouping bars into cross-sections.
func (r *XSecRunner) tsKey(t time.Time) string {
	return t.UTC().Truncate(time.Minute).Format(time.RFC3339)
}

// isComplete returns true if all universe symbols have a bar at this timestamp.
func (r *XSecRunner) isComplete(tsKey string) bool {
	bars, ok := r.buffer[tsKey]
	if !ok {
		return false
	}
	for sym := range r.universe {
		if _, ok := bars[sym]; !ok {
			return false
		}
	}
	return true
}

// flush dispatches the buffered cross-section for the given timestamp key,
// removes it from the buffer, and cancels any pending grace timer.
func (r *XSecRunner) flush(ctx domstrategy.Context, tsKey string) ([]domstrategy.Signal, error) {
	r.mu.Lock()
	bars, ok := r.buffer[tsKey]
	if !ok {
		r.mu.Unlock()
		return nil, nil
	}
	// Copy bars out and delete buffer entry under the lock.
	barsCopy := make(map[string]domstrategy.Bar, len(bars))
	for k, v := range bars {
		barsCopy[k] = v
	}
	delete(r.buffer, tsKey)
	state := r.state
	r.mu.Unlock()

	// Cancel grace timer if any.
	r.timersMu.Lock()
	if timer, exists := r.timers[tsKey]; exists {
		timer.Stop()
		delete(r.timers, tsKey)
	}
	r.timersMu.Unlock()

	// Log missing symbols if partial.
	if len(barsCopy) < len(r.universe) {
		missing := make([]string, 0, len(r.universe)-len(barsCopy))
		for sym := range r.universe {
			if _, ok := barsCopy[sym]; !ok {
				missing = append(missing, sym)
			}
		}
		r.log.Warn("xsec_runner: partial cross-section dispatch",
			"ts_key", tsKey,
			"have", len(barsCopy),
			"want", len(r.universe),
			"missing", missing,
		)
	}

	// Parse timestamp from key for the callback.
	ts, err := time.Parse(time.RFC3339, tsKey)
	if err != nil {
		return nil, fmt.Errorf("xsec_runner: parse ts_key %q: %w", tsKey, err)
	}

	// Dispatch to strategy (outside the lock — strategy may be slow).
	nextState, signals, err := r.strategy.OnCrossSectionalBar(ctx, ts, barsCopy, state)
	if err != nil {
		return nil, fmt.Errorf("xsec_runner: OnCrossSectionalBar: %w", err)
	}

	r.mu.Lock()
	r.state = nextState
	r.mu.Unlock()

	return signals, nil
}

// IsXSec returns true if the strategy implements CrossSectionalStrategy.
func IsXSec(s domstrategy.Strategy) bool {
	_, ok := s.(domstrategy.CrossSectionalStrategy)
	return ok
}

// UniverseSize returns the number of symbols in the runner's universe.
func (r *XSecRunner) UniverseSize() int {
	return len(r.universe)
}

// BufferedTimestamps returns the number of timestamps currently buffered.
func (r *XSecRunner) BufferedTimestamps() int {
	return len(r.buffer)
}
