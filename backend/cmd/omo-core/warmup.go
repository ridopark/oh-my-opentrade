package main

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/formingbar"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/rs/zerolog"
)

type symbolLists struct {
	equity    []domain.Symbol
	crypto    []domain.Symbol
	all       []domain.Symbol
	timeframe domain.Timeframe
}

func buildSymbolLists(cfg *config.Config) symbolLists {
	equityStrs := cfg.Symbols.SymbolsByAssetClass("EQUITY")
	cryptoStrs := cfg.Symbols.SymbolsByAssetClass("CRYPTO")
	allStrs := cfg.Symbols.AllSymbols()

	equitySymbols := make([]domain.Symbol, len(equityStrs))
	for i, s := range equityStrs {
		equitySymbols[i] = domain.Symbol(s)
	}
	cryptoSymbols := make([]domain.Symbol, len(cryptoStrs))
	for i, s := range cryptoStrs {
		cryptoSymbols[i] = domain.Symbol(s)
	}
	symbols := make([]domain.Symbol, len(allStrs))
	for i, s := range allStrs {
		symbols[i] = domain.Symbol(s)
	}

	return symbolLists{
		equity:    equitySymbols,
		crypto:    cryptoSymbols,
		all:       symbols,
		timeframe: domain.Timeframe(cfg.Symbols.Timeframe),
	}
}

