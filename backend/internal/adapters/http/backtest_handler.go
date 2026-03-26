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
}

type backtestControlRequest struct {
	Action string `json:"action"`
	Speed  string `json:"speed"`
}

// BacktestHandler manages backtest lifecycle via HTTP endpoints.
type BacktestHandler struct {
	db         *sql.DB
	appCfg     *config.Config
	marketData ports.MarketDataPort
	log        zerolog.Logger

	mu     sync.RWMutex
	active *backtest.Runner
}

// NewBacktestHandler creates a handler for backtest HTTP endpoints.
func NewBacktestHandler(db *sql.DB, appCfg *config.Config, marketData ports.MarketDataPort, log zerolog.Logger) *BacktestHandler {
	return &BacktestHandler{
		db:         db,
		appCfg:     appCfg,
		marketData: marketData,
		log:        log.With().Str("component", "backtest_http").Logger(),
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

	if len(req.Symbols) == 0 {
		jsonError(w, "symbols required", http.StatusBadRequest)
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
	if h.active != nil && (h.active.Status() == "running" || h.active.Status() == "paused") {
		h.mu.Unlock()
		jsonError(w, "a backtest is already running — cancel it first", http.StatusConflict)
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
		UseDailyScreener: req.UseDailyScreener,
		ScreenerTopN:     req.ScreenerTopN,
	}, h.db, h.appCfg, h.marketData, h.log)

	h.active = runner
	h.mu.Unlock()

	go func() {
		if runErr := runner.Run(context.Background()); runErr != nil {
			h.log.Error().Err(runErr).Str("backtest_id", runner.ID()).Msg("backtest run failed")
		}
	}()

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

func (h *BacktestHandler) handleStrategies(w http.ResponseWriter, r *http.Request) {
	stratDir := "configs/strategies"
	entries, err := os.ReadDir(stratDir)
	if err != nil {
		jsonError(w, "failed to read strategies: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type stratInfo struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		State       string `json:"state"`
	}

	var strategies []stratInfo
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
		}
		if tomlErr := toml.Unmarshal(data, &raw); tomlErr != nil {
			continue
		}
		strategies = append(strategies, stratInfo{
			ID:          raw.Strategy.ID,
			Name:        raw.Strategy.Name,
			Description: raw.Strategy.Description,
			State:       raw.Lifecycle.State,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(strategies)
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
	if h.active != nil && h.active.ID() == id {
		return h.active
	}
	return nil
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
