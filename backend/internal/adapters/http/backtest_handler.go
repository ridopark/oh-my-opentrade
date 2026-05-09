package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	optadapter "github.com/oh-my-opentrade/backend/internal/adapters/options"
	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
	"github.com/oh-my-opentrade/backend/internal/app/tradingthetrendreplay"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type backtestRunRequest struct {
	Symbols       []string `json:"symbols"`
	From          string   `json:"from"`
	To            string   `json:"to"`
	Timeframe     string   `json:"timeframe"`
	InitialEquity float64  `json:"initial_equity"`
	SlippageBPS   int64    `json:"slippage_bps"`
	Speed         string   `json:"speed"`
	NoAI             bool     `json:"no_ai"`
	Strategies       []string `json:"strategies"`
	StrategyDir      string   `json:"strategy_dir"`
	MaxPositions     int      `json:"max_positions"`
	MaxPerGroup      int      `json:"max_per_group"`
	CompoundEquity   *bool    `json:"compound_equity"`

	// Option fill-realism knobs. OptionEntrySpreadEnabled is nullable so an
	// omitted field gets the realistic default (half-spread applied on entry);
	// pass false explicitly to reproduce legacy mid-fill backtests.
	OptionSpreadMultiplier   float64 `json:"option_spread_multiplier"`
	OptionEntrySpreadEnabled *bool   `json:"option_entry_spread_enabled"`

	// Tier 1 market-impact knobs. Both zero (default) reproduces today's
	// fill path byte-identically. Non-zero either field activates the
	// participation cap and sqrt-impact term on option fills.
	OptionImpactScaleBps      float64 `json:"option_impact_scale_bps"`
	OptionMaxParticipationPct float64 `json:"option_max_participation_pct"`

	// Copytrade replay wiring (required when "copytrade_v1" is in Strategies).
	// CopytradeLedgerDir defaults to "_workspace/copytrade_replay" when empty.
	CopytradeHistory   string `json:"copytrade_history"`
	CopytradeLedgerDir string `json:"copytrade_ledger_dir"`

	// TradingTheTrend replay wiring (auto-defaulted when "tradingthetrend_v1"
	// is in Strategies). Path to a Discord watchlist history JSONL emitted by
	// services/discord-tradingthetrend.
	TradingTheTrendHistory string `json:"tradingthetrend_history"`

	// EmitGatedDiag, when true, persists EntryGated rows to
	// strategy_signal_events with payload.tag = "backtest_<runID>" so a SQL
	// diff against live rows on (symbol, bar.Time) can attribute gate
	// divergences. Off by default. Mirrors omo-replay's --emit-gated-diag.
	EmitGatedDiag bool `json:"emit_gated_diag"`

	// PreferLiveChain enables the live Alpaca options-chain fallback inside
	// the backtest's HistoricalOptionsAdapter (DoltHub -> live -> synth ->
	// empty). Off by default. Same-day backtests only; off-day runs WARN
	// at runner start because the live snapshot reflects current quotes.
	PreferLiveChain bool `json:"prefer_live_chain"`
}

type backtestControlRequest struct {
	Action string `json:"action"`
	Speed  string `json:"speed"`
}

// BacktestHandler manages backtest lifecycle via HTTP endpoints.
// Backtests are queued and executed one at a time to avoid resource contention.
type BacktestHandler struct {
	db          *sql.DB
	appCfg      *config.Config
	marketData  ports.MarketDataPort
	log         zerolog.Logger
	historyRepo ports.BacktestHistoryPort // optional; nil disables history persistence
	liveOptions ports.OptionsMarketDataPort // optional; nil disables prefer_live_chain

	mu      sync.RWMutex
	runners map[string]*backtest.Runner

	// queue serializes backtest execution — at most one runs at a time.
	queue chan *backtestJob
}

type backtestJob struct {
	runner *backtest.Runner
	log    zerolog.Logger
}

