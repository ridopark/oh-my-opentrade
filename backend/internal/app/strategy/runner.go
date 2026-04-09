package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
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
	htfCalcs             map[string]*monitor.IndicatorCalculator // key: "symbol:tf"
	regimeDetector       *monitor.RegimeDetector
	anchorRegimes        map[string]domain.MarketRegime // key: "symbol:tf" → latest regime
	signalsRTHSuppressed atomic.Int64
	anchorResolver       func(symbol string, barTime time.Time, anchors []string) map[string]time.Time
	prevDayBarsFn        func(symbol string, since time.Time) []start.Bar
	keyLevelPricesFn     func(symbol string, barTime time.Time) map[string]float64
	keyLevelsBySymbol    map[string]map[string]float64
	aiAnchorResolver     *AIAnchorResolver
	lastSessionDate      map[string]string
	lastResolvedRegime   map[string]domain.RegimeType

	// Dark pool lookup for backtests: keyed by "symbol|5m-truncated-time".
	dpLookup map[DPLookupKey]domain.DarkPoolBar

	// Whale accumulation lookup: ticker -> latest score.
	whaleLookup map[string]domain.WhaleAccumulation

	// Signal progress cache: last emitted event per symbol for initial SSE snapshots.
	signalProgressMu    sync.RWMutex
	signalProgressCache map[string]domain.Event // key: eventType+":"+symbol
}

// DPLookupKey uniquely identifies a dark pool bar for O(1) access during replay.
type DPLookupKey struct {
	Symbol string
	Time   time.Time
}

func (r *Runner) SignalsRTHSuppressed() int64 {
	return r.signalsRTHSuppressed.Load()
}

func (r *Runner) SetAnchorResolver(fn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time) {
	r.anchorResolver = fn
	r.lastSessionDate = make(map[string]string)
}

func (r *Runner) SetPrevDayBarsFn(fn func(symbol string, since time.Time) []start.Bar) {
	r.prevDayBarsFn = fn
}

func (r *Runner) SetKeyLevelPricesFn(fn func(symbol string, barTime time.Time) map[string]float64) {
	r.keyLevelPricesFn = fn
	r.keyLevelsBySymbol = make(map[string]map[string]float64)
}

func (r *Runner) SetAIAnchorResolver(resolver *AIAnchorResolver) {
	r.aiAnchorResolver = resolver
	r.lastSessionDate = make(map[string]string)
	r.lastResolvedRegime = make(map[string]domain.RegimeType)

	resolver.SetApplyFn(func(symbol string, anchors map[string]time.Time) {
		for _, inst := range r.router.InstancesForSymbol(symbol) {
			st, ok := inst.GetState(symbol)
			if !ok {
				continue
			}
			if ar, ok := st.(anchorResettable); ok {
				ar.ResetAnchors(anchors)
				r.logger.Info("AI anchors hot-swapped", "symbol", symbol, "anchors", len(anchors))
			}
		}
	})
}

type anchorResettable interface {
	AnchorNames() []string
	ResetAnchors(map[string]time.Time)
}

// ResolveAnchorsForWarmup triggers anchor resolution for all given symbols.
// Called during startup to ensure AVWAP anchors are set before warmup bars are fed,
// so that mid-day restarts produce valid confluence scores immediately.
func (r *Runner) ResolveAnchorsForWarmup(symbols []string, barTime time.Time) {
	if r.anchorResolver == nil {
		return
	}
	loc := domain.NYLocation()
	dateStr := barTime.In(loc).Format("2006-01-02")
	for _, sym := range symbols {
		if r.lastSessionDate == nil {
			r.lastSessionDate = make(map[string]string)
		}
		r.lastSessionDate[sym] = dateStr
		r.resolveSessionAnchors(sym, barTime)
	}
}

