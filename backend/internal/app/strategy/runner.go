package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/gate"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/warmup"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/observability/parity"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type htfClose struct {
	tf     string
	closed domain.MarketBar
	snap   domain.IndicatorSnapshot
}

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
	indicator            *indicator.Service
	// htfUnsubs holds the indicator.Service unsubscribe closures for each
	// (sym, tf) registered in InitAggregators. Re-init paths drop the old
	// subscriptions before re-registering so callbacks cannot leak.
	htfUnsubs map[string]func()
	// htfPending collects (closed HTF bar, snap) tuples produced by
	// indicator.Service Subscribe callbacks. The indicator service drives
	// its own Update lifecycle (subscribes to MarketBarSanitized first) and
	// fires HTF callbacks on bucket close — those callbacks write here, on
	// the indicator handler's goroutine. handleBarCore runs LATER on the
	// same goroutine (synchronous bus dispatch) and drains the slice during
	// HTF strategy fan-out at runner.go:~1725.
	htfPending []htfClose
	warmedHTFKeys        map[string]bool // "symbol:tf" → true; idempotent guard for WarmUpTF
	htfLabelSuffix       string          // empty in live; "_backtest_<id>" when TagBacktest called
	regimeDetector       *monitor.RegimeDetector
	anchorRegimes          map[string]map[string]domain.MarketRegime   // symbol → tf → latest regime
	collectedAnchorRegimes map[string]map[string]start.AnchorRegime   // per-symbol reusable result map
	signalsRTHSuppressed atomic.Int64
	anchorResolver       func(symbol string, barTime time.Time, anchors []string) map[string]time.Time
	sessionRefresher     func(symbol string, barTime time.Time)
	prevDayBarsFn        func(symbol string, since, until time.Time) []start.Bar
	keyLevelPricesFn     func(symbol string, barTime time.Time) map[string]float64
	keyLevelsBySymbol    map[string]map[string]float64
	aiAnchorResolver     *AIAnchorResolver
	lastSessionDate      map[string]int
	lastResolvedRegime   map[string]domain.RegimeType

	// Dark pool lookup for backtests: keyed by "symbol|5m-truncated-time".
	// dpSource is the runner's read port for dark-pool 5m bars. NewRunner
	// installs noopDPSource so it is never nil. SetDarkPoolLookup wraps a
	// map in staticDPSource for the backtest path; SetDarkPoolSource takes
	// the live aggregator (Phase 4 of the parity plan) directly. The two
	// setters are mutually exclusive — last write wins.
	dpSource DPSource

	// dpRolling maintains per-symbol rolling statistics for DP ratio Z-score computation.
	dpRolling map[string]*dpRollingStats

	// Late-session DP Z-score: daily signal derived from 14:00-15:30 ET buy ratio.
	// Computed once per trading day from previous day's DP bars.
	lateSessionDPZ       map[string]float64         // sym → current day's late Z (buy ratio)
	lateSessionDPRolling map[string]*dpRollingStats  // sym → 20-day rolling late buy ratios
	lateSessionDPDate    map[string]int              // sym → yyyymmdd of last computed day
	lateSessionLPZ       map[string]float64         // sym → current day's late large-print imbalance Z
	lateSessionLPRolling map[string]*dpRollingStats  // sym → 20-day rolling late LP imbalance
	lateSessionNFZ       map[string]float64         // sym → current day's late net-flow Z
	lateSessionNFRolling map[string]*dpRollingStats  // sym → 20-day rolling late net flow
	lateSessionDPVolRatioZ       map[string]float64         // sym → current day's late DP volume ratio Z
	lateSessionDPVolRatioRolling map[string]*dpRollingStats  // sym → 20-day rolling late DP vol ratio

	// Whale accumulation lookup: ticker -> latest score.
	whaleLookup map[string]domain.WhaleAccumulation

	// Signal progress cache: last emitted event per symbol for initial SSE snapshots.
	signalProgressMu    sync.RWMutex
	signalProgressCache map[string]domain.Event // key: eventType+":"+symbol

	// Health tracking: last time handleBar was invoked.
	// Stored as UnixNano int64 to avoid boxing a time.Time struct on every
	// bar (atomic.Value.Store heap-allocates its interface value; pprof
	// showed ~520k allocations per backtest).
	lastBarTime atomic.Int64

	// liveness tracks per-(strategy,symbol) tick/eval/signal counters so
	// the dashboard can distinguish idle-but-healthy from broken. Hot path
	// is lock-free atomic ops; see LivenessTracker.
	liveness *LivenessTracker
	// disableLiveness skips all liveness recording. Mirrors
	// suppressProgressEvents — intended for offline backtests where no
	// UI is watching and even cheap atomics add up across millions of bars.
	disableLiveness bool

	// disableAI mirrors bootstrap.StrategyDeps.DisableAI on the runner so
	// the instance-context emit path can stamp EntryGatedPayload.AIEnabled.
	// Parity plan Phase 2: makes "live and backtest blocked rows ran with
	// the same AI mode" provable via a SQL diff. The enricher itself is
	// still the authoritative consumer of DisableAI; this is read-only
	// telemetry derived from it.
	disableAI bool

	// tideTracker, when non-nil, is fed every SPY/QQQ 1m bar to maintain a
	// running intraday VWAP and expose market-tide deviation to AVWAP
	// entry-signal telemetry. Phase 1 of AVWAP SPY-tide plumbing — data
	// collection only, no gating.
	tideTracker *gate.IndexTideTracker

	// notifier, when non-nil, receives Discord/alert messages for recovered
	// strategy panics. Nil is safe — the runner will still log and increment
	// the panic metric. Set via SetNotifier.
	notifier ports.NotifierPort

	// universeHistory, when non-nil, lets copytrade gate author signals to
	// symbols that were in the tradable universe at PostedAt. Nil is safe —
	// the copytrade handler treats "no port" as "don't gate". Set via
	// SetUniverseHistory.
	universeHistory ports.UniverseHistoryPort

	// suppressProgressEvents, when true, causes emitDomainEvent to drop
	// telemetry-only payloads (EntryGatedPayload, ORBPhaseUpdatePayload)
	// without allocating an Event or publishing. Intended for offline
	// backtests where no SSE client is listening — was ~1M alloc per run.
	suppressProgressEvents bool

	// deferSignalPublish, when true, routes emitSignal into the pendingSignals
	// slice instead of publishing to the event bus. Used by the backtest
	// ShardedPipeline so multiple shards can run handleBar in parallel
	// (Phase A) and have the main goroutine drain + publish signals in
	// dispatch order (Phase B) to preserve single-threaded signal ordering
	// for downstream risk sizer / execution / position monitor state.
	// Single-writer per-runner — each shard owns its own Runner, so no
	// synchronization is needed on pendingSignals.
	deferSignalPublish bool
	pendingSignals     []domain.Event

	// isBacktest is read by instanceContext.IsBacktest. See Context.IsBacktest doc.
	isBacktest bool

	// deferReconcile, when true, skips the in-handleBar ReconcileSignals
	// pass so slice-to-completion shards don't apply reversal-entry ↔
	// close-position conversion against stale (empty) positions. The
	// replay loop re-runs ReconcileSignals with live posLookup just
	// before publishing each signal, which preserves exact single-
	// threaded semantics. Orthogonal to deferSignalPublish: both flags
	// are set together for slice-mode runners.
	deferReconcile bool

	// noInstancesLogged records symbols for which we've already logged a
	// "no instances for symbol" line, so unused symbols don't emit an Info
	// log on every bar (hot path — ~8% of backtest CPU pre-gating).
	noInstancesLogged map[string]struct{}

	// Scratch buffers reused across handleBar invocations to avoid per-bar
	// allocations. They live inside the runner's mutex (handleBar holds r.mu
	// while populating/reading them) so no external synchronization is
	// needed. Only valid inside a single handleBar call.
	scratchOneMin    []*Instance
	scratchHTFNeeded map[string][]*Instance
	scratchInstances []*Instance

	// pendingCopytradeCallbacks queues strategy-callback dispatches (e.g.
	// CopytradeExitRejected → Instance.OnEvent) that would otherwise run
	// synchronously inside an outer Instance.OnEvent on the same goroutine —
	// a reentrant mutex deadlock in syncMode backtests. Handlers enqueue a
	// closure and return; drainCopytradeCallbacks runs them AFTER r.mu and
	// inst.mu have been released by the outer call. Guarded by its own
	// mutex, not r.mu, so enqueue from inside a handler holding r.mu is
	// safe. Drain loops until empty because a drained callback can itself
	// publish events that enqueue more work.
	copytradeCallbackMu sync.Mutex
	copytradeCallbacks  []func()
}

// strategyEmitSeq is a monotonic counter used to generate cheap
// idempotency keys for strategy-emitted domain events (EntryGated,
// ORBPhaseUpdate, etc.). Previously emitDomainEvent called
// uuid.NewString() per event, which hit crypto/rand syscalls for
// events that don't actually need cryptographic uniqueness.
var strategyEmitSeq atomic.Uint64

// DPLookupKey uniquely identifies a dark pool bar for O(1) access during replay.
type DPLookupKey struct {
	Symbol string
	Time   time.Time
}

// dpRollingStats maintains a circular buffer for rolling mean/std computation
// of DP ratio values, used for Z-score normalization.
type dpRollingStats struct {
	values []float64
	size   int
	idx    int
	full   bool
}

func newDPRollingStats(size int) *dpRollingStats {
	return &dpRollingStats{
		values: make([]float64, size),
		size:   size,
	}
}

func (rs *dpRollingStats) push(v float64) {
	rs.values[rs.idx] = v
	rs.idx = (rs.idx + 1) % rs.size
	if rs.idx == 0 {
		rs.full = true
	}
}

// snapshot returns the current ring buffer contents in chronological order
// (oldest first) and the count of populated entries. Returns nil + 0 when
// the buffer is empty. Used by parity-diag emits.
func (rs *dpRollingStats) snapshot() ([]float64, int) {
	n := rs.size
	if !rs.full {
		n = rs.idx
	}
	if n == 0 {
		return nil, 0
	}
	out := make([]float64, n)
	if rs.full {
		for i := 0; i < n; i++ {
			out[i] = rs.values[(rs.idx+i)%rs.size]
		}
	} else {
		copy(out, rs.values[:n])
	}
	return out, n
}

func (rs *dpRollingStats) meanStd() (mean, std float64) {
	n := rs.size
	if !rs.full {
		n = rs.idx
	}
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += rs.values[i]
	}
	mean = sum / float64(n)
	var variance float64
	for i := 0; i < n; i++ {
		d := rs.values[i] - mean
		variance += d * d
	}
	if n > 1 {
		variance /= float64(n - 1) // Bessel's correction for sample std
	}
	std = math.Sqrt(variance)
	return mean, std
}

func (r *Runner) SignalsRTHSuppressed() int64 {
	return r.signalsRTHSuppressed.Load()
}

// hasMissingAnchor returns true if any configured AVWAP anchor is absent from
// the calc state for the given symbol. This happens when omo-core starts
// pre-market: session_open resolves to zero-time and is skipped by
// ResetAnchors. Detecting this lets handleBarCore re-resolve on the first
// RTH bar so the anchor gets a valid time.
func (r *Runner) hasMissingAnchor(symbol string) bool {
	instances := r.router.InstancesForSymbol(symbol)
	for _, inst := range instances {
		st, ok := inst.GetState(symbol)
		if !ok {
			continue
		}
		type anchorChecker interface {
			AnchorNames() []string
			HasAnchor(string) bool
		}
		if checker, ok := st.(anchorChecker); ok {
			for _, name := range checker.AnchorNames() {
				if !checker.HasAnchor(name) {
					return true
				}
			}
		}
	}
	return false
}

