package execution

import (
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// killSwitchState tracks stop-loss events and halt status for a single tenant+symbol pair.
type killSwitchState struct {
	stops     []time.Time
	haltUntil time.Time
}

// KillSwitch implements a circuit breaker that halts trading for a tenant+symbol
// pair when too many stop-loss events occur within a sliding time window.
type KillSwitch struct {
	maxStops     int
	window       time.Duration
	haltDuration time.Duration
	nowFunc      func() time.Time
	mu           sync.Mutex
	states       map[string]*killSwitchState
}

// NewKillSwitch creates a KillSwitch that triggers after maxStops stop-loss events
// within the sliding window, halting trading for haltDuration.
// The nowFunc parameter enables deterministic time control in tests.
func NewKillSwitch(maxStops int, window, haltDuration time.Duration, nowFunc func() time.Time) *KillSwitch {
	return &KillSwitch{
		maxStops:     maxStops,
		window:       window,
		haltDuration: haltDuration,
		nowFunc:      nowFunc,
		states:       make(map[string]*killSwitchState),
	}
}

// stateKey builds the map key for a tenant+symbol pair.
func stateKey(tenantID string, symbol domain.Symbol) string {
	return tenantID + ":" + string(symbol)
}

// getOrCreateState returns the killSwitchState for the given key, creating one if absent.
// Caller must hold k.mu.
func (k *KillSwitch) getOrCreateState(tenantID string, symbol domain.Symbol) *killSwitchState {
	key := stateKey(tenantID, symbol)
	state, ok := k.states[key]
	if !ok {
		state = &killSwitchState{}
		k.states[key] = state
	}
	return state
}

// RecordStop records a stop-loss event for the tenant+symbol pair.
// If the number of stops within the sliding window reaches maxStops,
// the pair is halted and an error is returned.
func (k *KillSwitch) RecordStop(tenantID string, symbol domain.Symbol) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	now := k.nowFunc()
	state := k.getOrCreateState(tenantID, symbol)

	// If already halted, reject immediately.
	if !state.haltUntil.IsZero() && now.Before(state.haltUntil) {
		return fmt.Errorf("kill switch engaged: %s:%s already halted until %s",
			tenantID, symbol, state.haltUntil.Format(time.RFC3339))
	}

	// Prune stops outside the sliding window.
	cutoff := now.Add(-k.window)
	valid := state.stops[:0] // reuse backing array
	for _, t := range state.stops {
		if !t.Before(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	state.stops = valid

	if len(state.stops) >= k.maxStops {
		state.haltUntil = now.Add(k.haltDuration)
		return fmt.Errorf("kill switch engaged: %s:%s reached %d stops in %s window",
			tenantID, symbol, k.maxStops, k.window)
	}

	return nil
}

// IsHalted reports whether trading is currently halted for the tenant+symbol pair.
func (k *KillSwitch) IsHalted(tenantID string, symbol domain.Symbol) bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	state := k.getOrCreateState(tenantID, symbol)
	return !state.haltUntil.IsZero() && k.nowFunc().Before(state.haltUntil)
}
