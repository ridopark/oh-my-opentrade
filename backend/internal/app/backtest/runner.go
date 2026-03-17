package backtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/adapters/noop"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// RunConfig holds the parameters for a backtest run.
type RunConfig struct {
	Symbols       []domain.Symbol
	From          time.Time
	To            time.Time
	Timeframe     domain.Timeframe
	InitialEquity float64
	SlippageBPS   int64
	Speed         string
	NoAI          bool
	StrategyDir   string
	Strategies    []string
}

// ProgressInfo tracks replay progress.
type ProgressInfo struct {
	BarsProcessed int       `json:"bars_processed"`
	TotalBars     int       `json:"total_bars"`
	Pct           float64   `json:"pct"`
	CurrentTime   time.Time `json:"current_time"`
	Speed         string    `json:"replay_speed"`
}

// Runner executes a single backtest using an isolated event bus and SimBroker.
type Runner struct {
	id     string
	cfg    RunConfig
	db     *sql.DB
	appCfg *config.Config
	log    zerolog.Logger

	eventBus  *memory.Bus
	collector *Collector
	emitter   *Emitter

	speedDelay atomic.Value // time.Duration
	paused     atomic.Bool
	pauseMu    sync.Mutex
	pauseCh    chan struct{}

	status   atomic.Value // string
	progress atomic.Value // *ProgressInfo
	result   atomic.Value // *Result

	cancelFn context.CancelFunc
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "bt-" + hex.EncodeToString(b)
}

// NewRunner creates a backtest Runner with an isolated event bus.
func NewRunner(cfg RunConfig, db *sql.DB, appCfg *config.Config, log zerolog.Logger) *Runner {
	id := generateID()
	rlog := log.With().Str("backtest_id", id).Str("component", "backtest_runner").Logger()

	r := &Runner{
		id:       id,
		cfg:      cfg,
		db:       db,
		appCfg:   appCfg,
		log:      rlog,
		eventBus: memory.NewBus(),
		emitter:  NewEmitter(rlog, cfg.Timeframe),
		pauseCh:  make(chan struct{}),
	}

	delay, _ := parseSpeedToDelay(cfg.Speed, cfg.Timeframe)
	r.speedDelay.Store(delay)
	r.status.Store("pending")
	r.progress.Store((*ProgressInfo)(nil))
	r.result.Store((*Result)(nil))

	return r
}

// ID returns the unique backtest identifier.
func (r *Runner) ID() string { return r.id }

// Status returns the current status string.
func (r *Runner) Status() string {
	v := r.status.Load()
	if v == nil {
		return "pending"
	}
	return v.(string)
}

// Progress returns the latest progress snapshot.
func (r *Runner) Progress() *ProgressInfo {
	v := r.progress.Load()
	if v == nil {
		return nil
	}
	return v.(*ProgressInfo)
}

// GetResult returns the final backtest result (nil until completed).
func (r *Runner) GetResult() *Result {
	v := r.result.Load()
	if v == nil {
		return nil
	}
	return v.(*Result)
}

// Emitter returns the SSE emitter for this backtest (used by HTTP handler).
func (r *Runner) GetEmitter() *Emitter { return r.emitter }

// Pause pauses the replay loop.
func (r *Runner) Pause() {
	r.paused.Store(true)
	r.status.Store("paused")
}

// Resume unblocks a paused replay loop.
func (r *Runner) Resume() {
	r.pauseMu.Lock()
	r.paused.Store(false)
	close(r.pauseCh)
	r.pauseCh = make(chan struct{})
	r.pauseMu.Unlock()
	r.status.Store("running")
}

// SetSpeed dynamically changes the replay speed.
func (r *Runner) SetSpeed(speedStr string) error {
	delay, err := parseSpeedToDelay(speedStr, r.cfg.Timeframe)
	if err != nil {
		return err
	}
	r.speedDelay.Store(delay)
	r.cfg.Speed = speedStr
	return nil
}

// Cancel stops a running backtest.
func (r *Runner) Cancel() {
	r.status.Store("cancelled")
	if r.cancelFn != nil {
		r.cancelFn()
	}
}

func (r *Runner) currentSpeed() string {
	delay := r.speedDelay.Load().(time.Duration)
	switch {
	case delay == 0:
		return "max"
	case delay <= 10*time.Millisecond:
		return "10x"
	case delay <= 40*time.Millisecond:
		return "5x"
	case delay <= 100*time.Millisecond:
		return "2x"
	default:
		return "1x"
	}
}

