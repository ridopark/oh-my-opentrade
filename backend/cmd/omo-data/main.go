package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/deribit"
	"github.com/oh-my-opentrade/backend/internal/adapters/dolthub"
	"github.com/oh-my-opentrade/backend/internal/adapters/finnhub"
	"github.com/oh-my-opentrade/backend/internal/adapters/fredfinnhub"
	"github.com/oh-my-opentrade/backend/internal/adapters/hyperliquid"
	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
	"github.com/oh-my-opentrade/backend/internal/adapters/onchain"
	"github.com/oh-my-opentrade/backend/internal/adapters/openfigi"
	"github.com/oh-my-opentrade/backend/internal/adapters/sec"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/adapters/yfinance"
	"github.com/oh-my-opentrade/backend/internal/app/datarefresh"
	"github.com/oh-my-opentrade/backend/internal/app/earnings"
	"github.com/oh-my-opentrade/backend/internal/app/gapdetect"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/ivcollector"
	"github.com/oh-my-opentrade/backend/internal/app/optionsimport"
	"github.com/oh-my-opentrade/backend/internal/app/whale13f"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	var (
		configPath  string
		envPath     string
		symbolsFlag string
		runOnce     bool
	)
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (overrides config)")
	flag.BoolVar(&runOnce, "run-once", false, "Run all tasks once and exit")
	flag.Parse()

	// Logger (same pattern as omo-ingest)
	logLevel := zerolog.InfoLevel
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zerolog.ParseLevel(lvl); err == nil {
			logLevel = parsed
		}
	}
	log := logger.New(logger.Config{
		Level:  logLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-data").Logger()

	log.Info().Msg("starting")

	// Config
	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve symbols
	var symbols []string
	if symbolsFlag != "" {
		for _, s := range strings.Split(symbolsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, s)
			}
		}
	} else {
		symbols = cfg.Symbols.AllSymbols()
	}
	if len(symbols) == 0 {
		log.Fatal().Msg("no symbols specified")
	}

	// Database (same initDB as omo-ingest)
	sqlDB := initDB(cfg, log)
	defer sqlDB.Close()
	repo := timescaledb.NewRepositoryWithLogger(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "timescaledb").Logger(),
	)

	// Alpaca REST adapter (no streaming)
	alpacaAdapter, err := alpaca.NewAdapter(
		cfg.Alpaca,
		log.With().Str("component", "alpaca").Logger(),
		alpaca.WithNoStream(),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	// Yahoo Finance client (for VIX)
	yahooClient := yfinance.NewClient(log.With().Str("component", "yfinance").Logger())

	// Discord notifier (optional — sends status updates after each data refresh)
	var notifier *notification.DiscordNotifier
	if webhookURL := os.Getenv("DISCORD_WEBHOOK_URL"); webhookURL != "" {
		notifier = notification.NewDiscordNotifier(webhookURL, nil, log.With().Str("component", "discord").Logger())
		log.Info().Msg("Discord notifications enabled")
	}

	// Dark pool repository
	dpRepo := timescaledb.NewDarkPoolRepo(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "darkpool_repo").Logger(),
	)

	// Context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, shutting down...")
		cancel()
	}()

	// datarefresh service
	refreshSvc := datarefresh.NewService(datarefresh.Config{
		VIXSymbol:      "VIX",
		IndexSymbols:   []string{"SPY", "QQQ", "IWM"},
		TradingSymbols: symbols,
		RunAtHourET:    16,
		RunAtMinuteET:  15,
		LookbackDays:   90,
	}, alpacaAdapter, repo, noopVIXSetter{}, log)
	refreshSvc.SetYahooClient(yahooClient)
	refreshSvc.SetDarkPool(alpacaAdapter, dpRepo)
	if notifier != nil {
		refreshSvc.SetNotifier(notifier)
	}

	// IV collector (uses Alpaca for options data)
	var ivSvc *ivcollector.Service
	ivSymbols := cfg.Symbols.SymbolsByAssetClass("EQUITY")
	if len(ivSymbols) > 0 {
		ivRepo := timescaledb.NewIVRepository(
			timescaledb.NewSqlDB(sqlDB),
			log.With().Str("component", "iv_repo").Logger(),
		)
		ivSvc = ivcollector.NewService(
			ivcollector.Config{
				Symbols:       ivSymbols,
				TargetDTE:     30,
				RunAtHourET:   16,
				RunAtMinuteET: 15,
			},
			alpacaAdapter, alpacaAdapter, ivRepo,
			log.With().Str("component", "iv_collector").Logger(),
		)

		// Enable full option chain capture for backtesting.
		histOptRepo := timescaledb.NewHistoricalOptionsRepository(
			timescaledb.NewSqlDB(sqlDB),
			log.With().Str("component", "hist_options").Logger(),
		)
		ivSvc.SetHistoricalOptionsRepo(histOptRepo)
		if notifier != nil {
			ivSvc.SetNotifier(notifier)
		}
	}

	// DoltHub options chain daily refresh (fills gaps not covered by Alpaca snapshots)
	dolthubClient := dolthub.NewClient(nil, log)
	histOptRepo := timescaledb.NewHistoricalOptionsRepository(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "hist_options").Logger(),
	)
	importSvc := optionsimport.NewService(dolthubClient, histOptRepo, log)
	dolthubSvc := optionsimport.NewScheduledService(optionsimport.ScheduledConfig{
		Symbols:       symbols,
		LookbackDays:  7,
		RunAtHourET:   7,
		RunAtMinuteET: 0,
		Concurrency:   4,
	}, importSvc, log)
	if notifier != nil {
		dolthubSvc.SetNotifier(notifier)
	}

	// 13F whale accumulation (periodic refresh — only when SEC_USER_AGENT is set)
	var whaleSvc *whale13f.Service
	if ua := os.Getenv("SEC_USER_AGENT"); ua != "" {
		whaleDB := timescaledb.NewSqlDB(sqlDB)
		whaleRepo := timescaledb.NewWhaleRepo(whaleDB, log.With().Str("component", "whale_repo").Logger())
		cusipCache := timescaledb.NewCUSIPCacheRepo(whaleDB, log.With().Str("component", "cusip_cache").Logger())
		edgarClient := sec.NewEdgarClient(ua, log.With().Str("component", "sec_edgar").Logger())
		figiClient := openfigi.NewClient(os.Getenv("OPENFIGI_API_KEY"), log.With().Str("component", "openfigi").Logger())
		whaleSvc = whale13f.NewScheduledService(whale13f.ScheduledConfig{
			RunAtHourET:   6,
			RunAtMinuteET: 0,
			UserAgent:     ua,
		}, edgarClient, figiClient, cusipCache, whaleRepo, log.With().Str("component", "whale_13f").Logger())
	}

	// Earnings calendar (Finnhub — daily refresh of next earnings dates)
	var earningsSvc *earnings.Service
	if apiKey := os.Getenv("FINNHUB_API_KEY"); apiKey != "" {
		finnhubClient := finnhub.NewClient(apiKey, log.With().Str("component", "finnhub").Logger())
		earningsRepo := timescaledb.NewEarningsRepo(sqlDB, log.With().Str("component", "earnings_repo").Logger())
		earningsSvc = earnings.NewService(earnings.Config{
			Symbols:       symbols,
			RunAtHourET:   5,
			RunAtMinuteET: 30,
			LookbackDays:  90,
		}, finnhubClient, earningsRepo, log.With().Str("component", "earnings").Logger())
		if notifier != nil {
			earningsSvc.SetNotifier(notifier)
		}
	}

	// Macro economic calendar (Finnhub + FRED — daily refresh). Uses
	// the same FINNHUB_API_KEY as the earnings path; FRED_API_KEY is
	// optional. If neither is set, Refresh is a no-op.
	var macroClient *fredfinnhub.Client
	{
		macroRepo := timescaledb.NewMacroEventsRepo(sqlDB, log.With().Str("component", "macro_events_repo").Logger())
		macroClient = fredfinnhub.NewClient(fredfinnhub.Config{
			FinnhubAPIKey: os.Getenv("FINNHUB_API_KEY"),
			FREDAPIKey:    os.Getenv("FRED_API_KEY"),
		}, macroRepo, log.With().Str("component", "macro_events").Logger())
	}

	// Corporate actions client (Alpaca splits)
	caClient := alpaca.NewCorporateActionsClient(
		cfg.Alpaca.BaseURL, cfg.Alpaca.APIKeyID, cfg.Alpaca.APISecretKey,
		log.With().Str("component", "corporate_actions").Logger(),
	)
	caRepo := timescaledb.NewCorporateActionsRepo(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "corporate_actions_repo").Logger(),
	)

	// Universe history repo (Alpaca asset listing)
	universeRepo := timescaledb.NewUniverseHistoryRepo(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "universe_history_repo").Logger(),
	)

	if runOnce {
		log.Info().Msg("run-once mode: executing all tasks")
		refreshSvc.RefreshAll(ctx)
		if ivSvc != nil {
			ivSvc.CollectAll(ctx)
		}
		if whaleSvc != nil {
			if err := whaleSvc.Refresh(ctx); err != nil {
				log.Warn().Err(err).Msg("whale 13F refresh failed")
			}
		}
		if earningsSvc != nil {
			if err := earningsSvc.Refresh(ctx); err != nil {
				log.Warn().Err(err).Msg("earnings calendar refresh failed")
			}
		}
		if err := refreshCorporateActions(ctx, caClient, caRepo, symbols, log); err != nil {
			log.Warn().Err(err).Msg("corporate actions refresh failed")
		}
		if err := refreshUniverseHistory(ctx, alpacaAdapter, universeRepo, symbols, log); err != nil {
			log.Warn().Err(err).Msg("universe history refresh failed")
		}
		if err := macroClient.Refresh(ctx); err != nil {
			log.Warn().Err(err).Msg("macro events refresh failed")
		}
		dolthubSvc.RunOnce(ctx)
		// Crypto funding rate -- single poll pass via adapter + repo directly.
		if err := collectFundingOnce(ctx, cfg.Hyperliquid, sqlDB, log); err != nil {
			log.Warn().Err(err).Msg("funding rate run-once collection failed")
		}
		// Crypto IV surface -- single collection pass.
		if deribitClient := initDeribitClient(cfg.Deribit, log); deribitClient != nil {
			defer deribitClient.Close()
			log.Info().Msg("run-once: collecting Deribit crypto IV surfaces")
			collectIVSurfaces(ctx, deribitClient, sqlDB, log)
		}
		// On-chain whale flow -- single collection pass.
		collectWhaleFlowOnce(ctx, cfg.OnChain, log)
		log.Info().Msg("run-once complete")
		return
	}

	// Start scheduled services
	if err := refreshSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start data refresh service")
	}

	if ivSvc != nil {
		if err := ivSvc.Start(ctx); err != nil {
			log.Warn().Err(err).Msg("IV collector failed to start")
		}
	}

	if whaleSvc != nil {
		if err := whaleSvc.Start(ctx); err != nil {
			log.Warn().Err(err).Msg("13F whale service failed to start")
		}
	}

	if earningsSvc != nil {
		if err := earningsSvc.Start(ctx); err != nil {
			log.Warn().Err(err).Msg("earnings calendar service failed to start")
		}
	}

	if err := dolthubSvc.Start(ctx); err != nil {
		log.Warn().Err(err).Msg("DoltHub options service failed to start")
	}

	// Macro events — kick once at startup then once every 24h. Refresh
	// is safe to call when no keys are configured (logs debug + exits).
	go func() {
		if err := macroClient.Refresh(ctx); err != nil {
			log.Warn().Err(err).Msg("macro events initial refresh failed")
		}
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := macroClient.Refresh(ctx); err != nil {
					log.Warn().Err(err).Msg("macro events refresh failed")
				}
			}
		}
	}()

	// Corporate actions — kick once at startup then once every 24h.
	go func() {
		if err := refreshCorporateActions(ctx, caClient, caRepo, symbols, log); err != nil {
			log.Warn().Err(err).Msg("corporate actions initial refresh failed")
		}
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := refreshCorporateActions(ctx, caClient, caRepo, symbols, log); err != nil {
					log.Warn().Err(err).Msg("corporate actions refresh failed")
				}
			}
		}
	}()

	// Universe history — kick once at startup then once every 24h.
	go func() {
		if err := refreshUniverseHistory(ctx, alpacaAdapter, universeRepo, symbols, log); err != nil {
			log.Warn().Err(err).Msg("universe history initial refresh failed")
		}
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := refreshUniverseHistory(ctx, alpacaAdapter, universeRepo, symbols, log); err != nil {
					log.Warn().Err(err).Msg("universe history refresh failed")
				}
			}
		}
	}()

	gapDetector := timescaledb.NewGapDetector(repo)
	gapSvc := gapdetect.NewService(gapDetector, repo, log.With().Str("component", "gapdetect").Logger(), nil)
	gapSymbols := make([]domain.Symbol, 0, len(symbols))
	for _, s := range symbols {
		gapSymbols = append(gapSymbols, domain.Symbol(s))
	}
	go func() {
		n := gapSvc.RunOnce(ctx, gapSymbols)
		log.Info().Int("gap_ranges", n).Int("symbols", len(gapSymbols)).Msg("gap detector startup scan complete")
	}()

	// Crypto funding rate live collector (Hyperliquid).
	// Polls latest funding rates every hour (HL uses 1h intervals).
	// Gated by Hyperliquid client init -- no-op if not configured.
	if fundingSvc, cryptoSymbols := initFundingLive(cfg.Hyperliquid, sqlDB, log); fundingSvc != nil {
		go func() {
			log.Info().Strs("symbols", symbolsToStrings(cryptoSymbols)).
				Msg("starting Hyperliquid funding rate live collector")
			if err := fundingSvc.Run(ctx, domain.VenueHyperliquid, cryptoSymbols, 1*time.Hour); err != nil {
				if ctx.Err() == nil {
					log.Warn().Err(err).Msg("funding rate live collector exited with error")
				}
			}
		}()
	}

	// Crypto IV surface collector (Deribit -- read-only, no auth).
	// Polls BTC/ETH IV surface every 5 minutes.
	if deribitClient := initDeribitClient(cfg.Deribit, log); deribitClient != nil {
		go func() {
			defer deribitClient.Close()
			log.Info().Msg("starting Deribit crypto IV surface collector (every 5m)")
			runCryptoIVCollector(ctx, deribitClient, sqlDB, log.With().Str("component", "crypto_iv").Logger())
		}()
	}

	// On-chain whale flow collector (Dune Analytics).
	// Polls net exchange flows every hour. Gated by DUNE_API_KEY env.
	if cfg.OnChain.Enabled && cfg.OnChain.DuneAPIKey != "" {
		go func() {
			log.Info().Msg("starting on-chain whale flow collector (every 1h)")
			runWhaleFlowCollector(ctx, cfg.OnChain, log.With().Str("component", "whale_flow").Logger())
		}()
	}

	log.Info().Int("symbols", len(symbols)).Msg("omo-data running")
	<-ctx.Done()
	log.Info().Msg("omo-data stopped")
}

