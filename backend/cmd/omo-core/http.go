package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	omhttp "github.com/oh-my-opentrade/backend/internal/adapters/http"
	"github.com/oh-my-opentrade/backend/internal/adapters/middleware"
	"github.com/oh-my-opentrade/backend/internal/adapters/sse"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func initHTTPServer(ctx context.Context, cfg *config.Config, infra *infraDeps, svc *appServices, log zerolog.Logger) *http.Server {
	// 6a. Start SSE handler — subscribes to the event bus and fans out to HTTP clients.
	sseLog := log.With().Str("component", "sse").Logger()
	sseHandler := sse.NewHandler(infra.eventBus, sseLog)
	go func() {
		if err := sseHandler.Start(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("SSE handler error")
		}
	}()

	// 6b. Start HTTP server for the SSE endpoint.
	httpLog := log.With().Str("component", "http").Logger()
	// Initialize Prometheus metrics and instrumented mux.
	met := metrics.New("dev", "unknown", "main", svc.useStrategyV2)

	// Wire Prometheus metrics into subsystems.
	svc.execution.SetMetrics(met)
	svc.ingestion.SetMetrics(met)
	svc.dailyLossBreaker.SetMetrics(met)
	svc.ledgerWriter.SetMetrics(met)
	infra.alpacaAdapter.WSClient().SetMetrics(met)
	infra.alpacaAdapter.TradeStream().SetOnConnect(func(connected bool) {
		if connected {
			met.Orders.TradeWSConnected.Set(1)
		} else {
			met.Orders.TradeWSConnected.Set(0)
		}
	})
	if svc.useStrategyV2 {
		svc.strategyRunner.SetMetrics(met)
	}
	if svc.orchestrator != nil {
		svc.orchestrator.SetMetrics(met)
	}

	imux := &metrics.InstrumentedMux{Mux: http.NewServeMux(), Metrics: met}
	registerRoutes(imux, cfg, infra, svc, httpLog, sseHandler)

	// Prometheus metrics endpoint (not instrumented by InstrumentedMux to avoid recursion).
	imux.Mux.Handle("/metrics", promhttp.HandlerFor(met.Reg, promhttp.HandlerOpts{}))

	handler := middleware.AccessLog(httpLog)(imux.Mux)
	handler = otelhttp.NewHandler(handler, "omo-core",
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)

	httpServer := &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		log.Info().Str("addr", httpServer.Addr).Msg("HTTP server listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP server error")
		}
	}()

	return httpServer
}

