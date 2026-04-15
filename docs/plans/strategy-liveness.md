# Strategy Liveness Feature Plan

**Status: SHIPPED (2026-04-15).** Phases 1–3 merged to `main`, plus HOLD-reason plumbing on AVWAP + MACD and `SetDisableLiveness(true)` wired at all four backtest entry points (`backtest.NewRunner`, `BuildStrategyShard` sharded path, and both `omo-replay` paths).

## 0. Summary

Introduce per-strategy liveness telemetry — last tick/eval/signal timestamps, counters, rolling bars-per-minute, last decision reason, plus a global data-source header. Phased so Phase 1 (polled HTTP) unblocks UX, Phase 2 (SSE push) adds real-time feel, Phase 3 (sparkline + feed + header) fills in richness.

Validation of proposed phasing: **accept with adjustments**. Phase 1 is the right shape, but `feedHealthy` per-symbol is not cheap today (the port is feed-level). Phase 1 exposes feed-level health labeled with the symbol's asset class; Phase 3 introduces a per-symbol adapter.

---

## 1. Architectural decisions (trade-offs)

### Where eval-count tracking lives
**Recommendation: new `LivenessTracker` in `backend/internal/app/strategy/liveness.go`, owned by the `Runner`.**

- Not a separate service: tracker must observe every OnBar call, so lives in-process next to the Runner to avoid an extra bus hop on the hot path.
- Not a new port: the Runner already knows about metrics and state. The tracker is an internal collaborator, not a pluggable dependency — no port needed.
- Not just on the Runner struct directly: the existing struct is already 2330 lines; liveness state (per strategy×symbol counters + ring buffer) deserves its own file and tests.

The `Runner` exposes a method `Liveness()` returning `*LivenessTracker`; the HTTP handler reads via that tracker.

### Hot-path cost
`handleBar` is profile-sensitive. Budget:

- **Per-bar update: ≤ 2 atomic stores + 1 atomic add.** Stored as `UnixNano int64` (same pattern as `lastBarTime`) keyed by a pre-computed `(strategyInstanceID, symbol)` pointer, no map lookup on the hot path.
- **Decision reason**: `atomic.Pointer[DecisionReason]` per (strategyID, symbol) — write only when a HOLD/SIGNAL fires, not every bar.
- **Sparkline bucket**: don't increment a minute-bucket slot on every bar. Increment a single atomic `evalCountTotal`; snapshot endpoint computes deltas. Phase 3 uses a 60-slot ring written at snapshot time, not hot path.
- **Map pre-sizing**: keys allocated once at instance-registration time, never re-allocated.

### SSE throttling: adapter or domain?
**Domain-level, inside `LivenessTracker`.**
- Throttle decision is per (strategy, symbol) — a property of the telemetry producer, not of any particular transport.
- Keeps SSE handler pure (broadcast-only).
- Implementation: tracker holds a last-emit timestamp; if < 1000ms since last emit for that key, drop the event. New `EventStrategyEvaluation` type appended to existing SSE handler's `eventTypes`.

### `lastDecisionReason`: structured or free-text?
**Structured.**
```go
type DecisionReason struct {
    At       time.Time `json:"at"`
    Outcome  string    `json:"outcome"`  // "HOLD" | "ENTRY" | "EXIT" | "SUPPRESSED"
    Summary  string    `json:"summary"`  // short human string
    Tags     map[string]string `json:"tags,omitempty"`
}
```
Strategies already produce structured `Tags` on signals — reuse the convention.

### Symbol-level feed health
`PipelineHealthReporter.LastProcessedAt(feedType)` is feed-level. Don't change the port in Phase 1.
- Phase 1: liveness endpoint returns `feedType` + `feedLastProcessedAt`; UI resolves symbol → asset class → feedType client-side.
- Phase 3: add new port method `LastProcessedAtSymbol(symbol string) time.Time` with fallback to feed-level.

---

## 2. Phase 1 — Polled liveness endpoint + pill + counters

### Backend

**Create `backend/internal/app/strategy/liveness.go`** (~150 lines):
```go
type LivenessTracker struct {
    entries map[*Instance]map[string]*livenessEntry
    mu      sync.RWMutex
}

type livenessEntry struct {
    lastTickAtNano   atomic.Int64
    lastEvalAtNano   atomic.Int64
    lastSignalAtNano atomic.Int64
    evalCount        atomic.Uint64
    barsTodayCount   atomic.Uint64
    signalCount      atomic.Uint64
    fillCount        atomic.Uint64
    dayKey           atomic.Int64
    reason           atomic.Pointer[DecisionReason]
}

func (t *LivenessTracker) RecordTick(inst *Instance, symbol string, at time.Time)
func (t *LivenessTracker) RecordEval(inst *Instance, symbol string, at time.Time, reason *DecisionReason)
func (t *LivenessTracker) RecordSignal(strategyID, symbol string, at time.Time)
func (t *LivenessTracker) RecordFill(strategyID, symbol string, at time.Time)
func (t *LivenessTracker) Snapshot(strategyID string) []SymbolLiveness
```

