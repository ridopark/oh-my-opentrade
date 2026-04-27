// Package livedarkpool runs the in-process dark-pool 5m aggregator that
// omo-core's strategy runner consumes during RTH. It replaces the
// omo-data REST round-trip on the read path: trades arrive on the live
// event bus, are dispatched per-symbol to a backfill.DPAggregator, and a
// 1-minute ticker emits closed 5m buckets into both an in-memory lookup
// cache (for the strategy runner) and the darkpool_bars table (for
// resilience and the Phase 5(d) audit).
//
// Phase 4 (B/D/E/F) of the backtest/live parity plan. The DPSource
// interface (Phase 4 C, in package strategy) is satisfied by *Service
// directly, so wiring is `runner.SetDarkPoolSource(svc)`. The boot
// replay sink swap (Phase 4 D) calls Service.AddTrade for every
// market_trades row since session_open, rebuilding the in-flight
// bucket and any earlier session buckets the live aggregator missed
// while omo-core was down.
package livedarkpool

import (
	"context"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/backfill"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// Repo persists closed 5m dark-pool bars. timescaledb.DarkPoolRepo
// satisfies it implicitly via SaveDarkPoolBars (the existing ON CONFLICT
// DO UPDATE upsert means boot-replay re-aggregations don't double-write).
type Repo interface {
	SaveDarkPoolBars(ctx context.Context, bars []domain.DarkPoolBar) (int, error)
}

// Service is the live DP aggregator. Constructed with a repo and an
// optional now-clock (defaults to time.Now). Safe for concurrent
// AddTrade and Lookup; the periodic flush runs on a single goroutine
// started by Run.
type Service struct {
	repo Repo
	log  zerolog.Logger
	now  func() time.Time

	// flushInterval gates how often the ticker calls FlushClosed on
	// every aggregator. 1 minute is short enough that a 5m bucket's
	// closure is reflected in the runner cache within ~60s of the
	// boundary, and long enough to keep DB load bounded (~34 symbols
	// x ≤1 bucket per minute = trivial write rate).
	flushInterval time.Duration

	mu    sync.Mutex
	aggs  map[domain.Symbol]*backfill.DPAggregator
	cache map[cacheKey]domain.DarkPoolBar
}

// cacheKey indexes the in-memory lookup the runner queries via Lookup.
// The runner always passes a 5m-bucket-truncated time; we store the bar
// under the same key the strategy code computes.
type cacheKey struct {
	sym  string
	time time.Time
}

// Option configures a Service at construction.
type Option func(*Service)

// WithNow injects a clock for deterministic tests. Production uses time.Now.
func WithNow(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithFlushInterval overrides the default 1-minute ticker. Tests pass a
// short interval to drive the loop predictably; production should leave
// the default unless ops decides otherwise.
func WithFlushInterval(d time.Duration) Option {
	return func(s *Service) { s.flushInterval = d }
}

// New constructs a Service. The repo persists closed buckets; nil is
// rejected by the caller (services.go panics earlier if the repo wire
// is missing). Logger is required for ops visibility on the flush loop.
func New(repo Repo, log zerolog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:          repo,
		log:           log.With().Str("component", "livedarkpool").Logger(),
		now:           time.Now,
		flushInterval: time.Minute,
		aggs:          make(map[domain.Symbol]*backfill.DPAggregator),
		cache:         make(map[cacheKey]domain.DarkPoolBar),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// AddTrade dispatches a tick to the per-symbol aggregator, lazy-init on
// first sight. Called from the bus subscriber goroutine for live trades
// and from the boot replay sink for historical session-open replay.
func (s *Service) AddTrade(t domain.MarketTrade) {
	if t.Symbol == "" {
		return
	}
	s.mu.Lock()
	agg, ok := s.aggs[t.Symbol]
	if !ok {
		agg = backfill.NewDPAggregator(t.Symbol)
		s.aggs[t.Symbol] = agg
	}
	s.mu.Unlock()
	// AddTrade has its own internal mutex (Phase 4 A) so we drop ours
	// before delegating — keeps Service.mu scoped to map reads.
	agg.AddTrade(t.Time, t.Exchange, t.Price, t.Size)
}

// Lookup implements strategy.DPSource. Returns the cached bar for the
// (symbol, 5m-bucket) key written by the most recent flushClosed pass;
// returns (zero, false) for keys not yet flushed (including the
// in-flight bucket).
func (s *Service) Lookup(sym string, t time.Time) (domain.DarkPoolBar, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bar, ok := s.cache[cacheKey{sym: sym, time: t}]
	return bar, ok
}

// HasData implements strategy.DPSource. Reports whether any bucket has
// been flushed yet — used by the runner to skip DP overlay blocks
// before any 5m boundary has crossed since boot.
func (s *Service) HasData() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cache) > 0
}

// Run drives the periodic flush. Blocks until ctx is canceled. Caller
// (services.go) launches it in a goroutine when cfg.LiveDarkPoolEnabled
// is true; off-path Service is constructed but Run is not called, so
// HasData stays false and the runner ignores it.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	s.log.Info().Dur("interval", s.flushInterval).Msg("dark pool flush ticker started")
	for {
		select {
		case <-ctx.Done():
			s.log.Info().Msg("dark pool flush ticker stopped")
			return
		case <-ticker.C:
			s.flushClosed(ctx)
		}
	}
}

// FlushNow is the same operation Run drives on a ticker. Exposed for
// tests and for the boot-replay path which may want to flush after
// feeding all historical trades so the cache is hot before the strategy
// runner starts evaluating.
func (s *Service) FlushNow(ctx context.Context) {
	s.flushClosed(ctx)
}

// flushClosed iterates every aggregator and calls FlushClosed(now). For
// each emitted bar: write to cache (so the runner can Lookup it) and
// persist to darkpool_bars (so a mid-day restart's boot replay sees the
// same closed buckets via Phase 6 + this Service combined).
func (s *Service) flushClosed(ctx context.Context) {
	now := s.now()

	s.mu.Lock()
	if len(s.aggs) == 0 {
		s.mu.Unlock()
		return
	}
	// Snapshot the (sym, agg) list under the lock; release before
	// calling FlushClosed so per-aggregator work doesn't block AddTrade
	// on other symbols. Each aggregator has its own mutex.
	type pair struct {
		sym domain.Symbol
		agg *backfill.DPAggregator
	}
	work := make([]pair, 0, len(s.aggs))
	for sym, agg := range s.aggs {
		work = append(work, pair{sym: sym, agg: agg})
	}
	s.mu.Unlock()

	var emitted []domain.DarkPoolBar
	for _, p := range work {
		bars := p.agg.FlushClosed(now)
		if len(bars) == 0 {
			continue
		}
		emitted = append(emitted, bars...)
		// Update cache under the Service mutex.
		s.mu.Lock()
		for _, b := range bars {
			s.cache[cacheKey{sym: string(p.sym), time: b.Time}] = b
		}
		s.mu.Unlock()
	}

	if len(emitted) == 0 {
		return
	}

	saved, err := s.repo.SaveDarkPoolBars(ctx, emitted)
	if err != nil {
		s.log.Warn().Err(err).Int("bars", len(emitted)).Msg("save dark pool bars failed; retaining in cache for next pass")
		return
	}
	s.log.Debug().Int("saved", saved).Int("emitted", len(emitted)).Msg("flushed closed dark pool buckets")
}
