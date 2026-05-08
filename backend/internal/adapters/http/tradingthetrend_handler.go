package http

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// TradingTheTrendHandler serves POST /internal/tradingthetrend/signal. Accepts
// signals from the discord-tradingthetrend sidecar, authenticates by shared
// secret, de-duplicates by signal_id, rejects stale posts, and republishes the
// parsed payload on the event bus as EventTradingTheTrendSignalReceived for
// the tradingthetrend_v1 strategy to act on.
//
// Structure mirrors CopytradeHandler intentionally; if a third Discord-following
// sidecar appears, factor the auth/dedupe/freshness/publish core into a shared
// helper. With only two services the duplication is cheaper than the
// generalization.
type TradingTheTrendHandler struct {
	bus          ports.EventBusPort
	secret       []byte
	freshnessTTL time.Duration
	log          zerolog.Logger

	now func() time.Time

	mu      sync.Mutex
	seen    map[string]time.Time
	seenCap int
}

const defaultTradingTheTrendDedupeCap = 4096

// NewTradingTheTrendHandler constructs a TradingTheTrendHandler. freshnessTTL
// is the maximum age of PostedAt; signals older than this are rejected so
// sidecar catch-ups after a restart don't execute on stale posts.
func NewTradingTheTrendHandler(bus ports.EventBusPort, secret string, freshnessTTL time.Duration, log zerolog.Logger) *TradingTheTrendHandler {
	return &TradingTheTrendHandler{
		bus:          bus,
		secret:       []byte(secret),
		freshnessTTL: freshnessTTL,
		log:          log,
		now:          time.Now,
		seen:         make(map[string]time.Time),
		seenCap:      defaultTradingTheTrendDedupeCap,
	}
}

type tradingTheTrendSignalRequest struct {
	SignalID  string  `json:"signal_id"`
	MessageID string  `json:"message_id"`
	Author    string  `json:"author"`
	PostedAt  string  `json:"posted_at"`
	Ticker    string  `json:"ticker"`
	Strike    float64 `json:"strike"`
	Right     string  `json:"right"`
	Trigger   float64 `json:"trigger"`
	RawLine   string  `json:"raw_line"`
}

type tradingTheTrendSignalResponse struct {
	OK      bool   `json:"ok"`
	Deduped bool   `json:"deduped,omitempty"`
	Message string `json:"message,omitempty"`
}

func (h *TradingTheTrendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	got := []byte(r.Header.Get("X-TradingTheTrend-Secret"))
	if len(h.secret) == 0 || subtle.ConstantTimeCompare(got, h.secret) != 1 {
		jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req tradingTheTrendSignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %s", err))
		return
	}

	payload, httpStatus, errMsg := h.buildPayload(req)
	if httpStatus != 0 {
		jsonError(w, httpStatus, errMsg)
		return
	}

	now := h.now()
	if age := now.Sub(payload.PostedAt); age > h.freshnessTTL {
		h.log.Warn().
			Str("signal_id", payload.SignalID).
			Str("author", payload.Author).
			Dur("age", age).
			Dur("ttl", h.freshnessTTL).
			Msg("tradingthetrend signal rejected: stale")
		jsonError(w, http.StatusGone, "signal is stale")
		return
	}

	if h.seenAndRecord(payload.SignalID, now) {
		h.log.Info().Str("signal_id", payload.SignalID).Msg("tradingthetrend signal deduped")
		writeJSON(w, http.StatusOK, tradingTheTrendSignalResponse{OK: true, Deduped: true})
		return
	}

	evt, err := domain.NewEvent(
		domain.EventTradingTheTrendSignalReceived,
		"system",
		domain.EnvModePaper,
		payload.SignalID,
		payload,
	)
	if err != nil {
		h.log.Error().Err(err).Str("signal_id", payload.SignalID).Msg("tradingthetrend: NewEvent failed")
		jsonError(w, http.StatusInternalServerError, "event construction failed")
		return
	}
	if err := h.bus.Publish(r.Context(), *evt); err != nil {
		h.log.Error().Err(err).Str("signal_id", payload.SignalID).Msg("tradingthetrend: Publish failed")
		jsonError(w, http.StatusInternalServerError, "publish failed")
		return
	}

	h.log.Info().
		Str("signal_id", payload.SignalID).
		Str("author", payload.Author).
		Str("ticker", string(payload.Ticker)).
		Float64("strike", payload.Strike).
		Str("right", string(payload.Right)).
		Float64("trigger", payload.Trigger).
		Msg("tradingthetrend signal accepted")

	writeJSON(w, http.StatusAccepted, tradingTheTrendSignalResponse{OK: true})
}

// buildPayload validates req and returns the domain payload. On failure,
// returns a non-zero HTTP status and user-safe error string.
func (h *TradingTheTrendHandler) buildPayload(req tradingTheTrendSignalRequest) (domain.TradingTheTrendSignalPayload, int, string) {
	var zero domain.TradingTheTrendSignalPayload

	if strings.TrimSpace(req.SignalID) == "" {
		return zero, http.StatusBadRequest, "signal_id is required"
	}
	if strings.TrimSpace(req.Author) == "" {
		return zero, http.StatusBadRequest, "author is required"
	}
	if strings.TrimSpace(req.Ticker) == "" {
		return zero, http.StatusBadRequest, "ticker is required"
	}

	right, err := parseTradingTheTrendRight(req.Right)
	if err != nil {
		return zero, http.StatusBadRequest, err.Error()
	}

	postedAt, err := time.Parse(time.RFC3339, req.PostedAt)
	if err != nil {
		return zero, http.StatusBadRequest, fmt.Sprintf("posted_at must be RFC3339: %s", err)
	}
	if req.Strike <= 0 {
		return zero, http.StatusBadRequest, "strike must be positive"
	}
	if req.Trigger <= 0 {
		return zero, http.StatusBadRequest, "trigger must be positive"
	}

	return domain.TradingTheTrendSignalPayload{
		SignalID:  req.SignalID,
		MessageID: req.MessageID,
		Author:    req.Author,
		PostedAt:  postedAt.UTC(),
		Ticker:    domain.Symbol(strings.ToUpper(strings.TrimSpace(req.Ticker))),
		Strike:    req.Strike,
		Right:     right,
		Trigger:   req.Trigger,
		RawLine:   req.RawLine,
	}, 0, ""
}

func parseTradingTheTrendRight(s string) (domain.OptionRight, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "C", "CALL":
		return domain.OptionRightCall, nil
	case "P", "PUT":
		return domain.OptionRightPut, nil
	default:
		return "", fmt.Errorf("right must be C or P (got %q)", s)
	}
}

// seenAndRecord returns true if signal_id was already recorded within the
// freshness window; otherwise records it and returns false. Expired entries
// are opportunistically purged, and the map is bounded at seenCap.
func (h *TradingTheTrendHandler) seenAndRecord(signalID string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if at, ok := h.seen[signalID]; ok && now.Sub(at) <= h.freshnessTTL {
		return true
	}

	if len(h.seen) >= h.seenCap {
		cutoff := now.Add(-h.freshnessTTL)
		for k, at := range h.seen {
			if at.Before(cutoff) {
				delete(h.seen, k)
			}
		}
		if len(h.seen) >= h.seenCap {
			for k := range h.seen {
				delete(h.seen, k)
				break
			}
		}
	}
	h.seen[signalID] = now
	return false
}

