package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// intentValidator is the consumer-side interface for dry-run gate
// validation. The handler stays free of the execution package import by
// flattening the *GateError return shape into (gate, reason, blocked).
// The composition root in cmd/omo-core wraps execution.Service.ValidateIntent
// behind a 3-line shim that maps nil → ("", "", false) and a non-nil
// *GateError → (e.Gate, e.Reason, true).
type intentValidator interface {
	ValidateIntent(ctx context.Context, intent domain.OrderIntent) (gate string, reason string, blocked bool)
}

// proposalSnapshotReader reads the four advisory snapshot fields echoed
// back to the caller. Equity is exposed separately because DailyLossUsedUSD
// needs it as an input.
type proposalSnapshotReader interface {
	KillSwitchState() string
	Equity(ctx context.Context) float64
	DailyLossUsedUSD(ctx context.Context, tenantID string, envMode domain.EnvMode, equity float64) float64
	OpenPositions(ctx context.Context) int
	InflightIntents() int
}

// ProposalHandler serves POST /api/proposals/order — a dry-run validator
// that returns an advisory would_pass + gate name + 30s TTL without
// enqueuing or mutating any state. HALTED short-circuits at this layer
// before the validator runs, regardless of direction (exit_* included).
type ProposalHandler struct {
	v   intentValidator
	s   proposalSnapshotReader
	log zerolog.Logger
}

func NewProposalHandler(v intentValidator, s proposalSnapshotReader, log zerolog.Logger) *ProposalHandler {
	return &ProposalHandler{v: v, s: s, log: log}
}

const proposalTTL = 30 * time.Second

type proposalRequest struct {
	Symbol         string  `json:"symbol"`
	Direction      string  `json:"direction"`
	LimitPrice     float64 `json:"limit_price"`
	StopLoss       float64 `json:"stop_loss"`
	Quantity       float64 `json:"quantity"`
	MaxSlippageBPS int     `json:"max_slippage_bps,omitempty"`
	Rationale      string  `json:"rationale,omitempty"`
}

type proposalResponse struct {
	WouldPass   bool             `json:"would_pass"`
	Gate        string           `json:"gate,omitempty"`
	Reason      string           `json:"reason,omitempty"`
	EvaluatedAt string           `json:"evaluated_at"`
	ValidUntil  string           `json:"valid_until"`
	RequestID   string           `json:"request_id"`
	Snapshot    proposalSnapshot `json:"snapshot"`
}

type proposalSnapshot struct {
	Equity           float64 `json:"equity"`
	DailyLossUsedUSD float64 `json:"daily_loss_used_usd"`
	OpenPositions    int     `json:"open_positions"`
	InflightIntents  int     `json:"inflight_intents"`
}

func (h *ProposalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rid := r.Header.Get("X-Request-ID")
	if rid == "" {
		rid = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", rid)

	var req proposalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dir, err := parseProposalDirection(req.Direction)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Symbol) == "" {
		jsonError(w, http.StatusBadRequest, "symbol must not be empty")
		return
	}
	if req.LimitPrice <= 0 {
		jsonError(w, http.StatusBadRequest, "limit_price must be > 0")
		return
	}
	if req.StopLoss <= 0 {
		jsonError(w, http.StatusBadRequest, "stop_loss must be > 0")
		return
	}
	if req.Quantity <= 0 {
		jsonError(w, http.StatusBadRequest, "quantity must be > 0")
		return
	}

	ctx := r.Context()

	evaluatedAt := time.Now().UTC()
	resp := proposalResponse{
		EvaluatedAt: evaluatedAt.Format(time.RFC3339),
		ValidUntil:  evaluatedAt.Add(proposalTTL).Format(time.RFC3339),
		RequestID:   rid,
		Snapshot:    h.snapshot(ctx),
	}

	if h.s.KillSwitchState() == "HALTED" {
		resp.WouldPass = false
		resp.Gate = "kill_switch"
		resp.Reason = "halted"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	intent, err := buildIntent(req, dir, rid)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	gate, reason, blocked := h.v.ValidateIntent(ctx, intent)
	if blocked {
		resp.WouldPass = false
		resp.Gate = gate
		resp.Reason = reason
	} else {
		resp.WouldPass = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *ProposalHandler) snapshot(ctx context.Context) proposalSnapshot {
	equity := h.s.Equity(ctx)
	return proposalSnapshot{
		Equity:           equity,
		DailyLossUsedUSD: h.s.DailyLossUsedUSD(ctx, "default", domain.EnvModePaper, equity),
		OpenPositions:    h.s.OpenPositions(ctx),
		InflightIntents:  h.s.InflightIntents(),
	}
}

func parseProposalDirection(s string) (domain.Direction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "long":
		return domain.DirectionLong, nil
	case "short":
		return domain.DirectionShort, nil
	case "exit_long":
		return domain.DirectionCloseLong, nil
	case "exit_short":
		return domain.DirectionCloseShort, nil
	default:
		return "", fmt.Errorf("direction must be one of long|short|exit_long|exit_short")
	}
}

func buildIntent(req proposalRequest, dir domain.Direction, rid string) (domain.OrderIntent, error) {
	sym, err := domain.NewSymbol(strings.TrimSpace(req.Symbol))
	if err != nil {
		return domain.OrderIntent{}, err
	}
	slip := req.MaxSlippageBPS
	if slip == 0 {
		slip = 10
	}
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "default",
		EnvMode:        domain.EnvModePaper,
		Symbol:         sym,
		Direction:      dir,
		LimitPrice:     req.LimitPrice,
		StopLoss:       req.StopLoss,
		MaxSlippageBPS: slip,
		Quantity:       req.Quantity,
		Strategy:       "claude_proposal",
		Rationale:      req.Rationale,
		IdempotencyKey: rid,
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
