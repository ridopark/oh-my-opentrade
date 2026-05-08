package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// budgetReader is the consumer-side interface for GET /api/risk/budget.
// Mirrors the KillSwitchController pattern (kill_switch_handler.go:17): the
// handler stays unaware of the concrete services so tests can stub a single
// type. The composition root (cmd/omo-core/http.go) adapts the real
// DailyLossBreaker / RiskEngine / position monitor / inflight tracker /
// account port behind this interface in Phase 5.
type budgetReader interface {
	KillSwitchState() string
	DailyLossUsedUSD(ctx context.Context, tenantID string, envMode domain.EnvMode, equity float64) float64
	MaxLossUSD() float64
	MaxLossPct() float64
	MaxRiskPctPerIntent() float64
	OpenPositionsCount(ctx context.Context) (count int, cap int)
	InflightIntents() int
	AccountEquity(ctx context.Context) float64
}

// RiskBudgetHandler serves GET /api/risk/budget — a 9-field read-only
// snapshot used by /loop-style supervisors before proposing trades.
// HALTED state does not block the read; visibility stays available.
type RiskBudgetHandler struct {
	r   budgetReader
	log zerolog.Logger
}

func NewRiskBudgetHandler(r budgetReader, log zerolog.Logger) *RiskBudgetHandler {
	return &RiskBudgetHandler{r: r, log: log}
}

type budgetResponse struct {
	KillSwitchState     string  `json:"kill_switch_state"`
	AccountEquity       float64 `json:"account_equity"`
	DailyLossUsedUSD    float64 `json:"daily_loss_used_usd"`
	MaxLossUSD          float64 `json:"max_loss_usd"`
	MaxLossPct          float64 `json:"max_loss_pct"`
	MaxRiskPctPerIntent float64 `json:"max_risk_pct_per_intent"`
	OpenPositionsCount  int     `json:"open_positions_count"`
	OpenPositionsCap    int     `json:"open_positions_cap"`
	InflightIntents     int     `json:"inflight_intents"`
}

func (h *RiskBudgetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rid := r.Header.Get("X-Request-ID")
	if rid == "" {
		rid = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", rid)

	ctx := r.Context()
	equity := h.r.AccountEquity(ctx)
	openCount, openCap := h.r.OpenPositionsCount(ctx)
	resp := budgetResponse{
		KillSwitchState:     h.r.KillSwitchState(),
		AccountEquity:       equity,
		DailyLossUsedUSD:    h.r.DailyLossUsedUSD(ctx, "default", domain.EnvModePaper, equity),
		MaxLossUSD:          h.r.MaxLossUSD(),
		MaxLossPct:          h.r.MaxLossPct(),
		MaxRiskPctPerIntent: h.r.MaxRiskPctPerIntent(),
		OpenPositionsCount:  openCount,
		OpenPositionsCap:    openCap,
		InflightIntents:     h.r.InflightIntents(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
