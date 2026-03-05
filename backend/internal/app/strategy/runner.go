package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Runner routes market bars to strategy instances and collects signals.
// It subscribes to MarketBarSanitized events, dispatches bars to matching
// instances via the Router, and emits SignalCreated events for each signal.
type Runner struct {
	mu          sync.Mutex
	eventBus    ports.EventBusPort
	router      *Router
	swapManager *SwapManager
	logger      *slog.Logger
	tenantID    string
	envMode     domain.EnvMode
	indicators  map[string]strat.IndicatorData // cached per-symbol indicators from StateUpdated
	metrics     *metrics.Metrics
}

// NewRunner creates a StrategyRunner.
func NewRunner(
	eventBus ports.EventBusPort,
	router *Router,
	tenantID string,
	envMode domain.EnvMode,
	logger *slog.Logger,
) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		eventBus:   eventBus,
		router:     router,
		logger:     logger.With("component", "strategy_runner"),
		tenantID:   tenantID,
		envMode:    envMode,
		indicators: make(map[string]strat.IndicatorData),
	}
}

// Router returns the underlying router for registration.
func (r *Runner) Router() *Router { return r.router }

// SetSwapManager attaches a SwapManager to feed shadow instances during bar processing.
func (r *Runner) SetSwapManager(sm *SwapManager) { r.swapManager = sm }

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (r *Runner) SetMetrics(m *metrics.Metrics) { r.metrics = m }

// Start subscribes the runner to MarketBarSanitized and StateUpdated events on the event bus.
func (r *Runner) Start(ctx context.Context) error {
	if err := r.eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, r.handleBar); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to MarketBarSanitized: %w", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventStateUpdated, r.handleStateUpdated); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to StateUpdated: %w", err)
	}
	r.logger.Info("strategy runner subscribed to MarketBarSanitized events")
	return nil
}

// handleStateUpdated caches indicator data from StateUpdated events.
// This data is used by handleBar to inject indicators into strategy instances.
func (r *Runner) handleStateUpdated(_ context.Context, event domain.Event) error {
	snap, ok := event.Payload.(domain.IndicatorSnapshot)
	if !ok {
		return nil
	}
	r.mu.Lock()
	r.indicators[snap.Symbol.String()] = strat.IndicatorData{
		RSI:           snap.RSI,
		StochK:        snap.StochK,
		StochD:        snap.StochD,
		EMA9:          snap.EMA9,
		EMA21:         snap.EMA21,
		VWAP:          snap.VWAP,
		Volume:        snap.Volume,
		VolumeSMA:     snap.VolumeSMA,
		AnchorRegimes: convertAnchorRegimes(snap.AnchorRegimes),
	}
	r.mu.Unlock()
	return nil
}

func convertAnchorRegimes(regimes map[domain.Timeframe]domain.MarketRegime) map[string]strat.AnchorRegime {
	if len(regimes) == 0 {
		return nil
	}
	result := make(map[string]strat.AnchorRegime, len(regimes))
	for tf, r := range regimes {
		result[tf.String()] = strat.AnchorRegime{
			Type:     r.Type.String(),
			Strength: r.Strength,
		}
	}
	return result
}