func registerRoutes(imux *metrics.InstrumentedMux, cfg *config.Config, infra *infraDeps, svc *appServices, httpLog zerolog.Logger, sseHandler *sse.Handler) {
	imux.Mux.HandleFunc("/debug/pprof/", pprof.Index)
	imux.Mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	imux.Mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	imux.Mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	imux.Mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	imux.Handle("/bars", omhttp.NewBarsHandler(infra.repo, infra.alpacaAdapter, httpLog))
	imux.Handle("/events", sseHandler)

	imux.Mux.HandleFunc("/debug/ai-screener/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
			return
		}
		if svc.aiScreenerSvc == nil {
			http.Error(w, `{"error":"ai screener not enabled — set AI_SCREENER_ENABLED=true and STRATEGY_V2=true"}`, http.StatusServiceUnavailable)
			return
		}
		loc, _ := time.LoadLocation("America/New_York")
		asOfET := time.Now().In(loc)
		httpLog.Info().Time("as_of_et", asOfET).Msg("debug: triggering AI screener run")

		go func() {
			if err := svc.aiScreenerSvc.RunAIScreen(context.Background(), asOfET); err != nil {
				httpLog.Error().Err(err).Msg("debug: AI screener run failed")
			} else {
				httpLog.Info().Msg("debug: AI screener run completed")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "started",
			"as_of":  asOfET.Format(time.RFC3339),
		})
	})
	imux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	imux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		sym := r.URL.Query().Get("symbol")
		if sym == "" {
			http.Error(w, "symbol required", http.StatusBadRequest)
			return
		}
		snap, ok := svc.monitor.GetLastSnapshot(sym)
		if !ok {
			http.Error(w, "no state for symbol", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})

	// Health and strategy endpoints.
	healthHandler := omhttp.NewHealthHandler(httpLog,
		omhttp.DBChecker(infra.sqlDB),
		omhttp.StaticChecker("ingestion"),
		omhttp.StaticChecker("monitor"),
		omhttp.StaticChecker("execution"),
		omhttp.StaticChecker("strategy"),
		omhttp.FeedChecker("ws_feed", func() (bool, string) {
			fh := infra.alpacaAdapter.WSClient().FeedHealth()
			if fh.IsHealthy() {
				return true, ""
			}
			detail := fmt.Sprintf("state=%s connected=%v last_bar_age=%s", fh.State, fh.Connected, fh.LastBarAge.Round(time.Second))
			return false, detail
		}),
	)
	imux.Handle("/healthz/services", healthHandler)

	const strategyBasePath = "configs/strategies"
	strategyHandler := omhttp.NewStrategyHandler(svc.dnaManager, strategyBasePath, httpLog)
	imux.Handle("/strategies/", strategyHandler)
	dnaApprovalHandler := omhttp.NewDNAApprovalHandler(svc.dnaApproval, httpLog)
	imux.Handle("/api/dna/", dnaApprovalHandler)
	// KakaoTalk routes disabled — notifier is disabled.
	// if svc.kakaoNotifier != nil {
	// 	kakaoRedirectURI := cfg.Notification.KakaoRedirectURI
	// 	if kakaoRedirectURI == "" {
	// 		kakaoRedirectURI = fmt.Sprintf("http://localhost:%d/api/v1/notifications/kakao/callback", cfg.Server.Port)
	// 	}
	// 	kakaoHandler := omhttp.NewKakaoHandler(svc.kakaoNotifier, infra.tokenStore, svc.kakaoNotifier, cfg.Notification.KakaoRestAPIKey, kakaoRedirectURI, httpLog)
	// 	imux.Handle("/api/v1/notifications/kakao/", kakaoHandler)
	// }
	if svc.useStrategyV2 {
		lifecycleHandler := omhttp.NewLifecycleHandler(svc.lifecycleSvc, httpLog)
		imux.Handle("/strategies/v2/", lifecycleHandler)
		stratPerfHandler := omhttp.NewStrategyPerfHandler(svc.strategyRunner, infra.pnlRepo, httpLog)
		imux.Handle("/api/strategies/", stratPerfHandler)
	}
	// Cross-strategy recent signals endpoint (used by dashboard main page).
	imux.HandleFunc("/api/signals/recent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		limit := 50
		if raw := q.Get("limit"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v > 0 {
				if v > 200 {
					v = 200
				}
				limit = v
			}
		}
		now := time.Now().UTC()
		from := now.AddDate(0, 0, -7)
		to := now
		if raw := q.Get("from"); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				from = t
			}
		}
		if raw := q.Get("to"); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				to = t
			}
		}
		query := ports.StrategySignalQuery{
			TenantID: "default",
			EnvMode:  domain.EnvModePaper,
			Symbol:   q.Get("symbol"),
			From:     from,
			To:       to,
			Limit:    limit,
		}
		page, err := infra.pnlRepo.GetStrategySignalEvents(r.Context(), query)
		if err != nil {
			httpLog.Error().Err(err).Msg("failed to query recent signals")
			http.Error(w, `{"error":"signal query failed"}`, http.StatusInternalServerError)
			return
		}
		type recentSignalsResponse struct {
			Items      []domain.StrategySignalEvent `json:"items"`
			NextCursor string                       `json:"next_cursor,omitempty"`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recentSignalsResponse{Items: page.Items, NextCursor: page.NextCursor})
	})
	imux.HandleFunc("/strategies/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		type dnaJSON struct {
			ID          string         `json:"id"`
			Version     string         `json:"version"`
			Description string         `json:"description,omitempty"`
			Parameters  map[string]any `json:"parameters"`
		}
		all := svc.dnaManager.GetAll()
		if len(all) == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no strategy loaded"}`))
			return
		}
		dna := all[0]
		_ = json.NewEncoder(w).Encode(dnaJSON{
			ID:          dna.ID,
			Version:     dna.Version,
			Description: dna.Description,
			Parameters:  dna.Parameters,
		})
	})
	imux.HandleFunc("/strategies/dna/all", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		type dnaJSON struct {
			ID          string         `json:"id"`
			Version     string         `json:"version"`
			Description string         `json:"description,omitempty"`
			Parameters  map[string]any `json:"parameters"`
		}
		all := svc.dnaManager.GetAll()
		out := make([]dnaJSON, 0, len(all))
		for _, dna := range all {
			out = append(out, dnaJSON{
				ID:          dna.ID,
				Version:     dna.Version,
				Description: dna.Description,
				Parameters:  dna.Parameters,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	imux.HandleFunc("/orb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		type orbJSON struct {
			Symbol    string  `json:"symbol"`
			State     string  `json:"state"`
			High      float64 `json:"orb_high"`
			Low       float64 `json:"orb_low"`
			BarCount  int     `json:"range_bar_count"`
			BreakDir  string  `json:"breakout_direction,omitempty"`
			BreakRVOL float64 `json:"breakout_rvol,omitempty"`
			Signals   int     `json:"signals_fired"`
		}
		var results []orbJSON
		for _, sym := range cfg.Symbols.Symbols {
			sess := svc.monitor.GetORBSession(sym)
			if sess == nil {
				results = append(results, orbJSON{Symbol: sym, State: "NO_SESSION"})
				continue
			}
			o := orbJSON{
				Symbol:   sess.Symbol,
				State:    string(sess.State),
				High:     sess.OrbHigh,
				Low:      sess.OrbLow,
				BarCount: sess.RangeBarCount,
				Signals:  sess.SignalsFired,
			}
			if sess.Breakout.Confirmed {
				o.BreakDir = string(sess.Breakout.Direction)
				o.BreakRVOL = sess.Breakout.RVOL
			}
			results = append(results, o)
		}
		_ = json.NewEncoder(w).Encode(results)
	})

	// Performance dashboard API
	perfHandler := omhttp.NewPerformanceHandler(infra.pnlRepo, infra.repo, httpLog)
	imux.Handle("/performance/", perfHandler)
	// Historical orders API
	orderHandler := omhttp.NewOrderHandler(infra.repo, httpLog)
	imux.Handle("/orders", orderHandler)

	imux.HandleFunc("/pnl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		envMode := domain.EnvModePaper
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		// Default: last 30 days
		from := today.AddDate(0, 0, -30)
		to := today
		if qFrom := r.URL.Query().Get("from"); qFrom != "" {
			if t, err := time.Parse("2006-01-02", qFrom); err == nil {
				from = t
			}
		}
		if qTo := r.URL.Query().Get("to"); qTo != "" {
			if t, err := time.Parse("2006-01-02", qTo); err == nil {
				to = t
			}
		}
		tenantID := r.URL.Query().Get("tenant")
		if tenantID == "" {
			tenantID = "default"
		}
		pnlData, err := infra.pnlRepo.GetDailyPnL(r.Context(), tenantID, envMode, from, to)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		type pnlJSON struct {
			Date        string  `json:"date"`
			Realized    float64 `json:"realized_pnl"`
			Unrealized  float64 `json:"unrealized_pnl"`
			TradeCount  int     `json:"trade_count"`
			MaxDrawdown float64 `json:"max_drawdown"`
		}
		var results []pnlJSON
		for _, p := range pnlData {
			results = append(results, pnlJSON{
				Date:        p.Date.Format("2006-01-02"),
				Realized:    p.RealizedPnL,
				Unrealized:  p.UnrealizedPnL,
				TradeCount:  p.TradeCount,
				MaxDrawdown: p.MaxDrawdown,
			})
		}
		if results == nil {
			results = []pnlJSON{}
		}
		_ = json.NewEncoder(w).Encode(results)
	})
}
