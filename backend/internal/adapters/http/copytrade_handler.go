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

// CopytradeHandler serves POST /internal/copytrade/signal. Accepts signals from
// the discord-copytrade sidecar, authenticates by shared secret, de-duplicates
// by signal_id, rejects stale posts, and republishes the parsed payload on the
// event bus as EventCopytradeSignalReceived for the copytrade strategy to act on.
type CopytradeHandler struct {
	bus          ports.EventBusPort
	secret       []byte
	freshnessTTL time.Duration
	log          zerolog.Logger

	now func() time.Time

	mu     sync.Mutex
	seen   map[string]time.Time
	seenCap int
}

// defaultCopytradeDedupeCap sizes the dedupe map. At ~100 signals/day and a
// 120s freshness_ttl, active-window entries stay under 20; the cap exists
// only to bound growth from a runaway sidecar burst or an attacker POSTing
// unique signal_ids. Purge scans under mutex, so we keep the cap small.
const defaultCopytradeDedupeCap = 4096

// NewCopytradeHandler constructs a CopytradeHandler. freshnessTTL is the
// maximum age of PostedAt; signals older than this are rejected so sidecar
// catch-ups after a restart don't execute on stale posts.
func NewCopytradeHandler(bus ports.EventBusPort, secret string, freshnessTTL time.Duration, log zerolog.Logger) *CopytradeHandler {
	return &CopytradeHandler{
		bus:          bus,
		secret:       []byte(secret),
		freshnessTTL: freshnessTTL,
		log:          log,
		now:          time.Now,
		seen:         make(map[string]time.Time),
		seenCap:      defaultCopytradeDedupeCap,
	}
}

type copytradeSignalRequest struct {
	SignalID  string  `json:"signal_id"`
	MessageID string  `json:"message_id"`
	Author    string  `json:"author"`
	PostedAt  string  `json:"posted_at"`
	Action    string  `json:"action"`
	Ticker    string  `json:"ticker"`
	Expiry    string  `json:"expiry"`
	Strike    float64 `json:"strike"`
	Right     string  `json:"right"`
	Price     float64 `json:"price"`
	Tail      string  `json:"tail"`
	RawLine   string  `json:"raw_line"`
}

type copytradeSignalResponse struct {
	OK      bool   `json:"ok"`
	Deduped bool   `json:"deduped,omitempty"`
	Message string `json:"message,omitempty"`
}

func (h *CopytradeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	got := []byte(r.Header.Get("X-Copytrade-Secret"))
	if len(h.secret) == 0 || subtle.ConstantTimeCompare(got, h.secret) != 1 {
		jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req copytradeSignalRequest
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
			Msg("copytrade signal rejected: stale")
		jsonError(w, http.StatusGone, "signal is stale")
		return
	}

	if h.seenAndRecord(payload.SignalID, now) {
		h.log.Info().Str("signal_id", payload.SignalID).Msg("copytrade signal deduped")
		h.writeJSON(w, http.StatusOK, copytradeSignalResponse{OK: true, Deduped: true})
		return
	}

	evt, err := domain.NewEvent(
		domain.EventCopytradeSignalReceived,
		"system",
		domain.EnvModePaper,
		payload.SignalID,
		payload,
	)
	if err != nil {
		h.log.Error().Err(err).Str("signal_id", payload.SignalID).Msg("copytrade: NewEvent failed")
		jsonError(w, http.StatusInternalServerError, "event construction failed")
		return
	}
	if err := h.bus.Publish(r.Context(), *evt); err != nil {
		h.log.Error().Err(err).Str("signal_id", payload.SignalID).Msg("copytrade: Publish failed")
		jsonError(w, http.StatusInternalServerError, "publish failed")
		return
	}

	h.log.Info().
		Str("signal_id", payload.SignalID).
		Str("author", payload.Author).
		Str("action", string(payload.Action)).
		Str("ticker", string(payload.Ticker)).
		Str("expiry", payload.Expiry.Format("2006-01-02")).
		Float64("strike", payload.Strike).
		Str("right", string(payload.Right)).
		Msg("copytrade signal accepted")

	h.writeJSON(w, http.StatusAccepted, copytradeSignalResponse{OK: true})
}

// buildPayload validates req and returns the domain payload. On failure,
// returns a non-zero HTTP status and user-safe error string.
func (h *CopytradeHandler) buildPayload(req copytradeSignalRequest) (domain.CopytradeSignalPayload, int, string) {
	var zero domain.CopytradeSignalPayload

	if strings.TrimSpace(req.SignalID) == "" {
		return zero, http.StatusBadRequest, "signal_id is required"
	}
	if strings.TrimSpace(req.Author) == "" {
		return zero, http.StatusBadRequest, "author is required"
	}
	if strings.TrimSpace(req.Ticker) == "" {
		return zero, http.StatusBadRequest, "ticker is required"
	}

	action, err := parseCopytradeAction(req.Action)
	if err != nil {
		return zero, http.StatusBadRequest, err.Error()
	}
	right, err := parseCopytradeRight(req.Right)
	if err != nil {
		return zero, http.StatusBadRequest, err.Error()
	}

	postedAt, err := time.Parse(time.RFC3339, req.PostedAt)
	if err != nil {
		return zero, http.StatusBadRequest, fmt.Sprintf("posted_at must be RFC3339: %s", err)
	}
	expiry, err := time.Parse("2006-01-02", req.Expiry)
	if err != nil {
		return zero, http.StatusBadRequest, fmt.Sprintf("expiry must be YYYY-MM-DD: %s", err)
	}
	if req.Strike <= 0 {
		return zero, http.StatusBadRequest, "strike must be positive"
	}
	if req.Price <= 0 {
		return zero, http.StatusBadRequest, "price must be positive"
	}

	return domain.CopytradeSignalPayload{
		SignalID:  req.SignalID,
		MessageID: req.MessageID,
		Author:    req.Author,
		PostedAt:  postedAt.UTC(),
		Action:    action,
		Ticker:    domain.Symbol(strings.ToUpper(strings.TrimSpace(req.Ticker))),
		Expiry:    expiry.UTC(),
		Strike:    req.Strike,
		Right:     right,
		Price:     req.Price,
		Tail:      req.Tail,
		RawLine:   req.RawLine,
	}, 0, ""
}

func parseCopytradeAction(s string) (domain.CopytradeAction, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "BTO":
		return domain.CopytradeActionBTO, nil
	case "STC":
		return domain.CopytradeActionSTC, nil
	case "AVG":
		return domain.CopytradeActionAVG, nil
	default:
		return "", fmt.Errorf("action must be BTO, STC, or AVG (got %q)", s)
	}
}

func parseCopytradeRight(s string) (domain.OptionRight, error) {
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
func (h *CopytradeHandler) seenAndRecord(signalID string, now time.Time) bool {
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
			// Still full after TTL purge: drop an arbitrary entry so we
			// don't grow unbounded under a burst of fresh unique IDs.
			for k := range h.seen {
				delete(h.seen, k)
				break
			}
		}
	}
	h.seen[signalID] = now
	return false
}

func (h *CopytradeHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