// handleBar processes a MarketBarSanitized event by routing to assigned instances.
func (r *Runner) handleBar(ctx context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("strategy runner: payload is not a MarketBar, got %T", event.Payload)
	}

	loopStart := time.Now()
	symbol := bar.Symbol.String()
	instances := r.router.InstancesForSymbol(symbol)
	if len(instances) == 0 {
		return nil
	}

	// Convert domain.MarketBar → strat.Bar
	sBar := domainBarToStratBar(bar)

	// Use cached indicators from StateUpdated events, with current bar volume.
	indicators := r.indicators[symbol]
	indicators.Volume = bar.Volume

	r.mu.Lock()
	defer r.mu.Unlock()

	var allSignals []strat.Signal

	for _, inst := range instances {
		now := time.Now()
		instCtx := &instanceContext{
			now:    now,
			logger: r.logger.With("instance_id", inst.ID().String(), "symbol", symbol),
			emit: func(evt any) error {
				return r.emitDomainEvent(ctx, event.TenantID, event.EnvMode, evt)
			},
		}

		signals, err := inst.OnBar(instCtx, symbol, sBar, indicators)
		if err != nil {
			r.logger.Error("instance OnBar failed",
				"instance_id", inst.ID().String(),
				"symbol", symbol,
				"error", err,
			)
			continue // Don't let one instance failure stop others.
		}

		allSignals = append(allSignals, signals...)
	}

	if r.swapManager != nil {
		swapCtx := &instanceContext{
			now:    time.Now(),
			logger: r.logger.With("symbol", symbol),
			emit:   func(_ any) error { return nil },
		}
		r.swapManager.OnBarProcessed(swapCtx, symbol, sBar, indicators)
	}

	// Emit SignalCreated events for each actionable signal.
	for _, sig := range allSignals {
		if !sig.Type.IsActionable() {
			continue
		}
		if r.metrics != nil {
			r.metrics.Strategy.SignalsTotal.WithLabelValues("orb_break_retest", string(sig.Type), string(sig.Side)).Inc()
		}
		if err := r.emitSignal(ctx, event.TenantID, event.EnvMode, sig); err != nil {
			r.logger.Error("failed to emit SignalCreated",
				"instance_id", sig.StrategyInstanceID.String(),
				"symbol", sig.Symbol,
				"error", err,
			)
		}
	}

	// Record strategy loop duration.
	if r.metrics != nil {
		r.metrics.Strategy.LoopDuration.WithLabelValues("orb_break_retest", "handle_bar").Observe(time.Since(loopStart).Seconds())
	}

	return nil
}

// ProcessBar allows direct bar processing without going through the event bus.
// Useful for testing and warmup scenarios.
func (r *Runner) ProcessBar(ctx context.Context, symbol string, bar strat.Bar, indicators strat.IndicatorData) ([]strat.Signal, error) {
	instances := r.router.InstancesForSymbol(symbol)
	if len(instances) == 0 {
		return nil, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var allSignals []strat.Signal

	for _, inst := range instances {
		now := time.Now()
		instCtx := &instanceContext{
			now:    now,
			logger: r.logger.With("instance_id", inst.ID().String(), "symbol", symbol),
			emit: func(evt any) error {
				return r.emitDomainEvent(ctx, r.tenantID, r.envMode, evt)
			},
		}

		signals, err := inst.OnBar(instCtx, symbol, bar, indicators)
		if err != nil {
			return allSignals, fmt.Errorf("instance %s: %w", inst.ID(), err)
		}
		allSignals = append(allSignals, signals...)
	}

	if r.swapManager != nil {
		swapCtx := &instanceContext{
			now:    time.Now(),
			logger: r.logger.With("symbol", symbol),
			emit:   func(_ any) error { return nil },
		}
		r.swapManager.OnBarProcessed(swapCtx, symbol, bar, indicators)
	}

	return allSignals, nil
}

// emitSignal publishes a SignalCreated domain event.
func (r *Runner) emitSignal(ctx context.Context, tenantID string, envMode domain.EnvMode, sig strat.Signal) error {
	ev, err := domain.NewEvent(
		domain.EventSignalCreated,
		tenantID,
		envMode,
		uuid.NewString(),
		sig,
	)
	if err != nil {
		return fmt.Errorf("strategy runner: failed to create signal event: %w", err)
	}
	return r.eventBus.Publish(ctx, *ev)
}

// emitDomainEvent publishes an arbitrary domain event (used by strategy Context).
func (r *Runner) emitDomainEvent(ctx context.Context, tenantID string, envMode domain.EnvMode, payload any) error {
	ev, err := domain.NewEvent(
		"StrategyDomainEvent",
		tenantID,
		envMode,
		uuid.NewString(),
		payload,
	)
	if err != nil {
		return err
	}
	return r.eventBus.Publish(ctx, *ev)
}

// domainBarToStratBar converts a domain.MarketBar to a strategy.Bar.
func domainBarToStratBar(bar domain.MarketBar) strat.Bar {
	return strat.Bar{
		Time:   bar.Time,
		Open:   bar.Open,
		High:   bar.High,
		Low:    bar.Low,
		Close:  bar.Close,
		Volume: bar.Volume,
	}
}