// noopVIXSetter satisfies datarefresh.VIXSetter when no monitor is running.
type noopVIXSetter struct{}

func (noopVIXSetter) SetVIXLevel(float64) {}

// refreshCorporateActions iterates configured symbols, fetches splits from
// Alpaca (5-year lookback), and upserts them into the corporate_actions table.
func refreshCorporateActions(
	ctx context.Context,
	client *alpaca.CorporateActionsClient,
	repo *timescaledb.CorporateActionsRepo,
	symbols []string,
	log zerolog.Logger,
) error {
	if client == nil {
		log.Debug().Msg("corporate_actions: client nil (no API keys), skipping")
		return nil
	}
	now := time.Now()
	from := now.AddDate(-5, 0, 0)
	total := 0
	symbolsWithSplits := 0
	for _, sym := range symbols {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		splits, err := client.GetSplits(ctx, sym, from, now)
		if err != nil {
			log.Warn().Err(err).Str("symbol", sym).Msg("corporate_actions: GetSplits failed")
			continue
		}
		if len(splits) > 0 {
			symbolsWithSplits++
		}
		for _, ca := range splits {
			if err := repo.Upsert(ctx, ca); err != nil {
				log.Warn().Err(err).Str("symbol", sym).Msg("corporate_actions: upsert failed")
				continue
			}
			total++
		}
	}
	log.Info().Int("splits", total).Int("symbols_with_splits", symbolsWithSplits).Int("symbols_checked", len(symbols)).
		Msg("corporate_actions: refreshed")
	return nil
}

