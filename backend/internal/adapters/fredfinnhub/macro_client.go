// Package fredfinnhub is a thin client that pulls macro-release events
// from Finnhub's economic-calendar endpoint and, where useful, augments
// them with FRED rate-release history. It exists to back the Sprint 4.6
// macro_event_gate: the gate only needs a forward-looking list of
// scheduled releases with an impact tag.
//
// Both upstreams are optional. If neither API key is set, Refresh and
// the Fetch* helpers return empty slices and no error — a sensible
// default for local and backtest runs that do not need live data.
package fredfinnhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	finnhubBase = "https://finnhub.io/api/v1"
	fredBase    = "https://api.stlouisfed.org/fred"
	// Keep the refresh tiny — the gate only cares about the next
	// couple of weeks of scheduled prints.
	defaultWindowDays = 14
)

// FREDSeries is the list of series IDs we pull historical prints for so
// the macro_events table has Actual/Previous values for released events.
// Additions are safe; the client does not fail if a series errors out.
var FREDSeries = []string{
	"FEDFUNDS",  // Federal Funds Effective Rate
	"CPIAUCSL",  // CPI, All Urban Consumers
	"UNRATE",    // Unemployment rate (NFP proxy)
	"PCEPI",     // PCE Price Index
	"PPIACO",    // Producer Price Index (All Commodities)
}

// MacroRepo is the narrow write surface the client needs. A full
// implementation lives in timescaledb.MacroEventsRepo.
type MacroRepo interface {
	UpsertBatch(ctx context.Context, events []ports.MacroEvent) error
}

// Client pulls macro events from Finnhub + FRED and hands them to the
// supplied repo. Construct one at boot and call Refresh on a schedule.
type Client struct {
	finnhubKey string
	fredKey    string
	http       *http.Client
	repo       MacroRepo
	log        zerolog.Logger
}

// Config tunes the HTTP client and window. Zero values are valid.
type Config struct {
	FinnhubAPIKey string
	FREDAPIKey    string
	WindowDays    int
	HTTPTimeout   time.Duration
}

// NewClient returns a Client wired to the given repo. When both API
// keys are empty, Refresh becomes a no-op.
func NewClient(cfg Config, repo MacroRepo, log zerolog.Logger) *Client {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		finnhubKey: cfg.FinnhubAPIKey,
		fredKey:    cfg.FREDAPIKey,
		http:       &http.Client{Timeout: timeout},
		repo:       repo,
		log:        log.With().Str("component", "fredfinnhub").Logger(),
	}
}

// Refresh pulls the next window of macro events and upserts them. It
// returns nil when no keys are configured so callers can run the hook
// unconditionally from omo-data.
func (c *Client) Refresh(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.finnhubKey == "" && c.fredKey == "" {
		c.log.Debug().Msg("no API keys configured; skipping macro refresh")
		return nil
	}
	var all []ports.MacroEvent
	if c.finnhubKey != "" {
		ev, err := c.fetchFinnhub(ctx, time.Now(), c.windowDays())
		if err != nil {
			c.log.Warn().Err(err).Msg("finnhub economic calendar fetch failed")
		} else {
			all = append(all, ev...)
		}
	}
	if c.fredKey != "" {
		ev, err := c.fetchFREDHistory(ctx)
		if err != nil {
			c.log.Warn().Err(err).Msg("FRED history fetch failed")
		} else {
			all = append(all, ev...)
		}
	}
	if len(all) == 0 {
		return nil
	}
	if c.repo == nil {
		return nil
	}
	if err := c.repo.UpsertBatch(ctx, all); err != nil {
		return fmt.Errorf("fredfinnhub: upsert: %w", err)
	}
	c.log.Info().Int("events", len(all)).Msg("macro events refreshed")
	return nil
}

func (c *Client) windowDays() int {
	return defaultWindowDays
}

// ---------------------------------------------------------------------
// Finnhub economic calendar
// ---------------------------------------------------------------------

type finnhubEconomicResp struct {
	EconomicCalendar []finnhubEvent `json:"economicCalendar"`
}

type finnhubEvent struct {
	Event    string  `json:"event"`
	Country  string  `json:"country"`
	Time     string  `json:"time"` // "2024-01-10 13:30:00"
	Impact   string  `json:"impact"`
	Actual   *float64 `json:"actual"`
	Estimate *float64 `json:"estimate"`
	Prev     *float64 `json:"prev"`
}