func (r *Runner) resolveSessionAnchors(symbol string, barTime time.Time) {
	instances := r.router.InstancesForSymbol(symbol)
	for _, inst := range instances {
		st, ok := inst.GetState(symbol)
		if !ok {
			continue
		}
		if ar, ok := st.(anchorResettable); ok {
			names := ar.AnchorNames()
			resolved := r.anchorResolver(symbol, barTime, names)
			if len(resolved) > 0 {
				for name, t := range resolved {
					r.logger.Info("AVWAP anchor resolved", "symbol", symbol, "anchor", name, "anchor_time", t, "bar_time", barTime)
				}
				ar.ResetAnchors(resolved)
				// Replay previous day's bars from each anchor time to end of session.
				// Without this, pd_high/pd_low anchors activate on today's first bar
				// and produce identical AVWAP values as session_open.
				if r.prevDayBarsFn != nil {
					type anchorUpdater interface {
						UpdateCalcAnchor(name string, bar start.Bar)
					}
					if au, ok := st.(anchorUpdater); ok {
						// Sort for deterministic replay order
						sortedNames := make([]string, 0, len(resolved))
						for name := range resolved {
							if name != "session_open" {
								sortedNames = append(sortedNames, name)
							}
						}
						sort.Strings(sortedNames)
						for _, name := range sortedNames {
							anchorTime := resolved[name]
							prevBars := r.prevDayBarsFn(symbol, anchorTime)
							if len(prevBars) > 0 {
								r.logger.Info("replaying prev-day bars for anchor",
									"symbol", symbol, "anchor", name, "bars", len(prevBars),
									"from", prevBars[0].Time, "to", prevBars[len(prevBars)-1].Time)
								for _, b := range prevBars {
									au.UpdateCalcAnchor(name, b)
								}
							}
						}
					}
				}
				r.logger.Info("reset AVWAP anchors for new session", "symbol", symbol, "anchors", len(resolved))
			}

			// Set key levels for confluence scoring
			if r.keyLevelPricesFn != nil {
				type keyLevelSetter interface {
					SetKeyLevels(map[string]float64)
				}
				if kls, ok2 := st.(keyLevelSetter); ok2 {
					levels := r.keyLevelPricesFn(symbol, barTime)
					if levels != nil {
						kls.SetKeyLevels(levels)
						if r.keyLevelsBySymbol == nil {
							r.keyLevelsBySymbol = make(map[string]map[string]float64)
						}
						r.keyLevelsBySymbol[symbol] = levels
					}
				}
			}
		}
	}
}

