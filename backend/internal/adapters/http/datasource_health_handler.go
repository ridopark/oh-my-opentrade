package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// DataSourceState is the tri-state the dashboard renders: healthy (green),
// unhealthy (red), or closed (gray). "closed" means the source is intentionally
// idle — e.g. the US equity WebSocket feed during weekends and after hours —
// which is operationally different from a feed that should be live but isn't.
type DataSourceState string

const (
	StateHealthy   DataSourceState = "healthy"
	StateUnhealthy DataSourceState = "unhealthy"
	StateClosed    DataSourceState = "closed"
)

// DataSourceStatus is the per-source row returned by GET /api/health/datasources.
// The dashboard header renders four dots (IBKR, Alpaca, omo-data, DB) using
// this shape — the id/label split lets the client key on a stable id while
// still rendering a human-friendly label. LastEventAt is optional (zero
// value marshals as the RFC3339 zero time and the UI treats IsZero as
// "never"). Healthy is kept alongside State for dashboards that predate the
// tri-state rollout: Healthy == (State == StateHealthy).
type DataSourceStatus struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Healthy     bool            `json:"healthy"`
	State       DataSourceState `json:"state"`
	LastEventAt time.Time       `json:"lastEventAt"`
	Detail      string          `json:"detail,omitempty"`
}

// DataSourceHealthResponse is the envelope for /api/health/datasources.
// AsOf is included so the UI can show staleness of the probe itself (when
// the dashboard is offline the sources may look healthy even though no one
// re-queried).
type DataSourceHealthResponse struct {
	Sources []DataSourceStatus `json:"sources"`
	AsOf    time.Time          `json:"asOf"`
}

// DataSourceCheck probes a single data source and returns its status. The
// signature matches HealthChecker in spirit but adds the id+label+last-event
// fields the datasource header needs, so we do not reuse HealthChecker
// directly — the old handler is for the backend-only /healthz/services
// endpoint which has a different contract.
type DataSourceCheck func(ctx context.Context) DataSourceStatus

// DataSourceHealthHandler aggregates data-source probes for the dashboard
// header. The Phase 3 plan explicitly asks for "decorate or aggregate, don't
// duplicate checks"; the Runner or wiring layer is expected to build
// DataSourceCheck closures that delegate to the same underlying connection
// state the /healthz/services HealthChecker reads.
type DataSourceHealthHandler struct {
	checks []DataSourceCheck
	log    zerolog.Logger
}

// NewDataSourceHealthHandler wires a slice of probes into a handler.
func NewDataSourceHealthHandler(log zerolog.Logger, checks ...DataSourceCheck) *DataSourceHealthHandler {
	return &DataSourceHealthHandler{checks: checks, log: log}
}

// ServeHTTP responds with 200 regardless of individual source health — the
// dashboard expects to always render the header and needs per-source flags
// to style each dot. A 503 would mask the detail the UI renders.
func (h *DataSourceHealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	resp := DataSourceHealthResponse{
		Sources: make([]DataSourceStatus, 0, len(h.checks)),
		AsOf:    time.Now().UTC(),
	}
	for _, c := range h.checks {
		resp.Sources = append(resp.Sources, c(ctx))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error().Err(err).Msg("failed to encode datasource health")
	}
}

// StaticDataSource returns a DataSourceCheck that always reports healthy
// with no event timestamp. Useful for in-process components whose liveness
// is implied by the server running (e.g. omo-data when it's mounted as a
// sidecar the core talks to directly).
func StaticDataSource(id, label string) DataSourceCheck {
	return func(_ context.Context) DataSourceStatus {
		return DataSourceStatus{ID: id, Label: label, Healthy: true, State: StateHealthy}
	}
}

// DBDataSource probes a DB via a ping-style function. The probe is wrapped
// in a short timeout because health endpoints are polled frequently and a
// stuck DB connection would otherwise block the handler indefinitely.
func DBDataSource(id, label string, ping func(ctx context.Context) error) DataSourceCheck {
	return func(ctx context.Context) DataSourceStatus {
		pCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := ping(pCtx); err != nil {
			return DataSourceStatus{ID: id, Label: label, Healthy: false, State: StateUnhealthy, Detail: err.Error()}
		}
		return DataSourceStatus{ID: id, Label: label, Healthy: true, State: StateHealthy, LastEventAt: time.Now().UTC()}
	}
}

// FeedDataSource builds a DataSourceCheck from a last-event timestamp + a
// staleness threshold. The plan specifies per-feed dots for IBKR and Alpaca
// — both are driven by a "most recent tick" timestamp rather than a boolean
// connection state, so the shared helper avoids repeating the "healthy if
// within threshold" logic in every wiring site. probe returns (lastEvent,
// detail) — detail is shown on the UI only when the dot is unhealthy.
func FeedDataSource(id, label string, threshold time.Duration, probe func(ctx context.Context) (time.Time, string)) DataSourceCheck {
	return GatedFeedDataSource(id, label, threshold, probe, nil)
}

// GatedFeedDataSource is FeedDataSource plus an optional openGate. When the
// gate returns false AND the feed is not healthy, the source reports
// state=closed with a "market closed" detail instead of red-unhealthy. This
// lets the dashboard distinguish "feed intentionally quiet (weekends, after
// hours)" from "feed should be live but isn't". When openGate is nil, behavior
// matches FeedDataSource.
func GatedFeedDataSource(
	id, label string,
	threshold time.Duration,
	probe func(ctx context.Context) (time.Time, string),
	openGate func() bool,
) DataSourceCheck {
	return func(ctx context.Context) DataSourceStatus {
		last, detail := probe(ctx)
		status := DataSourceStatus{ID: id, Label: label, LastEventAt: last, Detail: detail, State: StateUnhealthy}
		if !last.IsZero() && time.Since(last) < threshold {
			status.Healthy = true
			status.State = StateHealthy
			// A healthy dot shouldn't carry the detail line; the UI would
			// otherwise render "IBKR OK — last tick 3s ago" vs the cleaner
			// plain "IBKR".
			status.Detail = ""
			return status
		}
		if openGate != nil && !openGate() {
			status.State = StateClosed
			status.Detail = "market closed"
		}
		return status
	}
}
