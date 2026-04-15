package strategy

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// livenessEmitSeq provides monotonic idempotency keys for StrategyEvaluation
// events without drawing from crypto/rand. Liveness events are transient UI
// telemetry — downstream consumers don't dedup on the key — so a cheap
// counter is sufficient and keeps the hot path allocation-free.
var livenessEmitSeq atomic.Uint64

// reasonFromSignals derives a DecisionReason from the signals a strategy
// returned for a single bar. Zero-length signals indicate HOLD with no
// explicit reason — we still record the outcome so the UI can distinguish
// "never evaluated" from "evaluated, decided to hold". Non-empty signals
// use the first actionable signal's type/side/tags.
func reasonFromSignals(signals []start.Signal) *domain.DecisionReason {
	now := time.Now().UTC()
	if len(signals) == 0 {
		return &domain.DecisionReason{At: now, Outcome: "HOLD", Summary: ""}
	}
	sig := signals[0]
	outcome := "HOLD"
	switch sig.Type {
	case start.SignalEntry:
		outcome = "ENTRY"
	case start.SignalExit:
		outcome = "EXIT"
	}
	summary := string(sig.Type)
	if sig.Side != "" {
		summary = summary + " " + string(sig.Side)
	}
	var tags map[string]string
	if len(sig.Tags) > 0 {
		tags = make(map[string]string, len(sig.Tags))
		for k, v := range sig.Tags {
			tags[k] = v
		}
	}
	return &domain.DecisionReason{At: now, Outcome: outcome, Summary: summary, Tags: tags}
}

// livenessEntry holds the hot-path counters for a single (strategy, symbol)
// pair. All reads/writes are atomic so handleBar never takes a mutex for
// tick/eval recording.
type livenessEntry struct {
	lastTickAtNano   atomic.Int64
	lastEvalAtNano   atomic.Int64
	lastSignalAtNano atomic.Int64
	evalCount        atomic.Uint64
	barsTodayCount   atomic.Uint64
	signalCount      atomic.Uint64
	fillCount        atomic.Uint64
	// dayKey is a packed yyyymmdd integer used to detect day rollovers
	// (NY for equity, UTC for crypto). Stored as int64 so CompareAndSwap
	// can be used to ensure exactly one reset per boundary crossing.
	dayKey atomic.Int64
	// reason is the most recent structured decision (HOLD/ENTRY/EXIT).
	// Written only when non-nil; nil means no reason has been recorded.
	reason atomic.Pointer[domain.DecisionReason]
	// crypto is set at registration time so RecordTick/RecordEval can pick
	// the correct timezone for day rollover without per-call symbol
	// parsing. Equity strategies roll at ET midnight; crypto rolls at UTC.
	crypto bool
	// lastEmitNano is the wall-clock UnixNano of the most recent
	// StrategyEvaluation event publish. 0 means "never emitted" so the
	// first RecordEval after registration fires immediately. Subsequent
	// publishes are throttled to at most one per `evaluationEmitMinGap`.
	lastEmitNano atomic.Int64

	// Phase 3 sparkline state. barsPerMinute is a 60-slot ring buffer
	// indexed by minute-of-hour. A background goroutine owned by the
	// tracker samples evalCount every 60s, computes the delta against
	// lastSnapshotEvalCount, stores it into the current slot, and advances
	// the index. Keeping the sampler off the hot path means handleBar
	// still pays only the counter atomic we already had — no extra
	// writes per bar. barsMu guards slot writes against concurrent
	// snapshotBarsPerMinute reads so neither side observes a half-rotated
	// ring.
	barsMu                 sync.RWMutex
	barsPerMinute          [60]uint32
	lastRotationNano       atomic.Int64
	lastSnapshotEvalCount  atomic.Uint64
}

// evaluationEmitMinGap is the minimum gap between two StrategyEvaluation
// events for the same (strategy, symbol). 1s matches the dashboard pulse-dot
// cadence and keeps SSE fan-out bounded even when a strategy evaluates on
// every trade tick.
const evaluationEmitMinGap = int64(time.Second)

// livenessKey identifies a tracked (strategy, symbol) pair. Keyed on strings
// rather than *Instance pointers because DNA hot-reload may recreate the
// instance struct without changing the strategy/symbol identity.
type livenessKey struct {
	strategy string
	symbol   string
}