// NewBacktestHandler creates a handler for backtest HTTP endpoints. The
// historyRepo is optional; pass nil to skip persistent history (e.g. in
// tests or when the feature is disabled). liveOptionsMarket is optional;
// pass nil to disable the prefer_live_chain request flag (a request that
// sets the flag without a configured port returns HTTP 400).
func NewBacktestHandler(db *sql.DB, appCfg *config.Config, marketData ports.MarketDataPort, historyRepo ports.BacktestHistoryPort, liveOptionsMarket ports.OptionsMarketDataPort, log zerolog.Logger) *BacktestHandler {
	h := &BacktestHandler{
		db:          db,
		appCfg:      appCfg,
		marketData:  marketData,
		log:         log.With().Str("component", "backtest_http").Logger(),
		historyRepo: historyRepo,
		liveOptions: liveOptionsMarket,
		runners:     make(map[string]*backtest.Runner),
		queue:       make(chan *backtestJob, 4), // buffer up to 4 pending backtests
	}
	// Single worker drains the queue — only one backtest runs at a time.
	go h.backtestWorker()
	return h
}

// backtestWorker processes backtest jobs sequentially.
func (h *BacktestHandler) backtestWorker() {
	for job := range h.queue {
		if runErr := job.runner.Run(context.Background()); runErr != nil {
			job.log.Error().Err(runErr).Str("backtest_id", job.runner.ID()).Msg("backtest run failed")
		}
	}
}

