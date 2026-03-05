package http

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// OrderHandler serves the historical orders API.
//
//	GET /orders?range=30d&symbol=AAPL&side=BUY&strategy=debate&limit=50&cursor=...
type OrderHandler struct {
	repo ports.RepositoryPort
	log  zerolog.Logger
}

// NewOrderHandler creates a new OrderHandler.
func NewOrderHandler(repo ports.RepositoryPort, log zerolog.Logger) *OrderHandler {
	return &OrderHandler{repo: repo, log: log}
}

func (h *OrderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.serveOrders(w, r)
}

// --- JSON response types ---

type orderJSON struct {
	Time          string          `json:"time"`
	IntentID      string          `json:"intent_id"`
	BrokerOrderID string          `json:"broker_order_id"`
	Symbol        string          `json:"symbol"`
	Side          string          `json:"side"`
	Quantity      float64         `json:"quantity"`
	LimitPrice    float64         `json:"limit_price"`
	StopLoss      float64         `json:"stop_loss"`
	Status        string          `json:"status"`
	Strategy      string          `json:"strategy"`
	Rationale     string          `json:"rationale"`
	Confidence    float64         `json:"confidence"`
	FilledAt      *string         `json:"filled_at,omitempty"`
	FilledPrice   float64         `json:"filled_price,omitempty"`
	FilledQty     float64         `json:"filled_qty,omitempty"`
	ThoughtLog    *thoughtLogJSON `json:"thought_log,omitempty"`
}

type thoughtLogJSON struct {
	BullArgument   string `json:"bull_argument"`
	BearArgument   string `json:"bear_argument"`
	JudgeReasoning string `json:"judge_reasoning"`
}

type ordersResponse struct {
	Items      []orderJSON `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func (h *OrderHandler) serveOrders(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	ctx := r.Context()
	q := r.URL.Query()

	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}

	query := ports.OrderQuery{
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		From:     from,
		To:       to,
		Symbol:   q.Get("symbol"),
		Side:     strings.ToUpper(q.Get("side")),
		Strategy: q.Get("strategy"),
		Limit:    limit,
	}

	// Decode cursor
	if cursor := q.Get("cursor"); cursor != "" {
		raw, err := base64.URLEncoding.DecodeString(cursor)
		if err == nil {
			parts := strings.SplitN(string(raw), "|", 2)
			if len(parts) == 2 {
				if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
					query.CursorTime = &t
					query.CursorID = parts[1]
				}
			}
		}
	}

	page, err := h.repo.ListOrders(ctx, query)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list orders")
		http.Error(w, `{"error":"orders query failed"}`, http.StatusInternalServerError)
		return
	}

	// Build a set of intent IDs for batch thought log lookup
	intentIDs := make([]string, 0, len(page.Items))
	for _, o := range page.Items {
		intentIDs = append(intentIDs, o.IntentID.String())
	}

	// Fetch thought logs for all orders in the page
	thoughtLogs := make(map[string]domain.ThoughtLog)
	for _, id := range intentIDs {
		logs, err := h.repo.GetThoughtLogsByIntentID(ctx, id)
		if err != nil {
			h.log.Warn().Err(err).Str("intent_id", id).Msg("failed to fetch thought log")
			continue
		}
		if len(logs) > 0 {
			thoughtLogs[id] = logs[0] // take most recent
		}
	}

	items := make([]orderJSON, 0, len(page.Items))
	for _, o := range page.Items {
		oj := orderJSON{
			Time:          o.Time.UTC().Format(time.RFC3339),
			IntentID:      o.IntentID.String(),
			BrokerOrderID: o.BrokerOrderID,
			Symbol:        string(o.Symbol),
			Side:          o.Side,
			Quantity:      o.Quantity,
			LimitPrice:    o.LimitPrice,
			StopLoss:      o.StopLoss,
			Status:        o.Status,
			Strategy:      o.Strategy,
			Rationale:     o.Rationale,
			Confidence:    o.Confidence,
			FilledPrice:   o.FilledPrice,
			FilledQty:     o.FilledQty,
		}
		if o.FilledAt != nil {
			ft := o.FilledAt.UTC().Format(time.RFC3339)
			oj.FilledAt = &ft
		}
		if tl, ok := thoughtLogs[o.IntentID.String()]; ok {
			oj.ThoughtLog = &thoughtLogJSON{
				BullArgument:   tl.BullArgument,
				BearArgument:   tl.BearArgument,
				JudgeReasoning: tl.JudgeReasoning,
			}
		}
		items = append(items, oj)
	}

	resp := ordersResponse{
		Items:      items,
		NextCursor: page.NextCursor,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error().Err(err).Msg("failed to encode orders response")
	}
}
