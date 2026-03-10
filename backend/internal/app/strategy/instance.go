package strategy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// InstanceAssignment defines which symbols a strategy instance is responsible for.
type InstanceAssignment struct {
	Symbols        []string
	Timeframes     []string
	AssetClasses   []string
	Priority       int
	ConflictPolicy start.ConflictPolicy
}

// Instance wraps a Strategy implementation with per-symbol state management
// and routing assignment. It is the unit of execution within the StrategyRunner.
type Instance struct {
	mu         sync.Mutex
	id         start.InstanceID
	strategy   start.Strategy
	params     map[string]any
	assignment InstanceAssignment
	lifecycle  start.LifecycleState
	states     map[string]start.State // per-symbol state
	warmupLeft map[string]int         // bars remaining for warmup per symbol
	logger     *slog.Logger
}

// NewInstance creates a new strategy instance with the given assignment.
func NewInstance(
	id start.InstanceID,
	strategy start.Strategy,
	params map[string]any,
	assignment InstanceAssignment,
	lifecycle start.LifecycleState,
	logger *slog.Logger,
) *Instance {
	if logger == nil {
		logger = slog.Default()
	}
	return &Instance{
		id:         id,
		strategy:   strategy,
		params:     params,
		assignment: assignment,
		lifecycle:  lifecycle,
		states:     make(map[string]start.State),
		warmupLeft: make(map[string]int),
		logger:     logger.With("instance_id", id.String()),
	}
}

// ID returns the instance identifier.
func (inst *Instance) ID() start.InstanceID { return inst.id }

func (inst *Instance) configStrategyID() string {
	parts := strings.SplitN(string(inst.id), ":", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return inst.strategy.Meta().ID.String()
}

// Strategy returns the underlying strategy implementation.
func (inst *Instance) Strategy() start.Strategy { return inst.strategy }

// Assignment returns the routing assignment.
func (inst *Instance) Assignment() InstanceAssignment { return inst.assignment }

// Lifecycle returns the current lifecycle state.
func (inst *Instance) Lifecycle() start.LifecycleState {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.lifecycle
}

// SetLifecycle updates the lifecycle state.
func (inst *Instance) SetLifecycle(state start.LifecycleState) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.lifecycle = state
}

// IsActive returns true if the instance is in an active lifecycle state.
func (inst *Instance) IsActive() bool {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.lifecycle.IsActive()
}

// InitSymbol initializes the strategy for a specific symbol.
// Must be called before processing bars for that symbol.
func (inst *Instance) InitSymbol(ctx start.Context, symbol string, prior start.State) error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	state, err := inst.strategy.Init(ctx, symbol, inst.params, prior)
	if err != nil {
		return fmt.Errorf("instance %s: init symbol %s: %w", inst.id, symbol, err)
	}

	inst.states[symbol] = state
	inst.warmupLeft[symbol] = inst.strategy.WarmupBars()
	return nil
}

// OnBar processes a bar for the given symbol. Returns signals produced.
// If the instance is still warming up for the symbol, signals are suppressed.
func (inst *Instance) OnBar(ctx start.Context, symbol string, bar start.Bar, indicators start.IndicatorData) ([]start.Signal, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	if !inst.lifecycle.IsActive() {
		return nil, nil
	}

	state, ok := inst.states[symbol]
	if !ok {
		return nil, fmt.Errorf("instance %s: symbol %s not initialized", inst.id, symbol)
	}

	// Inject indicators into state if it supports the SetIndicators interface.
	type indicatorSetter interface {
		SetIndicators(start.IndicatorData)
	}
	if setter, ok := state.(indicatorSetter); ok {
		setter.SetIndicators(indicators)
	}

	next, signals, err := inst.strategy.OnBar(ctx, symbol, bar, state)
	if err != nil {
		return nil, fmt.Errorf("instance %s: OnBar %s: %w", inst.id, symbol, err)
	}

	inst.states[symbol] = next

	// Decrement warmup counter; suppress signals during warmup.
	if inst.warmupLeft[symbol] > 0 {
		inst.warmupLeft[symbol]--
		return nil, nil
	}

	for i := range signals {
		signals[i].StrategyInstanceID = inst.id
	}

	for i := range signals {
		if signals[i].Tags == nil {
			signals[i].Tags = make(map[string]string)
		}
		if _, exists := signals[i].Tags["ind_atr"]; !exists && indicators.ATR > 0 {
			signals[i].Tags["ind_atr"] = fmt.Sprintf("%f", indicators.ATR)
		}
		if _, exists := signals[i].Tags["ind_vwap"]; !exists && indicators.VWAP > 0 {
			signals[i].Tags["ind_vwap"] = fmt.Sprintf("%f", indicators.VWAP)
		}
		if _, exists := signals[i].Tags["ind_vwap_sd"]; !exists && indicators.VWAPSD > 0 {
			signals[i].Tags["ind_vwap_sd"] = fmt.Sprintf("%f", indicators.VWAPSD)
		}
	}

	return signals, nil
}