// Run executes the full backtest. Blocks until completion or cancellation.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	r.cancelFn = cancel
	defer cancel()

	r.status.Store("running")
	r.log.Info().
		Strs("symbols", symbolStrings(r.cfg.Symbols)).
		Time("from", r.cfg.From).
		Time("to", r.cfg.To).
		Str("speed", r.cfg.Speed).
		Float64("equity", r.cfg.InitialEquity).
		Msg("backtest starting")

	repo := timescaledb.NewRepositoryWithLogger(
		timescaledb.NewSqlDB(r.db),
		r.log.With().Str("component", "timescaledb").Logger(),
	)

	const replayTimeframe = domain.Timeframe("1m")

	var currentBarTime atomic.Value
	currentBarTime.Store(time.Now())
	clockFn := func() time.Time { return currentBarTime.Load().(time.Time) }

	// --- Build pipeline (isolated event bus) ---

	ingBundle, err := bootstrap.BuildIngestion(bootstrap.IngestionDeps{
		EventBus:   r.eventBus,
		Repo:       &noop.NoopRepo{},
		IsBacktest: true,
		Logger:     r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build ingestion: %w", err)
	}

	monitorSvc, err := bootstrap.BuildMonitor(bootstrap.MonitorDeps{
		EventBus: r.eventBus,
		Repo:     repo,
		Logger:   r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build monitor: %w", err)
	}

	specDir := r.cfg.StrategyDir
	if specDir == "" {
		specDir = "/home/ridopark/src/oh-my-opentrade/configs/strategies"
	}
	var specStore portstrategy.SpecStore = store_fs.NewStore(specDir, strategy.LoadSpecFile)
	if len(r.cfg.Strategies) > 0 {
		specStore = &filteredSpecStore{inner: specStore, allowed: r.cfg.Strategies}
	}

	orbID, _ := start.NewStrategyID("orb_break_retest")
	if orbSpec, loadErr := specStore.GetLatest(context.Background(), orbID); loadErr == nil {
		monitorSvc.SetORBConfig(orbSpec.Params)
	}

	sim := simbroker.New(simbroker.Config{
		SlippageBPS:     r.cfg.SlippageBPS,
		InitialEquity:   r.cfg.InitialEquity,
		DisableFillChan: true,
	}, r.log.With().Str("component", "simbroker").Logger())

	execBundle, err := bootstrap.BuildExecutionService(bootstrap.ExecutionDeps{
		EventBus:      r.eventBus,
		Broker:        sim,
		Repo:          &noop.NoopRepo{},
		QuoteProvider: sim,
		AccountPort:   sim,
		PnLRepo:       &noop.NoopPnLRepo{},
		TradeReader:   nil,
		Clock:         clockFn,
		Config:        r.appCfg,
		InitialEquity: r.cfg.InitialEquity,
		IsBacktest:    true,
		Logger:        r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build execution: %w", err)
	}

	posMonBundle, err := bootstrap.BuildPositionMonitor(bootstrap.PosMonitorDeps{
		EventBus:     r.eventBus,
		PositionGate: execBundle.PositionGate,
		Broker:       sim,
		SpecStore:    specStore,
		SnapshotFn:   monitorSvc.GetLastSnapshot,
		TenantID:     "default",
		EnvMode:      domain.EnvModePaper,
		Clock:        clockFn,
		IsBacktest:   true,
		Logger:       r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build position monitor: %w", err)
	}

	pipeline, err := bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
		EventBus:        r.eventBus,
		SpecStore:       specStore,
		AIAdvisor:       llm.NewNoOpAdvisor(),
		PositionLookup:  posMonBundle.Service.LookupPosition,
		MarketDataFn:    monitorSvc.GetLastSnapshot,
		Repo:            nil,
		TenantID:        "default",
		EnvMode:         domain.EnvModePaper,
		Equity:          r.cfg.InitialEquity,
		Clock:           clockFn,
		DisableEnricher: r.cfg.NoAI,
		Logger:          r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build strategy pipeline: %w", err)
	}

	if pipeline.Enricher == nil {
		if subErr := r.eventBus.Subscribe(ctx, domain.EventSignalCreated, signalPassthrough(r.eventBus, r.log)); subErr != nil {
			r.status.Store("error")
			return fmt.Errorf("subscribe signal passthrough: %w", subErr)
		}
	}

	signalTracker := perf.NewSignalTracker(r.eventBus, &noop.NoopPnLRepo{}, r.log.With().Str("component", "signal_tracker").Logger())
	monitorSvc.SetBaseSymbols(pipeline.BaseSymbols)

	r.collector, err = NewCollector(r.eventBus, Config{InitialEquity: r.cfg.InitialEquity}, r.log.With().Str("component", "backtest_collector").Logger())
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("create collector: %w", err)
	}

	// Subscribe SSE emitter to our isolated bus.
	r.emitter.SetSnapshotFn(monitorSvc.GetLastSnapshot)
	if subErr := r.emitter.Subscribe(ctx, r.eventBus); subErr != nil {
		r.status.Store("error")
		return fmt.Errorf("subscribe emitter: %w", subErr)
	}

	// --- Warmup ---

	prevStart, prevEnd := domain.PreviousRTHSession(r.cfg.From)
	warmupFrom := r.cfg.From.Add(-7 * 24 * time.Hour)
	warmupTo := r.cfg.From
	_ = prevEnd
	r.log.Info().
		Time("prev_session_start", prevStart).
		Time("prev_session_end", prevEnd).
		Time("warmup_from", warmupFrom).
		Time("warmup_to", warmupTo).
		Msg("warming indicators")

	warmupBarsCache := make(map[string][]domain.MarketBar, len(r.cfg.Symbols))
	for _, sym := range r.cfg.Symbols {
		bars, fetchErr := repo.GetMarketBars(ctx, sym, replayTimeframe, warmupFrom, warmupTo)
		if fetchErr != nil {
			r.log.Warn().Err(fetchErr).Str("symbol", sym.String()).Msg("warmup fetch failed")
			continue
		}
		warmupBarsCache[sym.String()] = bars
		n := monitorSvc.WarmUp(bars)
		monitorSvc.ResetSessionIndicators(sym.String())
		monitorSvc.MarkReady(sym.String())
		r.log.Debug().Str("symbol", sym.String()).Int("bars", n).Msg("indicator warmup done")
	}

	for _, sym := range r.cfg.Symbols {
		if bars, ok := warmupBarsCache[sym.String()]; ok && len(bars) > 0 {
			ingBundle.Filter.Seed(sym, bars)
		}
	}

	loc, _ := time.LoadLocation("America/New_York")
	fromET := r.cfg.From.In(loc)
	replaySessionOpen := time.Date(fromET.Year(), fromET.Month(), fromET.Day(), 9, 30, 0, 0, loc)
	monitorSvc.InitAggregators(r.cfg.Symbols, replaySessionOpen)

	if pipeline.Runner != nil {
		snapshotFn := makeSnapshotFn()
		for _, sym := range r.cfg.Symbols {
			bars := warmupBarsCache[sym.String()]
			if len(bars) == 0 {
				continue
			}
			pipeline.Runner.WarmUp(sym.String(), bars, snapshotFn)
		}
		pipeline.Runner.InitAggregators(replaySessionOpen)
		pipeline.Runner.ClearAllPendingStates()
	}

	// --- Load replay bars ---

	type barStream struct {
		symbol domain.Symbol
		bars   []domain.MarketBar
		idx    int
	}
	peekBar := func(s *barStream) (domain.MarketBar, bool) {
		if s == nil || s.idx >= len(s.bars) {
			return domain.MarketBar{}, false
		}
		return s.bars[s.idx], true
	}
	popBar := func(s *barStream) {
		if s != nil && s.idx < len(s.bars) {
			s.idx++
		}
	}

	streams := make([]*barStream, 0, len(r.cfg.Symbols))
	totalBars := 0
	for _, sym := range r.cfg.Symbols {
		bars, fetchErr := repo.GetMarketBars(ctx, sym, replayTimeframe, r.cfg.From, r.cfg.To)
		if fetchErr != nil {
			r.status.Store("error")
			return fmt.Errorf("load bars for %s: %w", sym, fetchErr)
		}
		streams = append(streams, &barStream{symbol: sym, bars: bars})
		totalBars += len(bars)
		r.log.Info().Str("symbol", sym.String()).Int("bars", len(bars)).Msg("loaded bars")
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].symbol.String() < streams[j].symbol.String() })

	// --- Start services ---

	if startErr := ingBundle.Service.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start ingestion: %w", startErr)
	}
	if startErr := monitorSvc.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start monitor: %w", startErr)
	}
	if startErr := execBundle.LedgerWriter.Start(ctx, "backtest", domain.EnvModePaper); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start ledger writer: %w", startErr)
	}
	if startErr := signalTracker.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start signal tracker: %w", startErr)
	}
	if startErr := execBundle.Service.Start(ctx, "backtest", domain.EnvModePaper); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start execution: %w", startErr)
	}
	if startErr := posMonBundle.PriceCache.Start(ctx, r.eventBus); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start price cache: %w", startErr)
	}
	if startErr := posMonBundle.Service.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start position monitor: %w", startErr)
	}
	if startErr := pipeline.Runner.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start strategy runner: %w", startErr)
	}
	if pipeline.Enricher != nil {
		if startErr := pipeline.Enricher.Start(ctx); startErr != nil {
			r.status.Store("error")
			return fmt.Errorf("start signal enricher: %w", startErr)
		}
	}
	pipeline.RiskSizer.SetNowFn(clockFn)
	pipeline.RiskSizer.SetExitCooldown(3 * time.Minute)
	if startErr := pipeline.RiskSizer.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start risk sizer: %w", startErr)
	}

	// --- Replay loop ---

	const tenantID = "default"
	envMode := domain.EnvModePaper
	barsProcessed := 0
	currentSessionDate := replaySessionOpen

	for ctx.Err() == nil {
		// Find next minimum timestamp across all streams.
		var minTime time.Time
		found := false
		for _, s := range streams {
			b, ok := peekBar(s)
			if !ok {
				continue
			}
			if !found || b.Time.Before(minTime) {
				minTime = b.Time
				found = true
			}
		}
		if !found {
			break
		}

		// Pause gate.
		if r.paused.Load() {
			r.pauseMu.Lock()
			ch := r.pauseCh
			r.pauseMu.Unlock()
			select {
			case <-ctx.Done():
				break
			case <-ch:
			}
			if ctx.Err() != nil {
				break
			}
		}

		currentBarTime.Store(minTime)

		// Reset MTFA aggregators on new trading day.
		minET := minTime.In(loc)
		dayOpen := time.Date(minET.Year(), minET.Month(), minET.Day(), 9, 30, 0, 0, loc)
		if dayOpen.After(currentSessionDate) {
			monitorSvc.ResetAggregators(dayOpen)
			for _, sym := range r.cfg.Symbols {
				monitorSvc.ResetSessionIndicators(sym.String())
			}
			currentSessionDate = dayOpen
		}

		for _, s := range streams {
			if ctx.Err() != nil {
				break
			}
			bar, has := peekBar(s)
			if !has || !bar.Time.Equal(minTime) {
				continue
			}
			popBar(s)

			sim.UpdatePrice(bar.Symbol, bar.Close, bar.Time)

			evt, evtErr := domain.NewEvent(domain.EventMarketBarReceived, tenantID, envMode, bar.Time.String()+string(bar.Symbol), bar)
			if evtErr != nil {
				continue
			}
			if pubErr := r.eventBus.Publish(ctx, *evt); pubErr != nil {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			barsProcessed++
		}

		r.eventBus.WaitPending()
		if posMonBundle.Service != nil {
			posMonBundle.Service.EvalExitRules(minTime)
			r.eventBus.WaitPending()
		}

		// Emit progress every 10 bar groups.
		if barsProcessed%10 == 0 || !found {
			pct := 0.0
			if totalBars > 0 {
				pct = math.Round(float64(barsProcessed)/float64(totalBars)*1000) / 10
			}
			pi := &ProgressInfo{
				BarsProcessed: barsProcessed,
				TotalBars:     totalBars,
				Pct:           pct,
				CurrentTime:   minTime,
				Speed:         r.currentSpeed(),
			}
			r.progress.Store(pi)
			r.emitter.EmitProgress(pi)

			// Emit live metrics.
			if r.collector != nil {
				partialResult := r.collector.Result()
				r.emitter.EmitMetrics(map[string]any{
					"equity":         partialResult.FinalEquity,
					"total_pnl":      partialResult.TotalPnL,
					"total_return":   partialResult.TotalReturn,
					"trades":         partialResult.TradeCount,
					"win_rate":       partialResult.WinRate,
					"max_drawdown":   partialResult.MaxDrawdown,
					"sharpe":         partialResult.SharpeRatio,
					"profit_factor":  partialResult.ProfitFactor,
					"open_positions": len(r.collector.openBuys),
				})
			}
		}

		if ctx.Err() != nil {
			break
		}

		// Speed delay.
		delay := r.speedDelay.Load().(time.Duration)
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
	}

	// --- Completion ---

	finalResult := r.collector.Result()
	r.result.Store(&finalResult)
	r.emitter.EmitComplete(&finalResult)

	if r.Status() != "cancelled" {
		r.status.Store("completed")
	}

	r.log.Info().
		Int("bars_processed", barsProcessed).
		Int("trades", finalResult.TradeCount).
		Float64("final_equity", finalResult.FinalEquity).
		Float64("total_return_pct", finalResult.TotalReturn).
		Msg("backtest complete")

	return nil
}