// refreshUniverseHistory calls Alpaca ListTradeable to determine which
// configured symbols are active vs inactive, then upserts universe_history
// rows accordingly: active symbols get an open window (to_date=NULL),
// inactive symbols get their window closed (to_date=today).
func refreshUniverseHistory(
	ctx context.Context,
	adapter *alpaca.Adapter,
	repo *timescaledb.UniverseHistoryRepo,
	symbols []string,
	log zerolog.Logger,
) error {
	assets, err := adapter.ListTradeable(ctx, domain.AssetClassEquity)
	if err != nil {
		return fmt.Errorf("universe_history: list tradeable: %w", err)
	}

	activeSet := make(map[string]bool, len(assets))
	for _, a := range assets {
		activeSet[a.Symbol] = true
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	upserted, closed := 0, 0
	for _, sym := range symbols {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ds := domain.Symbol(sym)

		// Check existing windows to decide action.
		windows, err := repo.WindowsFor(ctx, ds)
		if err != nil {
			log.Warn().Err(err).Str("symbol", sym).Msg("universe_history: WindowsFor failed")
			continue
		}

		if activeSet[sym] {
			// Symbol is active — ensure an open window exists.
			hasOpen := false
			for _, w := range windows {
				if w.ToDate == nil {
					hasOpen = true
					break
				}
			}
			if !hasOpen {
				if err := repo.Upsert(ctx, ports.UniverseWindow{
					Symbol:   ds,
					FromDate: today,
					Source:   "alpaca",
					Note:     "auto-detected active",
				}); err != nil {
					log.Warn().Err(err).Str("symbol", sym).Msg("universe_history: upsert active failed")
					continue
				}
				upserted++
			}
		} else {
			// Symbol not in active list — close any open window.
			for _, w := range windows {
				if w.ToDate == nil {
					w.ToDate = &today
					if err := repo.Upsert(ctx, w); err != nil {
						log.Warn().Err(err).Str("symbol", sym).Msg("universe_history: close window failed")
						continue
					}
					closed++
				}
			}
		}
	}

	log.Info().Int("new_active", upserted).Int("closed", closed).Int("symbols_checked", len(symbols)).
		Msg("universe_history: refreshed")
	return nil
}

// collectIVSurfaces fetches and inserts IV surfaces for BTC/ETH. Shared by
// both the polling loop and the run-once path to avoid SQL duplication.
func collectIVSurfaces(ctx context.Context, ivPort ports.OptionsIVPort, sqlDB *sql.DB, log zerolog.Logger) {
	for _, asset := range []string{"BTC", "ETH"} {
		surface, err := ivPort.Surface(ctx, asset)
		if err != nil {
			log.Warn().Err(err).Str("asset", asset).Msg("crypto IV collection failed")
			continue
		}
		_, err = sqlDB.ExecContext(ctx,
			`INSERT INTO crypto_iv_surface (asset, timestamp, atm_iv_7d, atm_iv_30d, rr_25d_7d, rr_25d_30d, bf_25d_7d, bf_25d_30d, term_slope, put_skew_7d)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (asset, timestamp) DO NOTHING`,
			asset, surface.Timestamp, surface.ATMIV7d, surface.ATMIV30d,
			surface.RR25d7d, surface.RR25d30d, surface.BF25d7d, surface.BF25d30d,
			surface.TermSlope, surface.PutSkew7d)
		if err != nil {
			log.Warn().Err(err).Str("asset", asset).Msg("crypto IV insert failed")
			continue
		}
		log.Info().Str("asset", asset).Float64("atm_iv_30d", surface.ATMIV30d).
			Float64("term_slope", surface.TermSlope).Msg("crypto IV surface collected")
	}
}

// runCryptoIVCollector polls the Deribit IV surface every 5 minutes.
// Blocks until ctx is canceled.
func runCryptoIVCollector(ctx context.Context, ivPort ports.OptionsIVPort, sqlDB *sql.DB, log zerolog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	collectIVSurfaces(ctx, ivPort, sqlDB, log)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectIVSurfaces(ctx, ivPort, sqlDB, log)
		}
	}
}