// ServeHTTP routes backtest requests.
//
//	POST /backtest/run         — start a new backtest
//	GET  /backtest/events/{id} — SSE stream
//	POST /backtest/control/{id} — pause/resume/speed
//	GET  /backtest/results/{id} — final results
//	DELETE /backtest/{id}       — cancel
//	GET  /backtest/status/{id}  — current status + progress
func (h *BacktestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/backtest")
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 3)

	switch {
	case parts[0] == "symbols" && r.Method == http.MethodGet:
		h.handleSymbols(w, r)
	case parts[0] == "strategies" && r.Method == http.MethodGet:
		h.handleStrategies(w, r)
	case parts[0] == "run" && r.Method == http.MethodPost:
		h.handleRun(w, r)
	case parts[0] == "history":
		h.handleHistory(w, r, parts[1:])
	case len(parts) >= 2:
		id := parts[0]
		action := parts[1]
		switch {
		case action == "events" && r.Method == http.MethodGet:
			h.handleEvents(w, r, id)
		case action == "control" && r.Method == http.MethodPost:
			h.handleControl(w, r, id)
		case action == "results" && r.Method == http.MethodGet:
			h.handleResults(w, r, id)
		case action == "status" && r.Method == http.MethodGet:
			h.handleStatus(w, r, id)
		case action == "status" && r.Method == http.MethodDelete:
			h.handleCancel(w, r, id)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	case len(parts) == 1 && parts[0] != "" && r.Method == http.MethodDelete:
		h.handleCancel(w, r, parts[0])
	default:
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

func (h *BacktestHandler) handleRun(w http.ResponseWriter, r *http.Request) {
	var req backtestRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.PreferLiveChain && h.liveOptions == nil {
		jsonError(w, http.StatusBadRequest, "prefer_live_chain requires Alpaca options data: not configured")
		return
	}

	copytradeSelected := false
	tttSelected := false
	for _, s := range req.Strategies {
		if s == "copytrade_v1" {
			copytradeSelected = true
		}
		if s == "tradingthetrend_v1" {
			tttSelected = true
		}
	}

	// Default TTT history path early so the universe-derivation step below
	// can read it before the symbols-required guard fires.
	if tttSelected {
		if req.TradingTheTrendHistory == "" {
			req.TradingTheTrendHistory = "services/discord-tradingthetrend/state/history.jsonl"
		}
		if _, statErr := os.Stat(req.TradingTheTrendHistory); statErr != nil {
			jsonError(w, http.StatusBadRequest, "tradingthetrend_history unreadable: "+statErr.Error())
			return
		}
	}

	// Drop sentinel symbols (e.g. __copytrade__) up front — they can come
	// from either the TOML-driven collectStrategySymbols path or from an
	// explicit client payload that forwarded strategy-meta.symbols verbatim.
	if len(req.Symbols) > 0 {
		filtered := req.Symbols[:0]
		for _, s := range req.Symbols {
			if strings.HasPrefix(s, "__") {
				continue
			}
			filtered = append(filtered, s)
		}
		req.Symbols = filtered
	}

	// When no symbols provided, collect union from selected strategy configs
	// so each strategy runs on its own tuned symbol list.
	useNativeSymbols := false
	if len(req.Symbols) == 0 && len(req.Strategies) > 0 {
		req.Symbols = h.collectStrategySymbols(req.Strategies)
		useNativeSymbols = true
	}
	// Copytrade strategies route everything through the sentinel, so no
	// strategy TOML has a tradeable symbol list. Fall back to the canonical
	// 23-symbol universe covered by the scraped history window.
	if copytradeSelected && len(req.Symbols) == 0 {
		req.Symbols = copytradeDefaultSymbols()
	}
	// TradingTheTrend has the same sentinel-only routing shape but its
	// universe is the union of tickers in the JSONL history (which the
	// caller doesn't necessarily know). Derive it from the history file
	// rather than baking a static default that drifts as the watchlist
	// evolves.
	if tttSelected && len(req.Symbols) == 0 {
		uni, uErr := tradingthetrendreplay.LoadUniverse(req.TradingTheTrendHistory, time.Time{}, time.Time{})
		if uErr != nil {
			jsonError(w, http.StatusBadRequest, "tradingthetrend universe load failed: "+uErr.Error())
			return
		}
		req.Symbols = uni
	}
	if len(req.Symbols) == 0 {
		if copytradeSelected && len(req.Strategies) == 1 {
			jsonError(w, http.StatusBadRequest, "copytrade_v1 has no non-sentinel symbols — pass explicit symbols in request body")
			return
		}
		if tttSelected && len(req.Strategies) == 1 {
			jsonError(w, http.StatusBadRequest, "tradingthetrend_v1 universe load returned 0 tickers — check history JSONL")
			return
		}
		jsonError(w, http.StatusBadRequest, "symbols required (provide symbols or strategies with configured symbols)")
		return
	}
	if req.From == "" {
		jsonError(w, http.StatusBadRequest, "from date required")
		return
	}

	fromTime, err := parseTimeParam(req.From)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid from: "+err.Error())
		return
	}
	toTime := time.Now().UTC()
	if req.To != "" {
		toTime, err = parseTimeParam(req.To)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "invalid to: "+err.Error())
			return
		}
	}
	if !toTime.After(fromTime) {
		jsonError(w, http.StatusBadRequest, "to must be after from")
		return
	}

	speed := req.Speed
	if speed == "" {
		speed = "max"
	}

	if copytradeSelected {
		if req.CopytradeHistory == "" {
			req.CopytradeHistory = "services/discord-copytrade/state/history_90d.jsonl"
		}
		if _, statErr := os.Stat(req.CopytradeHistory); statErr != nil {
			jsonError(w, http.StatusBadRequest, "copytrade_history unreadable: "+statErr.Error())
			return
		}
		if speed != "max" {
			jsonError(w, http.StatusBadRequest, "copytrade_v1 backtest requires speed=max (sharded pipeline only)")
			return
		}
		if req.CopytradeLedgerDir == "" {
			req.CopytradeLedgerDir = "_workspace/copytrade_replay"
		}
	}
	if tttSelected && speed != "max" {
		jsonError(w, http.StatusBadRequest, "tradingthetrend_v1 backtest requires speed=max (sharded pipeline only)")
		return
	}

	h.mu.Lock()
	// Prune finished runners to avoid unbounded growth.
	for id, r := range h.runners {
		st := r.Status()
		if st != "running" && st != "paused" {
			delete(h.runners, id)
		}
	}
	const maxQueued = 4
	if len(h.runners) >= maxQueued {
		h.mu.Unlock()
		jsonError(w, http.StatusConflict, "max queued backtests reached — cancel one first")
		return
	}

	symbols := make([]domain.Symbol, len(req.Symbols))
	for i, s := range req.Symbols {
		symbols[i] = domain.Symbol(s)
	}

	tf := domain.Timeframe("1m")
	if req.Timeframe != "" {
		tf = domain.Timeframe(req.Timeframe)
	}
	equity := req.InitialEquity
	if equity <= 0 {
		equity = 100000
	}
	slippage := req.SlippageBPS
	if slippage <= 0 {
		slippage = 10
	}

	var liveChainPort ports.OptionsMarketDataPort
	if req.PreferLiveChain {
		liveChainPort = optadapter.NewCachingMarket(h.liveOptions)
	}

	runner := backtest.NewRunner(backtest.RunConfig{
		Symbols:       symbols,
		From:          fromTime,
		To:            toTime,
		Timeframe:     tf,
		InitialEquity: equity,
		SlippageBPS:   slippage,
		Speed:         speed,
		NoAI:             req.NoAI,
		Strategies:       req.Strategies,
		StrategyDir:      req.StrategyDir,
		MaxPositions:     req.MaxPositions,
		MaxPerGroup:      req.MaxPerGroup,
		UseNativeSymbols: useNativeSymbols,
		CompoundEquity:   req.CompoundEquity == nil || *req.CompoundEquity,
		CopytradeHistory:   req.CopytradeHistory,
		CopytradeLedgerDir: req.CopytradeLedgerDir,
		TradingTheTrendHistory: req.TradingTheTrendHistory,
		ForceActiveStrategies:  forceActiveTTT(tttSelected),
		EmitGatedDiag:      req.EmitGatedDiag,
		PreferLiveChain:   req.PreferLiveChain,
		LiveOptionsMarket: liveChainPort,
	}, bootstrap.BuildBacktestInfra(bootstrap.BacktestDeps{
		DB:     h.db,
		AppCfg: h.appCfg,
		Logger: h.log,
	}, slippage, equity, req.NoAI, bootstrap.BacktestInfraOptions{
		OptionExitSpreadMultiplier: req.OptionSpreadMultiplier,
		OptionEntrySpreadEnabled:   req.OptionEntrySpreadEnabled,
		OptionImpactScaleBps:       req.OptionImpactScaleBps,
		OptionMaxParticipationPct:  req.OptionMaxParticipationPct,
		BacktestFrom:               fromTime,
		BacktestTo:                 toTime,
	}), h.appCfg, h.marketData, h.log)

	// Wire history persistence: capture meta & DNA now, so the save is
	// deterministic even if config files change mid-run. The save itself
	// runs in a goroutine with its own timeout so it can't slow the run.
	if h.historyRepo != nil {
		meta := backtestRunMeta{
			id:            runner.ID(),
			strategies:    append([]string(nil), req.Strategies...),
			symbols:       domain.SymbolsToStrings(symbols),
			periodStart:   fromTime,
			periodEnd:     toTime,
			initialEquity: equity,
			slippageBPS:   int(slippage),
			noAI:          req.NoAI,
			dnaSnapshot:   captureDNASnapshot(req.Strategies),
		}
		repo := h.historyRepo
		log := h.log
		runner.SetFinalizer(func(res *backtest.Result) {
			go saveBacktestHistory(repo, meta, res, log)
		})
	}

	h.runners[runner.ID()] = runner
	h.mu.Unlock()

	entrySpread := req.OptionEntrySpreadEnabled == nil || *req.OptionEntrySpreadEnabled
	h.log.Info().
		Str("backtest_id", runner.ID()).
		Int64("slippage_bps", slippage).
		Str("fill_model", h.appCfg.Backtest.FillModel).
		Str("fee_schedule", h.appCfg.Backtest.FeeSchedule).
		Bool("option_entry_spread", entrySpread).
		Float64("option_spread_mult", req.OptionSpreadMultiplier).
		Float64("option_impact_scale_bps", req.OptionImpactScaleBps).
		Float64("option_max_participation_pct", req.OptionMaxParticipationPct).
		Msg("backtest enqueued — realism knobs resolved")

	h.queue <- &backtestJob{runner: runner, log: h.log}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"backtest_id": runner.ID(),
		"status":      runner.Status(),
	})
}

