package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	appsweep "github.com/oh-my-opentrade/backend/internal/app/sweep"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domsweep "github.com/oh-my-opentrade/backend/internal/domain/sweep"
	"github.com/rs/zerolog"
)

type SweepHandler struct {
	orchestrator *appsweep.Orchestrator
	log          zerolog.Logger
}

func NewSweepHandler(orchestrator *appsweep.Orchestrator, log zerolog.Logger) *SweepHandler {
	return &SweepHandler{
		orchestrator: orchestrator,
		log:          log.With().Str("component", "sweep_http").Logger(),
	}
}

type sweepStartRequest struct {
	Ranges         []domsweep.ParamRange `json:"ranges"`
	TargetMetric   string                `json:"target_metric"`
	Symbols        []string              `json:"symbols"`
	Strategies     []string              `json:"strategies"`
	From           string                `json:"from"`
	To             string                `json:"to"`
	Timeframe      string                `json:"timeframe"`
	InitialEquity  float64               `json:"initial_equity"`
	SlippageBPS    int64                 `json:"slippage_bps"`
	NoAI           bool                  `json:"no_ai"`
	MaxConcurrency int                   `json:"max_concurrency"`
}

func (h *SweepHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/strategies/sweep/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.SplitN(path, "/", 4)

	if len(parts) < 2 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}

	strategyID := parts[0]
	action := parts[1]

	switch {
	case action == "start" && r.Method == http.MethodPost:
		h.handleStart(w, r, strategyID)
	case action == "events" && len(parts) >= 3 && r.Method == http.MethodGet:
		h.handleEvents(w, r, parts[2])
	case action == "results" && len(parts) >= 3 && r.Method == http.MethodGet:
		h.handleResults(w, parts[2])
	case action == "cancel" && len(parts) >= 3 && r.Method == http.MethodDelete:
		h.handleCancel(w, parts[2])
	case action == "apply" && len(parts) >= 4 && r.Method == http.MethodPost:
		h.handleApply(w, r, parts[2], parts[3])
	default:
		jsonError(w, "not found", http.StatusNotFound)
	}
}

func (h *SweepHandler) handleStart(w http.ResponseWriter, r *http.Request, strategyID string) {
	var req sweepStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Ranges) == 0 {
		jsonError(w, "at least one parameter range required", http.StatusBadRequest)
		return
	}
	for _, rng := range req.Ranges {
		if rng.Step <= 0 {
			jsonError(w, fmt.Sprintf("range %q: step must be > 0", rng.Key), http.StatusBadRequest)
			return
		}
	}

	symbols := make([]domain.Symbol, len(req.Symbols))
	for i, s := range req.Symbols {
		symbols[i] = domain.Symbol(s)
	}

	fromTime, err := parseTimeParam(req.From)
	if err != nil {
		jsonError(w, "invalid from date", http.StatusBadRequest)
		return
	}
	toTime, err := parseTimeParam(req.To)
	if err != nil {
		jsonError(w, "invalid to date", http.StatusBadRequest)
		return
	}

	strategies := req.Strategies
	if len(strategies) == 0 {
		strategies = []string{strategyID}
	}

	cfg := domsweep.SweepConfig{
		StrategyID:   strategyID,
		Ranges:       req.Ranges,
		TargetMetric: req.TargetMetric,
		BacktestConfig: backtest.RunConfig{
			Symbols:       symbols,
			From:          fromTime,
			To:            toTime,
			Timeframe:     domain.Timeframe(req.Timeframe),
			InitialEquity: req.InitialEquity,
			SlippageBPS:   req.SlippageBPS,
			Speed:         "max",
			NoAI:          req.NoAI,
			Strategies:    strategies,
		},
		MaxConcurrency: req.MaxConcurrency,
	}

	sweepID, err := h.orchestrator.Start(r.Context(), cfg)
	if err != nil {
		jsonError(w, "failed to start sweep: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sweep_id":   sweepID,
		"total_runs": domsweep.TotalRuns(req.Ranges),
	})
}

func (h *SweepHandler) handleEvents(w http.ResponseWriter, r *http.Request, sweepID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, err := h.orchestrator.Events(r.Context(), sweepID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}

func (h *SweepHandler) handleResults(w http.ResponseWriter, sweepID string) {
	result, err := h.orchestrator.GetResult(sweepID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	if result == nil {
		jsonError(w, "sweep not completed yet", http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *SweepHandler) handleCancel(w http.ResponseWriter, sweepID string) {
	if err := h.orchestrator.Cancel(sweepID); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

func (h *SweepHandler) handleApply(w http.ResponseWriter, r *http.Request, sweepID string, runIndexStr string) {
	runIndex, err := strconv.Atoi(runIndexStr)
	if err != nil {
		jsonError(w, "invalid run index", http.StatusBadRequest)
		return
	}

	if err := h.orchestrator.ApplyBest(r.Context(), sweepID, runIndex); err != nil {
		jsonError(w, "apply failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "applied"})
}

func init() {
	_ = time.Now
}
