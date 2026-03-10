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

// PerformanceHandler serves the performance dashboard API.
//
//	GET /performance/dashboard?range=30d
//	GET /performance/trades?range=30d&symbol=AAPL&side=BUY&limit=50&cursor=...
type PerformanceHandler struct {
	pnlRepo ports.PnLPort
	repo    ports.RepositoryPort
	log     zerolog.Logger
}

// NewPerformanceHandler creates a new PerformanceHandler.
func NewPerformanceHandler(pnlRepo ports.PnLPort, repo ports.RepositoryPort, log zerolog.Logger) *PerformanceHandler {
	return &PerformanceHandler{pnlRepo: pnlRepo, repo: repo, log: log}
}

func (h *PerformanceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/dashboard"):
		h.serveDashboard(w, r)
	case strings.HasSuffix(path, "/trades"):
		h.serveTrades(w, r)
	case strings.HasSuffix(path, "/strategies"):
		h.serveStrategies(w, r)
	case strings.HasSuffix(path, "/symbols"):
		h.serveSymbols(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ---------- Dashboard ----------

type dashboardResponse struct {
	Range    rangeInfo                 `json:"range"`
	Summary  domain.PerformanceSummary `json:"summary"`
	Equity   []equityPointJSON         `json:"equity"`
	DailyPnL []dailyPnlJSON            `json:"daily_pnl"`
	Drawdown []domain.DrawdownPoint    `json:"drawdown"`
}

type rangeInfo struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Bucket string `json:"bucket"`
}

type equityPointJSON struct {
	Time        string  `json:"time"`
	Equity      float64 `json:"equity"`
	Cash        float64 `json:"cash"`
	DrawdownPct float64 `json:"drawdown_pct"`
}

type dailyPnlJSON struct {
	Date          string  `json:"date"`
	RealizedPnL   float64 `json:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	TradeCount    int     `json:"trade_count"`
	MaxDrawdown   float64 `json:"max_drawdown"`
}

func (h *PerformanceHandler) serveDashboard(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	bucket := parseBucket(r, to.Sub(from))
	ctx := r.Context()
	envMode := domain.EnvModePaper
	tenantID := "default"

	// Fetch data in sequence (simple, correct; parallelism can be added later if needed)
	equityPts, err := h.pnlRepo.GetBucketedEquityCurve(ctx, tenantID, envMode, from, to, bucket)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get bucketed equity curve")
		http.Error(w, `{"error":"equity curve query failed"}`, http.StatusInternalServerError)
		return
	}

	dailyData, err := h.pnlRepo.GetDailyPnL(ctx, tenantID, envMode, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get daily P&L")
		http.Error(w, `{"error":"daily pnl query failed"}`, http.StatusInternalServerError)
		return
	}

	maxDD, err := h.pnlRepo.GetMaxDrawdown(ctx, tenantID, envMode, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get max drawdown")
		http.Error(w, `{"error":"max drawdown query failed"}`, http.StatusInternalServerError)
		return
	}

	sharpe, err := h.pnlRepo.GetSharpe(ctx, tenantID, envMode, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get sharpe")
	}

	sortino, err := h.pnlRepo.GetSortino(ctx, tenantID, envMode, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get sortino")
	}

	// Fetch full-resolution equity for drawdown curve and CAGR
	fullEquity, err := h.pnlRepo.GetEquityCurve(ctx, tenantID, envMode, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get full equity curve")
		fullEquity = nil
	}

	summary := domain.ComputeSummary(dailyData, maxDD, sharpe, sortino, fullEquity)
	drawdown := domain.ComputeDrawdownCurve(fullEquity)

	// Build response
	equity := make([]equityPointJSON, 0, len(equityPts))
	for _, pt := range equityPts {
		equity = append(equity, equityPointJSON{
			Time:        pt.Time.UTC().Format(time.RFC3339),
			Equity:      pt.Equity,
			Cash:        pt.Cash,
			DrawdownPct: pt.Drawdown,
		})
	}

	daily := make([]dailyPnlJSON, 0, len(dailyData))
	for _, d := range dailyData {
		daily = append(daily, dailyPnlJSON{
			Date:          d.Date.Format("2006-01-02"),
			RealizedPnL:   d.RealizedPnL,
			UnrealizedPnL: d.UnrealizedPnL,
			TradeCount:    d.TradeCount,
			MaxDrawdown:   d.MaxDrawdown,
		})
	}

	resp := dashboardResponse{
		Range: rangeInfo{
			From:   from.UTC().Format(time.RFC3339),
			To:     to.UTC().Format(time.RFC3339),
			Bucket: bucket,
		},
		Summary:  summary,
		Equity:   equity,
		DailyPnL: daily,
		Drawdown: drawdown,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error().Err(err).Msg("failed to encode dashboard response")
	}
}

// ---------- Trades ----------

type tradeJSON struct {
	Time       string  `json:"time"`
	TradeID    string  `json:"trade_id"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Quantity   float64 `json:"quantity"`
	Price      float64 `json:"price"`
	Commission float64 `json:"commission"`
	Status     string  `json:"status"`
}

type tradesResponse struct {
	Items      []tradeJSON `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func (h *PerformanceHandler) serveTrades(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	ctx := r.Context()
	q := r.URL.Query()

	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}

	query := ports.TradeQuery{
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

	page, err := h.repo.ListTrades(ctx, query)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list trades")
		http.Error(w, `{"error":"trades query failed"}`, http.StatusInternalServerError)
		return
	}

	items := make([]tradeJSON, 0, len(page.Items))
	for _, t := range page.Items {
		items = append(items, tradeJSON{
			Time:       t.Time.UTC().Format(time.RFC3339),
			TradeID:    t.TradeID.String(),
			Symbol:     string(t.Symbol),
			Side:       t.Side,
			Quantity:   t.Quantity,
			Price:      t.Price,
			Commission: t.Commission,
			Status:     t.Status,
		})
	}

	resp := tradesResponse{
		Items:      items,
		NextCursor: page.NextCursor,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error().Err(err).Msg("failed to encode trades response")
	}
}

// ---------- Strategies ----------

type strategyRowJSON struct {
	Strategy     string   `json:"strategy"`
	RealizedPnL  float64  `json:"realized_pnl"`
	Fees         float64  `json:"fees"`
	TotalTrades  int      `json:"total_trades"`
	WinCount     int      `json:"win_count"`
	LossCount    int      `json:"loss_count"`
	WinRate      *float64 `json:"win_rate"`
	ProfitFactor *float64 `json:"profit_factor"`
	GrossProfit  float64  `json:"gross_profit"`
	GrossLoss    float64  `json:"gross_loss"`
}

func (h *PerformanceHandler) serveStrategies(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	ctx := r.Context()

	rows, err := h.pnlRepo.ListStrategySummaries(ctx, "default", domain.EnvModePaper, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list strategy summaries")
		http.Error(w, `{"error":"strategy summaries query failed"}`, http.StatusInternalServerError)
		return
	}

	items := make([]strategyRowJSON, 0, len(rows))
	for _, row := range rows {
		item := strategyRowJSON{
			Strategy:    row.Strategy,
			RealizedPnL: row.RealizedPnL,
			Fees:        row.Fees,
			TotalTrades: row.TotalTrades,
			WinCount:    row.WinCount,
			LossCount:   row.LossCount,
			GrossProfit: row.GrossProfit,
			GrossLoss:   row.GrossLoss,
		}
		if row.TotalTrades > 0 {
			wr := float64(row.WinCount) / float64(row.TotalTrades)
			item.WinRate = &wr
		}
		if row.GrossLoss != 0 {
			pf := row.GrossProfit / (-row.GrossLoss)
			item.ProfitFactor = &pf
		}
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		h.log.Error().Err(err).Msg("failed to encode strategy summaries")
	}
}

// ---------- Symbols ----------

func (h *PerformanceHandler) serveSymbols(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	ctx := r.Context()
	strategy := r.URL.Query().Get("strategy")

	attrs, err := h.pnlRepo.ListSymbolAttribution(ctx, "default", domain.EnvModePaper, strategy, from, to)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list symbol attribution")
		http.Error(w, `{"error":"symbol attribution query failed"}`, http.StatusInternalServerError)
		return
	}

	if attrs == nil {
		attrs = []domain.SymbolAttribution{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(attrs); err != nil {
		h.log.Error().Err(err).Msg("failed to encode symbol attribution")
	}
}

// ---------- Helpers ----------

// parseRange extracts the from/to time range from query params.
// Supports ?range=7d|30d|90d|all or explicit ?from=<RFC3339>&to=<RFC3339>.
func parseRange(r *http.Request) (time.Time, time.Time) {
	q := r.URL.Query()
	now := time.Now().UTC()
	to := now

	// Explicit from/to override range param
	if qFrom := q.Get("from"); qFrom != "" {
		if t, err := time.Parse(time.RFC3339, qFrom); err == nil {
			from := t
			if qTo := q.Get("to"); qTo != "" {
				if t2, err := time.Parse(time.RFC3339, qTo); err == nil {
					to = t2
				}
			}
			return from, to
		}
	}

	// Named range
	switch q.Get("range") {
	case "1d":
		return now.AddDate(0, 0, -1), to
	case "7d":
		return now.AddDate(0, 0, -7), to
	case "90d":
		return now.AddDate(0, -3, 0), to
	case "all":
		return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), to
	default: // "30d" or unspecified
		return now.AddDate(0, 0, -30), to
	}
}

// parseBucket returns the time_bucket interval based on request param or auto-select.
func parseBucket(r *http.Request, window time.Duration) string {
	if raw := r.URL.Query().Get("bucket"); raw != "" {
		switch raw {
		case "1m", "5m", "15m", "1h", "4h", "1d":
			return raw
		}
	}
	// Auto-select bucket based on window size
	switch {
	case window <= 24*time.Hour:
		return "5m"
	case window <= 7*24*time.Hour:
		return "15m"
	case window <= 30*24*time.Hour:
		return "1h"
	case window <= 90*24*time.Hour:
		return "4h"
	default:
		return "1d"
	}
}