func (c *Client) fetchFinnhub(ctx context.Context, from time.Time, windowDays int) ([]ports.MacroEvent, error) {
	to := from.AddDate(0, 0, windowDays)
	url := fmt.Sprintf("%s/calendar/economic?from=%s&to=%s&token=%s",
		finnhubBase, from.Format("2006-01-02"), to.Format("2006-01-02"), c.finnhubKey)

	body, err := c.getJSON(ctx, url)
	if err != nil {
		return nil, err
	}
	var resp finnhubEconomicResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("fredfinnhub: decode: %w", err)
	}

	out := make([]ports.MacroEvent, 0, len(resp.EconomicCalendar))
	for _, e := range resp.EconomicCalendar {
		if !strings.EqualFold(e.Country, "US") {
			continue
		}
		if !isTrackedEvent(e.Event) {
			continue
		}
		ts, err := time.Parse("2006-01-02 15:04:05", e.Time)
		if err != nil {
			continue
		}
		id := fmt.Sprintf("finnhub:%s:%s", normalizeName(e.Event), ts.UTC().Format("20060102T150405Z"))
		out = append(out, ports.MacroEvent{
			ID:          id,
			Name:        canonicalName(e.Event),
			ScheduledAt: ts.UTC(),
			Impact:      strings.ToLower(strings.TrimSpace(e.Impact)),
			Actual:      e.Actual,
			Consensus:   e.Estimate,
			Previous:    e.Prev,
			Released:    e.Actual != nil,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------
// FRED historical releases
// ---------------------------------------------------------------------

type fredObservation struct {
	Date  string `json:"date"`
	Value string `json:"value"`
}

type fredResp struct {
	Observations []fredObservation `json:"observations"`
}

func (c *Client) fetchFREDHistory(ctx context.Context) ([]ports.MacroEvent, error) {
	out := make([]ports.MacroEvent, 0, len(FREDSeries))
	for _, series := range FREDSeries {
		url := fmt.Sprintf("%s/series/observations?series_id=%s&api_key=%s&file_type=json&sort_order=desc&limit=1",
			fredBase, series, c.fredKey)
		body, err := c.getJSON(ctx, url)
		if err != nil {
			c.log.Debug().Err(err).Str("series", series).Msg("FRED fetch failed; skipping")
			continue
		}
		var resp fredResp
		if err := json.Unmarshal(body, &resp); err != nil {
			continue
		}
		if len(resp.Observations) == 0 {
			continue
		}
		obs := resp.Observations[0]
		ts, err := time.Parse("2006-01-02", obs.Date)
		if err != nil {
			continue
		}
		var actual *float64
		var v float64
		if _, err := fmt.Sscanf(obs.Value, "%f", &v); err == nil {
			actual = &v
		}
		out = append(out, ports.MacroEvent{
			ID:          fmt.Sprintf("fred:%s:%s", series, obs.Date),
			Name:        fredSeriesName(series),
			ScheduledAt: ts.UTC(),
			Impact:      "medium",
			Actual:      actual,
			Released:    true,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func (c *Client) getJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fredfinnhub: new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fredfinnhub: http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fredfinnhub: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fredfinnhub: status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// isTrackedEvent keeps the macro_events table focused on the releases
// the gate actually blocks on.
func isTrackedEvent(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "fomc") ||
		strings.Contains(n, "cpi") ||
		strings.Contains(n, "ppi") ||
		strings.Contains(n, "pce") ||
		strings.Contains(n, "nonfarm") ||
		strings.Contains(n, "unemployment") ||
		strings.Contains(n, "fed funds") ||
		strings.Contains(n, "federal funds")
}

func canonicalName(raw string) string {
	n := strings.ToLower(raw)
	switch {
	case strings.Contains(n, "fomc") && strings.Contains(n, "minutes"):
		return "FOMC Minutes"
	case strings.Contains(n, "fomc"):
		return "FOMC Rate Decision"
	case strings.Contains(n, "nonfarm") || strings.Contains(n, "unemployment"):
		return "NFP"
	case strings.Contains(n, "core pce"):
		return "Core PCE"
	case strings.Contains(n, "pce"):
		return "PCE"
	case strings.Contains(n, "core cpi"):
		return "Core CPI"
	case strings.Contains(n, "cpi"):
		return "CPI"
	case strings.Contains(n, "ppi"):
		return "PPI"
	}
	return strings.TrimSpace(raw)
}

func normalizeName(raw string) string {
	n := strings.ToLower(strings.TrimSpace(raw))
	n = strings.ReplaceAll(n, " ", "_")
	return n
}

func fredSeriesName(id string) string {
	switch id {
	case "FEDFUNDS":
		return "Fed Funds Rate"
	case "CPIAUCSL":
		return "CPI"
	case "UNRATE":
		return "Unemployment Rate"
	case "PCEPI":
		return "PCE"
	case "PPIACO":
		return "PPI"
	}
	return id
}
