package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	alpacaadapter "github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// runBacktestViaRunner replaces omo-replay's previous inline backtest
// implementation with a delegation to the canonical backtest.Runner that the
// HTTP /backtest/run endpoint uses. Sole-source of truth for backtest wiring
// kills the drift class that produced post-PR-67 nil-derefs (indicator,
// AVWAPFn) and signal-divergence gaps (dark-pool, whale, HTF warmup) that
// repeatedly shipped as patch PRs against omo-replay's parallel impl.
//
// Output JSON, if requested, is the same Result shape the HTTP path returns
// at GET /backtest/results/{id}, so any tooling that consumes one consumes
// the other.
func runBacktestViaRunner(
	ctx context.Context,
	log zerolog.Logger,
	sqlDB *sql.DB,
	appCfg *config.Config,
	symbols []domain.Symbol,
	fromTime, toTime time.Time,
	timeframeFlag string,
	strategiesFlag string,
	initialEquity float64,
	slippageBPS int64,
	speedFlag string,
	noAIFlag bool,
	emitGatedDiag bool,
	outputJSON string,
	copytradeHist string,
	copytradeLedgerDir string,
) error {
	tf := domain.Timeframe("1m")
	if timeframeFlag != "" {
		tf = domain.Timeframe(timeframeFlag)
	}

	var strategies []string
	for _, s := range strings.Split(strategiesFlag, ",") {
		if t := strings.TrimSpace(s); t != "" {
			strategies = append(strategies, t)
		}
	}

	cfg := backtest.RunConfig{
		Symbols:            symbols,
		From:               fromTime,
		To:                 toTime,
		Timeframe:          tf,
		InitialEquity:      initialEquity,
		SlippageBPS:        slippageBPS,
		Speed:              speedFlag,
		NoAI:               noAIFlag,
		Strategies:         strategies,
		CompoundEquity:     true,
		EmitGatedDiag:      emitGatedDiag,
		CopytradeHistory:   copytradeHist,
		CopytradeLedgerDir: copytradeLedgerDir,
	}

	infra := bootstrap.BuildBacktestInfra(
		bootstrap.BacktestDeps{
			DB:     sqlDB,
			AppCfg: appCfg,
			Logger: log,
		},
		slippageBPS,
		initialEquity,
		noAIFlag,
		bootstrap.BacktestInfraOptions{
			BacktestFrom: fromTime,
			BacktestTo:   toTime,
		},
	)

	var marketData ports.MarketDataPort
	if appCfg.Alpaca.APIKeyID != "" {
		a, err := alpacaadapter.NewAdapter(appCfg.Alpaca, log.With().Str("component", "alpaca_replay").Logger())
		if err != nil {
			log.Warn().Err(err).Msg("backtest: alpaca adapter unavailable; runner falls through to repo-only bar fetches")
		} else {
			marketData = a
		}
	}

	runner := backtest.NewRunner(cfg, infra, appCfg, marketData, log)

	log.Info().
		Str("backtest_id", runner.ID()).
		Strs("symbols", symbolStringsForLog(symbols)).
		Time("from", fromTime).
		Time("to", toTime).
		Float64("equity", initialEquity).
		Bool("no_ai", noAIFlag).
		Msg("omo-replay --backtest delegating to backtest.Runner")

	if err := runner.Run(ctx); err != nil {
		return fmt.Errorf("backtest run: %w", err)
	}

	result := runner.GetResult()
	if result == nil {
		return fmt.Errorf("backtest completed but no result captured")
	}

	log.Info().
		Float64("final_equity", result.FinalEquity).
		Float64("total_pnl", result.TotalPnL).
		Int("trade_count", result.TradeCount).
		Float64("win_rate_pct", result.WinRate).
		Float64("profit_factor", result.ProfitFactor).
		Msg("backtest complete")

	if outputJSON != "" {
		b, jerr := json.MarshalIndent(result, "", "  ")
		if jerr != nil {
			return fmt.Errorf("marshal result: %w", jerr)
		}
		if werr := os.WriteFile(outputJSON, b, 0o644); werr != nil {
			return fmt.Errorf("write output JSON %s: %w", outputJSON, werr)
		}
		log.Info().Str("path", outputJSON).Msg("backtest results written to JSON")
	}

	return nil
}

func symbolStringsForLog(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.String()
	}
	return out
}