// WarmupOnBar processes a bar for warmup purposes only — bypasses lifecycle
// check and always suppresses signals. If the strategy implements
// ReplayableStrategy, it calls ReplayOnBar instead of OnBar to enable
// replay-aware state recovery (e.g., ORB tracker with replay=true).
func (inst *Instance) WarmupOnBar(ctx start.Context, symbol string, bar start.Bar, indicators start.IndicatorData) error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	state, ok := inst.states[symbol]
	if !ok {
		return fmt.Errorf("instance %s: symbol %s not initialized", inst.id, symbol)
	}

	// Check if strategy supports replay-aware warmup.
	if r, ok := inst.strategy.(start.ReplayableStrategy); ok {
		next, err := r.ReplayOnBar(ctx, symbol, bar, state, indicators)
		if err != nil {
			return fmt.Errorf("instance %s: ReplayOnBar %s: %w", inst.id, symbol, err)
		}
		inst.states[symbol] = next
	} else {
		// Non-replayable strategies: inject indicators, call OnBar, discard signals.
		type indicatorSetter interface {
			SetIndicators(start.IndicatorData)
		}
		if setter, ok := state.(indicatorSetter); ok {
			setter.SetIndicators(indicators)
		}

		next, _, err := inst.strategy.OnBar(ctx, symbol, bar, state)
		if err != nil {
			return fmt.Errorf("instance %s: WarmupOnBar %s: %w", inst.id, symbol, err)
		}
		inst.states[symbol] = next
	}

	if inst.warmupLeft[symbol] > 0 {
		inst.warmupLeft[symbol]--
	}

	return nil
}

// OnEvent processes a non-bar event for the given symbol.
func (inst *Instance) OnEvent(ctx start.Context, symbol string, evt any) ([]start.Signal, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	if !inst.lifecycle.IsActive() {
		return nil, nil
	}

	state, ok := inst.states[symbol]
	if !ok {
		return nil, nil // ignore events for uninitialized symbols
	}

	next, signals, err := inst.strategy.OnEvent(ctx, symbol, evt, state)
	if err != nil {
		return nil, fmt.Errorf("instance %s: OnEvent %s: %w", inst.id, symbol, err)
	}

	inst.states[symbol] = next
	return signals, nil
}

// IsWarmedUp returns true if the symbol has passed the warmup period.
func (inst *Instance) IsWarmedUp(symbol string) bool {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.warmupLeft[symbol] <= 0
}

// GetState returns the current state for a symbol (for persistence/inspection).
func (inst *Instance) GetState(symbol string) (start.State, bool) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	st, ok := inst.states[symbol]
	return st, ok
}

// Snapshot returns a point-in-time state snapshot for a symbol.
// It marshals the strategy's internal state into a domain.StateSnapshot.
func (inst *Instance) Snapshot(symbol string) (domain.StateSnapshot, bool) {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	st, ok := inst.states[symbol]
	if !ok {
		return domain.StateSnapshot{}, false
	}

	data, err := st.Marshal()
	if err != nil {
		inst.logger.Error("snapshot marshal failed",
			"symbol", symbol,
			"error", err,
		)
		return domain.StateSnapshot{}, false
	}

	configID := inst.configStrategyID()
	return domain.StateSnapshot{
		Strategy: configID,
		Symbol:   symbol,
		Kind:     configID,
		AsOf:     time.Now(),
		Payload:  json.RawMessage(data),
	}, true
}

// AllSnapshots returns state snapshots for all initialized symbols.
func (inst *Instance) AllSnapshots() []domain.StateSnapshot {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	configID := inst.configStrategyID()
	snaps := make([]domain.StateSnapshot, 0, len(inst.states))
	for symbol, st := range inst.states {
		data, err := st.Marshal()
		if err != nil {
			inst.logger.Error("snapshot marshal failed",
				"symbol", symbol,
				"error", err,
			)
			continue
		}
		snaps = append(snaps, domain.StateSnapshot{
			Strategy: configID,
			Symbol:   symbol,
			Kind:     configID,
			AsOf:     time.Now(),
			Payload:  json.RawMessage(data),
		})
	}
	return snaps
}

// instanceContext implements start.Context for use within the runner.
type instanceContext struct {
	now    time.Time
	logger *slog.Logger
	emit   func(evt any) error
}

func (c *instanceContext) Now() time.Time                { return c.now }
func (c *instanceContext) Logger() *slog.Logger          { return c.logger }
func (c *instanceContext) EmitDomainEvent(evt any) error { return c.emit(evt) }

// NewContext creates a start.Context for use outside the runner (e.g., main.go wiring).
// The emit function is called when a strategy invokes EmitDomainEvent; pass nil for a no-op.
func NewContext(now time.Time, logger *slog.Logger, emit func(evt any) error) start.Context {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(evt any) error { return nil }
	}
	return &instanceContext{now: now, logger: logger, emit: emit}
}