func (h *BacktestHandler) handleEvents(w http.ResponseWriter, r *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, http.StatusNotFound, "backtest not found")
		return
	}
	runner.GetEmitter().ServeHTTP(w, r)
}

func (h *BacktestHandler) handleControl(w http.ResponseWriter, r *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, http.StatusNotFound, "backtest not found")
		return
	}

	var req backtestControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch req.Action {
	case "pause":
		runner.Pause()
	case "resume":
		runner.Resume()
	case "set_speed":
		if err := runner.SetSpeed(req.Speed); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid speed: "+err.Error())
			return
		}
	default:
		jsonError(w, http.StatusBadRequest, "unknown action: "+req.Action)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": runner.Status(),
		"speed":  req.Speed,
	})
}

func (h *BacktestHandler) handleResults(w http.ResponseWriter, _ *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, http.StatusNotFound, "backtest not found")
		return
	}

	result := runner.GetResult()
	if result == nil {
		jsonError(w, http.StatusAccepted, "backtest not yet completed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *BacktestHandler) handleStatus(w http.ResponseWriter, _ *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, http.StatusNotFound, "backtest not found")
		return
	}

	resp := map[string]any{
		"backtest_id": runner.ID(),
		"status":      runner.Status(),
	}
	if p := runner.Progress(); p != nil {
		resp["progress"] = p
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *BacktestHandler) handleCancel(w http.ResponseWriter, _ *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, http.StatusNotFound, "backtest not found")
		return
	}

	runner.Cancel()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"backtest_id": runner.ID(),
		"status":      runner.Status(),
	})
}