func (r *Runner) SetAnchorResolver(fn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time) {
	r.anchorResolver = fn
	r.lastSessionDate = make(map[string]int)
}

// SetSessionRefresher installs a callback that the runner invokes on each
// session-day rollover before resolving anchors. Production wires this to
// SessionResolver.RefreshIfStale so the prevDay row for the new session day
// is loaded before findPreviousDay walks back. Nil is safe — the refresher
// is only called when set.
func (r *Runner) SetSessionRefresher(fn func(symbol string, barTime time.Time)) {
	r.sessionRefresher = fn
}

func (r *Runner) SetPrevDayBarsFn(fn func(symbol string, since, until time.Time) []start.Bar) {
	r.prevDayBarsFn = fn
}

func (r *Runner) SetKeyLevelPricesFn(fn func(symbol string, barTime time.Time) map[string]float64) {
	r.keyLevelPricesFn = fn
	r.keyLevelsBySymbol = make(map[string]map[string]float64)
}

func (r *Runner) SetAIAnchorResolver(resolver *AIAnchorResolver) {
	r.aiAnchorResolver = resolver
	r.lastSessionDate = make(map[string]int)
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
	AnchorTime(name string) (time.Time, bool)
	// ResetAnchors returns the names of anchors that were freshly created
	// (or whose state was dropped because the anchor time changed). Callers
	// driving a replay after Reset MUST filter the replay set to only those
	// fresh anchors — re-feeding bars into preserved-state anchors compounds
	// CumPV/CumV and corrupts pd_high/pd_low VWAP across the day rollover.
	ResetAnchors(map[string]time.Time) []string
}

type anchorUpdater interface {
	UpdateCalcAnchor(name string, bar start.Bar)
}