// EvaluationPublisher is the optional hook LivenessTracker calls from
// RecordEval when the per-key throttle admits a publish. Passing the
// publisher as a plain function (not ports.EventBusPort) keeps the tracker
// free of transport concerns and makes it trivial to fake in tests.
type EvaluationPublisher func(domain.Event)

// LivenessTracker owns per-strategy telemetry. Registration is slow-path
// (takes a write lock, pre-allocates the entry pointer); the hot path is
// lock-free atomic ops only.
type LivenessTracker struct {
	mu      sync.RWMutex
	entries map[livenessKey]*livenessEntry
	// byStrategy lets Snapshot enumerate without scanning every entry.
	byStrategy map[string][]*livenessEntry
	// symbolFor maps entry pointer -> symbol (for Snapshot output).
	symbolFor map[*livenessEntry]string
	// publisher, when non-nil, receives throttled StrategyEvaluation events
	// from RecordEval. Set via SetPublisher; nil tracker / nil publisher
	// keeps RecordEval pure-atomic (used in backtest mode).
	publisher EvaluationPublisher

	// Phase 3 sparkline sampler lifecycle. startOnce/stopOnce make
	// Start/Stop idempotent — the Runner calls Stop only on shutdown but
	// callers re-mounting the tracker (tests) shouldn't panic. stopCh is
	// closed by Stop to release the goroutine; wg lets Stop block until
	// the sampler has returned so tests don't race the goroutine.
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// rotationInterval is how often the sparkline sampler rotates the 60-slot
// ring. One minute matches the "bars per minute" semantic — samples aligned
// to wall-clock minutes would add complexity (clock drift, DST) without
// changing the UX of a rolling 60-minute activity trail.
const rotationInterval = time.Minute

// NewLivenessTracker returns an empty tracker. Call Register for each
// (strategyID, symbol) you intend to observe so the hot-path avoids
// a map lookup + mutex.
func NewLivenessTracker() *LivenessTracker {
	return &LivenessTracker{
		entries:    make(map[livenessKey]*livenessEntry),
		byStrategy: make(map[string][]*livenessEntry),
		symbolFor:  make(map[*livenessEntry]string),
		stopCh:     make(chan struct{}),
	}
}

// Start launches the background sparkline sampler. Safe to call once per
// tracker; subsequent calls no-op via startOnce. The Runner invokes Start
// from Start(ctx); the sampler terminates when either ctx is canceled or
// Stop is called, whichever comes first.
func (t *LivenessTracker) Start(ctx context.Context) {
	if t == nil {
		return
	}
	t.startOnce.Do(func() {
		t.wg.Add(1)
		go t.runSampler(ctx)
	})
}

// Stop halts the background sparkline sampler and blocks until it returns.
// Idempotent — multiple callers may invoke Stop (e.g. Runner shutdown plus
// test teardown) without panicking on a double-close of stopCh.
func (t *LivenessTracker) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
	t.wg.Wait()
}

// runSampler periodically rotates the barsPerMinute ring for every tracked
// entry. Rotation is O(entries) and runs once per rotationInterval, so the
// cost stays constant regardless of bar throughput.
func (t *LivenessTracker) runSampler(ctx context.Context) {
	defer t.wg.Done()
	ticker := time.NewTicker(rotationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case now := <-ticker.C:
			t.rotateAll(now)
		}
	}
}

// rotateAll samples every entry's evalCount delta into the current minute
// slot and stores the rotation timestamp. Exposed for tests so they can
// drive rotations deterministically instead of waiting on wall-clock ticks.
func (t *LivenessTracker) rotateAll(now time.Time) {
	t.mu.RLock()
	entries := make([]*livenessEntry, 0, len(t.entries))
	for _, e := range t.entries {
		entries = append(entries, e)
	}
	t.mu.RUnlock()
	for _, e := range entries {
		e.rotate(now)
	}
}

// rotate samples this entry's evalCount delta into the slot for `now` and
// updates lastSnapshotEvalCount + lastRotationNano. Slot index is the
// minute-of-hour, so the 60 slots form a rolling one-hour window.
func (e *livenessEntry) rotate(now time.Time) {
	current := e.evalCount.Load()
	prev := e.lastSnapshotEvalCount.Load()
	var delta uint32
	if current >= prev {
		// Clamp ridiculously large deltas to uint32 max — we never expect
		// > 4B evals in one minute per (strategy, symbol), but the cast is
		// a cheap defense against a counter that was already in overflow.
		d := current - prev
		if d > uint64(^uint32(0)) {
			delta = ^uint32(0)
		} else {
			delta = uint32(d)
		}
	}
	slot := now.Minute() % 60
	e.barsMu.Lock()
	e.barsPerMinute[slot] = delta
	e.barsMu.Unlock()
	e.lastSnapshotEvalCount.Store(current)
	e.lastRotationNano.Store(now.UnixNano())
}