func (r *Runner) resolveAIAnchors(ctx context.Context, symbol string, bar domain.MarketBar, opt AnchorResolveOption) {
	var regime domain.MarketRegime
	var indicators domain.IndicatorSnapshot

	if snap, ok := r.indicators[symbol]; ok {
		if ar, arOK := snap.AnchorRegimes["5m"]; arOK {
			regime = domain.MarketRegime{
				Symbol:   domain.Symbol(symbol),
				Type:     domain.RegimeType(ar.Type),
				Strength: ar.Strength,
			}
		}
	}

	var anchorNames []string
	instances := r.router.InstancesForSymbol(symbol)
	for _, inst := range instances {
		st, ok := inst.GetState(symbol)
		if !ok {
			continue
		}
		if ar, ok := st.(anchorResettable); ok {
			anchorNames = ar.AnchorNames()
			break
		}
	}

	resolved, err := r.aiAnchorResolver.ResolveAnchors(ctx, symbol, bar.Time, bar.Close, regime, indicators, anchorNames, opt)
	if err != nil {
		r.logger.Error("AI anchor resolution failed", "symbol", symbol, "error", err)
		return
	}
	if len(resolved) == 0 {
		r.logger.Warn("AI anchor resolution returned empty", "symbol", symbol, "bar_time", bar.Time)
		return
	}

	for name, t := range resolved {
		r.logger.Debug("resolved anchor", "symbol", symbol, "name", name, "anchor_time", t, "bar_time", bar.Time)
	}

	r.mu.Lock()
	r.lastResolvedRegime[symbol] = regime.Type
	r.mu.Unlock()

	for _, inst := range instances {
		st, ok := inst.GetState(symbol)
		if !ok {
			continue
		}
		if ar, ok := st.(anchorResettable); ok {
			// Merge AI-resolved anchors with existing session anchors.
			// Preserve configured anchors (session_open, pd_high, pd_low) that
			// were set during Init or resolveSessionAnchors. The AI resolver
			// only adds dynamic anchors on top.
			merged := make(map[string]time.Time)
			if r.anchorResolver != nil {
				names := ar.AnchorNames()
				for k, v := range r.anchorResolver(symbol, bar.Time, names) {
					merged[k] = v
				}
			}
			for k, v := range resolved {
				merged[k] = v
			}
			ar.ResetAnchors(merged)
			// Replay previous day's bars for non-session_open anchors
			if r.prevDayBarsFn != nil {
				type anchorUpdater interface {
					UpdateCalcAnchor(name string, bar start.Bar)
				}
				if au, ok2 := st.(anchorUpdater); ok2 {
					// Sort anchor names for deterministic replay order
					sortedNames := make([]string, 0, len(merged))
					for name := range merged {
						if name != "session_open" {
							sortedNames = append(sortedNames, name)
						}
					}
					sort.Strings(sortedNames)
					for _, name := range sortedNames {
						anchorTime := merged[name]
						prevBars := r.prevDayBarsFn(symbol, anchorTime)
						if len(prevBars) > 0 {
							r.logger.Info("replaying prev-day bars for anchor",
								"symbol", symbol, "anchor", name, "bars", len(prevBars),
								"from", prevBars[0].Time, "to", prevBars[len(prevBars)-1].Time)
							for _, b := range prevBars {
								au.UpdateCalcAnchor(name, b)
							}
						}
					}
				}
			}
			r.logger.Info("AI anchor resolution complete", "symbol", symbol, "anchors", len(merged))
		}

		// Set key levels for confluence scoring
		if r.keyLevelPricesFn != nil {
			type keyLevelSetter interface {
				SetKeyLevels(map[string]float64)
			}
			if kls, ok2 := st.(keyLevelSetter); ok2 {
				levels := r.keyLevelPricesFn(symbol, bar.Time)
				if levels != nil {
					kls.SetKeyLevels(levels)
					if r.keyLevelsBySymbol == nil {
						r.keyLevelsBySymbol = make(map[string]map[string]float64)
					}
					r.keyLevelsBySymbol[symbol] = levels
				}
			}
		}
	}
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
		indicators:     make(map[string]start.IndicatorData),
		indLogOnce:     make(map[string]bool),
		aggregators:    make(map[string]*domain.BarAggregator),
		htfCalcs:       make(map[string]*monitor.IndicatorCalculator),
		regimeDetector: monitor.NewRegimeDetector(),
		anchorRegimes:  make(map[string]domain.MarketRegime),
	}
}

// Router returns the underlying router for registration.
func (r *Runner) Router() *Router { return r.router }

// SetSwapManager attaches a SwapManager to feed shadow instances during bar processing.
func (r *Runner) SetSwapManager(sm *SwapManager) { r.swapManager = sm }

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (r *Runner) SetMetrics(m *metrics.Metrics) { r.metrics = m }

func (r *Runner) SetPositionLookup(fn PositionLookupFunc) { r.posLookup = fn }

// SetDarkPoolLookup injects pre-loaded dark pool bars for backtesting.
// The strategy runner overlays DP data onto IndicatorData during bar processing.
func (r *Runner) SetDarkPoolLookup(lookup map[DPLookupKey]domain.DarkPoolBar) {
	r.dpLookup = lookup
}

// SetWhaleLookup provides whale accumulation scores for 13F confluence.
func (r *Runner) SetWhaleLookup(lookup map[string]domain.WhaleAccumulation) {
	r.whaleLookup = lookup
}

// UpdateAVWAPCalc feeds a 1m bar into the AVWAP calculator for smooth chart
// rendering. Also evaluates exit-only logic on 1m bars for faster exit
// reaction (per Brian Shannon: fine-tune exits on short-term chart).
func (r *Runner) UpdateAVWAPCalc(symbol string, bar start.Bar) []start.Signal {
	type avwapUpdater interface {
		UpdateCalc(bar start.Bar)
	}
	type avwap1mExitChecker interface {
		CheckExitsOn1m(symbol string, bar start.Bar) []start.Signal
	}
	instances := r.router.InstancesForSymbol(symbol)
	for _, inst := range instances {
		st, ok := inst.GetState(symbol)
		if !ok {
			continue
		}
		if u, ok := st.(avwapUpdater); ok {
			u.UpdateCalc(bar)
			// Also check exits on 1m for faster reaction
			if checker, ok2 := st.(avwap1mExitChecker); ok2 {
				if sigs := checker.CheckExitsOn1m(symbol, bar); len(sigs) > 0 {
					return sigs
				}
			}
			return nil
		}
	}
	return nil
}

