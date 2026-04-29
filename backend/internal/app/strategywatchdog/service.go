// Package strategywatchdog reports when a strategy stops evaluating bars
// during equity RTH. It is pure observability: reads liveness snapshots,
// emits logs + notifications + a gauge, never touches trading state.
//
// The watchdog exists because a silent pipeline failure (strategy_runner
// subscription going dead after a WS reconnect) cost a morning of trading
// before the operator noticed. Detection within 90s beats discovery next day.
package strategywatchdog

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
)

// WatchedStrategy is the minimal slice of per-strategy info the watchdog needs.
type WatchedStrategy struct {
	ID      string
	Symbols []string
	Active  bool
}

// Deps wires the watchdog's read surfaces + outputs. Kept as plain function
// fields so tests can inject fakes without mocking interfaces.
type Deps struct {
	ListStrategies func() []WatchedStrategy
	LivenessFor    func(strategyID string) []domain.SymbolLiveness
	Notifier       ports.NotifierPort
	StaleGauge     *prometheus.GaugeVec // labels: strategy, symbol
	Log            zerolog.Logger
	Now            func() time.Time
}

// Config holds tunable thresholds. Zero values fall back to defaults.
//
// Defaults assume wall-clock-stamped LastDecision.At (see Tick) — staleness
// is now the real time since the last evaluation, not inflated by bar-time
// semantics. A healthy 5m strategy evaluates every ~5m wall clock so 6m is
// a safe warn threshold (single missed cycle); 10m alerts on two missed.
type Config struct {
	TickInterval time.Duration // default 30s
	WarnStale    time.Duration // default 6m — log WARN per tick
	AlertStale   time.Duration // default 10m — log ERROR + notify, deduped
	DedupeWindow time.Duration // default 15m — per strategy+symbol
}

func (c *Config) applyDefaults() {
	if c.TickInterval == 0 {
		c.TickInterval = 30 * time.Second
	}
	if c.WarnStale == 0 {
		c.WarnStale = 6 * time.Minute
	}
	if c.AlertStale == 0 {
		c.AlertStale = 10 * time.Minute
	}
	if c.DedupeWindow == 0 {
		c.DedupeWindow = 15 * time.Minute
	}
}

// Service is the watchdog. Start() launches a goroutine that ticks on
// Config.TickInterval, skips outside RTH, and escalates per threshold.
type Service struct {
	deps Deps
	cfg  Config

	startTime time.Time // set at New; used to ignore warmup-replayed LastEvalAt

	mu          sync.Mutex
	lastAlertAt map[string]time.Time // key: "strategy:symbol"
}

func New(deps Deps, cfg Config) *Service {
	cfg.applyDefaults()
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Service{
		deps:        deps,
		cfg:         cfg,
		startTime:   deps.Now(),
		lastAlertAt: make(map[string]time.Time),
	}
}

// Start launches the watchdog goroutine. It exits when ctx is canceled.
// A panic in the tick loop is recovered so a bug in this service cannot
// take omo-core down.
func (s *Service) Start(ctx context.Context) {
	go s.run(ctx)
}

func (s *Service) run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.deps.Log.Error().Interface("panic", r).Msg("strategywatchdog: panicked, watchdog stopping")
		}
	}()
	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx, s.deps.Now())
		}
	}
}

// Tick is exposed for tests to drive a single evaluation deterministically.
func (s *Service) Tick(ctx context.Context, now time.Time) {
	cal := domain.CalendarFor(domain.AssetClassEquity)
	if !cal.IsOpen(now) {
		return
	}
	for _, ws := range s.deps.ListStrategies() {
		if !ws.Active {
			continue
		}
		snap := s.deps.LivenessFor(ws.ID)
		for _, sl := range snap {
			// Prefer LastDecision.At (wall clock at eval time) over LastEvalAt
			// (bar.Time, which is always ~5m stale for 5m-bar strategies and
			// caused a false-alert storm on 2026-04-21 even with an 8-minute
			// threshold). LastDecision is populated whenever a strategy records
			// a HOLD/ENTRY/EXIT reason, which covers every live evaluation for
			// avwap_v4 and macd_only_v1.
			evalAt := sl.LastEvalAt
			if sl.LastDecision != nil && !sl.LastDecision.At.IsZero() {
				evalAt = sl.LastDecision.At
			}
			if evalAt.IsZero() {
				continue
			}
			// Ignore timestamps stamped by warmup-replay before the watchdog
			// started — those are historical bar times from the replay path,
			// not live evaluations. Only alert once we've observed a post-
			// start eval; until then staleness is meaningless.
			if evalAt.Before(s.startTime) {
				continue
			}
			staleFor := now.Sub(evalAt)
			if s.deps.StaleGauge != nil {
				s.deps.StaleGauge.WithLabelValues(ws.ID, sl.Symbol).Set(staleFor.Seconds())
			}
			switch {
			case staleFor >= s.cfg.AlertStale:
				s.alertOnce(ctx, ws.ID, sl.Symbol, staleFor, now)
			case staleFor >= s.cfg.WarnStale:
				s.deps.Log.Warn().
					Str("strategy", ws.ID).
					Str("symbol", sl.Symbol).
					Dur("stale", staleFor).
					Time("last_eval_at", evalAt).
					Msg("strategy liveness: eval stale")
			}
		}
	}
}

func (s *Service) alertOnce(ctx context.Context, strategy, symbol string, staleFor time.Duration, now time.Time) {
	key := strategy + ":" + symbol
	s.mu.Lock()
	last, seen := s.lastAlertAt[key]
	if seen && now.Sub(last) < s.cfg.DedupeWindow {
		s.mu.Unlock()
		return
	}
	s.lastAlertAt[key] = now
	s.mu.Unlock()

	s.deps.Log.Error().
		Str("strategy", strategy).
		Str("symbol", symbol).
		Dur("stale", staleFor).
		Msg("strategy liveness: eval stale — ALERT")

	if s.deps.Notifier != nil {
		msg := fmt.Sprintf("STRATEGY STALE: %s on %s — no evaluation for %v", strategy, symbol, staleFor.Round(time.Second))
		if err := s.deps.Notifier.Notify(ctx, "system", msg); err != nil {
			s.deps.Log.Warn().Err(err).Msg("strategywatchdog: notify failed")
		}
	}
}

// NewStaleGauge constructs the Prometheus gauge the watchdog writes to.
// Expose separately so bootstrap can register it with the shared registry.
func NewStaleGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "omo_strategy_liveness_stale_seconds",
		Help: "Seconds since strategy's last handleBar evaluation per symbol. Rises without bound if the runner subscription dies.",
	}, []string{"strategy", "symbol"})
}