// stratHeader is the lightweight summary returned by loadStrategyHeaders.
type stratHeader struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Symbols     []string `json:"symbols"`
	Timeframes  []string `json:"timeframes"`
}

// loadStrategyHeaders scans configs/strategies/*.toml and returns a summary
// for each strategy found. Shared by handleStrategies and collectStrategySymbols.
func loadStrategyHeaders(stratDir string) ([]stratHeader, error) {
	entries, err := os.ReadDir(stratDir)
	if err != nil {
		return nil, err
	}

	var headers []stratHeader
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, readErr := os.ReadFile(stratDir + "/" + e.Name())
		if readErr != nil {
			continue
		}
		var raw struct {
			Strategy struct {
				ID          string `toml:"id"`
				Name        string `toml:"name"`
				Description string `toml:"description"`
			} `toml:"strategy"`
			Lifecycle struct {
				State string `toml:"state"`
			} `toml:"lifecycle"`
			Routing struct {
				Symbols    []string `toml:"symbols"`
				Timeframes []string `toml:"timeframes"`
			} `toml:"routing"`
		}
		if tomlErr := toml.Unmarshal(data, &raw); tomlErr != nil {
			continue
		}
		headers = append(headers, stratHeader{
			ID:          raw.Strategy.ID,
			Name:        raw.Strategy.Name,
			Description: raw.Strategy.Description,
			State:       raw.Lifecycle.State,
			Symbols:     raw.Routing.Symbols,
			Timeframes:  raw.Routing.Timeframes,
		})
	}
	return headers, nil
}