`livenessEntry` pointers are preallocated when the Runner registers an instance (search `Register` / `SetAssignment` paths in `runner.go` near line 2290) so `handleBar` does array/pointer atomics only.

**Modify `backend/internal/app/strategy/runner.go`:**
- Line 83 area: add `liveness *LivenessTracker` field.
- `NewRunner`/constructor: initialize.
- Line 1191 (`r.lastBarTime.Store`): additionally call `r.liveness.RecordTickBulk(symbol, loopStart)`.
- Line 1407 / 1554 / 1677 (after `safeOnBar`): call `t.RecordEval(inst, symbol, time.Now(), reasonFromSignals(signals))`.
- Line 1639 (emit signal): call `t.RecordSignal(strategyLabel, sig.Symbol, now)`.
- `handleFill`: call `t.RecordFill(...)`.
- New public method `func (r *Runner) Liveness(strategyID string) []SymbolLiveness`.

**Modify `backend/internal/adapters/http/strategy_perf_handler.go`:**
- Add case `"liveness"` at line 78 switch. New method `serveLiveness`. Returns:
```json
{
  "strategy": "avwap_reclaim",
  "symbols": [{
    "symbol": "AAPL",
    "lastTickAt": "...", "lastEvalAt": "...", "lastSignalAt": "...",
    "evalCount": 1203, "barsToday": 389, "signalCount": 2, "fillCount": 1,
    "feedType": "equity", "feedLastProcessedAt": "...", "feedHealthy": true,
    "lastDecision": {"at":"...","outcome":"HOLD","summary":"below VWAP"}
  }],
  "asOf": "..."
}
```
- Inject `PipelineHealthReporter` via the handler constructor — update `NewStrategyPerfHandler` signature and the call site in `backend/cmd/omo-core/http.go:226`.

**Types:** Add `SymbolLiveness`, `StrategyLiveness`, `DecisionReason` to new `backend/internal/domain/liveness.go`.

### Frontend

**Modify `apps/dashboard/hooks/queries.ts`:**
- Add `strategyLiveness: (id: string) => ["strategies", "perf", id, "liveness"] as const`.
- Add `useStrategyLiveness(id: string)` with `refetchInterval: 2_000`, `staleTime: 1_000`.
- Add `useStrategiesLiveness()` for the list page.

**Add `apps/dashboard/lib/types.ts`** entries: `SymbolLiveness`, `StrategyLiveness`, `DecisionReason`.

**Create `apps/dashboard/components/strategy/LivenessPill.tsx`:**
- Props: `lastTickAt`, `feedHealthy`.
- Logic: green if tick < 15s, amber 15-60s, red > 60s OR !feedHealthy, grey if never.
- shadcn `Badge` + ticking clock via `useEffect` setInterval(1000).

**Create `apps/dashboard/components/strategy/LivenessCounters.tsx`:**
- Grid row: Ticks | Bars | Signals | Fills. shadcn `Card`, `tabular-nums`.

**Modify list + detail pages:** render the two components per card / per-symbol rows.

### Phase 1 tests
- **Unit**: `liveness_test.go` — concurrent RecordTick/RecordEval, day rollover resets barsToday, snapshot ordering stable.
- **Unit (handler)**: extend `strategy_perf_handler_test.go` — `/api/strategies/{id}/liveness` returns 200 with expected shape; missing strategy returns empty symbols array.
- **Integration**: drive a bar through the runner, assert counters increment.
- **Frontend**: Playwright smoke on `/strategies` ensuring pill flips amber when API lastTickAt stale.

---

## 3. Phase 2 — SSE push + pulse-dot + decision reason

### Backend

**Add to `backend/internal/domain/event.go`:**
```go
EventStrategyEvaluation EventType = "StrategyEvaluation"
```

**Payload in `backend/internal/domain/liveness.go`:**
```go
type StrategyEvaluationPayload struct {
    Strategy     string          `json:"strategy"`
    Symbol       string          `json:"symbol"`
    At           time.Time       `json:"at"`
    EvalCount    uint64          `json:"evalCount"`
    BarsToday    uint64          `json:"barsToday"`
    LastDecision *DecisionReason `json:"lastDecision,omitempty"`
}
```