func warmupIndicators(ctx context.Context, cfg *config.Config, infra *infraDeps, svc *appServices, syms symbolLists, log zerolog.Logger) {
	equityStrs := cfg.Symbols.SymbolsByAssetClass("EQUITY")
	cryptoStrs := cfg.Symbols.SymbolsByAssetClass("CRYPTO")

	log.Info().
		Strs("equity_symbols", equityStrs).
		Strs("crypto_symbols", cryptoStrs).
		Int("total_symbols", len(syms.all)).
		Msg("symbol lists initialized")

	for _, sym := range syms.crypto {
		svc.spikeFilter.SetMaxDeviation(sym, ingestion.DeviationCrypto)
	}
	for _, sym := range syms.equity {
		svc.spikeFilter.SetMaxDeviation(sym, ingestion.DeviationEquity)
	}

	warmupLog := log.With().Str("component", "warmup").Logger()
	warmupBarsCache := make(map[string][]domain.MarketBar)

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}
	nowET := time.Now().In(loc)
	todayOpen := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 9, 30, 0, 0, loc)

	svc.monitor.InitAggregators(syms.all, todayOpen)

	// Equity warmup: use previous NYSE RTH session.
	if len(syms.equity) > 0 {
		prevStart, prevEnd := domain.PreviousRTHSession(time.Now())
		warmupFrom := prevEnd.Add(-120 * time.Minute)
		warmupTo := prevEnd
		warmupLog.Info().
			Time("prev_session_start", prevStart).
			Time("prev_session_end", prevEnd).
			Time("warmup_from", warmupFrom).
			Time("warmup_to", warmupTo).
			Msg("warming equity indicators from previous RTH session")
		for _, sym := range syms.equity {
			bars, err := infra.alpacaAdapter.GetHistoricalBars(ctx, sym, syms.timeframe, warmupFrom, warmupTo)
			if err != nil {
				warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("equity warmup fetch failed, starting cold")
				continue
			}
			n := svc.monitor.WarmUp(bars)
			svc.monitor.ResetSessionIndicators(sym.String())
			warmupBarsCache[string(sym)] = bars
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", n).
				Msg("equity indicator warmup complete")
		}
	}

	// Crypto warmup: 24/7 market, use last 2 hours.
	if len(syms.crypto) > 0 {
		cryptoWarmupTo := time.Now().UTC()
		cryptoWarmupFrom := cryptoWarmupTo.Add(-120 * time.Minute)
		warmupLog.Info().
			Time("warmup_from", cryptoWarmupFrom).
			Time("warmup_to", cryptoWarmupTo).
			Msg("warming crypto indicators from last 2 hours")
		for _, sym := range syms.crypto {
			bars, err := infra.alpacaAdapter.GetHistoricalBars(ctx, sym, syms.timeframe, cryptoWarmupFrom, cryptoWarmupTo)
			if err != nil {
				warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto warmup fetch failed, starting cold")
				continue
			}
			n := svc.monitor.WarmUp(bars)
			svc.monitor.ResetSessionIndicators(sym.String())
			warmupBarsCache[string(sym)] = bars
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", n).
				Msg("crypto indicator warmup complete")
		}
	}

	for _, sym := range syms.all {
		if bars, ok := warmupBarsCache[string(sym)]; ok && len(bars) > 0 {
			n := svc.spikeFilter.Seed(sym, bars)
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", n).
				Msg("adaptive spike filter seeded")
		}
	}

	var runnerWarmupCalc *monitor.IndicatorCalculator
	var runnerWarmupSnapshotFn strategy.IndicatorSnapshotFunc
	if svc.useStrategyV2 && svc.strategyRunner != nil {
		runnerWarmupCalc = monitor.NewIndicatorCalculator()
		runnerWarmupSnapshotFn = func(bar domain.MarketBar) start.IndicatorData {
			snap := runnerWarmupCalc.Update(bar)
			return start.IndicatorData{
				RSI:       snap.RSI,
				StochK:    snap.StochK,
				StochD:    snap.StochD,
				EMA9:      snap.EMA9,
				EMA21:     snap.EMA21,
				VWAP:      snap.VWAP,
				Volume:    snap.Volume,
				VolumeSMA: snap.VolumeSMA,
			}
		}
		for _, sym := range syms.all {
			bars := warmupBarsCache[string(sym)]
			if len(bars) == 0 {
				continue
			}
			n := svc.strategyRunner.WarmUp(string(sym), bars, runnerWarmupSnapshotFn)
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", n).
				Msg("strategy runner warmup complete (1m)")
		}

		svc.strategyRunner.InitAggregators(todayOpen)

		htfReqs := collectHTFWarmupReqs(svc.strategyRunner)
		if len(htfReqs) > 0 {
			warmupLog.Info().Int("htf_pairs", len(htfReqs)).Msg("warming up HTF strategies")
		}
		for _, req := range htfReqs {
			var from, to time.Time
			if req.symbol.IsCryptoSymbol() {
				to = time.Now().UTC()
				from = to.Add(-req.lookback)
			} else {
				_, prevEnd := domain.PreviousRTHSession(time.Now())
				to = prevEnd
				from = to.Add(-req.lookback)
			}
			bars, err := infra.alpacaAdapter.GetHistoricalBars(ctx, req.symbol, req.timeframe, from, to)
			if err != nil {
				warmupLog.Warn().Err(err).
					Str("symbol", string(req.symbol)).
					Str("timeframe", string(req.timeframe)).
					Msg("HTF warmup fetch failed, starting cold")
				continue
			}
			n := svc.strategyRunner.WarmUpTF(string(req.symbol), string(req.timeframe), bars, runnerWarmupSnapshotFn)
			warmupLog.Info().
				Str("symbol", string(req.symbol)).
				Str("timeframe", string(req.timeframe)).
				Int("bars", n).
				Msg("HTF strategy warmup complete")
		}
	}

	isWeekday := nowET.Weekday() != time.Saturday && nowET.Weekday() != time.Sunday
	isOpen := !domain.IsNYSEHoliday(nowET) && isWeekday
	if isOpen && nowET.After(todayOpen) {
		warmupLog.Info().Msg("replaying current-session bars for ORB state recovery")
		for _, sym := range syms.equity {
			orbBars, err := infra.alpacaAdapter.GetHistoricalBars(ctx, sym, syms.timeframe, todayOpen.UTC(), time.Now())
			if err != nil {
				warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("ORB warmup fetch failed")
				continue
			}
			svc.monitor.WarmUpORB(orbBars)
			if svc.useStrategyV2 && svc.strategyRunner != nil {
				if runnerWarmupCalc == nil {
					runnerWarmupCalc = monitor.NewIndicatorCalculator()
				}
				if runnerWarmupSnapshotFn == nil {
					runnerWarmupSnapshotFn = func(bar domain.MarketBar) start.IndicatorData {
						snap := runnerWarmupCalc.Update(bar)
						return start.IndicatorData{
							RSI:       snap.RSI,
							StochK:    snap.StochK,
							StochD:    snap.StochD,
							EMA9:      snap.EMA9,
							EMA21:     snap.EMA21,
							VWAP:      snap.VWAP,
							Volume:    snap.Volume,
							VolumeSMA: snap.VolumeSMA,
						}
					}
				}
				_ = svc.strategyRunner.WarmUp(string(sym), orbBars, runnerWarmupSnapshotFn)
			}
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", len(orbBars)).
				Msg("ORB warmup complete")
		}
	}
}