// snapshotBarsPerMinute returns the ring laid out oldest->newest relative
// to the last rotation. The slot for `last.Minute()` is the newest; walking
// backwards modulo 60 yields the oldest. When no rotation has happened yet
// the raw ring (all zeros) is returned, which is indistinguishable from
// "sampled but saw zero bars" — acceptable for a sparkline.
func (e *livenessEntry) snapshotBarsPerMinute() []uint32 {
	out := make([]uint32, 60)
	e.barsMu.RLock()
	defer e.barsMu.RUnlock()
	lastNano := e.lastRotationNano.Load()
	if lastNano == 0 {
		// Ring untouched; return zeros in natural slot order. The UI
		// treats an all-zero ring as "no activity yet" regardless of
		// orientation.
		copy(out, e.barsPerMinute[:])
		return out
	}
	newest := time.Unix(0, lastNano).Minute() % 60
	// out[59] = newest slot, out[0] = oldest (newest+1 mod 60).
	for i := 0; i < 60; i++ {
		src := (newest + 1 + i) % 60
		out[i] = e.barsPerMinute[src]
	}
	return out
}

// SetPublisher installs the optional EvaluationPublisher. Safe to call once
// at Runner construction; callers must not race SetPublisher with concurrent
// RecordEval. Passing nil disables publishing (RecordEval still records
// counters). This is separate from the backtest-wide DisableLiveness flag —
// callers that run offline should simply not install a publisher.
func (t *LivenessTracker) SetPublisher(p EvaluationPublisher) {
	if t == nil {
		return
	}
	t.publisher = p
}

// Register pre-allocates the livenessEntry for (strategyID, symbol) so the
// bar hot-path can resolve it without allocating. Safe to call multiple
// times — existing entries are left unchanged.
func (t *LivenessTracker) Register(strategyID, symbol string) {
	if t == nil {
		return
	}
	t.register(strategyID, symbol)
}

func (t *LivenessTracker) register(strategyID, symbol string) *livenessEntry {
	k := livenessKey{strategy: strategyID, symbol: symbol}
	t.mu.RLock()
	if e, ok := t.entries[k]; ok {
		t.mu.RUnlock()
		return e
	}
	t.mu.RUnlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[k]; ok {
		return e
	}
	e := &livenessEntry{crypto: domain.Symbol(symbol).IsCryptoSymbol()}
	t.entries[k] = e
	t.byStrategy[strategyID] = append(t.byStrategy[strategyID], e)
	t.symbolFor[e] = symbol
	return e
}

// entry fetches a pre-registered entry; if missing it lazily registers.
// Callers on the hot path should pre-register at instance-register time
// to avoid this lookup.
func (t *LivenessTracker) entry(strategyID, symbol string) *livenessEntry {
	if t == nil {
		return nil
	}
	k := livenessKey{strategy: strategyID, symbol: symbol}
	t.mu.RLock()
	e, ok := t.entries[k]
	t.mu.RUnlock()
	if ok {
		return e
	}
	return t.register(strategyID, symbol)
}

// dayKeyFor returns yyyymmdd packed as int64 for the entry's timezone.
func dayKeyFor(at time.Time, crypto bool) int64 {
	if crypto {
		y, m, d := at.UTC().Date()
		return int64(y*10000 + int(m)*100 + d)
	}
	y, m, d := at.In(domain.NYLocation()).Date()
	return int64(y*10000 + int(m)*100 + d)
}

// rolloverIfNeeded atomically resets barsTodayCount when the entry's dayKey
// has advanced. Exactly one CAS succeeds per boundary crossing per entry.
func (e *livenessEntry) rolloverIfNeeded(at time.Time) {
	want := dayKeyFor(at, e.crypto)
	prev := e.dayKey.Load()
	if prev == want {
		return
	}
	if e.dayKey.CompareAndSwap(prev, want) && prev != 0 {
		e.barsTodayCount.Store(0)
	}
}

