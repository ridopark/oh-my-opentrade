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

	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
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
	UseDailyScreener bool     `json:"use_daily_screener"`
	ScreenerTopN     int      `json:"screener_top_n"`
	StrategyDir      string   `json:"strategy_dir"`
	MaxPositions     int      `json:"max_positions"`
	MaxPerGroup      int      `json:"max_per_group"`
	CompoundEquity   *bool    `json:"compound_equity"`
}

type backtestControlRequest struct {
	Action string `json:"action"`
	Speed  string `json:"speed"`
}

// BacktestHandler manages backtest lifecycle via HTTP endpoints.
// Backtests are queued and executed one at a time to avoid resource contention.
type BacktestHandler struct {
	db         *sql.DB
	appCfg     *config.Config
	marketData ports.MarketDataPort
	log        zerolog.Logger

	mu      sync.RWMutex
	runners map[string]*backtest.Runner

	// queue serializes backtest execution — at most one runs at a time.
	queue chan *backtestJob
}

type backtestJob struct {
	runner *backtest.Runner
	log    zerolog.Logger
}

// NewBacktestHandler creates a handler for backtest HTTP endpoints.
func NewBacktestHandler(db *sql.DB, appCfg *config.Config, marketData ports.MarketDataPort, log zerolog.Logger) *BacktestHandler {
	h := &BacktestHandler{
		db:         db,
		appCfg:     appCfg,
		marketData: marketData,
		log:        log.With().Str("component", "backtest_http").Logger(),
		runners:    make(map[string]*backtest.Runner),
		queue:      make(chan *backtestJob, 4), // buffer up to 4 pending backtests
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
		jsonError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// When no symbols provided, collect union from selected strategy configs
	// so each strategy runs on its own tuned symbol list.
	useNativeSymbols := false
	if len(req.Symbols) == 0 && len(req.Strategies) > 0 {
		req.Symbols = h.collectStrategySymbols(req.Strategies)
		useNativeSymbols = true
	}
	if len(req.Symbols) == 0 {
		jsonError(w, "symbols required (provide symbols or strategies with configured symbols)", http.StatusBadRequest)
		return
	}
	if req.From == "" {
		jsonError(w, "from date required", http.StatusBadRequest)
		return
	}

	fromTime, err := parseTimeParam(req.From)
	if err != nil {
		jsonError(w, "invalid from: "+err.Error(), http.StatusBadRequest)
		return
	}
	toTime := time.Now().UTC()
	if req.To != "" {
		toTime, err = parseTimeParam(req.To)
		if err != nil {
			jsonError(w, "invalid to: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if !toTime.After(fromTime) {
		jsonError(w, "to must be after from", http.StatusBadRequest)
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
		jsonError(w, "max queued backtests reached — cancel one first", http.StatusConflict)
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
		slippage = 5
	}
	speed := req.Speed
	if speed == "" {
		speed = "5x"
	}

	// When daily screener is on, expand candidate pool to a broad universe
	// of liquid US equities for effective screening.
	backtestSymbols := symbols
	if req.UseDailyScreener {
		seen := make(map[string]bool)
		for _, s := range backtestSymbols {
			seen[string(s)] = true
		}
		// Canonical universe of liquid US equities from domain.KnownSymbols().
		screenPool := domain.KnownSymbols()
		for _, s := range screenPool {
			if !seen[s] {
				backtestSymbols = append(backtestSymbols, domain.Symbol(s))
				seen[s] = true
			}
		}
		// Also include any config symbols not already in the pool
		for _, s := range h.appCfg.Symbols.AllSymbols() {
			if !seen[s] && !strings.Contains(s, "/") {
				backtestSymbols = append(backtestSymbols, domain.Symbol(s))
				seen[s] = true
			}
		}
		h.log.Info().Int("user_symbols", len(symbols)).Int("expanded_pool", len(backtestSymbols)).Msg("daily screener: expanded candidate pool")
	}

	runner := backtest.NewRunner(backtest.RunConfig{
		Symbols:       backtestSymbols,
		From:          fromTime,
		To:            toTime,
		Timeframe:     tf,
		InitialEquity: equity,
		SlippageBPS:   slippage,
		Speed:         speed,
		NoAI:             req.NoAI,
		Strategies:       req.Strategies,
		StrategyDir:      req.StrategyDir,
		UseDailyScreener: req.UseDailyScreener,
		ScreenerTopN:     req.ScreenerTopN,
		FixedSymbols:     symbols, // user's original picks — always active
		MaxPositions:     req.MaxPositions,
		MaxPerGroup:      req.MaxPerGroup,
		UseNativeSymbols: useNativeSymbols,
		CompoundEquity:   req.CompoundEquity == nil || *req.CompoundEquity,
	}, bootstrap.BuildBacktestInfra(bootstrap.BacktestDeps{
		DB:     h.db,
		AppCfg: h.appCfg,
		Logger: h.log,
	}, slippage, equity, req.NoAI), h.appCfg, h.marketData, h.log)

	h.runners[runner.ID()] = runner
	h.mu.Unlock()

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
		jsonError(w, "backtest not found", http.StatusNotFound)
		return
	}
	runner.GetEmitter().ServeHTTP(w, r)
}

func (h *BacktestHandler) handleControl(w http.ResponseWriter, r *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, "backtest not found", http.StatusNotFound)
		return
	}

	var req backtestControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "pause":
		runner.Pause()
	case "resume":
		runner.Resume()
	case "set_speed":
		if err := runner.SetSpeed(req.Speed); err != nil {
			jsonError(w, "invalid speed: "+err.Error(), http.StatusBadRequest)
			return
		}
	default:
		jsonError(w, "unknown action: "+req.Action, http.StatusBadRequest)
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
		jsonError(w, "backtest not found", http.StatusNotFound)
		return
	}

	result := runner.GetResult()
	if result == nil {
		jsonError(w, "backtest not yet completed", http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *BacktestHandler) handleStatus(w http.ResponseWriter, _ *http.Request, id string) {
	runner := h.getRunner(id)
	if runner == nil {
		jsonError(w, "backtest not found", http.StatusNotFound)
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
		jsonError(w, "backtest not found", http.StatusNotFound)
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
		jsonError(w, "failed to read strategies: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(headers)
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
		jsonError(w, "failed to query symbols: "+err.Error(), http.StatusInternalServerError)
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

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