**Modify `LivenessTracker.RecordEval`:** after updating counters, check throttle (`lastEmitNano` per entry, 1000ms), then call injected `publisher func(domain.Event)`.

**Modify `backend/internal/adapters/sse/handler.go`:** append `domain.EventStrategyEvaluation` to `eventTypes` (line 23).

### Frontend

**Create `apps/dashboard/hooks/use-strategy-evaluation-stream.ts`:**
- Subscribes via existing `useEventStream({ eventTypes: ["StrategyEvaluation"] })`.
- Merges deltas into a keyed `Map<string, SymbolLiveness>`; polled hook seeds, SSE keeps fresh.

**Modify `LivenessPill.tsx`** — add animated pulse dot (CSS `@keyframes`) triggered by `lastEvalAt` changing.

**Create `apps/dashboard/components/strategy/LastDecision.tsx`:**
- `"2s ago: HOLD — below VWAP threshold"`. Uses `DecisionReason.outcome` + `.summary`.

### Phase 2 tests
- Throttle correctness (≤ 1 publish per 1000ms per key, first event immediate).
- Integration pub/sub round-trip via existing in-memory event bus.
- SSE handler forwards `StrategyEvaluation`.

---

## 4. Phase 3 — Sparkline, activity feed, data-source header

### Backend

**Extend `LivenessTracker`**: 60-slot ring buffer `barsPerMinute [60]uint32` per (strategyID, symbol), rotated by a background goroutine. Returned on `/liveness`.

**New port `backend/internal/ports/pipeline_health.go`:**
```go
type PipelineHealthReporter interface {
    LastProcessedAt(feedType string) time.Time
    LastProcessedAtSymbol(symbol string) time.Time
}
```

**New handler `/api/health/datasources`** — `backend/internal/adapters/http/datasource_health_handler.go`. May decorate existing `ServiceHealthChecker`.

### Frontend

- `BarsSparkline.tsx` — 60-pt inline SVG.
- `ActivityFeed.tsx` — expandable shadcn `Collapsible`, streams `StrategyEvaluation` + `StrategySignalLifecycle` + `FillReceived` events.
- `DataSourceHeader.tsx` — four health dots (IBKR, Alpaca, omo-data, DB) in `(dashboard)/layout.tsx`.

### Phase 3 tests
- Ring buffer rotation, off-by-one at minute boundaries, concurrent readers.
- Symbol→feed resolution fallback.
- Playwright screenshot diff.

---

## 5. Risks & open questions

1. **Backtest mode**: `handleBar` runs in offline backtest too. Liveness should be suppressed via a `DisableLiveness` flag alongside `suppressProgressEvents`.
2. **Day rollover timezone**: `barsToday` resets need ET (`domain.NYLocation()`). Crypto has no "today" — UTC 00:00 or skip.
3. **HOLD reasons don't exist today**: strategies return `[]Signal`; zero-length = HOLD with no reason. Phase 2 ships outcome-only; add optional `LastHoldReason(symbol) *DecisionReason` hook gradually.
4. **Counter persistence**: in-memory; restart zeroes. Accept. Prometheus remains durable.
5. **SSE fan-out cost**: N strategies × M symbols × 1Hz. Mitigate with server-side filter (`?strategies=`) or drop when no subscribers. Measure first.
6. **Instance key stability**: keying on `*Instance` pointer. Confirm Runner doesn't recreate instances on DNA hot-reload; else switch to `(strategyID, symbol)` string key.
7. **Counters row granularity**: card sums across symbols; detail page shows per-symbol.

---

## 6. Deliverable checklist

**go-architect**
- [ ] `backend/internal/app/strategy/liveness.go` + test
- [ ] Wire into `runner.go` at marked lines
- [ ] `backend/internal/domain/liveness.go` with types + new event constant
- [ ] Extend `StrategyPerfHandler` + update constructor; wire in `cmd/omo-core/http.go`
- [ ] Port extension in Phase 3

**dashboard-dev**
- [ ] `useStrategyLiveness` hook + types
- [ ] `LivenessPill`, `LivenessCounters`, `LastDecision`, `BarsSparkline`, `ActivityFeed`, `DataSourceHeader`
- [ ] SSE merge hook in Phase 2
- [ ] Mount into list + detail pages + dashboard layout

---

### Critical files
- `backend/internal/app/strategy/runner.go`
- `backend/internal/adapters/http/strategy_perf_handler.go`
- `backend/internal/adapters/sse/handler.go`
- `backend/cmd/omo-core/http.go`
- `apps/dashboard/hooks/queries.ts`
