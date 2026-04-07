package backtest

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/backfill"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
	"github.com/oh-my-opentrade/backend/internal/app/debate"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// RunConfig holds the parameters for a backtest run.
type RunConfig struct {
	Symbols          []domain.Symbol
	From             time.Time
	To               time.Time
	Timeframe        domain.Timeframe
	InitialEquity    float64
	SlippageBPS      int64
	Speed            string
	NoAI             bool
	StrategyDir      string
	Strategies       []string
	UseDailyScreener bool            // dynamically pick symbols each day using screener
	ScreenerTopN     int             // how many top symbols to pick per day (default 5)
	FixedSymbols     []domain.Symbol // user's original symbols (always active, union with screener)
	MaxPositions     int             // portfolio-level max simultaneous positions (0=use config default)
	MaxPerGroup      int             // max positions per sector group (0=use config default)
	CompoundEquity   bool            // when true, position sizing compounds with P&L
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
	id         string
	cfg        RunConfig
	appCfg     *config.Config
	marketData ports.MarketDataPort
	infra      bootstrap.BacktestInfra
	log        zerolog.Logger

	collector *Collector
	emitter   *Emitter

	speedDelay    atomic.Value // time.Duration
	paused        atomic.Bool
	pauseMu       sync.Mutex
	pauseCh       chan struct{}
	lastEmitTime  time.Time // throttle progress/metrics emission

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
func NewRunner(cfg RunConfig, infra bootstrap.BacktestInfra, appCfg *config.Config, marketData ports.MarketDataPort, log zerolog.Logger) *Runner {
	id := generateID()
	rlog := log.With().Str("backtest_id", id).Str("component", "backtest_runner").Logger()

	r := &Runner{
		id:         id,
		cfg:        cfg,
		appCfg:     appCfg,
		marketData: marketData,
		infra:      infra,
		log:        rlog,
		emitter:    NewEmitter(rlog, cfg.Timeframe),
		pauseCh:    make(chan struct{}),
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
	r.status.Store("canceled")
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
	r.emitter.EmitSetup("Initializing pipeline…")
	r.log.Info().
		Strs("symbols", symbolStrings(r.cfg.Symbols)).
		Time("from", r.cfg.From).
		Time("to", r.cfg.To).
		Str("speed", r.cfg.Speed).
		Float64("equity", r.cfg.InitialEquity).
		Msg("backtest starting")

	repo := r.infra.Repo

	const replayTimeframe = domain.Timeframe("1m")

	var currentBarTime atomic.Value
	currentBarTime.Store(r.cfg.From) // use backtest start time, not wall clock
	clockFn := func() time.Time { return currentBarTime.Load().(time.Time) }

	// --- Build pipeline (isolated event bus) ---

	ingBundle, err := bootstrap.BuildIngestion(bootstrap.IngestionDeps{
		EventBus:   r.infra.EventBus,
		Repo:       r.infra.NoopRepo,
		IsBacktest: true,
		Logger:     r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build ingestion: %w", err)
	}

	monitorSvc, err := bootstrap.BuildMonitor(bootstrap.MonitorDeps{
		EventBus:   r.infra.EventBus,
		Repo:       repo,
		Logger:     r.log,
		BacktestID: r.id,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build monitor: %w", err)
	}

	specDir := r.cfg.StrategyDir
	if specDir == "" {
		specDir = "/home/ridopark/src/oh-my-opentrade/configs/strategies"
	}
	// Fallback for Docker container
	if _, err := os.Stat(specDir); err != nil {
		specDir = "/configs/strategies"
	}
	if specDir == "" {
		specDir = "/home/ridopark/src/oh-my-opentrade/configs/strategies"
	}
	var specStore = bootstrap.NewBacktestSpecStore(specDir)
	if len(r.cfg.Strategies) > 0 {
		specStore = &filteredSpecStore{inner: specStore, allowed: r.cfg.Strategies}
	}
	// Override each strategy's routing.symbols with the UI-requested symbols
	// so the strategy runs on exactly the symbols the user selected.
	if len(r.cfg.Symbols) > 0 {
		syms := make([]string, len(r.cfg.Symbols))
		for i, s := range r.cfg.Symbols {
			syms[i] = s.String()
		}
		specStore = &symbolOverrideSpecStore{inner: specStore, symbols: syms}
	}

	// Only load ORB config if orb_break_retest is among the selected strategies
	// (or no strategy filter is set, meaning all are active).
	orbSelected := len(r.cfg.Strategies) == 0
	for _, s := range r.cfg.Strategies {
		if s == "orb_break_retest" {
			orbSelected = true
			break
		}
	}
	var spyDailyBars []domain.MarketBar
	if orbSelected {
		orbID, _ := start.NewStrategyID("orb_break_retest")
		if orbSpec, loadErr := specStore.GetLatest(context.Background(), orbID); loadErr == nil {
			monitorSvc.SetORBConfig(orbSpec.Params)
			if len(orbSpec.Routing.Timeframes) > 0 {
				monitorSvc.SetORBTimeframe(string(orbSpec.Routing.Timeframes[0]))
			}
		}

		// Fetch SPY daily bars ONCE for the entire backtest range (with 60-day
		// lookback before From) so we can recompute realized vol per day without
		// repeated DB queries.
		spySym, _ := domain.NewSymbol("SPY")
		rvFrom := r.cfg.From.Add(-60 * 24 * time.Hour) // 60 days to have enough for 20-day window
		spyDailyBars, _ = repo.GetMarketBars(ctx, spySym, "1d", rvFrom, r.cfg.To)
		if len(spyDailyBars) == 0 && r.marketData != nil {
			apiBars, _ := r.marketData.GetHistoricalBars(ctx, spySym, "1d", rvFrom, r.cfg.To)
			if len(apiBars) > 0 {
				spyDailyBars = apiBars
			}
		}
		// Set initial VIX level using bars up to the backtest start.
		var initBars []domain.MarketBar
		for _, b := range spyDailyBars {
			if b.Time.Before(r.cfg.From) {
				initBars = append(initBars, b)
			}
		}
		if len(initBars) > 21 {
			rv := monitor.ComputeRealizedVol(initBars, 20)
			monitorSvc.SetVIXLevel(rv)
			r.log.Info().Float64("realized_vol", rv).Int("daily_bars", len(spyDailyBars)).Msg("VIX level set from SPY realized volatility")
		}
	}

	sim := r.infra.SimBroker

	// Apply backtest-specific overrides to a copy of the app config
	// to avoid mutating the shared config across concurrent backtests.
	cfgCopy := *r.appCfg
	execCfg := &cfgCopy
	if r.cfg.MaxPositions > 0 {
		execCfg.Trading.MaxSimultaneousPos = r.cfg.MaxPositions
	}
	if r.cfg.MaxPerGroup > 0 {
		execCfg.Trading.MaxPositionsPerGroup = r.cfg.MaxPerGroup
	}

	execBundle, err := bootstrap.BuildExecutionService(bootstrap.ExecutionDeps{
		EventBus:      r.infra.EventBus,
		Broker:        sim,
		Repo:          r.infra.NoopRepo,
		QuoteProvider: sim,
		AccountPort:   sim,
		PnLRepo:       r.infra.NoopPnLRepo,
		TradeReader:   nil,
		Clock:         clockFn,
		Config:        execCfg,
		InitialEquity: r.cfg.InitialEquity,
		IsBacktest:    true,
		Logger:        r.log,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build execution: %w", err)
	}

	posMonBundle, err := bootstrap.BuildPositionMonitor(bootstrap.PosMonitorDeps{
		EventBus:     r.infra.EventBus,
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

	aiAdvisor := r.infra.AIAdvisor
	histOptRepo := r.infra.HistOptRepo
	importer := r.infra.Importer

	// Historical options import is disabled by default — data should be
	// pre-imported via `omo-backfill` or a prior backtest run. Re-importing
	// on every backtest wastes time checking DoltHub for already-cached data.
	// To force a fresh import, run importer.EnsureData() manually.
	if importer != nil {
		r.log.Debug().Msg("skipping DoltHub options import (data assumed pre-cached)")
	}

	// Adapt historical options data for the RiskSizer's OptionsMarketDataPort.
	// The adapter also serves as a cached HistoricalOptionsPort for SimBroker
	// exit pricing once PreLoad populates its in-memory cache.
	optionsAdapter := NewHistoricalOptionsAdapter(histOptRepo, clockFn)
	optionsAdapter.SetLogger(r.log)

	// Wire historical options to simbroker for realistic exit pricing.
	// Uses the cached adapter instead of the raw repo so that after PreLoad,
	// SimBroker exit lookups are served from memory.
	sim.SetHistoricalOptions(optionsAdapter)

	// Debate service: processes SetupDetected events (ORB) and emits OrderIntentCreated.
	// Only start if ORB strategy is selected (it's the only consumer of SetupDetected).
	if orbSelected {
		debateSvc := debate.NewService(r.infra.EventBus, aiAdvisor, r.infra.NoopRepo, 0.50, r.log.With().Str("component", "debate").Logger())
		debateSvc.SetEquity(r.cfg.InitialEquity)
		debateSvc.SetSpecStore(specStore)
		debateSvc.SetHistoricalOptions(histOptRepo)
		if startErr := debateSvc.Start(ctx); startErr != nil {
			r.status.Store("error")
			return fmt.Errorf("start debate: %w", startErr)
		}
	}

	pipeline, err := bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
		EventBus:        r.infra.EventBus,
		SpecStore:       specStore,
		AIAdvisor:       aiAdvisor,
		PositionLookup:  posMonBundle.Service.LookupPosition,
		MarketDataFn:    monitorSvc.GetLastSnapshot,
		OptionsMarket:   optionsAdapter,
		Repo:            nil,
		TenantID:        "default",
		EnvMode:         domain.EnvModePaper,
		Equity:          r.cfg.InitialEquity,
		Clock:           clockFn,
		DisableEnricher: r.cfg.NoAI,
		Logger:          r.log,
		BacktestID:      r.id,
	})
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("build strategy pipeline: %w", err)
	}

	if pipeline.Enricher == nil {
		if subErr := r.infra.EventBus.Subscribe(ctx, domain.EventSignalCreated, signalPassthrough(r.infra.EventBus, r.log)); subErr != nil {
			r.status.Store("error")
			return fmt.Errorf("subscribe signal passthrough: %w", subErr)
		}
	}

	signalTracker := perf.NewSignalTracker(r.infra.EventBus, r.infra.NoopPnLRepo, r.log.With().Str("component", "signal_tracker").Logger())

	symSet := make(map[string]struct{})
	for _, s := range pipeline.BaseSymbols {
		symSet[s] = struct{}{}
	}
	for _, s := range r.cfg.Symbols {
		symSet[s.String()] = struct{}{}
	}
	allSymbols := make([]string, 0, len(symSet))
	for s := range symSet {
		allSymbols = append(allSymbols, s)
	}
	monitorSvc.SetBaseSymbols(allSymbols)

	r.collector, err = NewCollector(r.infra.EventBus, Config{InitialEquity: r.cfg.InitialEquity}, r.log.With().Str("component", "backtest_collector").Logger())
	if err != nil {
		r.status.Store("error")
		return fmt.Errorf("create collector: %w", err)
	}

	// Emitter subscription is deferred to AFTER all pipeline services start,
	// so the strategy runner's AVWAP update processes each bar before the
	// emitter reads the value. See below after pipeline.Runner.Start().
	r.emitter.SetSnapshotFn(monitorSvc.GetLastSnapshot)
	r.emitter.SetAVWAPFn(pipeline.Runner.GetAVWAPValues)

	// --- Load replay bars first (needed to determine warmup endpoint) ---

	loc := domain.NYLocation()

	// Gap-fill is disabled by default — run `omo-backfill --gap-fill` before
	// backtesting to ensure data is complete. This avoids slow gap detection
	// queries on every backtest run.
	//
	// To re-enable inline gap-fill, set GapFill: true in RunConfig.

	r.emitter.EmitSetup("Loading market data…")
	streams := make([]*barStream, 0, len(r.cfg.Symbols))
	totalBars := 0
	firstBarTime := make(map[string]time.Time)
	{
		type loadResult struct {
			stream       *barStream
			firstBarTime time.Time
		}
		results := make([]loadResult, len(r.cfg.Symbols))
		var loadErr atomic.Value
		var loadWg sync.WaitGroup

		// Pre-load historical options chain in parallel with bar loading.
		// On failure, the adapter falls back to per-query DB lookups gracefully.
		loadWg.Add(1)
		go func() {
			defer loadWg.Done()
			if preloadErr := optionsAdapter.PreLoad(ctx, r.cfg.Symbols, r.cfg.From, r.cfg.To); preloadErr != nil {
				r.log.Warn().Err(preloadErr).Msg("options chain pre-load failed, falling back to per-query")
			}
		}()

		// Limit concurrent DB fetches to avoid overwhelming the connection pool.
		loadSem := make(chan struct{}, 8)
		for i, sym := range r.cfg.Symbols {
			loadWg.Add(1)
			go func(i int, sym domain.Symbol) {
				defer loadWg.Done()
				loadSem <- struct{}{}
				defer func() { <-loadSem }()
				bars, fetchErr := repo.GetMarketBars(ctx, sym, replayTimeframe, r.cfg.From, r.cfg.To)
				if fetchErr != nil {
					loadErr.Store(fmt.Errorf("load bars for %s: %w", sym, fetchErr))
					return
				}
				var fbt time.Time
				if len(bars) > 0 {
					fbt = bars[0].Time
				}
				results[i] = loadResult{stream: &barStream{symbol: sym, bars: bars}, firstBarTime: fbt}
				r.log.Info().Str("symbol", sym.String()).Int("bars", len(bars)).Msg("loaded bars")
			}(i, sym)
		}
		loadWg.Wait()
		if v := loadErr.Load(); v != nil {
			r.status.Store("error")
			return v.(error)
		}
		for _, res := range results {
			if res.stream == nil {
				continue
			}
			streams = append(streams, res.stream)
			totalBars += len(res.stream.bars)
			if !res.firstBarTime.IsZero() {
				firstBarTime[res.stream.symbol.String()] = res.firstBarTime
			}
		}
	}

	sort.Slice(streams, func(i, j int) bool { return streams[i].symbol.String() < streams[j].symbol.String() })

	// --- Initialize aggregators for strategy timeframe ---
	userTF := string(r.cfg.Timeframe)
	if userTF == "" {
		userTF = "1m"
	}
	useAggregation := userTF != "1m"

	aggregators := make(map[string]*BarAggregator, len(r.cfg.Symbols))
	for _, sym := range r.cfg.Symbols {
		aggregators[sym.String()] = NewBarAggregator(userTF)
	}

	// --- Warmup (uses actual first bar time as endpoint) ---

	r.emitter.EmitSetup("Warming up indicators…")
	const minWarmupBars = 250
	warmupBarsCache := make(map[string][]domain.MarketBar, len(r.cfg.Symbols))
	dailyBarsCache := make(map[string][]domain.MarketBar, len(r.cfg.Symbols))
	{
		type warmupResult struct {
			sym    string
			bars   []domain.MarketBar
			bars1d []domain.MarketBar
			bars1h []domain.MarketBar
		}
		warmupResults := make([]warmupResult, len(r.cfg.Symbols))
		var warmupWg sync.WaitGroup
		warmupSem := make(chan struct{}, 8)
		for i, sym := range r.cfg.Symbols {
			warmupWg.Add(1)
			go func(i int, sym domain.Symbol) {
				defer warmupWg.Done()
				warmupSem <- struct{}{}
				defer func() { <-warmupSem }()
				warmupEnd := r.cfg.From
				if t, ok := firstBarTime[sym.String()]; ok {
					warmupEnd = t
				}
				warmupStart := warmupEnd.Add(-7 * 24 * time.Hour)

				bars, fetchErr := repo.GetMarketBars(ctx, sym, replayTimeframe, warmupStart, warmupEnd)
				if fetchErr != nil {
					r.log.Warn().Err(fetchErr).Str("symbol", sym.String()).Msg("warmup fetch failed")
				}
				if len(bars) < minWarmupBars && r.marketData != nil {
					apiFrom := warmupEnd.Add(-30 * 24 * time.Hour)
					apiBars, apiErr := r.marketData.GetHistoricalBars(ctx, sym, replayTimeframe, apiFrom, warmupEnd)
					if apiErr == nil && len(apiBars) > len(bars) {
						r.log.Info().Str("symbol", sym.String()).Int("db_bars", len(bars)).Int("api_bars", len(apiBars)).Msg("fetched warmup bars from market data API")
						if _, batchErr := repo.SaveMarketBars(ctx, apiBars); batchErr != nil {
							r.log.Warn().Err(batchErr).Str("symbol", sym.String()).Msg("batch save warmup bars failed")
						}
						bars = apiBars
					} else if apiErr != nil {
						r.log.Warn().Err(apiErr).Str("symbol", sym.String()).Msg("API warmup fetch failed")
					}
				}
				if len(bars) > minWarmupBars {
					bars = bars[len(bars)-minWarmupBars:]
				}

				// Fetch 1D bars: lookback for HTF EMA200 + full backtest range for anchor detectors.
				// Try DB first, fall back to IBKR API on cache miss.
				var bars1d []domain.MarketBar
				{
					dailyBarsNeeded := 200
					dailyTo := r.cfg.To // extend through backtest end for catalyst/capitulation detection
					dailyFrom := r.cfg.From.Add(-time.Duration(float64(dailyBarsNeeded)*2.0) * 24 * time.Hour)
					dbBars, dbErr := repo.GetMarketBars(ctx, sym, "1d", dailyFrom, dailyTo)
					if dbErr != nil {
						r.log.Warn().Err(dbErr).Str("symbol", sym.String()).Msg("1D DB fetch failed")
					}
					switch {
					case len(dbBars) >= dailyBarsNeeded:
						bars1d = dbBars
					case r.marketData != nil:
						fetched, err := r.marketData.GetHistoricalBars(ctx, sym, "1d", dailyFrom, dailyTo)
						if err != nil || len(fetched) < dailyBarsNeeded {
							r.log.Warn().Err(err).Str("symbol", sym.String()).Int("db_bars", len(dbBars)).Int("api_bars", len(fetched)).Int("needed", dailyBarsNeeded).Msg("insufficient 1D bars for HTF EMA200 — ORB signals will be blocked for this symbol")
						}
						if len(fetched) > len(dbBars) {
							bars1d = fetched
						} else {
							bars1d = dbBars
						}
					default:
						bars1d = dbBars
					}
				}

				// Fetch 1H bars: lookback for HTF EMA50.
				// Try DB first, fall back to IBKR API on cache miss.
				var bars1h []domain.MarketBar
				{
					hourlyBarsNeeded := 50
					hourlyTo := r.cfg.From
					hourlyFrom := hourlyTo.Add(-time.Duration(float64(hourlyBarsNeeded)*2.0) * time.Hour)
					dbBars, dbErr := repo.GetMarketBars(ctx, sym, "1h", hourlyFrom, hourlyTo)
					if dbErr != nil {
						r.log.Warn().Err(dbErr).Str("symbol", sym.String()).Msg("1H DB fetch failed")
					}
					switch {
					case len(dbBars) >= hourlyBarsNeeded:
						bars1h = dbBars
					case r.marketData != nil:
						fetched, err := r.marketData.GetHistoricalBars(ctx, sym, "1h", hourlyFrom, hourlyTo)
						if err != nil {
							r.log.Warn().Err(err).Str("symbol", sym.String()).Msg("1H API warmup fetch failed")
						}
						if len(fetched) > len(dbBars) {
							bars1h = fetched
						} else {
							bars1h = dbBars
						}
						if len(bars1h) < hourlyBarsNeeded {
							r.log.Warn().Str("symbol", sym.String()).Int("got", len(bars1h)).Int("needed", hourlyBarsNeeded).Msg("insufficient 1H bars for HTF EMA50")
						}
					default:
						bars1h = dbBars
					}
				}

				warmupResults[i] = warmupResult{sym: sym.String(), bars: bars, bars1d: bars1d, bars1h: bars1h}
			}(i, sym)
		}
		warmupWg.Wait()
		for _, res := range warmupResults {
			warmupBarsCache[res.sym] = res.bars
			dailyBarsCache[res.sym] = res.bars1d
			n := monitorSvc.WarmUp(res.bars)
			if len(res.bars1d) > 0 {
				// Use only bars before backtest start for HTF EMA200 (no look-ahead bias).
				var preBars []domain.MarketBar
				for _, b := range res.bars1d {
					if b.Time.Before(r.cfg.From) {
						preBars = append(preBars, b)
					}
				}
				closes := make([]float64, len(preBars))
				for i, b := range preBars {
					closes[i] = b.Close
				}
				dailyBarsNeeded := 200
				ema200 := monitor.ComputeStaticEMA(closes, dailyBarsNeeded)
				if ema200 > 0 {
					bias := "NEUTRAL"
					lastClose := preBars[len(preBars)-1].Close
					if lastClose > ema200*1.005 {
						bias = "BULLISH"
					} else if lastClose < ema200*0.995 {
						bias = "BEARISH"
					}
					nr7 := monitor.ComputeNR7(preBars)
					dailyATR := monitor.ComputeDailyATR(preBars, 14)
					monitorSvc.SetStaticHTFData(res.sym, "1d", domain.HTFData{
						EMA200:   ema200,
						Bias:     bias,
						NR7:      nr7,
						DailyATR: dailyATR,
					})
					r.log.Info().Str("symbol", res.sym).Float64("ema200", ema200).Str("bias", bias).Bool("nr7", nr7).Float64("daily_atr", dailyATR).Int("daily_bars", len(preBars)).Msg("1D HTF warmup complete")
				}
			}
			if len(res.bars1h) > 0 {
				// Use only bars before backtest start for 1H EMA50 (no look-ahead bias).
				var preHourly []domain.MarketBar
				for _, b := range res.bars1h {
					if b.Time.Before(r.cfg.From) {
						preHourly = append(preHourly, b)
					}
				}
				if len(preHourly) > 0 {
					nh := monitorSvc.WarmUpHTF(preHourly)
					r.log.Info().Str("symbol", res.sym).Int("bars", nh).Msg("1H EMA50 warmup complete")
				}
			}
			monitorSvc.ResetSessionIndicators(res.sym)
			monitorSvc.MarkReady(res.sym)
			r.log.Info().Str("symbol", res.sym).Int("warmup_bars", n).Msg("indicator warmup done")
		}
	}

	for _, s := range streams {
		replayBars := s.bars
		if len(replayBars) > 0 {
			bridgeCount := 50
			if bridgeCount > len(replayBars) {
				bridgeCount = len(replayBars)
			}
			monitorSvc.WarmUp(replayBars[:bridgeCount])
		}
	}

	for _, sym := range r.cfg.Symbols {
		if bars, ok := warmupBarsCache[sym.String()]; ok && len(bars) > 0 {
			ingBundle.Filter.Seed(sym, bars)
		}
	}

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
		// Aggregate warmup 1m bars into HTF candles (5m, 15m, etc.) and feed
		// them through strategy instances that are configured for those timeframes.
		// Without this, HTF instances start with empty state on the first replay day.
		for _, sym := range r.cfg.Symbols {
			bars := warmupBarsCache[sym.String()]
			if len(bars) == 0 {
				continue
			}
			pipeline.Runner.WarmUpHTF(sym.String(), bars, snapshotFn, loc)
		}
		pipeline.Runner.ClearAllPendingStates()

		sessionResolver := NewSessionResolver(loc)
		// Extend lookback by 5 calendar days so previous-day anchors (pd_high, pd_low, etc.)
		// are available on the first replay day even on Mondays (need Friday = -3 calendar days).
		sessionFrom := r.cfg.From.Add(-5 * 24 * time.Hour)
		{
			var wg sync.WaitGroup
			sem := make(chan struct{}, 10) // limit concurrent DB connections
			for _, sym := range r.cfg.Symbols {
				wg.Add(1)
				go func(s domain.Symbol) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					if loadErr := sessionResolver.Load(ctx, r.infra.DB, s, sessionFrom, r.cfg.To); loadErr != nil {
						r.log.Warn().Err(loadErr).Str("symbol", s.String()).Msg("failed to load session data")
					}
					// Pre-load 1m bars into cache so GetBarsSince avoids per-anchor DB queries.
					if loadErr := sessionResolver.LoadBars(ctx, r.infra.DB, s, sessionFrom, r.cfg.To); loadErr != nil {
						r.log.Warn().Err(loadErr).Str("symbol", s.String()).Msg("failed to pre-load 1m bars")
					}
				}(sym)
			}
			wg.Wait()
		}

		aiResolver := strategy.NewAIAnchorResolver(aiAdvisor, nil, nil)
		aiResolver.SetSessionResolver(sessionResolver.ResolveAnchors)
		for _, sym := range r.cfg.Symbols {
			isCrypto := strings.Contains(sym.String(), "/") || strings.HasSuffix(sym.String(), "USD")
			aiResolver.RegisterSymbol(sym.String(), isCrypto)
		}
		pipeline.Runner.SetAIAnchorResolver(aiResolver)
		pipeline.Runner.SetAnchorResolver(sessionResolver.ResolveAnchors)
		pipeline.Runner.SetPrevDayBarsFn(func(symbol string, since time.Time) []start.Bar {
			return sessionResolver.GetBarsSince(ctx, r.infra.DB, symbol, since)
		})
		pipeline.Runner.SetKeyLevelPricesFn(sessionResolver.KeyLevelPrices)

		// Seed daily bars into AI anchor detectors (catalyst_gap, capitulation, swing 1d)
		// so they have historical context before replay starts.
		for _, sym := range r.cfg.Symbols {
			if bars1d := dailyBarsCache[sym.String()]; len(bars1d) > 0 {
				for _, b := range bars1d {
					sBar := start.Bar{Time: b.Time, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume}
					aiResolver.OnBar(sym.String(), sBar, "1d")
				}
				r.log.Info().Str("symbol", sym.String()).Int("daily_bars", len(bars1d)).Msg("seeded daily bars into anchor detectors")
			}
		}

		// Seed warmup 5m bars into AI anchor detectors (swing, volume profile)
		// so they have the same candidates as a run that replayed those days live.
		for _, sym := range r.cfg.Symbols {
			bars := warmupBarsCache[sym.String()]
			if len(bars) == 0 {
				continue
			}
			firstET := bars[0].Time.In(loc)
			sessOpen := time.Date(firstET.Year(), firstET.Month(), firstET.Day(), 9, 30, 0, 0, loc)
			agg5m, err := domain.NewBarAggregator(sym, "5m", sessOpen)
			if err != nil {
				continue
			}
			var count int
			for _, b := range bars {
				closed, ok := agg5m.Push(b)
				if ok {
					sBar := start.Bar{Time: closed.Time, Open: closed.Open, High: closed.High, Low: closed.Low, Close: closed.Close, Volume: closed.Volume}
					aiResolver.OnBar(sym.String(), sBar, "5m")
					count++
				}
			}
			if count > 0 {
				r.log.Info().Str("symbol", sym.String()).Int("bars_5m", count).Msg("seeded 5m warmup bars into anchor detectors")
			}
		}

		r.log.Info().Msg("AI anchor resolver configured for backtest (with session baseline)")
	}

	r.emitter.EmitSetup("Starting services…")

	if startErr := ingBundle.Service.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start ingestion: %w", startErr)
	}
	if startErr := monitorSvc.Start(ctx); startErr != nil {
		r.status.Store("error")
		return fmt.Errorf("start monitor: %w", startErr)
	}
	execBundle.LedgerWriter.SetNowFunc(clockFn)
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
	if startErr := posMonBundle.PriceCache.Start(ctx, r.infra.EventBus); startErr != nil {
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

	// Compound equity: update position sizing after each fill so P&L compounds.
	if r.cfg.CompoundEquity {
		_ = r.infra.EventBus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, _ domain.Event) error {
			eq, err := sim.GetAccountEquity(ctx)
			if err != nil {
				return nil
			}
			pipeline.RiskSizer.SetAccountEquity(eq)
			execBundle.Service.SetAccountEquity(eq)
			execBundle.LedgerWriter.SetAccountEquity(eq)
			return nil
		})
	}

	// Subscribe emitter LAST so all pipeline handlers (strategy runner's
	// AVWAP update, monitor, execution) process each bar before the emitter
	// reads values for SSE emission.
	if subErr := r.emitter.Subscribe(ctx, r.infra.EventBus); subErr != nil {
		r.status.Store("error")
		return fmt.Errorf("subscribe emitter: %w", subErr)
	}

	// Pre-compute daily screener if enabled
	var dailyScreenerMap map[string]map[string]bool
	if r.cfg.UseDailyScreener && r.marketData != nil {
		r.emitter.EmitSetup("Computing daily screener…")
		topN := r.cfg.ScreenerTopN
		if topN <= 0 {
			topN = 5
		}

		// Collect all unique trading days from the bar streams
		tradingDays := func() []time.Time {
			seen := make(map[string]bool)
			var days []time.Time
			for _, s := range streams {
				for _, bar := range s.bars {
					et := bar.Time.In(loc)
					dayOpen := time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, loc)
					key := dayOpen.Format("2006-01-02")
					if !seen[key] {
						seen[key] = true
						days = append(days, dayOpen)
					}
				}
			}
			sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
			return days
		}()
		// Build a larger candidate pool from the strategy's routing config.
		// The user's symbol list is the candidate pool; topN picks the best each day.
		// If candidate pool <= topN, warn that screener won't filter anything.
		candidates := r.cfg.Symbols
		if len(candidates) <= topN {
			r.log.Warn().
				Int("candidates", len(candidates)).
				Int("top_n", topN).
				Msg("daily screener: candidate pool <= top_n, screener will not filter. Add more symbols to the candidate pool.")
		}
		r.log.Info().Int("trading_days", len(tradingDays)).Int("top_n", topN).Int("candidates", len(candidates)).Msg("pre-computing daily screener")

		screener := NewDailyScreener(repo, r.marketData, r.log)
		dailyScreenerMap = screener.PrecomputeAll(ctx, tradingDays, candidates, topN, func(day, total int, date string) {
			r.emitter.EmitSetup(fmt.Sprintf("Screening day %d/%d (%s)…", day+1, total, date))
		})

		// Log sample of screener results
		totalSymsAcrossDays := 0
		for date, syms := range dailyScreenerMap {
			totalSymsAcrossDays += len(syms)
			if len(syms) > 0 {
				symList := make([]string, 0, len(syms))
				for s := range syms {
					symList = append(symList, s)
				}
				r.log.Debug().Str("date", date).Strs("symbols", symList).Msg("daily screener picks")
			}
		}
		r.log.Info().Int("days_screened", len(dailyScreenerMap)).Int("total_symbol_days", totalSymsAcrossDays).Msg("daily screener pre-computation complete")
	}

	r.emitter.EmitSetup("Replaying bars…")

	// --- Load historical auction data for synthetic imbalance events ---
	// Keyed by "YYYY-MM-DD:SYMBOL" for O(1) lookup in the replay loop.
	auctionByDateSym := make(map[string]domain.AuctionImbalanceSnapshot)
	{
		auctionDB := timescaledb.NewSqlDB(r.infra.DB)
		auctionRepo := timescaledb.NewAuctionImbalanceRepo(auctionDB, r.log.With().Str("component", "auction_repo").Logger())
		for _, sym := range r.cfg.Symbols {
			snaps, err := auctionRepo.GetAuctionImbalances(ctx, sym, r.cfg.From, r.cfg.To)
			if err != nil {
				r.log.Warn().Err(err).Str("symbol", sym.String()).Msg("failed to load auction data")
				continue
			}
			for _, s := range snaps {
				et := s.Time.In(loc)
				key := et.Format("2006-01-02") + ":" + s.Symbol.String()
				auctionByDateSym[key] = s
			}
		}
		if len(auctionByDateSym) > 0 {
			r.log.Info().Int("auction_snapshots", len(auctionByDateSym)).Msg("loaded historical auction data for backtest")
		}
	}
	publishedAuctions := make(map[string]bool, len(auctionByDateSym))

	// Reduce GC frequency during the replay hot loop. GOGC=200 lets the heap
	// grow 2x before collecting, balancing memory usage and GC pauses.
	prevGOGC := debug.SetGCPercent(200)
	defer debug.SetGCPercent(prevGOGC)

	// Freeze the handler map so PublishDirect can bypass locking.
	// All Subscribe calls have completed by this point.
	r.infra.EventBus.FreezeHandlers()

	const tenantID = "default"
	envMode := domain.EnvModePaper
	barsProcessed := 0
	currentSessionDate := replaySessionOpen

	// Pre-build fixed symbol set for O(1) lookup in the hot loop.
	fixedSymbolSet := make(map[string]bool, len(r.cfg.FixedSymbols))
	for _, fs := range r.cfg.FixedSymbols {
		fixedSymbolSet[fs.String()] = true
	}

	// Build a min-heap for efficient stream merging (O(log N) vs O(N) per iteration).
	sh := make(streamHeap, 0, len(streams))
	for _, s := range streams {
		if s.idx < len(s.bars) {
			sh = append(sh, s)
		}
	}
	heap.Init(&sh)

	for ctx.Err() == nil && sh.Len() > 0 {
		// Peek at the minimum timestamp from the heap root.
		minTime := sh[0].bars[sh[0].idx].Time

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

			// Recompute realized vol for this trading day from cached SPY daily bars.
			if orbSelected {
				rvCutoff := dayOpen.Add(-60 * 24 * time.Hour)
				var windowBars []domain.MarketBar
				for _, b := range spyDailyBars {
					if !b.Time.Before(rvCutoff) && b.Time.Before(dayOpen) {
						windowBars = append(windowBars, b)
					}
				}
				if len(windowBars) > 21 {
					rv := monitor.ComputeRealizedVol(windowBars, 20)
					monitorSvc.SetVIXLevel(rv)
				}
			}
		}

		for sh.Len() > 0 && sh[0].bars[sh[0].idx].Time.Equal(minTime) {
			if ctx.Err() != nil {
				break
			}
			s := heap.Pop(&sh).(*barStream)
			bar := s.bars[s.idx]
			s.idx++
			// Re-push stream if it has more bars.
			if s.idx < len(s.bars) {
				heap.Push(&sh, s)
			}

			// Daily screener filter: allow bars if symbol is in today's screened set
			// OR in the user's fixed symbol list (union of both, like live trading).
			if dailyScreenerMap != nil {
				symStr := bar.Symbol.String()
				if !fixedSymbolSet[symStr] {
					dateKey := currentSessionDate.Format("2006-01-02")
					if activeSyms, ok := dailyScreenerMap[dateKey]; ok && len(activeSyms) > 0 {
						if !activeSyms[symStr] {
							continue
						}
					}
				}
			}

			sim.UpdatePrice(bar.Symbol, bar.Close, bar.Time)

			// Track aggregation for exit timing
			if useAggregation {
				if agg, ok := aggregators[bar.Symbol.String()]; ok {
					agg.Add(bar)
				}
			}

			// Emit synthetic auction imbalance event at 15:45 ET so the PHM
			// strategy's second entry window receives auction flow data.
			// The event is published BEFORE the bar so the strategy has the
			// auction snapshot when processing the 15:45+ bar.
			if len(auctionByDateSym) > 0 {
				barET := bar.Time.In(loc)
				if barET.Hour() == 15 && barET.Minute() >= 45 {
					symStr := bar.Symbol.String()
					dateKey := barET.Format("2006-01-02") + ":" + symStr
					if !publishedAuctions[dateKey] {
						if aSnap, ok := auctionByDateSym[dateKey]; ok {
							// Derive synthetic imbalance direction from bar close vs VWAP (option B).
							imbalance := aSnap.Volume // default positive
							if snap, snapOK := monitorSvc.GetLastSnapshot(symStr); snapOK && snap.VWAP > 0 {
								if bar.Close < snap.VWAP {
									imbalance = -aSnap.Volume
								}
							}
							synthSnap := domain.AuctionImbalanceSnapshot{
								Time:      bar.Time,
								Symbol:    bar.Symbol,
								Volume:    aSnap.Volume,
								Price:     aSnap.Price,
								Imbalance: imbalance,
							}
							aEvt := domain.NewBacktestEvent(
								domain.EventAuctionImbalance, tenantID, envMode,
								bar.Time.String()+"-auction-"+symStr, synthSnap, bar.Time,
							)
							_ = r.infra.EventBus.PublishDirect(ctx, aEvt)
							r.infra.EventBus.Flush()
							publishedAuctions[dateKey] = true
						}
					}
				}
			}

			// Publish market bar event (backtest fast path: counter ID, no UUID, no time.Now, no lock).
			evt := domain.NewBacktestEvent(domain.EventMarketBarReceived, tenantID, envMode, bar.Time.String()+string(bar.Symbol), bar, bar.Time)
			if pubErr := r.infra.EventBus.PublishDirect(ctx, evt); pubErr != nil {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			barsProcessed++
		}

		// Evaluate exit rules after all bars in this time-group are processed.
		// This avoids WaitGroup reuse panics from concurrent handler chains.
		r.infra.EventBus.Flush()
		if posMonBundle.Service != nil {
			if useAggregation {
				for _, agg := range aggregators {
					if agg.HasPending() {
						closedTime := agg.LastClosedTime()
						if closedTime > 0 {
							posMonBundle.Service.EvalExitRules(time.Unix(closedTime, 0).UTC())
							r.infra.EventBus.Flush()
						}
					}
				}
			} else {
				posMonBundle.Service.EvalExitRules(minTime)
				r.infra.EventBus.Flush()
			}
		}

		// Emit progress at most ~5 times/sec (200ms gate).
		if time.Since(r.lastEmitTime) > 200*time.Millisecond || sh.Len() == 0 {
			r.lastEmitTime = time.Now()
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
		} else {
			// "max" speed: yield to the scheduler periodically to prevent
			// starving other goroutines (dashboard, HTTP handlers, etc.).
			runtime.Gosched()
		}
	}

	// --- Completion ---

	// Flush any remaining pending aggregators at end of backtest
	if useAggregation {
		for _, agg := range aggregators {
			if agg.HasPending() {
				closedTime := agg.LastClosedTime()
				if closedTime > 0 && posMonBundle.Service != nil {
					posMonBundle.Service.EvalExitRules(time.Unix(closedTime, 0).UTC())
					r.infra.EventBus.Flush()
				}
			}
		}
	}

	finalResult := r.collector.Result()
	r.result.Store(&finalResult)
	r.emitter.EmitComplete(&finalResult)

	if r.Status() != "canceled" {
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

func signalPassthrough(bus ports.EventBusPort, log zerolog.Logger) func(context.Context, domain.Event) error {
	return func(ctx context.Context, ev domain.Event) error {
		sig, ok := ev.Payload.(start.Signal)
		if !ok {
			return nil
		}
		direction := domain.DirectionLong
		if sig.Type == start.SignalExit {
			// All exits use CloseLong — execution resolves position side from broker.
			direction = domain.DirectionCloseLong
		} else if sig.Side == start.SideSell {
			direction = domain.DirectionShort
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
			Rationale:  fmt.Sprintf("passthrough (no-ai): %s %s strength=%.2f setup:%s confluence:%s(%s)", sig.Type, sig.Side, sig.Strength, sig.Tags["setup"], sig.Tags["confluence"], sig.Tags["confluence_detail"]),
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
			ATR:           snap.ATR,
			VWAPSD:        snap.VWAPSD,
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

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.String()
	}
	return out
}

type barStream struct {
	symbol domain.Symbol
	bars   []domain.MarketBar
	idx    int
}

// streamHeap implements heap.Interface for merging barStreams by timestamp.
// Secondary sort by symbol preserves deterministic ordering within a time-group.
type streamHeap []*barStream

func (h streamHeap) Len() int { return len(h) }
func (h streamHeap) Less(i, j int) bool {
	ti := h[i].bars[h[i].idx].Time
	tj := h[j].bars[h[j].idx].Time
	if ti.Equal(tj) {
		return h[i].symbol < h[j].symbol
	}
	return ti.Before(tj)
}
func (h streamHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *streamHeap) Push(x any)         { *h = append(*h, x.(*barStream)) }
func (h *streamHeap) Pop() any           { old := *h; n := len(old); x := old[n-1]; old[n-1] = nil; *h = old[:n-1]; return x }

// isRTHGap delegates to the shared backfill.IsRTHGap implementation.
// Kept as a package-private wrapper so existing internal tests continue to work.
func isRTHGap(gapStart, gapEnd time.Time, loc *time.Location) bool {
	return backfill.IsRTHGap(gapStart, gapEnd, loc)
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

// symbolOverrideSpecStore replaces each spec's routing symbols with the
// backtest-requested symbols so strategies run on UI-selected symbols
// regardless of what the TOML config lists.
type symbolOverrideSpecStore struct {
	inner   portstrategy.SpecStore
	symbols []string
}

func (s *symbolOverrideSpecStore) List(ctx context.Context, filter *portstrategy.SpecFilter) ([]portstrategy.Spec, error) {
	specs, err := s.inner.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	for i := range specs {
		specs[i].Routing.Symbols = s.symbols
	}
	return specs, nil
}

func (s *symbolOverrideSpecStore) Get(ctx context.Context, id start.StrategyID, version start.Version) (*portstrategy.Spec, error) {
	spec, err := s.inner.Get(ctx, id, version)
	if err != nil {
		return nil, err
	}
	spec.Routing.Symbols = s.symbols
	return spec, nil
}

func (s *symbolOverrideSpecStore) GetLatest(ctx context.Context, id start.StrategyID) (*portstrategy.Spec, error) {
	spec, err := s.inner.GetLatest(ctx, id)
	if err != nil {
		return nil, err
	}
	spec.Routing.Symbols = s.symbols
	return spec, nil
}

func (s *symbolOverrideSpecStore) Save(ctx context.Context, spec portstrategy.Spec) error {
	return s.inner.Save(ctx, spec)
}

func (s *symbolOverrideSpecStore) Watch(ctx context.Context) (<-chan start.StrategyID, error) {
	return s.inner.Watch(ctx)
}