// RecordTick is called once per bar per symbol before strategy evaluation.
// Budget: 1 atomic store. The day-rollover check is a branch-predictable
// load+compare on the steady state.
func (t *LivenessTracker) RecordTick(strategyID, symbol string, at time.Time) {
	if t == nil {
		return
	}
	e := t.entry(strategyID, symbol)
	if e == nil {
		return
	}
	e.rolloverIfNeeded(at)
	e.lastTickAtNano.Store(at.UnixNano())
}

// RecordEval is called after each safeOnBar. Increments eval/bars-today
// counters and stores the latest decision reason (may be nil for "no
// reason produced").
func (t *LivenessTracker) RecordEval(strategyID, symbol string, at time.Time, reason *domain.DecisionReason) {
	if t == nil {
		return
	}
	e := t.entry(strategyID, symbol)
	if e == nil {
		return
	}
	e.rolloverIfNeeded(at)
	e.lastEvalAtNano.Store(at.UnixNano())
	evalCount := e.evalCount.Add(1)
	barsToday := e.barsTodayCount.Add(1)
	if reason != nil {
		e.reason.Store(reason)
	}
	// Throttled publish — at most one StrategyEvaluation event per
	// `evaluationEmitMinGap` per (strategy, symbol). First emit fires
	// immediately because lastEmitNano starts at zero. CAS ensures only
	// one racing RecordEval wins per gap window.
	pub := t.publisher
	if pub == nil {
		return
	}
	nowNano := time.Now().UnixNano()
	prev := e.lastEmitNano.Load()
	if prev != 0 && nowNano-prev < evaluationEmitMinGap {
		return
	}
	if !e.lastEmitNano.CompareAndSwap(prev, nowNano) {
		return
	}
	payload := domain.StrategyEvaluationPayload{
		Strategy:     strategyID,
		Symbol:       symbol,
		At:           at.UTC(),
		EvalCount:    evalCount,
		BarsToday:    barsToday,
		LastDecision: reason,
	}
	evt := domain.Event{
		ID:             "liveness-" + strconv.FormatUint(livenessEmitSeq.Add(1), 36),
		Type:           domain.EventStrategyEvaluation,
		OccurredAt:     time.Unix(0, nowNano),
		IdempotencyKey: strategyID + ":" + symbol + ":" + strconv.FormatInt(nowNano, 10),
		Payload:        payload,
	}
	pub(evt)
}

// RecordSignal bumps the signal counter and the last-signal timestamp.
func (t *LivenessTracker) RecordSignal(strategyID, symbol string, at time.Time) {
	if t == nil {
		return
	}
	e := t.entry(strategyID, symbol)
	if e == nil {
		return
	}
	e.lastSignalAtNano.Store(at.UnixNano())
	e.signalCount.Add(1)
}

// RecordFill bumps the fill counter.
func (t *LivenessTracker) RecordFill(strategyID, symbol string, _ time.Time) {
	if t == nil {
		return
	}
	e := t.entry(strategyID, symbol)
	if e == nil {
		return
	}
	e.fillCount.Add(1)
}

// Snapshot returns a stable, symbol-sorted copy of the liveness state for
// a single strategy. Safe to call concurrently with Record* methods; each
// returned field is a self-consistent atomic read but fields are not
// mutually consistent (e.g. evalCount may be incremented between loads of
// evalCount and lastEvalAt). Acceptable for telemetry.
func (t *LivenessTracker) Snapshot(strategyID string) []domain.SymbolLiveness {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	entries := t.byStrategy[strategyID]
	// Defensive copy of the slice header + pair each entry with its symbol.
	type pair struct {
		e   *livenessEntry
		sym string
	}
	snap := make([]pair, 0, len(entries))
	for _, e := range entries {
		snap = append(snap, pair{e: e, sym: t.symbolFor[e]})
	}
	t.mu.RUnlock()

	out := make([]domain.SymbolLiveness, 0, len(snap))
	for _, p := range snap {
		out = append(out, domain.SymbolLiveness{
			Symbol:        p.sym,
			LastTickAt:    nanoToTime(p.e.lastTickAtNano.Load()),
			LastEvalAt:    nanoToTime(p.e.lastEvalAtNano.Load()),
			LastSignalAt:  nanoToTime(p.e.lastSignalAtNano.Load()),
			EvalCount:     p.e.evalCount.Load(),
			BarsToday:     p.e.barsTodayCount.Load(),
			SignalCount:   p.e.signalCount.Load(),
			FillCount:     p.e.fillCount.Load(),
			LastDecision:  p.e.reason.Load(),
			BarsPerMinute: p.e.snapshotBarsPerMinute(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

func nanoToTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
