package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Runner routes market bars to strategy instances and collects signals.
// It subscribes to MarketBarSanitized events, dispatches bars to matching
// instances via the Router, and emits SignalCreated events for each signal.
type Runner struct {
	mu                   sync.Mutex
	eventBus             ports.EventBusPort
	router               *Router
	swapManager          *SwapManager
	posLookup            PositionLookupFunc
	logger               *slog.Logger
	tenantID             string
	envMode              domain.EnvMode
	indicators           map[string]start.IndicatorData
	indLogOnce           map[string]bool
	metrics              *metrics.Metrics
	aggregators          map[string]*domain.BarAggregator
	signalsRTHSuppressed atomic.Int64
}

func (r *Runner) SignalsRTHSuppressed() int64 {
	return r.signalsRTHSuppressed.Load()
}

// IndicatorSnapshotFunc maps a market bar to indicator data.
// Used for warmup without introducing an import cycle with the monitor package.
type IndicatorSnapshotFunc func(domain.MarketBar) start.IndicatorData

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
		eventBus:    eventBus,
		router:      router,
		logger:      logger.With("component", "strategy_runner"),
		tenantID:    tenantID,
		envMode:     envMode,
		indicators:  make(map[string]start.IndicatorData),
		indLogOnce:  make(map[string]bool),
		aggregators: make(map[string]*domain.BarAggregator),
	}
}

// Router returns the underlying router for registration.
func (r *Runner) Router() *Router { return r.router }

// SetSwapManager attaches a SwapManager to feed shadow instances during bar processing.
func (r *Runner) SetSwapManager(sm *SwapManager) { r.swapManager = sm }

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (r *Runner) SetMetrics(m *metrics.Metrics) { r.metrics = m }

func (r *Runner) SetPositionLookup(fn PositionLookupFunc) { r.posLookup = fn }

// InitAggregators creates BarAggregators for all non-1m timeframes needed by registered instances.
// Must be called after all instances are registered and before Start().
func (r *Runner) InitAggregators(sessionOpen time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, inst := range r.router.AllInstances() {
		tfs := inst.Assignment().Timeframes
		if len(tfs) == 0 {
			tfs = []string{"1m"}
		}
		for _, tf := range tfs {
			if tf == "1m" {
				continue
			}
			for _, sym := range inst.Assignment().Symbols {
				key := sym + ":" + tf
				if _, exists := r.aggregators[key]; exists {
					continue
				}
				domSym := domain.Symbol(sym)
				domTF := domain.Timeframe(tf)
				var agg *domain.BarAggregator
				var err error
				if domSym.IsCryptoSymbol() {
					agg, err = domain.NewClockAlignedAggregator(domSym, domTF)
				} else {
					agg, err = domain.NewBarAggregator(domSym, domTF, sessionOpen)
				}
				if err != nil {
					r.logger.Error("failed to create aggregator", "symbol", sym, "timeframe", tf, "error", err)
					continue
				}
				r.aggregators[key] = agg
				r.logger.Info("HTF aggregator created", "symbol", sym, "timeframe", tf)
			}
		}
	}
}

// Start subscribes the runner to MarketBarSanitized, StateUpdated, FillReceived,
// and OrderIntentRejected events on the event bus.
func (r *Runner) Start(ctx context.Context) error {
	if err := r.eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, r.handleBar); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to MarketBarSanitized: %w", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventStateUpdated, r.handleStateUpdated); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to StateUpdated: %w", err)
	}
	if err := r.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, r.handleFill); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to FillReceived: %w", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventOrderIntentRejected, r.handleRejection); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to OrderIntentRejected: %w", err)
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
	r.indicators[snap.Symbol.String()] = start.IndicatorData{
		RSI:           snap.RSI,
		StochK:        snap.StochK,
		StochD:        snap.StochD,
		EMA9:          snap.EMA9,
		EMA21:         snap.EMA21,
		EMA50:         snap.EMA50,
		EMAFast:       snap.EMAFast,
		EMASlow:       snap.EMASlow,
		EMAFastPeriod: snap.EMAFastPeriod,
		EMASlowPeriod: snap.EMASlowPeriod,
		VWAP:          snap.VWAP,
		Volume:        snap.Volume,
		VolumeSMA:     snap.VolumeSMA,
		ATR:           snap.ATR,
		VWAPSD:        snap.VWAPSD,
		AnchorRegimes: convertAnchorRegimes(snap.AnchorRegimes),
		HTF:           convertHTFData(snap.HTF),
	}
	r.mu.Unlock()
	return nil
}

