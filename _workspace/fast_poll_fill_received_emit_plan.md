# Fast-poll FillReceived emit gap — fix plan

## Problem

Entry-side broker fills that come through the fast-poll branch persist to the
trades table but never publish `domain.EventFillReceived`. Five subscribers
silently miss the entry: copytrade ghost-position confirmation, position
monitor `processFill` (entry registration), revaluator pending-thesis claim,
liveness tracker, perf ledger writer, signal_tracker, copytrade replay ledger,
backtest collector, SSE fan-out.

Real-world incident (2026-05-04): SPY 5/07 724C BTO from `TradingTheTrend`
filled on IBKR (12 contracts at 09:39:12). The fast-poll path detected the
fill via `livePos`, called `recordFillsFromExecHistory`, and returned. No
`FillReceived`. The copytrade ghost stayed `Pending=true`. 90 minutes later
the STC arrived; `expireStalePending` evicted the ghost (TTL=120s, age=5427s);
STC saw no prior BTO and dropped. The 12 contracts are still open at IBKR
with no strategy-side knowledge.

## Root cause

`backend/internal/app/execution/service.go` has three persist paths and
parallel emit logic in two of them, none in the third:

| Path | Persist | Emit FillReceived | Site |
|---|---|---|---|
| Slow poll | `handleFill` | YES | service.go:1562 |
| WS stream | `emitPartialFillReceived` (per-leg) + aggregate finalizer | YES | service.go:2026, 2125 |
| Fast poll | `recordFillsFromExecHistory` -> `insertFillLeg` | **NO** | gap |

`fastPollPosition` claims the pending order via `LoadAndDelete` (atomic), so
when fast-poll wins the race the WS-stream path can't emit either. Single
point of emit failure.

## Design (Variant C — unified emit site)

Funnel all three paths through one helper. `finalizeOrderFill` owns:
1. `LoadAndDelete` claim of the pending order (race-safe by construction).
2. Cumulative payload assembly (one place to evolve the contract).
3. Single `eventBus.Publish(EventFillReceived, ...)`.

Per-leg `emitPartialFillReceived` stays as-is — it's a different semantic
(in-progress partial vs terminal). Aggregated emit per ORDER is what
subscribers expect, per qa-inspector audit (5 of 9 subscribers double-count
or false-page on per-leg events).

### Files touched

- `backend/internal/app/execution/service.go` — extract `finalizeOrderFill`,
  refactor `handleFill`, `recordFillsFromExecHistory` (called from fast-poll),
  and the WS-stream aggregate finalizer (~line 2080-2125) to delegate to it.
- `backend/internal/app/execution/service_test.go` — new test:
  `TestService_FastPollFillEmitsFillReceived`.
- `backend/internal/app/strategy/runner_copytrade_reentry_test.go` (or new
  test file) — integration test: simulate fast-path BTO fill end-to-end and
  assert copytrade ghost flips `Pending=false`.

Net diff target: <120 LOC across ~3 files. Net code reduction expected
because `handleFill` shrinks when its 80-line payload assembly moves into
the helper.

### Helper signature

```
func (s *Service) finalizeOrderFill(
    ctx context.Context,
    po *pendingOrder,
    brokerOrderID string,
    legs []ports.FillRecord,    // empty for slow-path; cum reconstructed from intent
    submitStart time.Time,      // zero allowed; only used for metric latency
    l zerolog.Logger,
) error
```

Inside:
1. Walk legs and call `insertFillLeg` per leg (or fall through to
   `insertFillLeg` once with execID="" for slow-path).
2. Compute `cumQty`, `cumAvgPrice` from legs (or use intent values if legs
   is empty).
3. Build the cumulative `fillPayload` (option meta, MFE/MAE, signal tags,
   regime/vix/market_context — same shape as today's `handleFill`).
4. `s.emit(ctx, domain.EventFillReceived, ...)`.
5. Bump fill metrics.

### Race semantics (preserved)

- Today: WS-stream and fast-poll both call `LoadAndDelete(brokerOrderID)`.
  Whoever wins the race persists. Slow-path `pollForFill` deletes via
  defer.
- After change: same pattern. `finalizeOrderFill` is called by whichever
  path won the claim. The claim happens before the helper, so emit fires
  exactly once.

### What does NOT change

- `emitPartialFillReceived` (WS partial leg) — different semantic, different
  payload (`partial=true`), different consumer expectations.
- Sweep path emit (service.go:2668) — unrelated reconciler path.
- `EventFillReceived` payload contract — same keys, same types.

## Success criteria

1. New unit test reproduces the bug pre-fix (`FAIL`) and passes post-fix.
2. Existing tests in `./internal/app/execution/...`,
   `./internal/app/strategy/...`, `./internal/app/positionmonitor/...`,
   `./internal/app/perf/...`, `./internal/app/backtest/...` all green.
3. Manual verification on next live BTO: log line `order filled — trade
   persisted and FillReceived emitted` appears for fast-poll fills (today
   it does not).
4. Copytrade strategy log shows `BTO fill confirmed` (today it never has
   in this session's logs — `grep -c` returned 0).

## Blast radius

- Affected: every fill that takes the fast-poll branch starts publishing
  `FillReceived`. That's the dominant path on IBKR live (200ms ticker beats
  the 5s slow poll and the WS-stream race most of the time).
- Subscribers that were already correctly handling other emit paths will
  start receiving fills they were missing — no misbehavior expected (per
  qa-inspector audit).
- No DB schema changes. No event payload contract changes. Pure
  control-flow refactor.

## Rollback

`git revert` of the fix commit. No migrations to undo.

## Test plan (TDD order)

1. RED: write `TestService_FastPollFillEmitsFillReceived` driving
   `fastPollPosition` -> `recordFillsFromExecHistory` and asserting an
   `EventFillReceived` was published. Fails on current main.
2. GREEN: extract `finalizeOrderFill`, route fast-poll through it. Test
   passes.
3. REFACTOR: route `handleFill` and WS-aggregate finalizer through the
   same helper. Existing tests stay green.
4. INTEGRATION: write the copytrade-fast-path test that proves the ghost
   flips. Fails on a tree without step 2; passes after.

## Out of scope

- Reconciling the 12 open SPY 5/07 724C contracts from this morning's
  incident — that's a manual close.
- Adding a per-leg `EventPartialFillReceived` event type. Could clean up
  semantics but is a separable refactor.
- Changing `emitPartialFillReceived` payload to include the full enrichment
  block. Subscribers either don't read those keys on `partial=true` or
  defer to the aggregate.

## Sign-off needed before first Edit

User to confirm:
- Variant C (unified helper) over Variants A (per-leg) / B (aggregated-only-at-fast-poll-site).
- TDD order acceptable (RED test first, then refactor).
- Branch name: `fix/fast-poll-fill-received-emit`.