func (h *BacktestHandler) handleStrategies(w http.ResponseWriter, r *http.Request) {
	headers, err := loadStrategyHeaders("configs/strategies")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to read strategies: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(headers)
}

// captureDNASnapshot reads each requested strategy's TOML file and returns a
// frozen snapshot of its full config (params, routing, lifecycle, etc.) as
// a map keyed by strategy ID. Safe to store on the backtest_runs row so
// later edits to the strategy file don't retroactively change history.
func captureDNASnapshot(strategyIDs []string) map[string]any {
	snapshot := make(map[string]any, len(strategyIDs))
	const stratDir = "configs/strategies"
	entries, err := os.ReadDir(stratDir)
	if err != nil {
		return snapshot
	}
	wanted := make(map[string]bool, len(strategyIDs))
	for _, id := range strategyIDs {
		wanted[id] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, readErr := os.ReadFile(stratDir + "/" + e.Name())
		if readErr != nil {
			continue
		}
		var decoded map[string]any
		if tomlErr := toml.Unmarshal(data, &decoded); tomlErr != nil {
			continue
		}
		id, _ := strategyField(decoded, "strategy", "id").(string)
		if id == "" || !wanted[id] {
			continue
		}
		snapshot[id] = decoded
	}
	return snapshot
}

// strategyField reads nested map keys safely. Returns nil on any missing key
// or non-map intermediate.
func strategyField(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// collectStrategySymbols returns the deduplicated union of routing.symbols
// for the given strategy IDs.
func (h *BacktestHandler) collectStrategySymbols(strategyIDs []string) []string {
	headers, err := loadStrategyHeaders("configs/strategies")
	if err != nil {
		return nil
	}

	wantedIDs := make(map[string]bool, len(strategyIDs))
	for _, id := range strategyIDs {
		wantedIDs[id] = true
	}

	seen := make(map[string]bool)
	var symbols []string
	for _, h := range headers {
		if !wantedIDs[h.ID] {
			continue
		}
		for _, s := range h.Symbols {
			if strings.HasPrefix(s, "__") {
				continue
			}
			if !seen[s] {
				seen[s] = true
				symbols = append(symbols, s)
			}
		}
	}
	return symbols
}

func (h *BacktestHandler) handleSymbols(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), "SELECT DISTINCT symbol FROM market_bars ORDER BY symbol")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to query symbols: "+err.Error())
		return
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var s string
		if scanErr := rows.Scan(&s); scanErr == nil {
			symbols = append(symbols, s)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(symbols)
}

func (h *BacktestHandler) getRunner(id string) *backtest.Runner {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runners[id]
}

func parseTimeParam(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, &json.UnsupportedValueError{}
}

// forceActiveTTT returns ["tradingthetrend_v1"] when the strategy is selected
// so the backtest runner promotes its TOML state from "Deactivated" to
// PaperActive at read time. Mirrors the omo-tradingthetrend-backtest cmd's
// --force-active flag — the TOML ships Deactivated by default so live
// deployments don't accidentally trade the strategy.
func forceActiveTTT(selected bool) []string {
	if !selected {
		return nil
	}
	return []string{"tradingthetrend_v1"}
}

// copytradeDefaultSymbols returns the canonical 23-symbol universe covered
// by services/discord-copytrade/state/history_90d.jsonl. The copytrade_v1
// TOML only lists the sentinel symbol, so neither collectStrategySymbols
// nor a dashboard forwarding strategy-meta.symbols can yield a tradeable
// list — fall back here when copytrade is selected and symbols are empty.
func copytradeDefaultSymbols() []string {
	return []string{
		"AAPL", "AMZN", "BABA", "BIDU", "ENPH", "FSLR", "GLD", "GOOGL",
		"INTC", "IWM", "KWEB", "MARA", "MSFT", "NIO", "NVDA", "ORCL",
		"PDD", "QQQ", "RKLB", "SLV", "SPY", "TSLA", "TSM",
	}
}