// initDeribitClient builds a Deribit read-only client from config. Returns nil
// if initialization fails (caller should skip IV collection).
func initDeribitClient(cfg config.DeribitConfig, log zerolog.Logger) *deribit.Client {
	pollInterval := 5 * time.Minute
	if cfg.PollInterval != "" {
		if d, err := time.ParseDuration(cfg.PollInterval); err == nil {
			pollInterval = d
		}
	}
	c, err := deribit.NewClient(deribit.Config{
		BaseURL:      cfg.BaseURL,
		PollInterval: pollInterval,
		Assets:       cfg.Assets,
	}, log)
	if err != nil {
		log.Warn().Err(err).Msg("Deribit client init failed -- crypto IV collection disabled")
		return nil
	}
	return c
}

// initFundingLive builds the Hyperliquid funding live collector components.
// Returns nil service if the HL client cannot be created.
func initFundingLive(cfg config.HyperliquidConfig, sqlDB *sql.DB, log zerolog.Logger) (*ingestion.FundingLive, []domain.Symbol) {
	// Override to mainnet for public data reads -- testnet has sparse funding data.
	hlCfg := cfg
	if hlCfg.Network == "" {
		hlCfg.Network = "mainnet"
	}
	hlClient, err := hyperliquid.NewClient(hlCfg, log.With().Str("component", "hyperliquid").Logger())
	if err != nil {
		log.Warn().Err(err).Msg("Hyperliquid client init failed -- funding rate collection disabled")
		return nil, nil
	}
	rest := hyperliquid.NewRESTClient(hlClient)
	fundingAdapter := hyperliquid.NewPublicFundingAdapter(rest, log.With().Str("component", "hl_funding").Logger())

	fundingRepo := timescaledb.NewFundingRepo(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "funding_repo").Logger(),
	)

	cryptoSymbols := []domain.Symbol{"BTC/USD", "ETH/USD", "SOL/USD"}
	svc := ingestion.NewFundingLive(fundingAdapter, fundingRepo, log.With().Str("component", "funding_live").Logger())
	return svc, cryptoSymbols
}

