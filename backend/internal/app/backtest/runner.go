package backtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"os"
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
	id         string
	cfg        RunConfig
	db         *sql.DB
	appCfg     *config.Config
	marketData ports.MarketDataPort
	log        zerolog.Logger

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
func NewRunner(cfg RunConfig, db *sql.DB, appCfg *config.Config, marketData ports.MarketDataPort, log zerolog.Logger) *Runner {
	id := generateID()
	rlog := log.With().Str("backtest_id", id).Str("component", "backtest_runner").Logger()

	r := &Runner{
		id:         id,
		cfg:        cfg,
		db:         db,
		appCfg:     appCfg,
		marketData: marketData,
		log:        rlog,
		eventBus:   memory.NewBus(),
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
	// Fallback for Docker container
	if _, err := os.Stat(specDir); err != nil {
		specDir = "/configs/strategies"
	}
	if specDir == "" {
		specDir = "/home/ridopark/src/oh-my-opentrade/configs/strategies"
	}
	var specStore portstrategy.SpecStore = store_fs.NewStore(specDir, strategy.LoadSpecFile)
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
	if orbSelected {
		orbID, _ := start.NewStrategyID("orb_break_retest")
		if orbSpec, loadErr := specStore.GetLatest(context.Background(), orbID); loadErr == nil {
			monitorSvc.SetORBConfig(orbSpec.Params)
		}
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

	var aiAdvisor ports.AIAdvisorPort = llm.NewNoOpAdvisor()
	if !r.cfg.NoAI && r.appCfg.AI.Enabled {
		aiAdvisor = llm.NewAdvisor(r.appCfg.AI.BaseURL, r.appCfg.AI.Model, r.appCfg.AI.APIKey, nil)
	}

	// Debate service: processes SetupDetected events (ORB) and emits OrderIntentCreated.
	// Only start if ORB strategy is selected (it's the only consumer of SetupDetected).
	if orbSelected {
		debateSvc := debate.NewService(r.eventBus, aiAdvisor, &noop.NoopRepo{}, 0.50, r.log.With().Str("component", "debate").Logger())
		debateSvc.SetEquity(r.cfg.InitialEquity)
		if startErr := debateSvc.Start(ctx); startErr != nil {
			r.status.Store("error")
			return fmt.Errorf("start debate: %w", startErr)
		}
	}

	pipeline, err := bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
		EventBus:        r.eventBus,
		SpecStore:       specStore,
		AIAdvisor:       aiAdvisor,
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

	// --- Load replay bars first (needed to determine warmup endpoint) ---

	type barStream struct {
		symbol domain.Symbol
		bars   []domain.MarketBar
		idx    int
	}

	loc, _ := time.LoadLocation("America/New_York")

	r.emitter.EmitSetup("Checking for data gaps…")
	if r.marketData != nil {
		var gapWg sync.WaitGroup
		for _, sym := range r.cfg.Symbols {
			gapWg.Add(1)
			go func(sym domain.Symbol) {
				defer gapWg.Done()
				gaps, gapErr := repo.FindDataGaps(ctx, sym, replayTimeframe, r.cfg.From, r.cfg.To, gapThreshold)
				if gapErr != nil {
					r.log.Warn().Err(gapErr).Str("symbol", sym.String()).Msg("gap detection failed")
					return
				}
				for _, g := range gaps {
					if !isRTHGap(g.Start, g.End, loc) {
						continue
					}
					r.log.Info().Str("symbol", sym.String()).Time("start", g.Start).Time("end", g.End).Dur("duration", g.Duration).Msg("detected RTH data gap — fetching from API")
					apiBars, apiErr := r.marketData.GetHistoricalBars(ctx, sym, replayTimeframe, g.Start.Add(time.Minute), g.End)
					if apiErr != nil {
						r.log.Warn().Err(apiErr).Str("symbol", sym.String()).Msg("failed to fetch gap bars")
						continue
					}
					if len(apiBars) > 0 {
						saved, saveErr := repo.SaveMarketBars(ctx, apiBars)
						if saveErr != nil {
							r.log.Warn().Err(saveErr).Msg("failed to persist gap bars")
						} else {
							r.log.Info().Str("symbol", sym.String()).Int("fetched", len(apiBars)).Int("saved", saved).Msg("filled RTH data gap")
						}
					}
				}
			}(sym)
		}
		gapWg.Wait()
	}

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
		for i, sym := range r.cfg.Symbols {
			loadWg.Add(1)
			go func(i int, sym domain.Symbol) {
				defer loadWg.Done()
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
	{
		type warmupResult struct {
			sym  string
			bars []domain.MarketBar
		}
		warmupResults := make([]warmupResult, len(r.cfg.Symbols))
		var warmupWg sync.WaitGroup
		for i, sym := range r.cfg.Symbols {
			warmupWg.Add(1)
			go func(i int, sym domain.Symbol) {
				defer warmupWg.Done()
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
						for _, b := range apiBars {
							_ = repo.SaveMarketBar(ctx, b)
						}
						bars = apiBars
					} else if apiErr != nil {
						r.log.Warn().Err(apiErr).Str("symbol", sym.String()).Msg("API warmup fetch failed")
					}
				}
				if len(bars) > minWarmupBars {
					bars = bars[len(bars)-minWarmupBars:]
				}
				warmupResults[i] = warmupResult{sym: sym.String(), bars: bars}
			}(i, sym)
		}
		warmupWg.Wait()
		for _, res := range warmupResults {
			warmupBarsCache[res.sym] = res.bars
			n := monitorSvc.WarmUp(res.bars)
			sym, _ := domain.NewSymbol(res.sym)
			// Fetch 1D bars for HTF EMA200 warmup (needed by ORB htf_bias).
			// The strategy fails-closed if HTF["1d"] is missing, blocking all signals.
			dailyBarsNeeded := 200
			dailyTo := r.cfg.From
			if t, ok := firstBarTime[res.sym]; ok && t.Before(dailyTo) {
				dailyTo = t
			}
			dailyFrom := dailyTo.Add(-time.Duration(float64(dailyBarsNeeded)*1.5) * 24 * time.Hour)
			bars1d, err := r.marketData.GetHistoricalBars(ctx, sym, "1d", dailyFrom, dailyTo)
			if err != nil || len(bars1d) < dailyBarsNeeded {
				r.log.Warn().Err(err).Str("symbol", res.sym).Int("got", len(bars1d)).Int("needed", dailyBarsNeeded).Msg("insufficient 1D bars for HTF EMA200 — ORB signals will be blocked for this symbol")
			}
			if len(bars1d) > 0 {
				closes := make([]float64, len(bars1d))
				for i, b := range bars1d {
					closes[i] = b.Close
				}
				ema200 := monitor.ComputeStaticEMA(closes, dailyBarsNeeded)
				if ema200 > 0 {
					bias := "NEUTRAL"
					lastClose := bars1d[len(bars1d)-1].Close
					if lastClose > ema200*1.005 {
						bias = "BULLISH"
					} else if lastClose < ema200*0.995 {
						bias = "BEARISH"
					}
					monitorSvc.SetStaticHTFData(res.sym, "1d", domain.HTFData{
						EMA200: ema200,
						Bias:   bias,
					})
					r.log.Info().Str("symbol", res.sym).Float64("ema200", ema200).Str("bias", bias).Int("daily_bars", len(bars1d)).Msg("1D HTF EMA200 warmup complete")
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
		pipeline.Runner.ClearAllPendingStates()

		sessionResolver := NewSessionResolver(loc)
		for _, sym := range r.cfg.Symbols {
			if loadErr := sessionResolver.Load(ctx, r.db, sym, r.cfg.From, r.cfg.To); loadErr != nil {
				r.log.Warn().Err(loadErr).Str("symbol", sym.String()).Msg("failed to load session data")
			}
		}

		aiResolver := strategy.NewAIAnchorResolver(aiAdvisor, nil, nil)
		aiResolver.SetSessionResolver(sessionResolver.ResolveAnchors)
		for _, sym := range r.cfg.Symbols {
			isCrypto := strings.Contains(sym.String(), "/") || strings.HasSuffix(sym.String(), "USD")
			aiResolver.RegisterSymbol(sym.String(), isCrypto)
		}
		pipeline.Runner.SetAIAnchorResolver(aiResolver)
		r.log.Info().Msg("AI anchor resolver configured for backtest (with session baseline)")
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

	r.emitter.EmitSetup("Replaying bars…")

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

			// Track aggregation for exit timing
			if useAggregation {
				if agg, ok := aggregators[bar.Symbol.String()]; ok {
					agg.Add(bar)
				}
			}

			// Publish market bar event
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

		// Evaluate exit rules after all bars in this time-group are processed.
		// This avoids WaitGroup reuse panics from concurrent handler chains.
		r.eventBus.Flush()
		if posMonBundle.Service != nil {
			if useAggregation {
				for _, agg := range aggregators {
					if agg.HasPending() {
						closedTime := agg.LastClosedTime()
						if closedTime > 0 {
							posMonBundle.Service.EvalExitRules(time.Unix(closedTime, 0).UTC())
							r.eventBus.Flush()
						}
					}
				}
			} else {
				posMonBundle.Service.EvalExitRules(minTime)
				r.eventBus.Flush()
			}
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

	// Flush any remaining pending aggregators at end of backtest
	if useAggregation {
		for _, agg := range aggregators {
			if agg.HasPending() {
				closedTime := agg.LastClosedTime()
				if closedTime > 0 && posMonBundle.Service != nil {
					posMonBundle.Service.EvalExitRules(time.Unix(closedTime, 0).UTC())
					r.eventBus.Flush()
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

func signalPassthrough(bus *memory.Bus, log zerolog.Logger) func(context.Context, domain.Event) error {
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

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.String()
	}
	return out
}

const gapThreshold = 4 * time.Hour

func isRTHGap(gapStart, gapEnd time.Time, loc *time.Location) bool {
	startET := gapStart.In(loc)
	endET := gapEnd.In(loc)
	if startET.Weekday() == time.Saturday || startET.Weekday() == time.Sunday {
		return false
	}
	rthOpen := time.Date(startET.Year(), startET.Month(), startET.Day(), 9, 30, 0, 0, loc)
	rthClose := time.Date(startET.Year(), startET.Month(), startET.Day(), 16, 0, 0, 0, loc)
	return startET.After(rthOpen) && endET.Before(rthClose)
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