// --- Helpers ---

func signalPassthrough(bus *memory.Bus, log zerolog.Logger) func(context.Context, domain.Event) error {
	return func(ctx context.Context, ev domain.Event) error {
		sig, ok := ev.Payload.(start.Signal)
		if !ok {
			return nil
		}
		direction := domain.DirectionLong
		if sig.Side == start.SideSell {
			direction = domain.DirectionShort
		}
		if sig.Type == start.SignalExit {
			direction = domain.DirectionCloseLong
		}
		enrichment := domain.SignalEnrichment{
			Signal: domain.SignalRef{
				StrategyInstanceID: string(sig.StrategyInstanceID),
				Symbol:             sig.Symbol,
				SignalType:         sig.Type.String(),
				Side:               sig.Side.String(),
				Strength:           sig.Strength,
				Tags:               sig.Tags,
			},
			Status:     domain.EnrichmentSkipped,
			Confidence: sig.Strength,
			Direction:  direction,
			Rationale:  fmt.Sprintf("passthrough (no-ai): %s %s strength=%.2f", sig.Type, sig.Side, sig.Strength),
		}
		enrichedEvt, evtErr := domain.NewEvent(domain.EventSignalEnriched, ev.TenantID, ev.EnvMode, ev.IdempotencyKey+"-enriched", enrichment)
		if evtErr != nil {
			log.Error().Err(evtErr).Msg("failed to create enriched event in passthrough")
			return nil
		}
		return bus.Publish(ctx, *enrichedEvt)
	}
}