func convertAnchorRegimes(regimes map[domain.Timeframe]domain.MarketRegime) map[string]start.AnchorRegime {
	if len(regimes) == 0 {
		return nil
	}
	result := make(map[string]start.AnchorRegime, len(regimes))
	for tf, r := range regimes {
		result[tf.String()] = start.AnchorRegime{
			Type:     r.Type.String(),
			Strength: r.Strength,
		}
	}
	return result
}

func convertHTFData(htf map[domain.Timeframe]domain.HTFData) map[string]start.HTFIndicator {
	if len(htf) == 0 {
		return nil
	}
	result := make(map[string]start.HTFIndicator, len(htf))
	for tf, d := range htf {
		result[tf.String()] = start.HTFIndicator{
			EMA50:  d.EMA50,
			EMA200: d.EMA200,
			Bias:   d.Bias,
		}
	}
	return result
}

// handleBar processes a MarketBarSanitized event by routing to assigned instances.
// 1m bars go directly to 1m-configured instances (zero behavioral change).
// For HTF instances, bars are aggregated via BarAggregator and delivered on completion.
func (r *Runner) handleBar(ctx context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("strategy runner: payload is not a MarketBar, got %T", event.Payload)
	}

	loopStart := time.Now()
	symbol := bar.Symbol.String()
	instances := r.router.InstancesForSymbol(symbol)
	if len(instances) == 0 {
		r.logger.Info("no instances for symbol", "symbol", symbol)
		return nil
	}

	r.mu.Lock()
	indicators := r.indicators[symbol]
	indicators.Volume = bar.Volume
	if !r.indLogOnce[symbol] {
		if indicators.RSI == 0 || indicators.VolumeSMA == 0 {
			r.logger.Debug("indicators may not be populated yet",
				"symbol", symbol,
				"rsi", indicators.RSI,
				"volumeSMA", indicators.VolumeSMA,
			)
			r.indLogOnce[symbol] = true
		}
	}
	r.mu.Unlock()

	r.mu.Lock()

	var oneMinInstances []*Instance
	htfNeeded := make(map[string][]*Instance)
	for _, inst := range instances {
		tfs := inst.Assignment().Timeframes
		if len(tfs) == 0 {
			tfs = []string{"1m"}
		}
		for _, tf := range tfs {
			if tf == "1m" {
				oneMinInstances = append(oneMinInstances, inst)
			} else {
				htfNeeded[tf] = append(htfNeeded[tf], inst)
			}
		}
	}

	sBar := domainBarToStratBar(bar)
	var allSignals []start.Signal

	for _, inst := range oneMinInstances {
		instCtx := &instanceContext{
			now:    bar.Time,
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
			continue
		}
		allSignals = append(allSignals, signals...)
	}

	for tf, htfInsts := range htfNeeded {
		key := symbol + ":" + tf
		agg, ok := r.aggregators[key]
		if !ok {
			continue
		}
		closed, emitted := agg.Push(bar)
		if !emitted {
			continue
		}
		htfBar := domainBarToStratBar(closed)
		for _, inst := range htfInsts {
			instCtx := &instanceContext{
				now:    closed.Time,
				logger: r.logger.With("instance_id", inst.ID().String(), "symbol", symbol),
				emit: func(evt any) error {
					return r.emitDomainEvent(ctx, event.TenantID, event.EnvMode, evt)
				},
			}
			signals, err := inst.OnBar(instCtx, symbol, htfBar, indicators)
			if err != nil {
				r.logger.Error("instance OnBar failed (HTF)",
					"instance_id", inst.ID().String(),
					"symbol", symbol,
					"timeframe", tf,
					"error", err,
				)
				continue
			}
			allSignals = append(allSignals, signals...)
		}
	}

	if r.swapManager != nil {
		swapCtx := &instanceContext{
			now:    bar.Time,
			logger: r.logger.With("symbol", symbol),
			emit:   func(_ any) error { return nil },
		}
		r.swapManager.OnBarProcessed(swapCtx, symbol, sBar, indicators)
	}

	allSignals = ReconcileSignals(allSignals, r.posLookup, r.logger)
	allSignals = r.filterByAllowedDirections(allSignals)

	// Unlock BEFORE signal emission. The emitSignal cascade can trigger sync
	// handlers (e.g. handleRejection) that also acquire r.mu — holding the lock
	// here would cause a self-deadlock. All state reads/writes are complete.
	r.mu.Unlock()

	r.logger.Info("bar processed",
		"symbol", symbol,
		"instances_1m", len(oneMinInstances),
		"htf_timeframes", len(htfNeeded),
		"signals", len(allSignals),
		"rsi", indicators.RSI,
		"volumeSMA", indicators.VolumeSMA,
		"volume", bar.Volume,
		"close", bar.Close,
	)

	for _, sig := range allSignals {
		if !domain.Symbol(sig.Symbol).IsCryptoSymbol() {
			cal := domain.CalendarFor(domain.AssetClassEquity)
			if !cal.IsOpen(bar.Time) {
				r.signalsRTHSuppressed.Add(1)
				r.logger.Info("suppressing equity signal outside RTH",
					"symbol", sig.Symbol,
					"bar_time", bar.Time,
				)
				if sig.Type == start.SignalEntry {
					if inst, ok := r.router.Instance(sig.StrategyInstanceID); ok {
						instCtx := &instanceContext{
							now:    bar.Time,
							logger: r.logger.With("instance_id", sig.StrategyInstanceID.String(), "symbol", sig.Symbol),
							emit:   func(_ any) error { return nil },
						}
						rejection := start.EntryRejection{Symbol: sig.Symbol, Side: sig.Side, Reason: "outside RTH"}
						_, _ = inst.OnEvent(instCtx, sig.Symbol, rejection)
					}
				}
				continue
			}
		}

		if !sig.Type.IsActionable() {
			continue
		}
		if r.metrics != nil {
			strategyLabel := "unknown"
			if sid, ok := parseStrategyIDFromInstance(sig.StrategyInstanceID); ok {
				strategyLabel = sid.String()
			}
			r.metrics.Strategy.SignalsTotal.WithLabelValues(strategyLabel, string(sig.Type), string(sig.Side)).Inc()
		}
		if err := r.emitSignal(ctx, event.TenantID, event.EnvMode, sig); err != nil {
			r.logger.Error("failed to emit SignalCreated",
				"instance_id", sig.StrategyInstanceID.String(),
				"symbol", sig.Symbol,
				"error", err,
			)
		}
	}

	if r.metrics != nil {
		r.metrics.Strategy.LoopDuration.WithLabelValues("all", "handle_bar").Observe(time.Since(loopStart).Seconds())
	}

	return nil
}

