package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

const (
	defaultTenantID   = "default"
	defaultEnvMode    = domain.EnvModePaper
	defaultInitEquity = 100_000.0
)

// positionEntry tracks avg entry price and quantity for realized P&L calculation.
type positionEntry struct {
	avgEntry float64
	quantity float64
}

func main() {
	var (
		fromFlag   string
		toFlag     string
		initEquity float64
		tenantID   string
		dryRun     bool
		configPath string
		envPath    string
	)

	flag.StringVar(&fromFlag, "from", "", "Start date in YYYY-MM-DD format (default: 2020-01-01)")
	flag.StringVar(&toFlag, "to", "", "End date in YYYY-MM-DD format (default: now)")
	flag.Float64Var(&initEquity, "equity", defaultInitEquity, "Initial account equity")
	flag.StringVar(&tenantID, "tenant", defaultTenantID, "Tenant ID")
	flag.BoolVar(&dryRun, "dry-run", false, "Fetch and compute but do not write to database")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.Parse()

	// Logger
	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill-trades").Logger()

	// Load config
	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve time range
	var fromTime, toTime time.Time
	if fromFlag != "" {
		fromTime, err = time.Parse("2006-01-02", fromFlag)
		if err != nil {
			log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from date")
		}
	} else {
		fromTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if toFlag != "" {
		toTime, err = time.Parse("2006-01-02", toFlag)
		if err != nil {
			log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to date")
		}
	} else {
		toTime = time.Now().UTC()
	}

	// Initialize Alpaca adapter
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	// Initialize TimescaleDB
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	if err := sqlDB.PingContext(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB")
	}
	defer sqlDB.Close()
	log.Info().Msg("TimescaleDB connected")

	dbWrapper := timescaledb.NewSqlDB(sqlDB)
	repo := timescaledb.NewRepositoryWithLogger(dbWrapper, log.With().Str("component", "repo").Logger())
	pnlRepo := timescaledb.NewPnLRepository(dbWrapper, log.With().Str("component", "pnl-repo").Logger())

	// Context with graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, cancelling...")
		cancel()
	}()

	// ── Step 1: Fetch closed orders from Alpaca ──
	log.Info().
		Time("from", fromTime).
		Time("to", toTime).
		Msg("fetching closed orders from Alpaca")

	orders, err := alpacaAdapter.GetClosedOrders(ctx, fromTime, toTime)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to fetch closed orders")
	}
	log.Info().Int("total_orders", len(orders)).Msg("fetched orders from Alpaca")

	// ── Step 2: Filter to filled orders and parse into domain trades ──
	type parsedTrade struct {
		trade    domain.Trade
		filledAt time.Time
	}
	var trades []parsedTrade

	for _, o := range orders {
		if o.Status != "filled" {
			continue
		}
		if o.FilledAt == nil {
			log.Warn().Str("order_id", o.ID).Msg("filled order missing filled_at, skipping")
			continue
		}

		filledAt, err := time.Parse(time.RFC3339Nano, *o.FilledAt)
		if err != nil {
			log.Warn().Str("order_id", o.ID).Str("filled_at", *o.FilledAt).Err(err).Msg("failed to parse filled_at, skipping")
			continue
		}

		filledQty, err := strconv.ParseFloat(o.FilledQty, 64)
		if err != nil || filledQty <= 0 {
			log.Warn().Str("order_id", o.ID).Str("filled_qty", o.FilledQty).Msg("invalid filled_qty, skipping")
			continue
		}

		filledPrice, err := strconv.ParseFloat(o.FilledAvgPrice, 64)
		if err != nil || filledPrice <= 0 {
			log.Warn().Str("order_id", o.ID).Str("filled_avg_price", o.FilledAvgPrice).Msg("invalid filled_avg_price, skipping")
			continue
		}

		// Normalize side to uppercase (DB constraint: 'BUY' or 'SELL')
		side := o.Side
		if side == "buy" {
			side = "BUY"
		} else if side == "sell" {
			side = "SELL"
		}

		trade := domain.Trade{
			Time:       filledAt,
			TenantID:   tenantID,
			EnvMode:    defaultEnvMode,
			TradeID:    uuid.New(),
			Symbol:     domain.Symbol(o.Symbol),
			Side:       side,
			Quantity:   filledQty,
			Price:      filledPrice,
			Commission: 0, // Alpaca paper has no commissions
			Status:     "FILLED",
			Strategy:   "backfill",
			Rationale:  fmt.Sprintf("backfill from alpaca order %s", o.ID),
		}

		trades = append(trades, parsedTrade{trade: trade, filledAt: filledAt})
	}

	// Sort by filled_at ascending
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].filledAt.Before(trades[j].filledAt)
	})

	log.Info().Int("filled_trades", len(trades)).Msg("parsed filled orders")

	if len(trades) == 0 {
		log.Warn().Msg("no filled trades found — nothing to backfill")
		return
	}

	if dryRun {
		log.Info().Msg("dry-run mode: printing trades without writing to DB")
		for _, t := range trades {
			log.Info().
				Time("time", t.trade.Time).
				Str("symbol", string(t.trade.Symbol)).
				Str("side", t.trade.Side).
				Float64("qty", t.trade.Quantity).
				Float64("price", t.trade.Price).
				Msg("trade")
		}
		return
	}

	// ── Step 3: Insert trades into DB ──
	log.Info().Msg("inserting trades into database")
	insertedCount := 0
	for _, t := range trades {
		if ctx.Err() != nil {
			log.Warn().Msg("context cancelled, stopping trade insertion")
			break
		}
		if err := repo.SaveTrade(ctx, t.trade); err != nil {
			log.Error().Err(err).Str("symbol", string(t.trade.Symbol)).Msg("failed to save trade, continuing")
			continue
		}
		insertedCount++
	}
	log.Info().Int("inserted", insertedCount).Msg("trades inserted")

	// ── Step 4: Compute daily P&L using position tracking (matches ledger_writer.go logic) ──
	log.Info().Msg("computing daily P&L from trades")

	positions := make(map[string]*positionEntry) // key: symbol
	type dailyAccum struct {
		date        time.Time
		realizedPnL float64
		tradeCount  int
	}
	dailyMap := make(map[string]*dailyAccum) // key: YYYY-MM-DD

	for _, t := range trades {
		sym := string(t.trade.Symbol)
		dateStr := t.filledAt.UTC().Format("2006-01-02")

		// Position tracking — same logic as ledger_writer.go handleFill
		var fillPnL float64
		switch t.trade.Side {
		case "BUY":
			pos := positions[sym]
			if pos == nil {
				pos = &positionEntry{}
				positions[sym] = pos
			}
			totalCost := pos.avgEntry*pos.quantity + t.trade.Price*t.trade.Quantity
			pos.quantity += t.trade.Quantity
			if pos.quantity > 0 {
				pos.avgEntry = totalCost / pos.quantity
			}
			fillPnL = 0

		case "SELL":
			pos := positions[sym]
			if pos != nil && pos.quantity > 0 {
				sellQty := t.trade.Quantity
				if sellQty > pos.quantity {
					sellQty = pos.quantity
				}
				fillPnL = (t.trade.Price - pos.avgEntry) * sellQty
				pos.quantity -= sellQty
				if pos.quantity <= 0 {
					pos.quantity = 0
					pos.avgEntry = 0
				}
			} else {
				log.Warn().Str("symbol", sym).Msg("sell without tracked position, recording zero P&L")
				fillPnL = 0
			}
		}

		accum, exists := dailyMap[dateStr]
		if !exists {
			date, _ := time.Parse("2006-01-02", dateStr)
			accum = &dailyAccum{date: date}
			dailyMap[dateStr] = accum
		}
		accum.realizedPnL += fillPnL
		accum.tradeCount++
	}

	// Sort daily entries chronologically
	type dailyEntry struct {
		dateStr string
		accum   *dailyAccum
	}
	var dailyEntries []dailyEntry
	for k, v := range dailyMap {
		dailyEntries = append(dailyEntries, dailyEntry{dateStr: k, accum: v})
	}
	sort.Slice(dailyEntries, func(i, j int) bool {
		return dailyEntries[i].dateStr < dailyEntries[j].dateStr
	})

	// ── Step 5: Compute equity curve and drawdown, then persist daily P&L + equity ──
	log.Info().Msg("computing equity curve and persisting P&L records")

	equity := initEquity
	peakEquity := initEquity

	// Insert initial equity point (before any trades)
	firstTradeDate := trades[0].filledAt.UTC()
	initTime := time.Date(firstTradeDate.Year(), firstTradeDate.Month(), firstTradeDate.Day(), 0, 0, 0, 0, time.UTC)
	initPt := domain.EquityPoint{
		Time:     initTime,
		TenantID: tenantID,
		EnvMode:  defaultEnvMode,
		Equity:   initEquity,
		Cash:     initEquity,
		Drawdown: 0,
	}
	if err := pnlRepo.SaveEquityPoint(ctx, initPt); err != nil {
		log.Error().Err(err).Msg("failed to save initial equity point")
	}

	pnlCount := 0
	eqCount := 0
	for _, entry := range dailyEntries {
		if ctx.Err() != nil {
			log.Warn().Msg("context cancelled, stopping P&L persistence")
			break
		}

		accum := entry.accum

		// Update running equity
		equity += accum.realizedPnL
		if equity > peakEquity {
			peakEquity = equity
		}

		// Compute drawdown
		var drawdown float64
		if peakEquity > 0 && equity < peakEquity {
			drawdown = (peakEquity - equity) / peakEquity
		}

		// Persist daily P&L
		pnl := domain.DailyPnL{
			Date:        accum.date,
			TenantID:    tenantID,
			EnvMode:     defaultEnvMode,
			RealizedPnL: accum.realizedPnL,
			TradeCount:  accum.tradeCount,
			MaxDrawdown: drawdown,
		}
		if err := pnlRepo.UpsertDailyPnL(ctx, pnl); err != nil {
			log.Error().Err(err).Str("date", entry.dateStr).Msg("failed to upsert daily P&L")
		} else {
			pnlCount++
		}

		// Persist equity curve point (end of day)
		eodTime := time.Date(accum.date.Year(), accum.date.Month(), accum.date.Day(), 23, 59, 59, 0, time.UTC)
		pt := domain.EquityPoint{
			Time:     eodTime,
			TenantID: tenantID,
			EnvMode:  defaultEnvMode,
			Equity:   equity,
			Cash:     initEquity, // baseline cash
			Drawdown: drawdown,
		}
		if err := pnlRepo.SaveEquityPoint(ctx, pt); err != nil {
			log.Error().Err(err).Str("date", entry.dateStr).Msg("failed to save equity point")
		} else {
			eqCount++
		}

		log.Info().
			Str("date", entry.dateStr).
			Float64("realized_pnl", accum.realizedPnL).
			Int("trade_count", accum.tradeCount).
			Float64("equity", equity).
			Float64("drawdown", math.Round(drawdown*10000)/10000).
			Msg("daily P&L recorded")
	}

	log.Info().
		Int("daily_pnl_records", pnlCount).
		Int("equity_points", eqCount+1). // +1 for initial point
		Float64("final_equity", equity).
		Float64("total_pnl", equity-initEquity).
		Float64("peak_equity", peakEquity).
		Msg("backfill complete")
}