// collectFundingOnce does a single Latest() poll for each crypto symbol and
// inserts the result. Reuses initFundingLive for client/repo setup.
func collectFundingOnce(ctx context.Context, hlCfg config.HyperliquidConfig, sqlDB *sql.DB, log zerolog.Logger) error {
	svc, cryptoSymbols := initFundingLive(hlCfg, sqlDB, log)
	if svc == nil {
		return nil
	}
	log.Info().Strs("symbols", symbolsToStrings(cryptoSymbols)).Msg("run-once: collecting Hyperliquid funding rates")
	// Run with a very short poll interval — FundingLive.Run polls once
	// immediately then blocks. Use a context that cancels after the first poll.
	oneShot, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = svc.Run(oneShot, domain.VenueHyperliquid, cryptoSymbols, 24*time.Hour)
	return nil
}

// symbolsToStrings converts domain.Symbol slice to string slice for logging.
func symbolsToStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}

func initDB(cfg *config.Config, log zerolog.Logger) *sql.DB {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)

	// Retry DB connection
	delay := time.Second
	for attempt := 1; attempt <= 10; attempt++ {
		if err := sqlDB.PingContext(context.Background()); err == nil {
			log.Info().Msg("TimescaleDB connected")
			return sqlDB
		}
		log.Warn().Int("attempt", attempt).Dur("retry_in", delay).Msg("DB not ready, retrying")
		time.Sleep(delay)
		delay *= 2
		if delay > 15*time.Second {
			delay = 15 * time.Second
		}
	}
	log.Fatal().Msg("failed to connect to TimescaleDB after retries")
	return nil
}

