package strategy

import "fmt"

// LifecycleState represents the current lifecycle stage of a strategy version.
//
// State machine:
//
//	Draft → BacktestReady → PaperActive → LiveActive → Deactivated → Archived
//	                                                 ↗
//	                                    PaperActive ─┘  (can deactivate from paper too)
//	                                    BacktestReady → Deactivated (skip trading)
//	                         Deactivated → PaperActive (reactivation)
type LifecycleState string

const (
	LifecycleDraft         LifecycleState = "Draft"
	LifecycleBacktestReady LifecycleState = "BacktestReady"
	LifecyclePaperActive   LifecycleState = "PaperActive"
	LifecycleLiveActive    LifecycleState = "LiveActive"
	LifecycleDeactivated   LifecycleState = "Deactivated"
	LifecycleArchived      LifecycleState = "Archived"
)

func (s LifecycleState) String() string { return string(s) }

// allLifecycleStates enumerates all valid lifecycle states.
var allLifecycleStates = map[LifecycleState]struct{}{
	LifecycleDraft:         {},
	LifecycleBacktestReady: {},
	LifecyclePaperActive:   {},
	LifecycleLiveActive:    {},
	LifecycleDeactivated:   {},
	LifecycleArchived:      {},
}

// NewLifecycleState creates a validated LifecycleState.
func NewLifecycleState(s string) (LifecycleState, error) {
	ls := LifecycleState(s)
	if _, ok := allLifecycleStates[ls]; !ok {
		return "", fmt.Errorf("invalid lifecycle state: %q", s)
	}
	return ls, nil
}

// validTransitions defines allowed state transitions.
// Key = from state, value = set of allowed target states.
var validTransitions = map[LifecycleState]map[LifecycleState]struct{}{
	LifecycleDraft: {
		LifecycleBacktestReady: {},
	},
	LifecycleBacktestReady: {
		LifecyclePaperActive: {},
		LifecycleDeactivated: {},
	},
	LifecyclePaperActive: {
		LifecycleLiveActive:  {},
		LifecycleDeactivated: {},
	},
	LifecycleLiveActive: {
		LifecycleDeactivated: {},
	},
	LifecycleDeactivated: {
		LifecyclePaperActive: {}, // reactivation
		LifecycleArchived:    {},
	},
	LifecycleArchived: {
		// terminal state — no transitions out
	},
}

// CanTransitionTo returns true if transitioning from the current state to
// the target state is valid according to the lifecycle state machine.
func (s LifecycleState) CanTransitionTo(target LifecycleState) bool {
	targets, ok := validTransitions[s]
	if !ok {
		return false
	}
	_, allowed := targets[target]
	return allowed
}

// ValidateTransition checks if a transition is valid and returns a descriptive
// error if not. Returns nil on success.
func ValidateTransition(from, to LifecycleState) error {
	if from == to {
		return fmt.Errorf("%w: %s → %s", ErrAlreadyInState, from, to)
	}
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, from, to)
	}
	return nil
}

// IsActive returns true if the strategy is actively processing bars.
func (s LifecycleState) IsActive() bool {
	return s == LifecyclePaperActive || s == LifecycleLiveActive
}

// IsTerminal returns true if no further transitions are allowed.
func (s LifecycleState) IsTerminal() bool {
	return s == LifecycleArchived
}

// AllowedTransitions returns the set of states that can be transitioned to
// from the current state.
func (s LifecycleState) AllowedTransitions() []LifecycleState {
	targets, ok := validTransitions[s]
	if !ok {
		return nil
	}
	result := make([]LifecycleState, 0, len(targets))
	for t := range targets {
		result = append(result, t)
	}
	return result
}