func makeSnapshotFn() strategy.IndicatorSnapshotFunc {
	calc := monitor.NewIndicatorCalculator()
	return func(bar domain.MarketBar) start.IndicatorData {
		snap := calc.Update(bar)
		return start.IndicatorData{
			RSI:           snap.RSI,
			StochK:        snap.StochK,
			StochD:        snap.StochD,
			EMA9:          snap.EMA9,
			EMA21:         snap.EMA21,
			EMAFast:       snap.EMAFast,
			EMASlow:       snap.EMASlow,
			EMAFastPeriod: snap.EMAFastPeriod,
			EMASlowPeriod: snap.EMASlowPeriod,
			VWAP:          snap.VWAP,
			Volume:        snap.Volume,
			VolumeSMA:     snap.VolumeSMA,
		}
	}
}

func parseSpeedToDelay(speedStr string, _ domain.Timeframe) (time.Duration, error) {
	s := strings.TrimSpace(strings.ToLower(speedStr))
	switch s {
	case "", "max":
		return 0, nil
	case "1x":
		return 200 * time.Millisecond, nil
	case "2x":
		return 100 * time.Millisecond, nil
	case "5x":
		return 40 * time.Millisecond, nil
	case "10x":
		return 10 * time.Millisecond, nil
	default:
		s = strings.TrimSuffix(s, "x")
		f, parseErr := parseFloat(s)
		if parseErr != nil {
			return 0, fmt.Errorf("invalid speed %q: %w", speedStr, parseErr)
		}
		if f <= 0 {
			return 0, fmt.Errorf("speed must be > 0, got %f", f)
		}
		delay := time.Duration(200/f) * time.Millisecond
		if delay < time.Millisecond {
			delay = 0
		}
		return delay, nil
	}
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

func timeframeToDuration(tf domain.Timeframe) (time.Duration, error) {
	switch tf {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown timeframe: %s", tf)
	}
}

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.String()
	}
	return out
}

type filteredSpecStore struct {
	inner   portstrategy.SpecStore
	allowed []string
}

func (f *filteredSpecStore) List(ctx context.Context, filter *portstrategy.SpecFilter) ([]portstrategy.Spec, error) {
	all, err := f.inner.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(f.allowed))
	for _, id := range f.allowed {
		allow[id] = true
	}
	var out []portstrategy.Spec
	for _, s := range all {
		if allow[string(s.ID)] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *filteredSpecStore) Get(ctx context.Context, id start.StrategyID, version start.Version) (*portstrategy.Spec, error) {
	return f.inner.Get(ctx, id, version)
}

func (f *filteredSpecStore) GetLatest(ctx context.Context, id start.StrategyID) (*portstrategy.Spec, error) {
	return f.inner.GetLatest(ctx, id)
}

func (f *filteredSpecStore) Save(ctx context.Context, spec portstrategy.Spec) error {
	return f.inner.Save(ctx, spec)
}

func (f *filteredSpecStore) Watch(ctx context.Context) (<-chan start.StrategyID, error) {
	return f.inner.Watch(ctx)
}