func startStreaming(ctx context.Context, infra *infraDeps, svc *appServices, syms symbolLists, log zerolog.Logger) {
	// 7a. Start forming-bar service for real-time chart candle formation.
	formingBarLog := log.With().Str("component", "formingbar").Logger()
	formingBarSvc := formingbar.NewService(infra.eventBus, formingBarLog)
	if err := formingBarSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start forming bar service")
	}

	// 7b. Wire trade handler → publishes EventTradeReceived for forming bar aggregation.
	infra.alpacaAdapter.SetTradeHandler(func(tCtx context.Context, trade domain.MarketTrade) error {
		evt, err := domain.NewEvent(domain.EventTradeReceived, "system", domain.EnvModePaper,
			fmt.Sprintf("trade-%s-%d", trade.Symbol, trade.Time.UnixNano()), trade)
		if err != nil {
			return nil
		}
		return infra.eventBus.Publish(tCtx, *evt)
	})

	infra.alpacaAdapter.CryptoWSClient().SetDegradedCallback(func(reason string) {
		evt, err := domain.NewEvent(domain.EventFeedDegraded, "system", domain.EnvModePaper,
			fmt.Sprintf("feed-degraded-%d", time.Now().UnixNano()),
			domain.FeedDegradedPayload{Feed: "crypto", Reason: reason})
		if err != nil {
			return
		}
		_ = infra.eventBus.Publish(ctx, *evt)
	})

	infra.alpacaAdapter.WSClient().SetPipelineHealth(svc.ingestion)
	infra.alpacaAdapter.CryptoWSClient().SetPipelineHealth(svc.ingestion)

	infra.alpacaAdapter.CryptoWSClient().SetCircuitBreakerCallback(func(consecutiveFails int, blockedFor time.Duration) {
		evt, err := domain.NewEvent(domain.EventWSCircuitBreakerTripped, "system", domain.EnvModePaper,
			fmt.Sprintf("ws-cb-tripped-%d", time.Now().UnixNano()),
			domain.WSCircuitBreakerTrippedPayload{
				Feed:              "crypto",
				ConsecutiveFails:  consecutiveFails,
				BlockedForSeconds: blockedFor.Seconds(),
			})
		if err != nil {
			return
		}
		_ = infra.eventBus.Publish(ctx, *evt)
	})

	log.Info().
		Strs("symbols", symbolStrings(syms.all)).
		Str("timeframe", string(syms.timeframe)).
		Msg("starting WebSocket stream")
	go func() {
		barHandler := func(bCtx context.Context, bar domain.MarketBar) error {
			barTenant := "default"
			if svc.orchestrator != nil {
				barTenant = "system"
			}
			evt, err := domain.NewEvent(domain.EventMarketBarReceived, barTenant, domain.EnvModePaper, bar.Time.String()+string(bar.Symbol), bar)
			if err != nil {
				log.Error().Err(err).Str("symbol", string(bar.Symbol)).Msg("failed to create bar event")
				return nil
			}
			if err := infra.eventBus.Publish(bCtx, *evt); err != nil {
				log.Error().Err(err).Str("symbol", string(bar.Symbol)).Msg("failed to publish bar event")
			}
			return nil
		}
		if err := infra.alpacaAdapter.StreamBars(ctx, syms.all, syms.timeframe, barHandler); err != nil {
			log.Error().Err(err).Msg("WebSocket stream error")
		}
	}()
	log.Info().Msg("ready — WebSocket streaming active")

	{
		stratNames := symbolStrings(syms.all)
		evt, err := domain.NewEvent(domain.EventSystemStarted, "system", domain.EnvModePaper,
			fmt.Sprintf("system-started-%d", time.Now().UnixNano()),
			domain.SystemStartedPayload{
				Version:    "dev",
				EnvMode:    string(domain.EnvModePaper),
				Strategies: stratNames,
			})
		if err == nil {
			_ = infra.eventBus.Publish(ctx, *evt)
		}
	}
}

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}

type htfWarmupReq struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
	lookback  time.Duration
}

func collectHTFWarmupReqs(runner *strategy.Runner) []htfWarmupReq {
	seen := make(map[string]struct{})
	var reqs []htfWarmupReq
	for _, inst := range runner.Router().AllInstances() {
		tfs := inst.Assignment().Timeframes
		if len(tfs) == 0 {
			continue
		}
		warmupBars := inst.Strategy().WarmupBars()
		for _, tf := range tfs {
			if tf == "1m" {
				continue
			}
			for _, sym := range inst.Assignment().Symbols {
				key := sym + ":" + tf
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				var barDur time.Duration
				switch tf {
				case "5m":
					barDur = 5 * time.Minute
				case "15m":
					barDur = 15 * time.Minute
				default:
					barDur = time.Minute
				}
				lookback := time.Duration(float64(warmupBars) * float64(barDur) * 1.2)
				reqs = append(reqs, htfWarmupReq{
					symbol:    domain.Symbol(sym),
					timeframe: domain.Timeframe(tf),
					lookback:  lookback,
				})
			}
		}
	}
	return reqs
}
