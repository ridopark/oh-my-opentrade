// Package http provides HTTP handlers for operational endpoints:
// health checks and strategy DNA management.
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// -----------------------------------------------------------------------
// Health handler
// -----------------------------------------------------------------------

// ServiceStatus represents the health state of a single named service.
type ServiceStatus struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
}

// HealthResponse is the JSON body returned by GET /healthz/services.
type HealthResponse struct {
	Healthy  bool            `json:"healthy"`
	Services []ServiceStatus `json:"services"`
}

// HealthChecker is a function that probes a single service.
type HealthChecker func(ctx context.Context) ServiceStatus

// HealthHandler returns a handler that runs all registered checks and
// serializes the result as JSON.  Overall status is 200 when all services
// are healthy, 503 otherwise.
type HealthHandler struct {
	checks []HealthChecker
	log    zerolog.Logger
}

// NewHealthHandler creates a HealthHandler with the provided checkers.
func NewHealthHandler(log zerolog.Logger, checks ...HealthChecker) *HealthHandler {
	return &HealthHandler{checks: checks, log: log}
}

// DBChecker returns a HealthChecker that pings the given *sql.DB.
func DBChecker(db *sql.DB) HealthChecker {
	return func(ctx context.Context) ServiceStatus {
		pCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := db.PingContext(pCtx); err != nil {
			return ServiceStatus{Name: "timescaledb", Healthy: false, Detail: err.Error()}
		}
		return ServiceStatus{Name: "timescaledb", Healthy: true}
	}
}

// StaticChecker returns a HealthChecker that always reports a named service
// as healthy (for in-process services whose liveness is implied by the
// process running).
func StaticChecker(name string) HealthChecker {
	return func(_ context.Context) ServiceStatus {
		return ServiceStatus{Name: name, Healthy: true}
	}
}

// FeedChecker returns a HealthChecker for the WebSocket market-data feed.
// healthFn should return (isHealthy, detail). The detail is only shown when unhealthy.
func FeedChecker(name string, healthFn func() (bool, string)) HealthChecker {
	return func(_ context.Context) ServiceStatus {
		ok, detail := healthFn()
		if ok {
			return ServiceStatus{Name: name, Healthy: true}
		}
		return ServiceStatus{Name: name, Healthy: false, Detail: detail}
	}
}
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ctx := r.Context()
	resp := HealthResponse{Healthy: true}
	for _, check := range h.checks {
		s := check(ctx)
		resp.Services = append(resp.Services, s)
		if !s.Healthy {
			resp.Healthy = false
		}
	}

	status := http.StatusOK
	if !resp.Healthy {
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error().Err(err).Msg("failed to encode health response")
	}
}

// StrategyHandler handles strategy DNA management endpoints.
type StrategyHandler struct {
	manager  *strategy.DNAManager
	basePath string // directory where TOML files live, e.g. "/configs/strategies"
	log      zerolog.Logger
}

// NewStrategyHandler creates a StrategyHandler.
// basePath is the directory containing strategy TOML files.
func NewStrategyHandler(manager *strategy.DNAManager, basePath string, log zerolog.Logger) *StrategyHandler {
	return &StrategyHandler{
		manager:  manager,
		basePath: strings.TrimRight(basePath, "/"),
		log:      log,
	}
}

// ServeHTTP routes:
//
//	PUT /strategies/{id}/script   — update the inline Go script for strategy {id}
func (h *StrategyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Parse path: /strategies/{id}/script
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "strategies" || parts[2] != "script" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id := parts[1]

	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.putScript(w, r, id)
}

func (h *StrategyHandler) putScript(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Script string `json:"script"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %s", err), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Script) == "" {
		http.Error(w, "script must not be empty", http.StatusBadRequest)
		return
	}

	// Validate the script compiles and has the right signature via Yaegi.
	if err := strategy.ValidateScript(req.Script); err != nil {
		http.Error(w, fmt.Sprintf("script validation failed: %s", err), http.StatusUnprocessableEntity)
		return
	}

	// Load the current TOML, inject the new script, and write it back.
	tomlPath := fmt.Sprintf("%s/%s.toml", h.basePath, id)
	if err := h.manager.UpdateScript(tomlPath, req.Script); err != nil {
		h.log.Error().Err(err).Str("strategy_id", id).Msg("failed to update strategy script")
		http.Error(w, fmt.Sprintf("failed to update strategy: %s", err), http.StatusInternalServerError)
		return
	}

	h.log.Info().Str("strategy_id", id).Msg("strategy script updated via API")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}` + "\n"))
}

// -----------------------------------------------------------------------
// Bars handler
// -----------------------------------------------------------------------

// BarsHandler serves GET /bars for dashboard chart seeding.
// If the requested range has no data in the DB, it transparently fetches
// from the MarketDataPort (Alpaca) and stores the bars before returning.
type BarsHandler struct {
	repo    ports.RepositoryPort
	fetcher ports.MarketDataPort
	log     zerolog.Logger
}