// GetAVWAPValues returns the current anchored VWAP values for a symbol
// by inspecting the strategy instance state. Returns nil if no AVWAP
// strategy is active for this symbol.
func (r *Runner) GetAVWAPValues(symbol string) map[string]float64 {
	instances := r.router.InstancesForSymbol(symbol)
	for _, inst := range instances {
		st, ok := inst.GetState(symbol)
		if !ok {
			continue
		}
		type avwapValuer interface {
			AVWAPValues() map[string]float64
		}
		if av, ok := st.(avwapValuer); ok {
			return av.AVWAPValues()
		}
	}
	return nil
}

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
	if err := r.eventBus.Subscribe(ctx, domain.EventFillReceived, r.handleFill); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to FillReceived: %w", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventOrderIntentRejected, r.handleRejection); err != nil {
		return fmt.Errorf("strategy runner: failed to subscribe to OrderIntentRejected: %w", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventAuctionImbalance, r.handleAuctionImbalance); err != nil {
		r.logger.Warn("failed to subscribe to AuctionImbalance (non-fatal)", "error", err)
		// Non-fatal: backtests won't have this event
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
		EMA200:        snap.EMA200,
		BBUpper:       snap.BBUpper,
		BBMiddle:      snap.BBMiddle,
		BBLower:       snap.BBLower,
		BBPercentB:    snap.BBPercentB,
		BBBandwidth:   snap.BBBandwidth,
		MACDLine:      snap.MACDLine,
		MACDSignal:    snap.MACDSignal,
		MACDHistogram: snap.MACDHistogram,
		ADX:           snap.ADX,
		RegimeScore:   snap.RegimeScore,
		AnchorRegimes: convertAnchorRegimes(snap.AnchorRegimes),
		HTF:           convertHTFData(snap.HTF),
	}

	// Overlay dark pool microstructure data when available (backtest only).
	if len(r.dpLookup) > 0 {
		barTime5m := snap.Time.Truncate(5 * time.Minute)
		key := DPLookupKey{Symbol: snap.Symbol.String(), Time: barTime5m}
		if dpBar, ok := r.dpLookup[key]; ok {
			ind := r.indicators[snap.Symbol.String()]
			ind.DPRatio = dpBar.DPRatio
			if dpBar.DPVolume > 0 {
				ind.DPBuyRatio = dpBar.BuyVolume / dpBar.DPVolume
			}
			if dpBar.DPVolume > 0 {
				ind.DPLargePrintPct = dpBar.LargePrintVolume / dpBar.DPVolume
			}
			r.indicators[snap.Symbol.String()] = ind
		}
	}

	// Overlay whale accumulation score when available.
	if len(r.whaleLookup) > 0 {
		sym := snap.Symbol.String()
		if wa, ok := r.whaleLookup[sym]; ok {
			ind := r.indicators[sym]
			ind.WhaleScore = wa.Score
			r.indicators[sym] = ind
		}
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

// splitSymbolTF splits a "SYMBOL:TF" key into [symbol, tf].
func splitSymbolTF(key string) [2]string {
	for i, c := range key {
		if c == ':' {
			return [2]string{key[:i], key[i+1:]}
		}
	}
	return [2]string{key, ""}
}

// collectAnchorRegimes builds AnchorRegimes map for a symbol from stored regimes.
func (r *Runner) collectAnchorRegimes(symbol string) map[string]start.AnchorRegime {
	result := make(map[string]start.AnchorRegime)
	for key, reg := range r.anchorRegimes {
		parts := splitSymbolTF(key)
		if parts[0] == symbol {
			result[parts[1]] = start.AnchorRegime{
				Type:     string(reg.Type),
				Strength: reg.Strength,
			}
		}
	}
	if len(result) == 0 {
		return nil
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

	if r.aiAnchorResolver != nil {
		r.aiAnchorResolver.OnBar(symbol, start.Bar{
			Time: bar.Time, Open: bar.Open, High: bar.High,
			Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
		}, string(bar.Timeframe))

		loc := domain.NYLocation()
		barDate := bar.Time.In(loc).Format("2006-01-02")

		r.mu.Lock()
		newSession := r.lastSessionDate[symbol] != barDate
		if newSession {
			r.lastSessionDate[symbol] = barDate
		}
		r.mu.Unlock()

		if newSession {
			r.resolveAIAnchors(ctx, symbol, bar, AnchorResolveOption{SyncAI: true})
		}
	} else if r.anchorResolver != nil {
		loc := domain.NYLocation()
		barDate := bar.Time.In(loc).Format("2006-01-02")
		if r.lastSessionDate[symbol] != barDate {
			r.lastSessionDate[symbol] = barDate
			r.resolveSessionAnchors(symbol, bar.Time)
		}
	}

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

	// Feed only 1m bars to the AVWAP calculator for smooth chart rendering.
	// Also evaluates exit-only logic on 1m for faster exit reaction.
	// The monitor re-publishes aggregated HTF bars (5m, 15m, etc.) as
	// EventMarketBarSanitized — processing those would double-count PV/V.
	var exitSignals1m []start.Signal
	if bar.Timeframe == "1m" {
		exitSignals1m = r.UpdateAVWAPCalc(symbol, domainBarToStratBar(bar))
	}

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
	// Add any exit signals from 1m AVWAP exit evaluation
	allSignals = append(allSignals, exitSignals1m...)

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

		// Compute indicators from the aggregated HTF bar (not 1m indicators).
		htfCalc, ok := r.htfCalcs[key]
		if !ok {
			htfCalc = monitor.NewIndicatorCalculator()
			r.htfCalcs[key] = htfCalc
		}
		htfSnap := htfCalc.Update(closed)

		// Compute and store anchor regime for this HTF bar
		regime, _ := r.regimeDetector.Detect(htfSnap)
		r.anchorRegimes[key] = regime

		htfIndicators := start.IndicatorData{
			RSI:           htfSnap.RSI,
			StochK:        htfSnap.StochK,
			StochD:        htfSnap.StochD,
			EMA9:          htfSnap.EMA9,
			EMA21:         htfSnap.EMA21,
			EMA50:         htfSnap.EMA50,
			EMAFast:       htfSnap.EMAFast,
			EMASlow:       htfSnap.EMASlow,
			EMAFastPeriod: htfSnap.EMAFastPeriod,
			EMASlowPeriod: htfSnap.EMASlowPeriod,
			VWAP:          htfSnap.VWAP,
			Volume:        htfSnap.Volume,
			VolumeSMA:     htfSnap.VolumeSMA,
			ATR:           htfSnap.ATR,
			VWAPSD:        htfSnap.VWAPSD,
			EMA200:        htfSnap.EMA200,
			BBUpper:       htfSnap.BBUpper,
			BBMiddle:      htfSnap.BBMiddle,
			BBLower:       htfSnap.BBLower,
			BBPercentB:    htfSnap.BBPercentB,
			BBBandwidth:   htfSnap.BBBandwidth,
			MACDLine:      htfSnap.MACDLine,
			MACDSignal:    htfSnap.MACDSignal,
			MACDHistogram: htfSnap.MACDHistogram,
			ADX:           htfSnap.ADX,
			RegimeScore:   htfSnap.RegimeScore,
			AnchorRegimes: r.collectAnchorRegimes(symbol),
			HTF:           indicators.HTF, // preserve daily HTF data from 1m pipeline
		}

		for _, inst := range htfInsts {
			instCtx := &instanceContext{
				now:    closed.Time,
				logger: r.logger.With("instance_id", inst.ID().String(), "symbol", symbol),
				emit: func(evt any) error {
					return r.emitDomainEvent(ctx, event.TenantID, event.EnvMode, evt)
				},
			}
			signals, err := inst.OnBar(instCtx, symbol, htfBar, htfIndicators)
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

	allSignals = r.filterByAllowedDirections(allSignals)
	allSignals = ReconcileSignals(allSignals, r.posLookup, r.logger)

	// Unlock BEFORE signal emission. The emitSignal cascade can trigger sync
	// handlers (e.g. handleRejection) that also acquire r.mu — holding the lock
	// here would cause a self-deadlock. All state reads/writes are complete.
	r.mu.Unlock()

	r.logger.Debug("bar processed",
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
		r.logger.Info("EMIT SIGNAL", "symbol", sig.Symbol, "type", sig.Type, "side", sig.Side, "instance", sig.StrategyInstanceID.String(),
			"setup", sig.Tags["setup"], "confluence", sig.Tags["confluence"], "confluence_detail", sig.Tags["confluence_detail"])
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
		instCtx := &instanceContext{
			now:    bar.Time, // use bar time, not wall clock — deterministic in backtests
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
			now:    bar.Time,
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

// WarmUpHTF aggregates 1m warmup bars into each HTF timeframe required by
// registered instances and feeds the resulting candles through WarmUpTF.
// Must be called AFTER InitAggregators.
func (r *Runner) WarmUpHTF(symbol string, bars1m []domain.MarketBar, snapshotFn IndicatorSnapshotFunc, loc *time.Location) {
	if len(bars1m) == 0 {
		return
	}

	// Collect unique HTF timeframes needed for this symbol.
	htfSet := make(map[string]struct{})
	for _, inst := range r.router.InstancesForSymbol(symbol) {
		for _, tf := range inst.Assignment().Timeframes {
			if tf != "1m" {
				htfSet[tf] = struct{}{}
			}
		}
	}
	if len(htfSet) == 0 {
		return
	}

	domSym := domain.Symbol(symbol)
	isCrypto := domSym.IsCryptoSymbol()

	// Derive the session open from the first warmup bar's trading day so that
	// bars from prior sessions can be aggregated correctly.
	firstET := bars1m[0].Time.In(loc)
	warmupSessionOpen := time.Date(firstET.Year(), firstET.Month(), firstET.Day(), 9, 30, 0, 0, loc)

	for tf := range htfSet {
		var agg *domain.BarAggregator
		var err error
		if isCrypto {
			agg, err = domain.NewClockAlignedAggregator(domSym, domain.Timeframe(tf))
		} else {
			agg, err = domain.NewBarAggregator(domSym, domain.Timeframe(tf), warmupSessionOpen)
		}
		if err != nil {
			r.logger.Error("WarmUpHTF: failed to create aggregator", "symbol", symbol, "tf", tf, "error", err)
			continue
		}

		var htfBars []domain.MarketBar
		for _, bar := range bars1m {
			closed, emitted := agg.Push(bar)
			if emitted {
				htfBars = append(htfBars, closed)
			}
		}

		if len(htfBars) > 0 {
			r.logger.Info("WarmUpHTF: aggregated warmup bars", "symbol", symbol, "tf", tf, "bars_1m", len(bars1m), "bars_htf", len(htfBars))
			r.WarmUpTF(symbol, tf, htfBars, snapshotFn)
		}
	}
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
			r.logger.Info("filtered entry signal by allowed_directions, sending rejection",
				"symbol", sig.Symbol,
				"side", sig.Side,
				"direction", direction,
				"instance_id", sig.StrategyInstanceID.String(),
			)
			rejection := start.EntryRejection{
				Symbol: sig.Symbol,
				Side:   sig.Side,
				Reason: "direction " + direction + " not in allowed_directions",
			}
			instCtx := &instanceContext{
				now:    time.Time{},
				logger: r.logger.With("instance_id", sig.StrategyInstanceID.String(), "symbol", sig.Symbol),
				emit:   func(_ any) error { return nil },
			}
			if _, err := inst.OnEvent(instCtx, sig.Symbol, rejection); err != nil {
				r.logger.Warn("failed to send direction rejection", "symbol", sig.Symbol, "error", err)
			}
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
// Known payload types (EntryGatedPayload, ORBPhaseUpdatePayload) are routed to
// their specific event types; all others use the generic StrategyDomainEvent type.
func (r *Runner) emitDomainEvent(ctx context.Context, tenantID string, envMode domain.EnvMode, payload any) error {
	eventType := domain.EventType("StrategyDomainEvent")
	var cacheKey string
	switch p := payload.(type) {
	case domain.EntryGatedPayload:
		eventType = domain.EventEntryGated
		cacheKey = "EntryGated:" + p.Symbol
	case domain.ORBPhaseUpdatePayload:
		eventType = domain.EventORBPhaseUpdate
		cacheKey = "ORBPhaseUpdate:" + p.Symbol
	}
	ev, err := domain.NewEvent(
		eventType,
		tenantID,
		envMode,
		uuid.NewString(),
		payload,
	)
	if err != nil {
		return err
	}
	// Cache for initial SSE snapshots.
	if cacheKey != "" {
		r.signalProgressMu.Lock()
		if r.signalProgressCache == nil {
			r.signalProgressCache = make(map[string]domain.Event)
		}
		r.signalProgressCache[cacheKey] = *ev
		r.signalProgressMu.Unlock()
	}
	return r.eventBus.Publish(ctx, *ev)
}

// SignalProgressSnapshots returns cached EntryGated and ORBPhaseUpdate events
// for all symbols. Used by the SSE handler to send initial state on client connect.
func (r *Runner) SignalProgressSnapshots() []domain.Event {
	r.signalProgressMu.RLock()
	defer r.signalProgressMu.RUnlock()
	events := make([]domain.Event, 0, len(r.signalProgressCache))
	for _, ev := range r.signalProgressCache {
		events = append(events, ev)
	}
	return events
}

// FlushSignalProgress iterates all strategy instances after warmup and emits
// signal progress events (EntryGated, ORBPhaseUpdate) to seed the SSE cache.
// This ensures the dashboard has data immediately without waiting for the first live bar.
func (r *Runner) FlushSignalProgress() {
	ctx := context.Background()
	for _, inst := range r.router.AllInstances() {
		for _, sym := range inst.Assignment().Symbols {
			st, ok := inst.GetState(sym)
			if !ok {
				continue
			}
			emitter, ok := st.(start.SignalProgressEmitter)
			if !ok {
				continue
			}
			for _, payload := range emitter.EmitSignalProgress() {
				_ = r.emitDomainEvent(ctx, r.tenantID, r.envMode, payload)
			}
		}
	}
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
	filledAt, _ := payload["filled_at"].(time.Time)
	if filledAt.IsZero() {
		filledAt = time.Now()
	}

	if symbol == "" || strategyName == "" {
		return nil
	}

	// Resolve OCC option symbol to underlying for strategy routing.
	routingSymbol := symbol
	if underlying := domain.UnderlyingFromOCC(domain.Symbol(symbol)); underlying != "" {
		routingSymbol = string(underlying)
	}

	inst := r.findInstanceByStrategyAndSymbol(strategyName, routingSymbol)
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
		now:    filledAt,
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

	rejSymbol := payload.Symbol
	if underlying := domain.UnderlyingFromOCC(domain.Symbol(rejSymbol)); underlying != "" {
		rejSymbol = string(underlying)
	}
	inst := r.findInstanceByStrategyAndSymbol(payload.Strategy, rejSymbol)
	if inst == nil {
		r.logger.Debug("handleRejection: no matching instance", "strategy", payload.Strategy, "symbol", rejSymbol)
		return nil
	}

	instCtx := &instanceContext{
		now:    time.Now(), // rejection timing is not critical for determinism
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

// handleAuctionImbalance routes NYSE closing auction imbalance data to all
// strategy instances subscribed to the given symbol.
func (r *Runner) handleAuctionImbalance(_ context.Context, event domain.Event) error {
	snap, ok := event.Payload.(domain.AuctionImbalanceSnapshot)
	if !ok {
		return nil
	}
	symbol := snap.Symbol.String()
	instances := r.router.InstancesForSymbol(symbol)

	update := start.AuctionImbalanceUpdate{
		Symbol:    symbol,
		Volume:    snap.Volume,
		Price:     snap.Price,
		Imbalance: snap.Imbalance,
	}

	instCtx := &instanceContext{
		now:    snap.Time,
		logger: r.logger.With("symbol", symbol),
		emit:   func(_ any) error { return nil },
	}

	r.mu.Lock()
	for _, inst := range instances {
		if _, err := inst.OnEvent(instCtx, symbol, update); err != nil {
			r.logger.Error("handleAuctionImbalance: OnEvent failed",
				"instance_id", inst.ID().String(),
				"symbol", symbol,
				"error", err,
			)
		}
	}
	r.mu.Unlock()
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