// ProcessBar allows direct bar processing without going through the event bus.
// Useful for testing and warmup scenarios.
func (r *Runner) ProcessBar(ctx context.Context, symbol string, bar start.Bar, indicators start.IndicatorData) ([]start.Signal, error) {
	instances := r.router.InstancesForSymbol(symbol)
	if len(instances) == 0 {
		return nil, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var allSignals []start.Signal

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

// WarmUp replays 1m historical bars through matching 1m instances for warmup.
// Backward-compatible wrapper around WarmUpTF.
func (r *Runner) WarmUp(symbol string, bars []domain.MarketBar, snapshotFn IndicatorSnapshotFunc) int {
	return r.WarmUpTF(symbol, "1m", bars, snapshotFn)
}

// WarmUpTF replays historical bars of a specific timeframe through matching instances.
// Only instances configured for the given timeframe will receive the bars.
func (r *Runner) WarmUpTF(symbol string, tf string, bars []domain.MarketBar, snapshotFn IndicatorSnapshotFunc) int {
	if len(bars) == 0 {
		return 0
	}
	instances := r.router.InstancesForSymbol(symbol)
	if len(instances) == 0 {
		return 0
	}

	var matched []*Instance
	for _, inst := range instances {
		tfs := inst.Assignment().Timeframes
		if len(tfs) == 0 {
			tfs = []string{"1m"}
		}
		for _, itf := range tfs {
			if itf == tf {
				matched = append(matched, inst)
				break
			}
		}
	}
	if len(matched) == 0 {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var lastIndicators start.IndicatorData
	for _, bar := range bars {
		indicators := snapshotFn(bar)
		indicators.Volume = bar.Volume
		lastIndicators = indicators

		sBar := domainBarToStratBar(bar)
		for _, inst := range matched {
			instCtx := &instanceContext{
				now:    bar.Time,
				logger: r.logger.With("instance_id", inst.ID().String(), "symbol", symbol),
				emit:   func(_ any) error { return nil },
			}
			if err := inst.WarmupOnBar(instCtx, symbol, sBar, indicators); err != nil {
				r.logger.Error("instance WarmupOnBar failed",
					"instance_id", inst.ID().String(),
					"symbol", symbol,
					"error", err,
				)
			}
		}
	}

	r.indicators[symbol] = lastIndicators

	for _, inst := range matched {
		inst.ClearPendingState(symbol)
	}

	return len(bars)
}

func (r *Runner) ClearAllPendingStates() {
	for _, inst := range r.router.AllInstances() {
		for _, sym := range inst.Assignment().Symbols {
			inst.ClearPendingState(sym)
		}
	}
}

func (r *Runner) filterByAllowedDirections(signals []start.Signal) []start.Signal {
	filtered := signals[:0]
	for _, sig := range signals {
		if sig.Type != start.SignalEntry {
			filtered = append(filtered, sig)
			continue
		}

		inst, ok := r.router.Instance(sig.StrategyInstanceID)
		if !ok {
			filtered = append(filtered, sig)
			continue
		}

		allowed := inst.Assignment().AllowedDirections
		if len(allowed) == 0 {
			filtered = append(filtered, sig)
			continue
		}

		direction := "LONG"
		if sig.Side == start.SideSell {
			direction = "SHORT"
		}

		ok = false
		for _, d := range allowed {
			if strings.EqualFold(d, direction) {
				ok = true
				break
			}
		}
		if ok {
			filtered = append(filtered, sig)
		} else {
			r.logger.Debug("filtered entry signal by allowed_directions",
				"symbol", sig.Symbol,
				"side", sig.Side,
				"direction", direction,
				"instance_id", sig.StrategyInstanceID.String(),
			)
		}
	}
	return filtered
}

// emitSignal publishes a SignalCreated domain event.
func (r *Runner) emitSignal(ctx context.Context, tenantID string, envMode domain.EnvMode, sig start.Signal) error {
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

// handleFill routes a FillReceived event to the matching strategy instance.
// The strategy uses this to confirm its entry and transition from PendingEntry
// to an actual PositionSide.
func (r *Runner) handleFill(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}

	symbol, _ := payload["symbol"].(string)
	strategyName, _ := payload["strategy"].(string)
	side, _ := payload["side"].(string)
	qty, _ := payload["quantity"].(float64)
	price, _ := payload["price"].(float64)

	if symbol == "" || strategyName == "" {
		return nil
	}

	inst := r.findInstanceByStrategyAndSymbol(strategyName, symbol)
	if inst == nil {
		r.logger.Debug("handleFill: no matching instance", "strategy", strategyName, "symbol", symbol)
		return nil
	}

	// Map side string to start.Side.
	var fillSide start.Side
	switch side {
	case "BUY":
		fillSide = start.SideBuy
	case "SELL":
		fillSide = start.SideSell
	default:
		r.logger.Warn("handleFill: unknown side", "side", side)
		return nil
	}

	instCtx := &instanceContext{
		now:    time.Now(),
		logger: r.logger.With("instance_id", inst.ID().String(), "symbol", symbol),
		emit:   func(_ any) error { return nil },
	}

	confirmation := start.FillConfirmation{
		Symbol:   symbol,
		Side:     fillSide,
		Quantity: qty,
		Price:    price,
	}

	r.mu.Lock()
	signals, err := inst.OnEvent(instCtx, symbol, confirmation)
	r.mu.Unlock()

	if err != nil {
		r.logger.Error("handleFill: OnEvent failed",
			"instance_id", inst.ID().String(),
			"symbol", symbol,
			"error", err,
		)
		return nil
	}

	_ = signals // Fill confirmations should not produce new signals.
	r.logger.Info("handleFill: routed to strategy",
		"instance_id", inst.ID().String(),
		"symbol", symbol,
		"side", side,
		"price", price,
	)
	return nil
}

// handleRejection routes an OrderIntentRejected event to the matching strategy
// instance. Only entry rejections (LONG, SHORT) are forwarded — exit rejections
// don't need feedback because re-emission on the next bar is the correct retry.
func (r *Runner) handleRejection(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.OrderIntentEventPayload)
	if !ok {
		return nil
	}

	// Only forward entry rejections. Exit rejections (CLOSE_LONG, CLOSE_SHORT)
	// don't need strategy feedback — the strategy will re-emit on next bar.
	var rejSide start.Side
	switch domain.Direction(payload.Direction) {
	case domain.DirectionLong:
		rejSide = start.SideBuy
	case domain.DirectionShort:
		rejSide = start.SideSell
	default:
		return nil // exit rejection — ignore
	}

	inst := r.findInstanceByStrategyAndSymbol(payload.Strategy, payload.Symbol)
	if inst == nil {
		r.logger.Debug("handleRejection: no matching instance", "strategy", payload.Strategy, "symbol", payload.Symbol)
		return nil
	}

	instCtx := &instanceContext{
		now:    time.Now(),
		logger: r.logger.With("instance_id", inst.ID().String(), "symbol", payload.Symbol),
		emit:   func(_ any) error { return nil },
	}

	rejection := start.EntryRejection{
		Symbol: payload.Symbol,
		Side:   rejSide,
		Reason: payload.Reason,
	}

	r.mu.Lock()
	signals, err := inst.OnEvent(instCtx, payload.Symbol, rejection)
	r.mu.Unlock()

	if err != nil {
		r.logger.Error("handleRejection: OnEvent failed",
			"instance_id", inst.ID().String(),
			"symbol", payload.Symbol,
			"error", err,
		)
		return nil
	}

	_ = signals // Entry rejections should not produce new signals.
	r.logger.Info("handleRejection: routed to strategy",
		"instance_id", inst.ID().String(),
		"symbol", payload.Symbol,
		"side", rejSide,
		"reason", payload.Reason,
	)
	return nil
}

func (r *Runner) findInstanceByStrategyAndSymbol(strategyName, symbol string) *Instance {
	instances := r.router.InstancesForSymbol(symbol)
	for _, inst := range instances {
		if inst.configStrategyID() == strategyName {
			return inst
		}
	}
	return nil
}

// domainBarToStratBar converts a domain.MarketBar to a strategy.Bar.
func domainBarToStratBar(bar domain.MarketBar) start.Bar {
	return start.Bar{
		Time:   bar.Time,
		Open:   bar.Open,
		High:   bar.High,
		Low:    bar.Low,
		Close:  bar.Close,
		Volume: bar.Volume,
	}
}

// StrategyInfo describes a registered strategy for the API.
type StrategyInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Symbols  []string `json:"symbols"`
	Priority int      `json:"priority"`
	Active   bool     `json:"active"`
}

func (r *Runner) ListStrategies() []StrategyInfo {
	instances := r.router.AllInstances()
	infos := make([]StrategyInfo, 0, len(instances))
	for _, inst := range instances {
		meta := inst.Strategy().Meta()
		infos = append(infos, StrategyInfo{
			ID:       inst.configStrategyID(),
			Name:     meta.Name,
			Version:  meta.Version.String(),
			Symbols:  inst.Assignment().Symbols,
			Priority: inst.Assignment().Priority,
			Active:   inst.IsActive(),
		})
	}
	return infos
}

func (r *Runner) StrategySnapshots(strategyID string) []domain.StateSnapshot {
	instances := r.router.AllInstances()
	var snaps []domain.StateSnapshot
	for _, inst := range instances {
		if inst.configStrategyID() != strategyID {
			continue
		}
		snaps = append(snaps, inst.AllSnapshots()...)
	}
	return snaps
}

func (r *Runner) StrategySnapshot(strategyID, symbol string) (domain.StateSnapshot, bool) {
	instances := r.router.AllInstances()
	for _, inst := range instances {
		if inst.configStrategyID() != strategyID {
			continue
		}
		if snap, ok := inst.Snapshot(symbol); ok {
			return snap, true
		}
	}
	return domain.StateSnapshot{}, false
}