// NewBarsHandler creates a BarsHandler.
// fetcher may be nil; when nil, cache-miss ranges return empty results.
func NewBarsHandler(repo ports.RepositoryPort, fetcher ports.MarketDataPort, log zerolog.Logger) *BarsHandler {
	return &BarsHandler{repo: repo, fetcher: fetcher, log: log}
}

// barResponse is the JSON shape returned to the dashboard.
type barResponse struct {
	Time      string  `json:"time"`
	Symbol    string  `json:"symbol"`
	Timeframe string  `json:"timeframe"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume"`
	Suspect   bool    `json:"suspect"`
}

// ServeHTTP handles GET /bars?symbols=AAPL,MSFT&timeframe=1m&from=<RFC3339>&to=<RFC3339>
// All query params are optional; symbols defaults to all, timeframe to 1m,
// from defaults to start of today (UTC), to defaults to now.
func (h *BarsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	q := r.URL.Query()

	// Parse symbols
	var symbols []domain.Symbol
	if raw := q.Get("symbols"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				symbols = append(symbols, domain.Symbol(s))
			}
		}
	}

	// Parse timeframe
	tf := domain.Timeframe("1m")
	if raw := q.Get("timeframe"); raw != "" {
		tf = domain.Timeframe(raw)
	}

	// Parse time range — default to today UTC
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	to := now
	if raw := q.Get("from"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			from = t
		}
	}
	if raw := q.Get("to"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			to = t
		}
	}

	ctx := r.Context()
	var result []barResponse

	for _, sym := range symbols {
		if err := h.ensureBars(ctx, sym, tf, from, to); err != nil {
			h.log.Warn().Err(err).Str("symbol", string(sym)).Msg("ensureBars failed, returning cached data")
			// non-fatal: fall through to query what we have
		}
		bars, err := h.repo.GetMarketBars(ctx, sym, tf, from, to)
		if err != nil {
			h.log.Error().Err(err).Str("symbol", string(sym)).Msg("bars query failed")
			http.Error(w, fmt.Sprintf("query failed for %s: %s", sym, err), http.StatusInternalServerError)
			return
		}
		for _, b := range bars {
			result = append(result, barResponse{
				Time:      b.Time.UTC().Format(time.RFC3339),
				Symbol:    string(b.Symbol),
				Timeframe: string(b.Timeframe),
				Open:      b.Open,
				High:      b.High,
				Low:       b.Low,
				Close:     b.Close,
				Volume:    b.Volume,
				Suspect:   b.Suspect,
			})
		}
	}

	if result == nil {
		result = []barResponse{} // return [] not null
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		h.log.Error().Err(err).Msg("failed to encode bars response")
	}
}

// ensureBars checks whether the DB has enough bars for the given symbol/timeframe/range.
// If the DB count is below a minimum useful threshold (10% of the expected bar count for
// the window, assuming full trading hours), it fetches from the market data provider and
// stores each bar via the repository (upsert-safe). This handles both cold-start (zero bars)
// and sparse-data cases (e.g. only 1 stale bar) that would produce an unusable chart.
func (h *BarsHandler) ensureBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) error {
	if h.fetcher == nil {
		return nil
	}

	// Compute expected bar count for the window using the configured timeframe.
	barDuration := timeframeDuration(tf)
	var minRequired int
	if barDuration > 0 {
		// Require at least 10% of the theoretical bar count to skip a fetch.
		// This prevents a stale single bar from blocking a full Alpaca refresh.
		expected := int(to.Sub(from) / barDuration)
		minRequired = expected / 10
		if minRequired < 1 {
			minRequired = 1
		}
	}

	existing, err := h.repo.GetMarketBars(ctx, symbol, tf, from, to)
	if err != nil {
		return fmt.Errorf("ensureBars check: %w", err)
	}
	if len(existing) >= minRequired {
		return nil // enough data present
	}

	h.log.Info().
		Str("symbol", string(symbol)).
		Str("timeframe", string(tf)).
		Time("from", from).
		Time("to", to).
		Int("db_bars", len(existing)).
		Int("min_required", minRequired).
		Msg("sparse data — fetching from Alpaca")
	bars, err := h.fetcher.GetHistoricalBars(ctx, symbol, tf, from, to)
	if err != nil {
		return fmt.Errorf("ensureBars fetch: %w", err)
	}
	for _, b := range bars {
		if saveErr := h.repo.SaveMarketBar(ctx, b); saveErr != nil {
			h.log.Warn().Err(saveErr).Str("symbol", string(symbol)).Msg("failed to store fetched bar")
		}
	}
	h.log.Info().
		Str("symbol", string(symbol)).
		Str("timeframe", string(tf)).
		Int("stored", len(bars)).
		Msg("Alpaca fetch complete")
	return nil
}

// timeframeDuration returns the time.Duration for a given Timeframe string.
// Returns 0 for unknown values.
func timeframeDuration(tf domain.Timeframe) time.Duration {
	switch tf {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return 0
	}
}