// replayBarsForAnchors feeds 1m bars from each anchor's time up to `barTime`
// into the state's cumulative AVWAP calculators. At a fresh-session reset,
// anchor_time == barTime for session_open so the replay is a no-op; on
// mid-session restart it seeds the bars needed to match bar-by-bar
// accumulation at the same moment.
func (r *Runner) replayBarsForAnchors(st any, symbol string, anchors map[string]time.Time, barTime time.Time) {
	if r.prevDayBarsFn == nil {
		return
	}
	au, ok := st.(anchorUpdater)
	if !ok {
		return
	}
	sortedNames := make([]string, 0, len(anchors))
	for name := range anchors {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	for _, name := range sortedNames {
		prevBars := r.prevDayBarsFn(symbol, anchors[name], barTime)
		if len(prevBars) == 0 {
			continue
		}
		r.logger.Info("replaying bars for anchor",
			"symbol", symbol, "anchor", name, "bars", len(prevBars),
			"from", prevBars[0].Time, "to", prevBars[len(prevBars)-1].Time)
		for _, b := range prevBars {
			au.UpdateCalcAnchor(name, b)
		}
	}
}

// ResolveAnchorsForWarmup triggers anchor resolution for all given symbols.
// Called during startup to ensure AVWAP anchors are set before warmup bars are fed,
// so that mid-day restarts produce valid confluence scores immediately.
func (r *Runner) ResolveAnchorsForWarmup(symbols []string, barTime time.Time) {
	if r.anchorResolver == nil {
		return
	}
	loc := domain.NYLocation()
	y, m, d := barTime.In(loc).Date()
	dateInt := y*10000 + int(m)*100 + d
	for _, sym := range symbols {
		if r.lastSessionDate == nil {
			r.lastSessionDate = make(map[string]int)
		}
		r.lastSessionDate[sym] = dateInt
		r.resolveSessionAnchors(sym, barTime)
	}
}

func (r *Runner) resolveSessionAnchors(symbol string, barTime time.Time) {
	if r.sessionRefresher != nil {
		r.sessionRefresher(symbol, barTime)
	}
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
				fresh := ar.ResetAnchors(resolved)
				if len(fresh) > 0 {
					freshTimes := make(map[string]time.Time, len(fresh))
					for _, name := range fresh {
						if t, ok := resolved[name]; ok {
							freshTimes[name] = t
						}
					}
					r.replayBarsForAnchors(st, symbol, freshTimes, barTime)
				}
				r.logger.Info("reset AVWAP anchors for new session",
					"symbol", symbol, "anchors", len(resolved),
					"fresh", len(fresh), "preserved", len(resolved)-len(fresh))
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
			// Additive merge: existing -> anchorResolver -> AI. Existing
			// anchors must be seeded first so a partial AI/resolver result
			// (e.g. session_open only in backtest) does not strip pd_high
			// /pd_low and re-trigger hasMissingAnchor on the next bar.
			names := ar.AnchorNames()
			merged := make(map[string]time.Time, len(names)+len(resolved))
			for _, name := range names {
				if t, ok := ar.AnchorTime(name); ok {
					merged[name] = t
				}
			}
			if r.anchorResolver != nil {
				if r.sessionRefresher != nil {
					r.sessionRefresher(symbol, bar.Time)
				}
				for k, v := range r.anchorResolver(symbol, bar.Time, names) {
					merged[k] = v
				}
			}
			for k, v := range resolved {
				merged[k] = v
			}
			fresh := ar.ResetAnchors(merged)
			if len(fresh) > 0 {
				freshTimes := make(map[string]time.Time, len(fresh))
				for _, name := range fresh {
					if t, ok := merged[name]; ok {
						freshTimes[name] = t
					}
				}
				r.replayBarsForAnchors(st, symbol, freshTimes, bar.Time)
			}
			r.logger.Info("AI anchor resolution complete",
				"symbol", symbol, "anchors", len(merged),
				"fresh", len(fresh), "preserved", len(merged)-len(fresh))
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

// Option configures a Runner at construction time.
type Option func(*Runner)

// WithIndicator injects the indicator.Service that owns HTF state. The
// runner's HTF read path (handleBarCore) and HTF warmup path (WarmUpTF)
// route through this service so live and backtest see identical seeded
// state.
func WithIndicator(svc *indicator.Service) Option {
	return func(r *Runner) {
		r.indicator = svc
	}
}

// NewRunner creates a StrategyRunner.
func NewRunner(
	eventBus ports.EventBusPort,
	router *Router,
	tenantID string,
	envMode domain.EnvMode,
	logger *slog.Logger,
	opts ...Option,
) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Runner{
		eventBus:    eventBus,
		router:      router,
		logger:      logger.With("component", "strategy_runner"),
		tenantID:    tenantID,
		envMode:     envMode,
		indicators:     make(map[string]start.IndicatorData),
		indLogOnce:     make(map[string]bool),
		htfUnsubs:      make(map[string]func()),
		warmedHTFKeys:  make(map[string]bool),
		regimeDetector: monitor.NewRegimeDetector(),
		anchorRegimes:          make(map[string]map[string]domain.MarketRegime),
		collectedAnchorRegimes: make(map[string]map[string]start.AnchorRegime),
		lateSessionDPZ:       make(map[string]float64),
		lateSessionDPRolling: make(map[string]*dpRollingStats),
		lateSessionDPDate:    make(map[string]int),
		lateSessionLPZ:       make(map[string]float64),
		lateSessionLPRolling: make(map[string]*dpRollingStats),
		lateSessionNFZ:       make(map[string]float64),
		lateSessionNFRolling: make(map[string]*dpRollingStats),
		lateSessionDPVolRatioZ:       make(map[string]float64),
		lateSessionDPVolRatioRolling: make(map[string]*dpRollingStats),
		dpSource:                     noopDPSource{},
		liveness:                     NewLivenessTracker(),
	}
	for _, opt := range opts {
		opt(r)
	}
	// Wire the liveness publisher so throttled StrategyEvaluation events
	// reach the SSE fan-out. Fire-and-forget: SSE clients don't care about
	// delivery errors, and publishes use a detached context because they
	// may happen after the per-bar request chain has returned.
	r.liveness.SetPublisher(func(evt domain.Event) {
		_ = r.eventBus.Publish(context.Background(), evt)
	})
	return r
}

// TagBacktest stamps a backtest suffix that BacktestTag exposes to
// instance.EmitDomainEvent so EntryGated rows from in-process backtests
// can be distinguished from live rows in shared SQL output. Mirrors
// monitor.Service.TagBacktest. Must be called before any bar feeds reach
// handleBarCore — bootstrap invokes it synchronously after NewRunner
// when StrategyDeps.BacktestID is non-empty. htfLabelSuffix MUST NOT be
// mutated after that bootstrap call: BacktestTag reads it without
// locking on the EntryGated emit path.
func (r *Runner) TagBacktest(backtestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.htfLabelSuffix = "_backtest_" + backtestID
}

// BacktestTag returns the runner's backtest label as a payload-friendly string
// ("backtest_<id>" without the leading underscore TagBacktest stamps), or ""
// when the runner is in live mode. Used by instance.EmitDomainEvent to stamp
// EntryGatedPayload.Tag so a SQL diff on strategy_signal_events can distinguish
// in-process backtest rows from live rows for the same (symbol, ts).
//
// Read is unlocked because htfLabelSuffix is set once at bootstrap (see the
// TagBacktest contract) and never mutated thereafter, so the field is
// effectively immutable when BacktestTag fires from the EmitDomainEvent path.
// Locking would be incorrect anyway — handleBarCore already holds r.mu when
// it calls into the strategy's OnBar → EmitDomainEvent path, and r.mu is a
// non-reentrant sync.Mutex.
func (r *Runner) BacktestTag() string {
	return strings.TrimPrefix(r.htfLabelSuffix, "_")
}

// SetDisableLiveness toggles liveness recording. Backtests call with true to
// skip per-bar atomic updates and counter bookkeeping.
func (r *Runner) SetDisableLiveness(disable bool) {
	r.mu.Lock()
	r.disableLiveness = disable
	r.mu.Unlock()
}

// SetDisableAI records whether the strategy pipeline this runner belongs to
// has the AI enricher disabled. The flag is consumed only by the instance-
// context emit path to stamp EntryGatedPayload.AIEnabled (parity plan
// Phase 2). It does NOT short-circuit the enricher itself — the enricher
// owns its own SkipAI option, set by bootstrap.
func (r *Runner) SetDisableAI(disable bool) {
	r.mu.Lock()
	r.disableAI = disable
	r.mu.Unlock()
}

// Liveness returns a stable snapshot of per-symbol telemetry for strategyID.
// Unknown strategies return an empty slice (not nil, not error) — callers
// treat "no rows yet" and "never seen" identically.
func (r *Runner) Liveness(strategyID string) []domain.SymbolLiveness {
	if r.liveness == nil {
		return []domain.SymbolLiveness{}
	}
	snap := r.liveness.Snapshot(strategyID)
	if snap == nil {
		return []domain.SymbolLiveness{}
	}
	return snap
}

// LivenessTracker exposes the underlying tracker (for tests and for Phase 2
// SSE wiring). Nil-safe.
func (r *Runner) LivenessTracker() *LivenessTracker { return r.liveness }

// Router returns the underlying router for registration.
func (r *Runner) Router() *Router { return r.router }

// SetSwapManager attaches a SwapManager to feed shadow instances during bar processing.
func (r *Runner) SetSwapManager(sm *SwapManager) { r.swapManager = sm }

// SetSuppressProgressEvents toggles whether the runner publishes telemetry-
// only EntryGated/ORBPhaseUpdate events. Enable in backtest/replay binaries
// where no SSE client consumes the cache — saves ~1M allocations per run.
func (r *Runner) SetSuppressProgressEvents(suppress bool) {
	r.mu.Lock()
	r.suppressProgressEvents = suppress
	r.mu.Unlock()
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (r *Runner) SetMetrics(m *metrics.Metrics) { r.metrics = m }

// SetTideTracker installs an IndexTideTracker that the runner feeds every
// SPY/QQQ 1m bar so AVWAP entry signals can be tagged with the current
// market-tide deviation. Telemetry only — no gating.
func (r *Runner) SetTideTracker(tracker *gate.IndexTideTracker) {
	r.tideTracker = tracker
}

// applyTideData pushes the current SPY/QQQ intraday-VWAP deviation onto any
// strategy state that implements the tideDataSetter interface (AVWAP). When
// the tracker is missing, unready, or the symbol has no reference index, the
// setter is called with ready=false so stale data from a previous symbol is
// cleared. Telemetry only — does not affect trading behavior.
func (r *Runner) applyTideData(inst *Instance, symbol string) {
	if r.tideTracker == nil {
		return
	}
	type tideDataSetter interface {
		SetTideData(devBps float64, ready bool, indexName string)
	}
	state, ok := inst.GetState(symbol)
	if !ok {
		return
	}
	setter, ok := state.(tideDataSetter)
	if !ok {
		return
	}
	sym := domain.Symbol(symbol)
	refIndex := gate.ReferenceIndex(sym)
	if refIndex == "" {
		setter.SetTideData(0, false, "")
		return
	}
	vwap, lastClose, ready := r.tideTracker.GetTide(sym)
	if !ready || vwap <= 0 {
		setter.SetTideData(0, false, refIndex)
		return
	}
	devBps := (lastClose - vwap) / vwap * 10000
	setter.SetTideData(devBps, true, refIndex)
}

func (r *Runner) SetPositionLookup(fn PositionLookupFunc) { r.posLookup = fn }

// SetUniverseHistory installs the port used by the copytrade handler to gate
// incoming author signals to symbols that were tradable at PostedAt. Nil keeps
// gating disabled (the handler skips the check).
func (r *Runner) SetUniverseHistory(p ports.UniverseHistoryPort) { r.universeHistory = p }

// SetDarkPoolLookup injects pre-loaded dark pool bars for backtesting. The
// strategy runner overlays DP data onto IndicatorData during bar processing.
// Internally wraps the map in a staticDPSource so the per-bar access path
// is the same as the live path (Phase 4 of the parity plan).
func (r *Runner) SetDarkPoolLookup(lookup map[DPLookupKey]domain.DarkPoolBar) {
	r.dpSource = staticDPSource{lookup: lookup}
}

// SetDarkPoolSource installs a DPSource implementation directly. Used by
// the live path to plug in livedarkpool.Service (Phase 4) without going
// through the legacy map shape. Mutually exclusive with SetDarkPoolLookup —
// last write wins.
func (r *Runner) SetDarkPoolSource(src DPSource) {
	if src == nil {
		src = noopDPSource{}
	}
	r.dpSource = src
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

// InitAggregators registers Subscribe callbacks on the indicator.Service for
// every (symbol, HTF timeframe) pair declared by the registered instances.
// Re-init paths drop the previous subscriptions before re-registering, so
// callbacks cannot leak across activation cycles. Must be called after all
// instances are registered and before Start().
func (r *Runner) InitAggregators(sessionOpen time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.indicator == nil {
		return
	}
	r.indicator.SetSessionOpen(sessionOpen)
	for key, unsub := range r.htfUnsubs {
		unsub()
		delete(r.htfUnsubs, key)
	}
	for _, inst := range r.router.AllInstances() {
		tfs := inst.Assignment().Timeframes
		if len(tfs) == 0 {
			tfs = []string{"1m"}
		}
		// Use router-routed symbols (Assignment + AddSymbol) so sentinel-
		// rooted dynamic-watchlist instances (TTT) subscribe HTF callbacks
		// for every real ticker, not just the sentinel routing key.
		routedSymbols := r.router.SymbolsForInstance(inst.ID())
		for _, tf := range tfs {
			if tf == "1m" {
				continue
			}
			for _, sym := range routedSymbols {
				key := sym + ":" + tf
				if _, exists := r.htfUnsubs[key]; exists {
					continue
				}
				cb := r.makeHTFCallback(tf)
				r.htfUnsubs[key] = r.indicator.Subscribe(domain.Symbol(sym), domain.Timeframe(tf), cb)
				r.logger.Info("HTF subscriber registered", "symbol", sym, "timeframe", tf)
			}
		}
	}
}

// subscribeHTFForInstanceSymbol wires HTF callbacks for a (instance, symbol)
// pair when a sentinel-routed ticker is added at runtime via AddSymbol. Bootstrap
// pre-registration is covered by InitAggregators; this path covers dynamic
// arrivals (live mode, late-day Discord watchlist updates). Idempotent — repeat
// calls reuse the existing subscription.
func (r *Runner) subscribeHTFForInstanceSymbol(inst *Instance, symbol string) {
	if r.indicator == nil || inst == nil {
		return
	}
	tfs := inst.Assignment().Timeframes
	if len(tfs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.htfUnsubs == nil {
		r.htfUnsubs = make(map[string]func())
	}
	for _, tf := range tfs {
		if tf == "1m" {
			continue
		}
		key := symbol + ":" + tf
		if _, exists := r.htfUnsubs[key]; exists {
			continue
		}
		cb := r.makeHTFCallback(tf)
		r.htfUnsubs[key] = r.indicator.Subscribe(domain.Symbol(symbol), domain.Timeframe(tf), cb)
		r.logger.Info("HTF subscriber registered (dynamic)", "symbol", symbol, "timeframe", tf)
	}
}

// makeHTFCallback returns a Subscribe callback that runs synchronously inside
// indicator.Service.UpdateWithEnv when an HTF bucket closes. The indicator
// service drives Update (subscribed to MarketBarSanitized BEFORE the runner),
// so the callback fires on the indicator handler's goroutine — the same
// goroutine that subsequently dispatches the bar to the runner's handleBar.
// Synchronous bus dispatch means htfPending is single-goroutine: written
// here, read by handleBarCore at the drain loop.
//
// The envelope is unused by the runner (it doesn't publish derived events;
// monitor's HTF callback handles that via AppendPublish).
func (r *Runner) makeHTFCallback(tf string) func(closed domain.MarketBar, snap domain.IndicatorSnapshot, env domain.MarketBarEnvelope) {
	return func(closed domain.MarketBar, snap domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		r.htfPending = append(r.htfPending, htfClose{tf: tf, closed: closed, snap: snap})
	}
}

// PrimeAggregators feeds 1m bars through the indicator.Service aggregator
// chain without firing subscribers or driving calc.Update. Used at boot
// after canonical-spec HTF warmup so the first live 5m/15m/1h close
// after boot contains today's pre-boot 1m bars, not just post-boot ones.
func (r *Runner) PrimeAggregators(symbol string, bars1m []domain.MarketBar) {
	if len(bars1m) == 0 || r.indicator == nil {
		return
	}
	rthBars := make([]domain.MarketBar, 0, len(bars1m))
	for _, bar := range bars1m {
		if warmup.IsEquityNonRTH(bar) {
			continue
		}
		rthBars = append(rthBars, bar)
	}
	if len(rthBars) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	domSym := domain.Symbol(symbol)
	for _, tf := range r.HTFTimeframesForSymbol(symbol) {
		r.indicator.PrimeAggregator(domSym, domain.Timeframe(tf), rthBars)
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
	if err := r.eventBus.Subscribe(ctx, domain.EventTradeReceived, r.handleTradeReceived); err != nil {
		r.logger.Warn("failed to subscribe to TradeReceived (non-fatal)", "error", err)
		// Non-fatal: backtests don't synthesize trade ticks; strategies fall
		// back to bar-sign TFI via the `tfi_source = "auto"` knob.
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventCopytradeSignalReceived, r.handleCopytradeSignal); err != nil {
		r.logger.Warn("failed to subscribe to CopytradeSignalReceived (non-fatal)", "error", err)
		// Non-fatal: most deployments/backtests don't run the copytrade strategy.
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventCopytradeExitRejected, r.handleCopytradeExitRejected); err != nil {
		r.logger.Warn("failed to subscribe to CopytradeExitRejected (non-fatal)", "error", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventTradingTheTrendSignalReceived, r.handleTradingTheTrendSignal); err != nil {
		r.logger.Warn("failed to subscribe to TradingTheTrendSignalReceived (non-fatal)", "error", err)
		// Non-fatal: most deployments/backtests don't run the tradingthetrend strategy.
	}
	r.logger.Info("strategy runner subscribed to MarketBarSanitized events")
	r.lastBarTime.Store(time.Now().UnixNano())
	go r.barHealthCheck(ctx)
	// Phase 3 sparkline sampler. Started here (rather than in NewRunner) so
	// that backtest callers who construct a Runner but never call Start
	// don't spin up an unused goroutine. Stop is chained off ctx so we
	// don't need an explicit Runner.Stop() method.
	if r.liveness != nil {
		r.liveness.Start(ctx)
		go func() {
			<-ctx.Done()
			r.liveness.Stop()
		}()
	}
	return nil
}

// barHealthCheck logs a warning if no bars have been received for 5 minutes
// during RTH (09:30-16:00 ET). This catches silent event bus failures.
func (r *Runner) barHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	et, _ := time.LoadLocation("America/New_York")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowET := time.Now().In(et)
			hhmm := nowET.Format("15:04")
			if hhmm < "09:30" || hhmm >= "16:00" {
				continue // outside RTH
			}
			if nowET.Weekday() == time.Saturday || nowET.Weekday() == time.Sunday {
				continue
			}
			lastNano := r.lastBarTime.Load()
			if lastNano == 0 {
				continue
			}
			last := time.Unix(0, lastNano)
			gap := time.Since(last)
			if gap > 5*time.Minute {
				r.logger.Error("HEALTH CHECK: strategy runner has not received a bar",
					"last_bar_ago", gap.Round(time.Second).String(),
					"last_bar_at", last.Format("15:04:05"),
				)
			}
		}
	}
}

// handleStateUpdated caches indicator data from StateUpdated events.
// This data is used by handleBar to inject indicators into strategy instances.
// HandleBarDirect is the public direct-dispatch entry to handleBar used by
// the backtest Pipeline. Equivalent to the bus-subscribed handler but
// callable without going through Subscribe/Publish.
func (r *Runner) HandleBarDirect(ctx context.Context, event domain.Event) error {
	return r.handleBar(ctx, event)
}

// HandleBarDirectTyped is the typed fast path for backtest slice
// dispatch: accepts the bar + envelope metadata directly, skipping
// Event construction on the caller side. Saves ~1.87 GB of
// allocation per 30 sym / 1 yr run from toEvent() calls.
func (r *Runner) HandleBarDirectTyped(ctx context.Context, bar domain.MarketBar, tenantID string, envMode domain.EnvMode) error {
	return r.handleBarCore(ctx, bar, tenantID, envMode)
}

// HandleStateUpdatedDirect is the public direct-dispatch entry to
// handleStateUpdated, mirroring HandleBarDirect.
func (r *Runner) HandleStateUpdatedDirect(ctx context.Context, event domain.Event) error {
	return r.handleStateUpdated(ctx, event)
}

// HandleStateUpdatedSnap is a typed direct-dispatch entry that
// accepts the IndicatorSnapshot value without Event wrapping. Used
// by the backtest Pipeline to route monitor drain results into the
// runner's indicator cache without paying the Event struct allocation
// + IdempotencyKey concat per bar — the single largest source of GC
// pressure in the Phase 3 profile. Delegates to the same shared body
// as handleStateUpdated.
func (r *Runner) HandleStateUpdatedSnap(_ context.Context, snap domain.IndicatorSnapshot) error {
	r.applyStateUpdate(snap)
	return nil
}

// SeedIndicatorSnapshot seeds the runner's per-symbol indicator cache
// with a pre-computed monitor snapshot. Call after bridge warmup so
// strategies that enter on bar #1 (e.g. overnight_z_v1) see HTF data
// (DailyATR, NR7, Bias) immediately instead of waiting one bar for
// the pipeline's drain-after-bar cycle to populate them.
func (r *Runner) SeedIndicatorSnapshot(snap domain.IndicatorSnapshot) {
	r.applyStateUpdate(snap)
}

func (r *Runner) handleStateUpdated(_ context.Context, event domain.Event) error {
	snap, ok := event.Payload.(domain.IndicatorSnapshot)
	if !ok {
		return nil
	}
	r.applyStateUpdate(snap)
	return nil
}

// applyStateUpdate writes the snapshot's indicator values into the
// per-symbol cache and applies dark-pool + whale overlay data.
// Shared body between handleStateUpdated (legacy Event-wrapped) and
// HandleStateUpdatedSnap (typed direct-dispatch).
func (r *Runner) applyStateUpdate(snap domain.IndicatorSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Reuse the previously-allocated AnchorRegimes / HTF maps for this
	// symbol to avoid ~1.3M map allocations per backtest. Safe under
	// replay's SyncBus dispatch — events process serially so no reader
	// holds a reference across the refill.
	sym := snap.Symbol.String()
	prev := r.indicators[sym]
	newAR := convertAnchorRegimesInto(prev.AnchorRegimes, snap.AnchorRegimes)
	newHTF := convertHTFDataInto(prev.HTF, snap.HTF)
	r.indicators[sym] = start.IndicatorData{
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
		AnchorRegimes: newAR,
		HTF:           newHTF,
	}

	// Overlay dark pool microstructure data when the runner's DPSource has
	// any (backtest, or Phase 4 live aggregator). Aggregates all 5m DP bars
	// within the decision bar's time window for correct alignment.
	if r.dpSource.HasData() {
		sym := snap.Symbol.String()
		barStart := snap.Time.UTC()
		// Determine bar duration from timeframe; fallback to 5m for unknown/1m bars.
		barDur := tfDuration(snap.Timeframe)
		if barDur < 5*time.Minute {
			barDur = 5 * time.Minute
		}
		barEnd := barStart.Add(barDur)

		// Aggregate all 5m DP bars within the decision window.
		var dpVol, dpBuy, dpSell, dpLarge, dpTotal float64
		for t := barStart.Truncate(5 * time.Minute); t.Before(barEnd); t = t.Add(5 * time.Minute) {
			if dp, ok := r.dpSource.Lookup(sym, t); ok {
				dpVol += dp.DPVolume
				dpBuy += dp.BuyVolume
				dpSell += dp.SellVolume
				dpLarge += dp.LargePrintVolume
				dpTotal += dp.TotalVolume
			}
		}
		if dpTotal > 0 {
			ind := r.indicators[sym]
			ind.DPRatio = dpVol / dpTotal
			if dpVol > 0 {
				ind.DPBuyRatio = dpBuy / dpVol
				ind.DPLargePrintPct = dpLarge / dpVol
			}

			// Z-score normalization of DP ratio using rolling lookback.
			if r.dpRolling == nil {
				r.dpRolling = make(map[string]*dpRollingStats)
			}
			rs, ok := r.dpRolling[sym]
			if !ok {
				rs = newDPRollingStats(20)
				r.dpRolling[sym] = rs
			}
			rs.push(ind.DPRatio)
			mean, std := rs.meanStd()
			if std > 0 {
				ind.DPRatioZScore = (ind.DPRatio - mean) / std
			}

			// DP S/R levels: scan trailing 20 5m bars, find top-3 by DP volume,
			// classify their DPVWAP as support (below price) or resistance (above price).
			// IndicatorSnapshot has no Close price; VWAP is the best intraday proxy.
			var currentPrice float64
			if snap.VWAP > 0 {
				currentPrice = snap.VWAP
			} else if snap.EMA9 > 0 {
				currentPrice = snap.EMA9
			}
			if currentPrice > 0 {
				type dpBarEntry struct {
					vol  float64
					vwap float64
				}
				var dpBars []dpBarEntry
				levelStart := barEnd.Add(-20 * 5 * time.Minute)
				for t := levelStart.Truncate(5 * time.Minute); t.Before(barEnd); t = t.Add(5 * time.Minute) {
					if dp, ok2 := r.dpSource.Lookup(sym, t); ok2 && dp.DPVolume > 0 && dp.DPVWAP > 0 {
						dpBars = append(dpBars, dpBarEntry{vol: dp.DPVolume, vwap: dp.DPVWAP})
					}
				}
				sort.Slice(dpBars, func(i, j int) bool { return dpBars[i].vol > dpBars[j].vol })
				topN := 3
				if len(dpBars) < topN {
					topN = len(dpBars)
				}
				for _, db := range dpBars[:topN] {
					if db.vwap < currentPrice {
						if db.vwap > ind.DPSupportLevel {
							ind.DPSupportLevel = db.vwap
						}
					} else if db.vwap > currentPrice {
						if ind.DPResistanceLevel == 0 || db.vwap < ind.DPResistanceLevel {
							ind.DPResistanceLevel = db.vwap
						}
					}
				}
			}

			r.indicators[sym] = ind
		}
	}

	// Overlay late-session DP Z-score (daily signal from previous day's 14:00-15:30 ET).
	if r.dpSource.HasData() && etLocation != nil {
		sym := snap.Symbol.String()
		barTime := snap.Time.UTC()
		etTime := barTime.In(etLocation)
		dayKey := etTime.Year()*10000 + int(etTime.Month())*100 + etTime.Day()

		if r.lateSessionDPDate[sym] != dayKey {
			r.lateSessionDPDate[sym] = dayKey
			prevDay := etTime.AddDate(0, 0, -1)
			for prevDay.Weekday() == time.Saturday || prevDay.Weekday() == time.Sunday {
				prevDay = prevDay.AddDate(0, 0, -1)
			}
			// Holiday skip: if no DP data exists for prevDay, step back further.
			for attempts := 0; attempts < 5; attempts++ {
				probe := time.Date(prevDay.Year(), prevDay.Month(), prevDay.Day(), 14, 0, 0, 0, etLocation).UTC()
				if _, ok := r.dpSource.Lookup(sym, probe); ok {
					break
				}
				prevDay = prevDay.AddDate(0, 0, -1)
				for prevDay.Weekday() == time.Saturday || prevDay.Weekday() == time.Sunday {
					prevDay = prevDay.AddDate(0, 0, -1)
				}
			}
			lateStart := time.Date(prevDay.Year(), prevDay.Month(), prevDay.Day(), 14, 0, 0, 0, etLocation).UTC()
			lateEnd := time.Date(prevDay.Year(), prevDay.Month(), prevDay.Day(), 15, 30, 0, 0, etLocation).UTC()

			var lateBuy, lateSell, lateLargePrint, lateDPVol, lateLitVol float64
			for t := lateStart.Truncate(5 * time.Minute); t.Before(lateEnd); t = t.Add(5 * time.Minute) {
				if dp, ok := r.dpSource.Lookup(sym, t); ok {
					lateBuy += dp.BuyVolume
					lateSell += dp.SellVolume
					lateLargePrint += dp.LargePrintVolume
					lateDPVol += dp.DPVolume
					lateLitVol += dp.LitVolume
				}
			}

			if lateBuy+lateSell > 0 {
				lateRatio := lateBuy / (lateBuy + lateSell)

				// 1) Buy ratio Z — compute Z BEFORE pushing today's value
				// to avoid lookback bias (today's observation contaminating its own Z).
				rs, ok := r.lateSessionDPRolling[sym]
				if !ok {
					rs = newDPRollingStats(20)
					r.lateSessionDPRolling[sym] = rs
				}
				mean, std := rs.meanStd()
				if std > 0 {
					r.lateSessionDPZ[sym] = (lateRatio - mean) / std
				} else {
					r.lateSessionDPZ[sym] = 0
				}
				rs.push(lateRatio)

				// 2) Large print imbalance Z — same fix: Z before push.
				lpImbalance := lateLargePrint * (2*lateRatio - 1)
				lpRS, lpOK := r.lateSessionLPRolling[sym]
				if !lpOK {
					lpRS = newDPRollingStats(20)
					r.lateSessionLPRolling[sym] = lpRS
				}
				lpMean, lpStd := lpRS.meanStd()
				if lpStd > 0 {
					r.lateSessionLPZ[sym] = (lpImbalance - lpMean) / lpStd
				} else {
					r.lateSessionLPZ[sym] = 0
				}
				lpRS.push(lpImbalance)

				// 3) Net flow Z — same fix: Z before push.
				netFlow := lateBuy - lateSell
				nfRS, nfOK := r.lateSessionNFRolling[sym]
				if !nfOK {
					nfRS = newDPRollingStats(20)
					r.lateSessionNFRolling[sym] = nfRS
				}
				nfMean, nfStd := nfRS.meanStd()
				if nfStd > 0 {
					r.lateSessionNFZ[sym] = (netFlow - nfMean) / nfStd
				} else {
					r.lateSessionNFZ[sym] = 0
				}
				nfRS.push(netFlow)
			}

			// 4) DP volume ratio Z (signing-free) — avoids buy/sell misclassification.
			if lateDPVol+lateLitVol > 0 {
				dpVolRatio := lateDPVol / (lateDPVol + lateLitVol)
				vrRS, vrOK := r.lateSessionDPVolRatioRolling[sym]
				if !vrOK {
					vrRS = newDPRollingStats(20)
					r.lateSessionDPVolRatioRolling[sym] = vrRS
				}
				vrMean, vrStd := vrRS.meanStd()
				if vrStd > 0 {
					r.lateSessionDPVolRatioZ[sym] = (dpVolRatio - vrMean) / vrStd
				} else {
					r.lateSessionDPVolRatioZ[sym] = 0
				}
				vrRS.push(dpVolRatio)
			}
		}

		if z, ok := r.lateSessionDPZ[sym]; ok {
			ind := r.indicators[sym]
			ind.LateSessionDPZ = z
			if lpZ, lpOK := r.lateSessionLPZ[sym]; lpOK {
				ind.LateSessionLPZ = lpZ
			}
			if nfZ, nfOK := r.lateSessionNFZ[sym]; nfOK {
				ind.LateSessionNetFlowZ = nfZ
			}
			if vrZ, vrOK := r.lateSessionDPVolRatioZ[sym]; vrOK {
				ind.LateSessionDPVolRatioZ = vrZ
			}
			r.indicators[sym] = ind
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
}

// tfDuration converts a domain.Timeframe to a time.Duration.
func tfDuration(tf domain.Timeframe) time.Duration {
	switch tf {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return 60 * time.Minute
	case "1d":
		return 24 * time.Hour
	default:
		return 0
	}
}

// convertAnchorRegimesInto writes the converted map into dst, allocating a
// new one only if dst is nil. Callers that hold a stable map per symbol can
// reuse it across bars to avoid ~650k map allocations per backtest.
// Replay mode dispatches events serially via SyncBus, so no reader holds a
// reference across a refill (verified by source walk over bus.Publish).
func convertAnchorRegimesInto(dst map[string]start.AnchorRegime, regimes map[domain.Timeframe]domain.MarketRegime) map[string]start.AnchorRegime {
	if len(regimes) == 0 {
		if dst != nil {
			clear(dst)
		}
		return dst
	}
	if dst == nil {
		dst = make(map[string]start.AnchorRegime, len(regimes))
	} else {
		clear(dst)
	}
	for tf, r := range regimes {
		dst[tf.String()] = start.AnchorRegime{
			Type:     r.Type.String(),
			Strength: r.Strength,
		}
	}
	return dst
}

func convertHTFDataInto(dst map[string]start.HTFIndicator, htf map[domain.Timeframe]domain.HTFData) map[string]start.HTFIndicator {
	if len(htf) == 0 {
		if dst != nil {
			clear(dst)
		}
		return dst
	}
	if dst == nil {
		dst = make(map[string]start.HTFIndicator, len(htf))
	} else {
		clear(dst)
	}
	for tf, d := range htf {
		dst[tf.String()] = start.HTFIndicator{
			EMA50:    d.EMA50,
			EMA200:   d.EMA200,
			Bias:     d.Bias,
			DailyATR: d.DailyATR,
			NR7:      d.NR7,
		}
	}
	return dst
}



// collectAnchorRegimes builds AnchorRegimes map for a symbol from stored regimes.
//
// Uses the nested anchorRegimes index (sym → tf → regime) so the cost is O(T)
// per call, not O(N*T) over every registered symbol. The result is written
// into a per-symbol reusable map (collectedAnchorRegimes) to avoid allocating
// on every HTF bar — was ~450MB across a 30-symbol backtest.
func (r *Runner) collectAnchorRegimes(symbol string) map[string]start.AnchorRegime {
	tfMap := r.anchorRegimes[symbol]
	if len(tfMap) == 0 {
		return nil
	}
	out, ok := r.collectedAnchorRegimes[symbol]
	if !ok {
		out = make(map[string]start.AnchorRegime, len(tfMap))
		if r.collectedAnchorRegimes == nil {
			r.collectedAnchorRegimes = make(map[string]map[string]start.AnchorRegime)
		}
		r.collectedAnchorRegimes[symbol] = out
	} else {
		clear(out)
	}
	for tf, reg := range tfMap {
		out[tf] = start.AnchorRegime{
			Type:     string(reg.Type),
			Strength: reg.Strength,
		}
	}
	return out
}

// handleBar processes a MarketBarSanitized event by routing to assigned instances.
// 1m bars go directly to 1m-configured instances (zero behavioral change).
// For HTF instances, bars are aggregated via BarAggregator and delivered on completion.
func (r *Runner) handleBar(ctx context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("strategy runner: payload is not a MarketBar, got %T", event.Payload)
	}
	// Monitor re-emits its own HTF aggregated bars as EventMarketBarSanitized
	// (used by SSE display and DB HTF backfill at services.go:792). The runner
	// has its own per-symbol aggregator fed from the 1m stream and forwards
	// closed HTF bars to the indicator service, so consuming monitor's HTF
	// bars here would double-feed the (sym, 5m) state — corrupting VolumeSMA,
	// regime classification, and every downstream gate that reads htfSnap.
	// Backtest's native-HTF replay path (daily crypto) reaches handleBarCore
	// via HandleBarDirectTyped, which bypasses this gate.
	if bar.Timeframe != "1m" {
		return nil
	}
	return r.handleBarCore(ctx, bar, event.TenantID, event.EnvMode)
}

// handleBarCore is the shared body for handleBar (Event-wrapped) and
// HandleBarDirectTyped (typed fast path). Takes the bar and envelope
// metadata directly.
func (r *Runner) handleBarCore(ctx context.Context, bar domain.MarketBar, tenantID string, envMode domain.EnvMode) error {
	loopStart := time.Now()
	r.lastBarTime.Store(loopStart.UnixNano())
	symbol := bar.Symbol.String()

	// Per-symbol liveness tick — best-effort, does not need to wait for
	// instance resolution. RecordTick is a lock-free atomic store once
	// the entry is pre-registered; an unregistered entry lazily registers
	// under a short write lock. Skipped entirely in backtest mode.
	trackLiveness := r.liveness != nil && !r.disableLiveness

	// Feed SPY/QQQ 1m bars to the tide tracker before dispatching so AVWAP
	// entries for any symbol (including SPY/QQQ itself downstream) see the
	// freshest intraday VWAP. Telemetry only.
	if r.tideTracker != nil && bar.Timeframe == "1m" && (symbol == "SPY" || symbol == "QQQ") {
		r.tideTracker.OnBar(bar)
	}

	if r.aiAnchorResolver != nil {
		r.aiAnchorResolver.OnBar(symbol, start.Bar{
			Time: bar.Time, Open: bar.Open, High: bar.High,
			Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
		}, string(bar.Timeframe))

		loc := domain.NYLocation()
		y, m, d := bar.Time.In(loc).Date()
		barDate := y*10000 + int(m)*100 + d

		r.mu.Lock()
		newSession := r.lastSessionDate[symbol] != barDate
		if newSession {
			r.lastSessionDate[symbol] = barDate
		}
		r.mu.Unlock()

		if newSession {
			r.resolveAIAnchors(ctx, symbol, bar, AnchorResolveOption{SyncAI: false})
		} else if r.hasMissingAnchor(symbol) {
			// Pre-market startup: session_open resolved to zero-time and was
			// skipped. Re-resolve now that RTH has started.
			r.resolveAIAnchors(ctx, symbol, bar, AnchorResolveOption{SyncAI: false})
		}
	} else if r.anchorResolver != nil {
		loc := domain.NYLocation()
		y, m, d := bar.Time.In(loc).Date()
		barDate := y*10000 + int(m)*100 + d
		if r.lastSessionDate[symbol] != barDate {
			r.lastSessionDate[symbol] = barDate
			r.resolveSessionAnchors(symbol, bar.Time)
		} else if r.hasMissingAnchor(symbol) {
			// Pre-market startup: session_open resolved to zero-time and was
			// skipped. Re-resolve now that RTH has started.
			r.resolveSessionAnchors(symbol, bar.Time)
		}
	}

	r.scratchInstances = r.router.InstancesForSymbolInto(symbol, r.scratchInstances)
	instances := r.scratchInstances
	if trackLiveness {
		for _, inst := range instances {
			r.liveness.RecordTick(inst.configStrategyID(), symbol, bar.Time)
		}
	}
	if len(instances) == 0 {
		// Log-once per symbol — this fact is static for the run and otherwise
		// fires on every bar for skipped symbols (~8% of CPU per pprof).
		r.mu.Lock()
		_, already := r.noInstancesLogged[symbol]
		if !already {
			if r.noInstancesLogged == nil {
				r.noInstancesLogged = make(map[string]struct{})
			}
			r.noInstancesLogged[symbol] = struct{}{}
		}
		r.mu.Unlock()
		if !already {
			r.logger.Info("no instances for symbol", "symbol", symbol)
		}
		return nil
	}

	r.mu.Lock()
	indicators := r.indicators[symbol]
	indicators.Volume = bar.Volume

	// Overlay dark pool data directly in handleBar to ensure it's fresh
	// when the strategy processes this bar. The handleStateUpdated overlay
	// can be overwritten by subsequent 1m snapshots before the 15m handleBar fires.
	if r.dpSource.HasData() {
		barStart := bar.Time.UTC()
		barDur := tfDuration(bar.Timeframe)
		if barDur < 5*time.Minute {
			barDur = 5 * time.Minute
		}
		barEnd := barStart.Add(barDur)
		var dpVol, dpBuy, dpLarge, dpTotal float64
		for t := barStart.Truncate(5 * time.Minute); t.Before(barEnd); t = t.Add(5 * time.Minute) {
			if dp, ok := r.dpSource.Lookup(symbol, t); ok {
				dpVol += dp.DPVolume
				dpBuy += dp.BuyVolume
				dpLarge += dp.LargePrintVolume
				dpTotal += dp.TotalVolume
			}
		}
		if dpTotal > 0 {
			indicators.DPRatio = dpVol / dpTotal
			if dpVol > 0 {
				indicators.DPBuyRatio = dpBuy / dpVol
				indicators.DPLargePrintPct = dpLarge / dpVol
			}
			// Z-score from rolling buffer (maintained by handleStateUpdated)
			if r.dpRolling != nil {
				if rs, ok := r.dpRolling[symbol]; ok {
					mean, std := rs.meanStd()
					if std > 0 {
						indicators.DPRatioZScore = (indicators.DPRatio - mean) / std
					}
				}
			}
			// S/R levels
			if bar.Close > 0 {
				levelStart := barEnd.Add(-20 * 5 * time.Minute)
				type dpE struct{ vol, vwap float64 }
				var dpBars []dpE
				for t := levelStart.Truncate(5 * time.Minute); t.Before(barEnd); t = t.Add(5 * time.Minute) {
					if dp, ok := r.dpSource.Lookup(symbol, t); ok && dp.DPVolume > 0 && dp.DPVWAP > 0 {
						dpBars = append(dpBars, dpE{dp.DPVolume, dp.DPVWAP})
					}
				}
				sort.Slice(dpBars, func(i, j int) bool { return dpBars[i].vol > dpBars[j].vol })
				topN := 3
				if len(dpBars) < topN {
					topN = len(dpBars)
				}
				for _, db := range dpBars[:topN] {
					if db.vwap < bar.Close {
						if db.vwap > indicators.DPSupportLevel {
							indicators.DPSupportLevel = db.vwap
						}
					} else {
						if indicators.DPResistanceLevel == 0 || db.vwap < indicators.DPResistanceLevel {
							indicators.DPResistanceLevel = db.vwap
						}
					}
				}
			}
		}
	}

	// Late-session DP Z-score: daily signal from prior day's late-session DP flow.
	// Must be outside the per-bar DP block because Z is computed once per day
	// in applyStateUpdate and doesn't depend on the current bar having DP data.
	if z, ok := r.lateSessionDPZ[symbol]; ok {
		indicators.LateSessionDPZ = z
		if lpZ, lpOK := r.lateSessionLPZ[symbol]; lpOK {
			indicators.LateSessionLPZ = lpZ
		}
		if nfZ, nfOK := r.lateSessionNFZ[symbol]; nfOK {
			indicators.LateSessionNetFlowZ = nfZ
		}
		if vrZ, vrOK := r.lateSessionDPVolRatioZ[symbol]; vrOK {
			indicators.LateSessionDPVolRatioZ = vrZ
		}
	}

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

	// Reuse scratch buffers to avoid allocating a fresh slice + map for every
	// bar. Safe because handleBar holds r.mu while they're populated/read.
	oneMinInstances := r.scratchOneMin[:0]
	if r.scratchHTFNeeded == nil {
		r.scratchHTFNeeded = make(map[string][]*Instance)
	} else {
		for k := range r.scratchHTFNeeded {
			r.scratchHTFNeeded[k] = r.scratchHTFNeeded[k][:0]
		}
	}
	htfNeeded := r.scratchHTFNeeded
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
	r.scratchOneMin = oneMinInstances

	// htfPending was populated for this bar by indicator.Service Subscribe
	// callbacks during the indicator's MarketBarSanitized handler — that
	// handler runs BEFORE the runner's handler on the same goroutine
	// (synchronous bus dispatch, indicator subscribed first). Driving Update
	// from here would re-introduce the dual-driver dedup-starvation
	// regression (PR 6a-2 era): the BarAggregator dedups on bar.Time, the
	// second Update is a no-op, and only the first caller's callbacks fire.
	sBar := domainBarToStratBar(bar)
	var allSignals []start.Signal
	// Add any exit signals from 1m AVWAP exit evaluation
	allSignals = append(allSignals, exitSignals1m...)

	for _, inst := range oneMinInstances {
		instCtx := instanceContextPool.Get().(*instanceContext)
		instCtx.now = bar.Time
		instCtx.logger = inst.Logger()
		instCtx.emit = nil
		instCtx.ctx = ctx
		instCtx.tenantID = tenantID
		instCtx.envMode = envMode
		instCtx.runner = r
		instCtx.specID = inst.configStrategyID()
		r.applyTideData(inst, symbol)
		signals, err := r.safeOnBar(inst, instCtx, symbol, sBar, indicators)
		instanceContextPool.Put(instCtx)
		if err != nil {
			r.logger.Error("instance OnBar failed",
				"instance_id", inst.ID().String(),
				"symbol", symbol,
				"error", err,
			)
			continue
		}
		if trackLiveness {
			r.liveness.RecordEval(inst.configStrategyID(), symbol, bar.Time, reasonFromOutcome(signals, inst.Strategy(), symbol))
		}
		allSignals = append(allSignals, signals...)
	}

	// Native-HTF passthrough (e.g. 1d daily replay): the input bar already
	// matches an HTF the runner consumes. The indicator handler ran first
	// for this bar (subscribed BEFORE the runner) and populated LastSnapshot;
	// we just read it here and dispatch without 1m aggregation. Non-RTH 1m
	// equity bars never reach this branch (Timeframe gate above).
	if bar.Timeframe != "1m" {
		nativeTF := string(bar.Timeframe)
		if _, want := htfNeeded[nativeTF]; want && r.indicator != nil {
			if nativeSnap, ok := r.indicator.LastSnapshot(bar.Symbol, bar.Timeframe); ok {
				r.htfPending = append(r.htfPending, htfClose{tf: nativeTF, closed: bar, snap: nativeSnap})
			}
		}
	}

	for _, p := range r.htfPending {
		tf := p.tf
		htfInsts := htfNeeded[tf]
		if len(htfInsts) == 0 {
			continue
		}
		closed := p.closed
		htfSnap := p.snap
		htfBar := domainBarToStratBar(closed)

		// Compute and store anchor regime for this HTF bar (keyed sym→tf)
		regime, _ := r.regimeDetector.Detect(htfSnap)
		symMap := r.anchorRegimes[symbol]
		if symMap == nil {
			symMap = make(map[string]domain.MarketRegime, 4)
			r.anchorRegimes[symbol] = symMap
		}
		symMap[tf] = regime

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

		// Overlay DP data onto HTF indicators using the closed bar's time window.
		if r.dpSource.HasData() {
			htfStart := closed.Time.UTC()
			htfEnd := htfStart.Add(tfDuration(domain.Timeframe(tf)))
			var hDPVol, hDPBuy, hDPLarge, hDPTotal float64
			for t := htfStart.Truncate(5 * time.Minute); t.Before(htfEnd); t = t.Add(5 * time.Minute) {
				if dp, ok := r.dpSource.Lookup(symbol, t); ok {
					hDPVol += dp.DPVolume
					hDPBuy += dp.BuyVolume
					hDPLarge += dp.LargePrintVolume
					hDPTotal += dp.TotalVolume
				}
			}
			if hDPTotal > 0 {
				htfIndicators.DPRatio = hDPVol / hDPTotal
				if hDPVol > 0 {
					htfIndicators.DPBuyRatio = hDPBuy / hDPVol
					htfIndicators.DPLargePrintPct = hDPLarge / hDPVol
				}
				if r.dpRolling != nil {
					if rs, ok := r.dpRolling[symbol]; ok {
						mean, std := rs.meanStd()
						if std > 0 {
							htfIndicators.DPRatioZScore = (htfIndicators.DPRatio - mean) / std
						}
					}
				}
				// Copy S/R levels from 1m overlay
				htfIndicators.DPSupportLevel = indicators.DPSupportLevel
				htfIndicators.DPResistanceLevel = indicators.DPResistanceLevel
			}
			// Copy late-session Z (daily signal, independent of per-bar DP data)
			if z, ok := r.lateSessionDPZ[symbol]; ok {
				htfIndicators.LateSessionDPZ = z
				if lpZ, lpOK := r.lateSessionLPZ[symbol]; lpOK {
					htfIndicators.LateSessionLPZ = lpZ
				}
				if nfZ, nfOK := r.lateSessionNFZ[symbol]; nfOK {
					htfIndicators.LateSessionNetFlowZ = nfZ
				}
				if vrZ, vrOK := r.lateSessionDPVolRatioZ[symbol]; vrOK {
					htfIndicators.LateSessionDPVolRatioZ = vrZ
				}
			}
		}

		for _, inst := range htfInsts {
			instCtx := instanceContextPool.Get().(*instanceContext)
			instCtx.now = closed.Time
			instCtx.logger = inst.Logger()
			instCtx.emit = nil
			instCtx.ctx = ctx
			instCtx.tenantID = tenantID
			instCtx.envMode = envMode
			instCtx.runner = r
			instCtx.specID = inst.configStrategyID()
			r.applyTideData(inst, symbol)
			signals, err := r.safeOnBar(inst, instCtx, symbol, htfBar, htfIndicators)
			instanceContextPool.Put(instCtx)
			if err != nil {
				r.logger.Error("instance OnBar failed (HTF)",
					"instance_id", inst.ID().String(),
					"symbol", symbol,
					"timeframe", tf,
					"error", err,
				)
				continue
			}
			if trackLiveness {
				r.liveness.RecordEval(inst.configStrategyID(), symbol, closed.Time, reasonFromOutcome(signals, inst.Strategy(), symbol))
			}
			allSignals = append(allSignals, signals...)
		}
	}
	// Drain consumed: clear so indicator's callback for the NEXT bar starts
	// from an empty slice. (We can't clear at handleBarCore entry like the
	// pre-PR-6a-2 code did — the indicator handler runs BEFORE us on the
	// same goroutine and has already populated htfPending for THIS bar.)
	r.htfPending = r.htfPending[:0]

	if r.swapManager != nil {
		swapCtx := &instanceContext{
			now:    bar.Time,
			logger: r.logger.With("symbol", symbol),
			emit:   func(_ any) error { return nil },
		}
		r.swapManager.OnBarProcessed(swapCtx, symbol, sBar, indicators)
	}

	allSignals = r.filterByAllowedDirections(allSignals)
	if !r.deferReconcile {
		allSignals = ReconcileSignals(allSignals, r.posLookup, r.logger)
	}

	// Unlock BEFORE signal emission. The emitSignal cascade can trigger sync
	// handlers (e.g. handleRejection) that also acquire r.mu — holding the lock
	// here would cause a self-deadlock. All state reads/writes are complete.
	r.mu.Unlock()

	// slog allocates every variadic arg at the call site before the level
	// check; this Debug fires per-bar per-symbol, so gate it explicitly.
	if r.logger.Enabled(ctx, slog.LevelDebug) {
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
	}

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
		if trackLiveness {
			if sid, ok := parseStrategyIDFromInstance(sig.StrategyInstanceID); ok {
				r.liveness.RecordSignal(sid.String(), sig.Symbol, bar.Time)
			}
		}
		if err := r.emitSignal(ctx, tenantID, envMode, sig); err != nil {
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

		signals, err := r.safeOnBar(inst, instCtx, symbol, bar, indicators)
		if err != nil {
			return allSignals, fmt.Errorf("instance %s: %w", inst.ID(), err)
		}
		if r.liveness != nil && !r.disableLiveness {
			r.liveness.RecordEval(inst.configStrategyID(), symbol, bar.Time, reasonFromOutcome(signals, inst.Strategy(), symbol))
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

	// Warm the per-(symbol,tf) indicator state with historical bars. The
	// live path creates this lazily on first bar, so without warmup SMA/EMA/
	// RSI/ATR/etc. are all zero for ~20 live bars and entry gates requiring
	// VolumeSMA > 0 (breakout volume check, etc.) never fire for the first
	// ~100 min post-restart. snapshotFn supplies indicators for the strategy
	// state itself; the indicator service owns HTF state. The early return
	// preserves idempotency for activation paths that may re-attach a
	// strategy for the same (symbol, tf) — second-call WarmupOnBar fires
	// would replay against state that already advanced.
	if tf != "1m" {
		key := symbol + ":" + tf
		if r.warmedHTFKeys[key] {
			return 0
		}
		r.warmedHTFKeys[key] = true
		r.indicator.WarmUp(bars)
	}

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
			if err := r.safeWarmupOnBar(inst, instCtx, symbol, sBar, indicators); err != nil {
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
		// Reset the gated bar time dedup guard so the first live bar emits.
		if st, ok := inst.GetState(symbol); ok {
			if resetter, ok := st.(interface{ ResetGatedBarTime() }); ok {
				resetter.ResetGatedBarTime()
			}
		}
	}

	return len(bars)
}

// HTFTimeframesForSymbol returns the unique non-1m timeframes registered by
// any strategy instance assigned to the given symbol. Used by warmup paths
// to drive per-(sym, tf) native HTF fetches.
func (r *Runner) HTFTimeframesForSymbol(symbol string) []string {
	seen := make(map[string]struct{})
	for _, inst := range r.router.InstancesForSymbol(symbol) {
		for _, tf := range inst.Assignment().Timeframes {
			if tf != "1m" {
				seen[tf] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for tf := range seen {
		result = append(result, tf)
	}
	return result
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
			if warmup.IsEquityNonRTH(bar) {
				continue
			}
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

// emitSignal publishes a SignalCreated domain event. When
// deferSignalPublish is true, the event is appended to pendingSignals
// instead — DrainPendingSignals is the sole caller that flushes them,
// and the backtest ShardedPipeline is expected to serialize drains per
// shard in dispatch order to preserve parity with the single-threaded
// path.
func (r *Runner) emitSignal(ctx context.Context, tenantID string, envMode domain.EnvMode, sig start.Signal) error {
	// Live persists fired signals via SignalTracker; this mirror captures
	// the dpRolling buffer alongside so backtest can diff on the same bar.
	if parity.Enabled() {
		rollingMean, rollingStd := 0.0, 0.0
		rollingValuesJSON := []byte("null")
		rollingCount := 0
		if rs, ok := r.dpRolling[string(sig.Symbol)]; ok && rs != nil {
			rollingMean, rollingStd = rs.meanStd()
			snap, n := rs.snapshot()
			rollingCount = n
			rollingValuesJSON, _ = json.Marshal(snap)
		}
		r.logger.Info("parity-diag",
			"stage", parity.StageSignalCreated,
			"symbol", string(sig.Symbol),
			"strategy_instance_id", sig.StrategyInstanceID,
			"side", string(sig.Side),
			"strength", sig.Strength,
			"dp_rolling_mean", rollingMean,
			"dp_rolling_std", rollingStd,
			"dp_rolling_count", rollingCount,
			"dp_rolling_values", string(rollingValuesJSON))
	}
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
	if r.deferSignalPublish {
		r.pendingSignals = append(r.pendingSignals, *ev)
		return nil
	}
	return r.eventBus.Publish(ctx, *ev)
}

// SetDeferSignalPublish flips emitSignal into buffer-only mode. When set,
// SignalCreated events go into pendingSignals instead of reaching the
// event bus. Callers MUST then invoke DrainPendingSignals after each
// HandleBarDirect to flush the buffer; otherwise signals sit unpublished.
func (r *Runner) SetDeferSignalPublish(v bool) {
	r.deferSignalPublish = v
}

// SetIsBacktest marks this runner as part of the backtest harness so
// ctx.IsBacktest() returns true to strategies. See Context.IsBacktest doc.
func (r *Runner) SetIsBacktest(v bool) {
	r.isBacktest = v
}

// SetDeferReconcile flips handleBar to skip the in-process
// ReconcileSignals pass. Used by slice-to-completion backtest so the
// reversal-entry conversion runs against live positions in the replay
// loop instead of the empty positions a shard sees mid-slice.
func (r *Runner) SetDeferReconcile(v bool) {
	r.deferReconcile = v
}

// DrainPendingSignals returns the buffered SignalCreated events in
// FIFO order and clears the slice. The returned slice aliases the
// runner's internal buffer and is only valid until the next
// HandleBarDirect / DrainPendingSignals call.
func (r *Runner) DrainPendingSignals() []domain.Event {
	out := r.pendingSignals
	r.pendingSignals = r.pendingSignals[:0]
	return out
}

// emitDomainEvent publishes an arbitrary domain event (used by strategy Context).
// Known payload types (EntryGatedPayload, ORBPhaseUpdatePayload) are routed to
// their specific event types; all others use the generic StrategyDomainEvent type.
func (r *Runner) emitDomainEvent(ctx context.Context, tenantID string, envMode domain.EnvMode, payload any) error {
	eventType := domain.EventType("StrategyDomainEvent")
	var cacheKey string
	isProgress := false
	switch p := payload.(type) {
	case domain.EntryGatedPayload:
		eventType = domain.EventEntryGated
		cacheKey = "EntryGated:" + p.Strategy + ":" + p.Symbol
		isProgress = true
		// Live persists blocked rows via EntryGatedWriter — this log line is
		// mainly for backtest (which uses NoopPnLRepo). Per-bar volume drops
		// dp_rolling_values (kept at SignalCreated which is rare); mean/std/
		// count are enough to detect buffer divergence.
		if parity.Enabled() {
			compsJSON, _ := json.Marshal(p.Confluence.Components)
			checksJSON, _ := json.Marshal(p.EntryChecks)
			barJSON, _ := json.Marshal(p.Bar)
			anchorsJSON := []byte("null")
			if p.AVWAPState != nil {
				anchorsJSON, _ = json.Marshal(p.AVWAPState.Anchors)
			}
			rollingMean, rollingStd, rollingCount := 0.0, 0.0, 0
			if rs, ok := r.dpRolling[p.Symbol]; ok && rs != nil {
				rollingMean, rollingStd = rs.meanStd()
				_, rollingCount = rs.snapshot()
			}
			r.logger.Info("parity-diag",
				"stage", parity.StageEntryGated,
				"symbol", p.Symbol,
				"strategy", p.Strategy,
				"setup", p.SetupType,
				"score", p.Confluence.Score,
				"max_score", p.Confluence.MaxScore,
				"blocking_gate", p.BlockingGate,
				"blocking_detail", p.BlockingDetail,
				"avwap_bias", p.Indicators.AVWAPBias,
				"slope_bps", p.Indicators.SlopeBPS,
				"rsi", p.Indicators.RSI,
				"vol_ratio", p.Indicators.VolumeRatio,
				"components", string(compsJSON),
				"entry_checks", string(checksJSON),
				"bar", string(barJSON),
				"avwap_anchors", string(anchorsJSON),
				"dp_rolling_mean", rollingMean,
				"dp_rolling_std", rollingStd,
				"dp_rolling_count", rollingCount)
		}
	case domain.ORBPhaseUpdatePayload:
		eventType = domain.EventORBPhaseUpdate
		cacheKey = "ORBPhaseUpdate:" + p.Symbol
		isProgress = true
	case domain.ChandelierTrailArmPayload:
		eventType = domain.EventChandelierTrailArm
	case domain.CopytradeExitRequestPayload:
		eventType = domain.EventCopytradeExitRequest
	case domain.CopytradeEntryExpiredPayload:
		eventType = domain.EventCopytradeEntryExpired
	case domain.CopytradeOrphanFillPayload:
		eventType = domain.EventCopytradeOrphanFill
	}
	if isProgress && r.suppressProgressEvents {
		return nil
	}
	// Use a monotonic counter for the idempotency key — strategies fire
	// gated events thousands of times per backtest and uuid.NewString()
	// reaches crypto/rand per call (~660k syscalls). The key only needs
	// to be unique within a run; live consumers (SSE) don't dedup on it.
	idemKey := strconv.FormatUint(strategyEmitSeq.Add(1), 36)
	ev, err := domain.NewEvent(
		eventType,
		tenantID,
		envMode,
		idemKey,
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
	dispatchSymbol := routingSymbol
	if inst == nil && strategyName == "copytrade_v1" {
		if fallback := r.findInstanceByStrategy(strategyName); fallback != nil {
			inst = fallback
			dispatchSymbol = copytradeSentinelSymbol
		}
	}
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
	signals, err := inst.OnEvent(instCtx, dispatchSymbol, confirmation)
	r.mu.Unlock()
	r.DrainCopytradeCallbacks()

	if err != nil {
		r.logger.Error("handleFill: OnEvent failed",
			"instance_id", inst.ID().String(),
			"symbol", symbol,
			"error", err,
		)
		return nil
	}

	_ = signals // Fill confirmations should not produce new signals.
	if r.liveness != nil && !r.disableLiveness {
		r.liveness.RecordFill(strategyName, routingSymbol, filledAt)
	}
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
	dispatchSymbol := payload.Symbol
	if inst == nil && payload.Strategy == "copytrade_v1" {
		if fallback := r.findInstanceByStrategy(payload.Strategy); fallback != nil {
			inst = fallback
			dispatchSymbol = copytradeSentinelSymbol
		}
	}
	if inst == nil {
		r.logger.Debug("handleRejection: no matching instance", "strategy", payload.Strategy, "symbol", rejSymbol)
		return nil
	}

	instCtx := &instanceContext{
		now:    r.handlerNow(event, "handleRejection"),
		logger: r.logger.With("instance_id", inst.ID().String(), "symbol", payload.Symbol),
		emit:   func(_ any) error { return nil },
	}

	rejection := start.EntryRejection{
		Symbol: payload.Symbol,
		Side:   rejSide,
		Reason: payload.Reason,
	}

	r.mu.Lock()
	signals, err := inst.OnEvent(instCtx, dispatchSymbol, rejection)
	r.mu.Unlock()
	r.DrainCopytradeCallbacks()

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

// handleTradeReceived routes a MarketTrade event to every strategy instance
// subscribed to the trade's symbol. Strategies that don't care about tick
// data can ignore the TradeTick event in their OnEvent switch; only flow-
// gated strategies (e.g. crypto_revert_v1) act on it. Fan-out is scoped by
// the router's symbol subscription so non-subscribed strategies are not
// flooded with ticks.
func (r *Runner) handleTradeReceived(_ context.Context, event domain.Event) error {
	trade, ok := event.Payload.(domain.MarketTrade)
	if !ok {
		return nil
	}
	symbol := string(trade.Symbol)
	instances := r.router.InstancesForSymbol(symbol)
	if len(instances) == 0 {
		return nil
	}

	tick := start.TradeTick{
		Symbol:    symbol,
		Time:      trade.Time,
		Price:     trade.Price,
		Size:      trade.Size,
		TakerSide: trade.TakerSide,
		Venue:     string(trade.Venue),
	}

	instCtx := &instanceContext{
		now:    trade.Time,
		logger: r.logger.With("symbol", symbol),
		emit:   func(_ any) error { return nil },
	}

	r.mu.Lock()
	for _, inst := range instances {
		if _, err := inst.OnEvent(instCtx, symbol, tick); err != nil {
			r.logger.Error("handleTradeReceived: OnEvent failed",
				"instance_id", inst.ID().String(),
				"symbol", symbol,
				"error", err,
			)
		}
	}
	r.mu.Unlock()
	return nil
}

// copytradeSentinelSymbol is the fixed symbol key under which the single-
// instance copytrade strategy stores its state. The strategy has no per-
// symbol routing (symbols = []); using a sentinel keeps Instance.states
// consistent with every other code path that indexes by symbol.
const copytradeSentinelSymbol = "__copytrade__"

// handlerNow returns event.OccurredAt so backtests see sim-time and live
// sees wall-clock. Falls back to wall clock if the envelope is unstamped,
// which should not happen — the canary log flags any path that misses it.
func (r *Runner) handlerNow(event domain.Event, handler string) time.Time {
	if !event.OccurredAt.IsZero() {
		return event.OccurredAt
	}
	r.logger.Debug("handler: event.OccurredAt is zero, falling back to wall clock",
		"handler", handler, "event_id", event.ID)
	return time.Now()
}

// handleCopytradeSignal routes a parsed Discord signal from the discord-
// copytrade sidecar to the single copytrade strategy instance. Because the
// strategy uses symbols = [], it lives in r.router.instances but not in the
// per-symbol routing map; we look it up by configStrategyID.
func (r *Runner) handleCopytradeSignal(ctx context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.CopytradeSignalPayload)
	if !ok {
		return nil
	}

	inst := r.findInstanceByStrategy("copytrade_v1")
	if inst == nil {
		r.logger.Debug("handleCopytradeSignal: no active copytrade_v1 instance")
		return nil
	}

	// Universe gate: if a history port is wired, drop signals for tickers that
	// were not tradable at PostedAt. Use PostedAt (not now) so replayed / backfilled
	// messages are validated against the state when the author posted.
	if r.universeHistory != nil {
		ok, err := r.universeHistory.WasTradable(ctx, domain.Symbol(payload.Ticker), payload.PostedAt)
		if err != nil {
			r.logger.Warn("handleCopytradeSignal: universe check failed",
				"ticker", string(payload.Ticker),
				"error", err,
			)
		} else if !ok {
			r.logger.Info("handleCopytradeSignal: ticker out-of-universe — dropping",
				"ticker", string(payload.Ticker),
				"author", payload.Author,
				"action", string(payload.Action),
				"posted_at", payload.PostedAt,
			)
			if r.notifier != nil {
				msg := fmt.Sprintf("copytrade: skipping out-of-universe %s %s %s %g%s @ %g (%s)",
					payload.Action, payload.Ticker,
					payload.Expiry.Format("2006-01-02"),
					payload.Strike, payload.Right,
					payload.Price, payload.Author,
				)
				_ = r.notifier.Notify(ctx, r.tenantID, msg)
			}
			return nil
		}
	}

	// Lazy-init sentinel state if bootstrap didn't seed it (defense in depth).
	if _, seeded := inst.GetState(copytradeSentinelSymbol); !seeded {
		initCtx := &instanceContext{
			ctx:      ctx,
			now:      r.handlerNow(event, "handleCopytradeSignal.init"),
			logger:   r.logger.With("instance_id", inst.ID().String(), "symbol", copytradeSentinelSymbol),
			tenantID: r.tenantID,
			envMode:  r.envMode,
			runner:   r,
		}
		if err := inst.InitSymbol(initCtx, copytradeSentinelSymbol, nil); err != nil {
			r.logger.Error("handleCopytradeSignal: InitSymbol failed",
				"instance_id", inst.ID().String(),
				"error", err,
			)
			return nil
		}
	}

	copySig := start.CopytradeSignal{
		SignalID:  payload.SignalID,
		MessageID: payload.MessageID,
		Author:    payload.Author,
		PostedAt:  payload.PostedAt,
		Action:    string(payload.Action),
		Ticker:    string(payload.Ticker),
		Expiry:    payload.Expiry,
		Strike:    payload.Strike,
		Right:     string(payload.Right),
		Price:     payload.Price,
		Tail:      payload.Tail,
		RawLine:   payload.RawLine,
	}

	instCtx := &instanceContext{
		ctx:      ctx,
		now:      r.handlerNow(event, "handleCopytradeSignal"),
		logger:   r.logger.With("instance_id", inst.ID().String(), "author", payload.Author, "ticker", string(payload.Ticker)),
		tenantID: r.tenantID,
		envMode:  r.envMode,
		runner:   r,
	}

	r.mu.Lock()
	signals, err := inst.OnEvent(instCtx, copytradeSentinelSymbol, copySig)
	r.mu.Unlock()
	r.DrainCopytradeCallbacks()
	if err != nil {
		r.logger.Error("handleCopytradeSignal: OnEvent failed",
			"instance_id", inst.ID().String(),
			"error", err,
		)
		return nil
	}

	for i := range signals {
		// Instance.OnEvent does not stamp StrategyInstanceID on returned
		// signals (only Instance.OnBar does). Stamp here so downstream
		// metrics and the SignalCreated event carry the right attribution.
		signals[i].StrategyInstanceID = inst.ID()
	}
	for _, sig := range signals {
		if !sig.Type.IsActionable() {
			continue
		}
		if emitErr := r.emitSignal(ctx, r.tenantID, r.envMode, sig); emitErr != nil {
			r.logger.Error("handleCopytradeSignal: emitSignal failed",
				"instance_id", inst.ID().String(),
				"symbol", sig.Symbol,
				"error", emitErr,
			)
		}
	}
	return nil
}

// handleCopytradeExitRejected routes a position-monitor-published exit
// rejection (prior exit in flight) to the copytrade strategy so it can roll
// its RemainingFrac back. The strategy has no per-symbol routing so we look
// up the single instance by strategy ID and dispatch on the sentinel symbol.
//
// This runs on the same goroutine that holds inst.mu and r.mu when syncMode
// is on (backtest), so we defer the actual Instance.OnEvent dispatch into
// pendingCopytradeCallbacks instead of invoking it here. Callers drain the
// queue after the outer OnEvent returns (see DrainCopytradeCallbacks).
func (r *Runner) handleCopytradeExitRejected(ctx context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.CopytradeExitRejectedPayload)
	if !ok {
		return nil
	}
	inst := r.findInstanceByStrategy("copytrade_v1")
	if inst == nil {
		r.logger.Debug("handleCopytradeExitRejected: no active copytrade_v1 instance")
		return nil
	}
	instCtx := &instanceContext{
		ctx:      ctx,
		now:      r.handlerNow(event, "handleCopytradeExitRejected"),
		logger:   r.logger.With("instance_id", inst.ID().String(), "contract_symbol", payload.ContractSymbol),
		tenantID: r.tenantID,
		envMode:  r.envMode,
		runner:   r,
	}
	rej := start.CopytradeExitRejection{
		ContractSymbol: payload.ContractSymbol,
		Fraction:       payload.Fraction,
		Reason:         payload.Reason,
	}
	r.enqueueCopytradeCallback(func() {
		if _, err := inst.OnEvent(instCtx, copytradeSentinelSymbol, rej); err != nil {
			r.logger.Error("handleCopytradeExitRejected: OnEvent failed",
				"instance_id", inst.ID().String(),
				"error", err,
			)
		}
	})
	return nil
}

func (r *Runner) enqueueCopytradeCallback(fn func()) {
	r.copytradeCallbackMu.Lock()
	r.copytradeCallbacks = append(r.copytradeCallbacks, fn)
	r.copytradeCallbackMu.Unlock()
}

// DrainCopytradeCallbacks must be invoked after inst.mu and r.mu are
// released. Loops to handle cascading work — a drained callback can
// publish events that enqueue more callbacks.
func (r *Runner) DrainCopytradeCallbacks() {
	for {
		r.copytradeCallbackMu.Lock()
		if len(r.copytradeCallbacks) == 0 {
			r.copytradeCallbackMu.Unlock()
			return
		}
		batch := r.copytradeCallbacks
		r.copytradeCallbacks = nil
		r.copytradeCallbackMu.Unlock()
		for _, fn := range batch {
			fn()
		}
	}
}

// handleTradingTheTrendSignal routes a parsed Discord watchlist line from
// the discord-tradingthetrend sidecar to the single tradingthetrend
// instance, keying state by ticker (one arm per ticker per session).
//
// Unlike copytrade, TTT has watchlist_mode=dynamic with symbols=[]; per-
// ticker state is lazy-initialized on the first signal for that ticker.
// The strategy's OnBar then drives the break-and-retest state machine
// against bars on that underlying.
func (r *Runner) handleTradingTheTrendSignal(ctx context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.TradingTheTrendSignalPayload)
	if !ok {
		return nil
	}

	ticker := string(payload.Ticker)
	if ticker == "" {
		r.logger.Warn("handleTradingTheTrendSignal: empty ticker, dropping signal",
			"signal_id", payload.SignalID)
		return nil
	}

	// TTT registers as a single sentinel-rooted instance per shard
	// (symbols = ["__tradingthetrend__"] in TOML); per-ticker bar routing
	// is grown dynamically as signals arrive. This handler:
	//   1. Finds the sentinel instance
	//   2. Lazy-inits per-ticker state on it (one arm per ticker per session)
	//   3. Adds the ticker to the router so subsequent bars dispatch to the
	//      sentinel instance — bar routing follows the watchlist, not the
	//      universe slab.
	inst := r.findInstanceByStrategy("tradingthetrend_v1")
	if inst == nil {
		r.logger.Debug("handleTradingTheTrendSignal: no active tradingthetrend_v1 instance")
		return nil
	}

	if _, seeded := inst.GetState(ticker); !seeded {
		initCtx := &instanceContext{
			ctx:      ctx,
			now:      r.handlerNow(event, "handleTradingTheTrendSignal.init"),
			logger:   r.logger.With("instance_id", inst.ID().String(), "symbol", ticker),
			tenantID: r.tenantID,
			envMode:  r.envMode,
			runner:   r,
		}
		if err := inst.InitSymbol(initCtx, ticker, nil); err != nil {
			r.logger.Error("handleTradingTheTrendSignal: InitSymbol failed",
				"instance_id", inst.ID().String(),
				"ticker", ticker,
				"error", err,
			)
			return nil
		}
		// Register the ticker in the router so subsequent bars for it
		// dispatch to this sentinel-rooted instance. Bar routing follows
		// the watchlist union, not the universe slab.
		if r.router != nil {
			r.router.AddSymbol(inst.ID(), ticker)
		}
		// Wire HTF subscribers for the new ticker so 5m/15m/etc. closes
		// drive the strategy's OnBar. Without this, late-arriving symbols
		// land in the router but the indicator service never calls back
		// for them on HTF boundaries (InitAggregators ran at bootstrap
		// before this ticker existed).
		r.subscribeHTFForInstanceSymbol(inst, ticker)
	}

	tttSig := start.TradingTheTrendSignal{
		SignalID:  payload.SignalID,
		MessageID: payload.MessageID,
		Author:    payload.Author,
		PostedAt:  payload.PostedAt,
		Ticker:    ticker,
		Strike:    payload.Strike,
		Right:     string(payload.Right),
		Trigger:   payload.Trigger,
		RawLine:   payload.RawLine,
	}

	instCtx := &instanceContext{
		ctx:      ctx,
		now:      r.handlerNow(event, "handleTradingTheTrendSignal"),
		logger:   r.logger.With("instance_id", inst.ID().String(), "ticker", ticker, "author", payload.Author),
		tenantID: r.tenantID,
		envMode:  r.envMode,
		runner:   r,
	}

	r.mu.Lock()
	signals, err := inst.OnEvent(instCtx, ticker, tttSig)
	r.mu.Unlock()
	if err != nil {
		r.logger.Error("handleTradingTheTrendSignal: OnEvent failed",
			"instance_id", inst.ID().String(),
			"error", err,
		)
		return nil
	}

	for i := range signals {
		signals[i].StrategyInstanceID = inst.ID()
	}
	for _, sig := range signals {
		if !sig.Type.IsActionable() {
			continue
		}
		if emitErr := r.emitSignal(ctx, r.tenantID, r.envMode, sig); emitErr != nil {
			r.logger.Error("handleTradingTheTrendSignal: emitSignal failed",
				"instance_id", inst.ID().String(),
				"symbol", sig.Symbol,
				"error", emitErr,
			)
		}
	}
	return nil
}

// findInstanceByStrategy returns the first active instance matching the given
// strategy ID across all registered instances, ignoring symbol routing. Used
// by handlers dispatching event-driven strategies (copytrade) that don't have
// per-symbol routing. Returns nil when no active instance matches.
func (r *Runner) findInstanceByStrategy(strategyName string) *Instance {
	for _, inst := range r.router.AllInstances() {
		if inst.configStrategyID() == strategyName && inst.IsActive() {
			return inst
		}
	}
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
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Symbols           []string `json:"symbols"`
	Priority          int      `json:"priority"`
	Active            bool     `json:"active"`
	NoviceDescription string   `json:"noviceDescription,omitempty"`
}

func (r *Runner) ListStrategies() []StrategyInfo {
	instances := r.router.AllInstances()
	infos := make([]StrategyInfo, 0, len(instances))
	for _, inst := range instances {
		meta := inst.Strategy().Meta()
		infos = append(infos, StrategyInfo{
			ID:                inst.configStrategyID(),
			Name:              meta.Name,
			Version:           meta.Version.String(),
			Symbols:           inst.Assignment().Symbols,
			Priority:          inst.Assignment().Priority,
			Active:            inst.IsActive(),
			NoviceDescription: inst.NoviceDescription(),
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