// runWhaleFlowCollector polls on-chain whale flow data every hour.
// Blocks until ctx is canceled.
func runWhaleFlowCollector(ctx context.Context, cfg config.OnChainConfig, log zerolog.Logger) {
	tracker, err := onchain.NewFlowTracker(toOnChainCfg(cfg), log)
	if err != nil {
		log.Warn().Err(err).Msg("on-chain flow tracker init failed")
		return
	}

	assets := []string{"BTC", "ETH", "SOL"}
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	collect := func() {
		for _, asset := range assets {
			result, ferr := tracker.NetFlow(ctx, asset, 24)
			if ferr != nil {
				log.Warn().Err(ferr).Str("asset", asset).Msg("whale flow fetch failed")
				continue
			}
			log.Info().Str("asset", asset).
				Float64("net_flow_usd", result.NetFlowUSD).
				Int("large_count", result.LargeCount).
				Msg("whale flow collected")
		}
	}

	collect()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

func toOnChainCfg(c config.OnChainConfig) onchain.OnChainConfig {
	return onchain.OnChainConfig{
		DuneAPIKey:  c.DuneAPIKey,
		DuneBaseURL: c.DuneBaseURL,
		QueryIDs:    c.QueryIDs,
		CacheTTL:    c.CacheTTL,
		Enabled:     c.Enabled,
	}
}

// collectWhaleFlowOnce runs a single whale flow collection (run-once mode).
func collectWhaleFlowOnce(ctx context.Context, cfg config.OnChainConfig, log zerolog.Logger) {
	if !cfg.Enabled || cfg.DuneAPIKey == "" {
		return
	}
	tracker, err := onchain.NewFlowTracker(toOnChainCfg(cfg), log)
	if err != nil {
		log.Warn().Err(err).Msg("on-chain flow tracker init failed")
		return
	}
	for _, asset := range []string{"BTC", "ETH", "SOL"} {
		result, ferr := tracker.NetFlow(ctx, asset, 24)
		if ferr != nil {
			log.Warn().Err(ferr).Str("asset", asset).Msg("whale flow fetch failed")
			continue
		}
		log.Info().Str("asset", asset).Float64("net_flow_usd", result.NetFlowUSD).Msg("whale flow collected")
	}
}
