# Plan: Fix backtest fill-event delivery so strategy OnEvent runs with live state

Date: 2026-05-06
Driver: `_workspace/whale_pullback_v1_backtest_fill_event_finding.md`
Status: Plan, awaiting acknowledgment.

## 1. Problem (one sentence)

In sharded max-speed backtest, the strategy runner buffers SignalCreated
events (`runner.deferSignalPublish=true`) so the entire downstream chain
(SignalCreated -> SignalEnriched -> OrderIntentCreated -> SubmitOrder ->
FillReceived -> handleFill -> inst.OnEvent) does not run until PhaseA
has already mutated strategy state through every bar in the slab; by
the time fills arrive in `replayFlat`, `PendingEntry` has been cleared
by the OnBar 5-min timeout, so the FillConfirmation guard short-
circuits and strategy-level exits (body-close, ATR-stop) never run.

## 2. Where the bug lives

- Buffering: `backend/internal/app/strategy/runner.go:2280-2284`
  (`emitSignal` -> `pendingSignals` when `deferSignalPublish=true`).
- Two-phase replay: `backend/internal/app/backtest/slice_pipeline.go:170-348`
  (PhaseA in `RunSliceToCompletion` parallel goroutines, replay in
  `replayFlat`). PhaseA only calls `OnBar`/`DrainPendingSignals`; no
  Submit/Fill cascade runs.
- Strategy guard: `backend/internal/app/strategy/builtin/whale_pullback_v1.go:241`
  (`PendingEntry` cleared after 5 minutes of bar time) and
  `whale_pullback_v1.go:269` (`evalExit` only runs if
  `PositionSide!="" && PendingEntry==""`).

The legacy heap-dispatch path (`runner.go:1940+`) does NOT have this
bug because it publishes events synchronously through the frozen sync
bus inline with each `MarketBarReceived` publication.

## 3. Goals / success criteria

a. In sharded backtest, `inst.OnEvent(FillConfirmation)` runs BEFORE
   the strategy's next `OnBar` for the same symbol fires. Verified by
   a one-symbol golden test that asserts: signal at bar T => fill +
   OnEvent observed before OnBar at bar T+1.

b. `whale_pullback_v1` body-close and ATR-stop exits actually fire in
   sharded backtest. Verified by a parameter-sensitivity test:
   `exit_body_closes={1,2,3}` and `atr_stop_mult={1.0, 2.0, 5.0}` must
   produce three DIFFERENT trade-count / pnl curves on a fixed window.

c. Sharded vs legacy heap path produce byte-identical results on a
   representative whale_pullback run (existing parity harness).

d. Live behavior unchanged. The fix is gated to backtest paths only.

## 4. Approach (recommended): synchronous in-shard fill loop

The shard's runner gets a hook that, after `OnBar` returns an entry
or exit signal, dispatches the signal through the risk-sizing +
broker + fill cascade synchronously inside the shard's goroutine, and
routes the resulting `FillConfirmation` directly to `inst.OnEvent`
before the shard reads the next bar. The downstream domain events
(SignalCreated, SignalEnriched, OrderIntentCreated, FillReceived) are
still buffered with `barIdx` and published in tick order during
`replayFlat`, so collector / signal_tracker / pnl observers see the
same stream they see today.

Concretely:

- Add a small "in-shard executor" struct that holds: a
  signal-passthrough enrichment closure, a synchronous risk-sizing
  call (`risk_sizer.SizeIntentForSignal`, new method, no bus), and a
  direct call into `simbroker.SubmitOrder` -> typed `FillConfirmation`.
- The shard's runner gains a callback `OnSignalEmitted(signal)` that
  the shard wires to that executor after `OnBar` returns. Inside the
  callback the executor:
    1. builds the enriched + intent in memory
    2. calls `simbroker.SubmitOrder` (already mu-locked, shard-safe)
    3. constructs a `FillConfirmation` and calls `inst.OnEvent`
       through the runner's existing event dispatch path
    4. records the corresponding `OrderIntentCreated` and
       `FillReceived` domain events into the shard's deferred buffer,
       tagged with the current `barIdx`, so `replayFlat` can publish
       them to the bus in tick order.
- `replayFlat` is taught that signals tagged "already filled" must
  NOT be re-routed through the live execution chain. The cleanest
  way: instead of publishing `SignalCreated` and letting the bus
  cascade fire `SubmitOrder` again, publish `SignalCreated` ->
  `SignalEnriched` -> `OrderIntentCreated` -> `FillReceived` from the
  shard's buffered stream directly, with the execution service
  recognizing pre-filled intents (idempotency key + a "pre-filled"
  flag on `OrderIntentCreated`) and treating its handler as a no-op.
  Alternative: skip publishing `OrderIntentCreated` entirely and
  publish only `FillReceived` from the buffer; collector and
  signal_tracker subscribers are confirmed to NOT depend on
  `OrderIntentCreated`.

## 5. Alternative fixes considered

- B. Strategy-level patch (relax `if wp.PendingEntry != ""` guard to
  trust fill side). Quick but does NOT fix the deeper symptom: OnBar
  in PhaseA still runs across all bars with `PositionSide==""`, so
  body-close and ATR-stop exits still never fire in backtest. Useful
  only as a backstop.

- C. Force whale_pullback (and similar) onto the legacy heap path.
  Slow (5-10x), kills tuning velocity.

- D. Per-shard simbroker partition. Heavier rewrite; cash/equity
  reconciliation across shards is fragile. Rejected.

The recommended approach (Section 4) keeps the shard parallelism for
the heavy work (OnBar, indicator updates) and only inlines the
relatively rare signal -> fill round-trip.

## 6. Files to touch

backend/internal/app/strategy/runner.go
  - Add `OnSignalEmitted` callback and a runner-level
    `DispatchInlineFill(sym, FillConfirmation)` helper.
backend/internal/app/strategy/risk_sizer.go
  - Extract sizing logic into a pure `SizeIntentForSignal(signal,
    enrichment) (OrderIntent, ok)` callable without the bus.
backend/internal/app/backtest/pipeline_shard.go
  - Wire the in-shard executor (signal -> intent -> SubmitOrder ->
    FillConfirmation) into each shard's runner.
backend/internal/app/backtest/slice_pipeline.go
  - Augment `shardEmission` to carry `OrderIntentCreated` /
    `FillReceived` events alongside `SignalCreated`. Update
    `replayFlat` to publish the buffered fill events without re-
    triggering execution.
backend/internal/app/execution/service.go
  - Add a "pre-filled" guard on `handleIntent` so a second
    publication of an already-simulated intent is a no-op. (Defensive
    only; preferred design publishes only `FillReceived`.)
backend/internal/adapters/simbroker/broker.go
  - No code change expected. Verify `SubmitOrder` is safe under
    concurrent shard goroutines (it is — `b.mu.Lock`).
backend/internal/app/strategy/builtin/whale_pullback_v1.go
  - No code change required. The fix is upstream.

## 7. Tests

a. New unit test:
   `backend/internal/app/backtest/runner_inline_fill_test.go`
   - 1 symbol, 5 1m bars, hand-built strategy that emits an entry on
     bar 1 and an exit on bar 3.
   - Assert: `OnEvent(FillConfirmation)` for bar 1's entry runs
     before `OnBar` for bar 2; exit OnEvent runs before bar 4 OnBar.
   - Assert: `OrderIntentCreated` and `FillReceived` events appear
     on the bus in tick order (bar 1 fill before bar 2 bar event).

b. New parity test:
   `backend/internal/app/backtest/whale_pullback_exit_sensitivity_test.go`
   - Run sharded backtest on 1 symbol, 5 trading days, 3 sets of
     `exit_body_closes` (1, 2, 999) and 3 of `atr_stop_mult` (1.0,
     2.0, 100.0).
   - Assert: trade counts and total pnl differ across the 9
     combinations (today they're identical). Acceptable variance
     >1% pnl between any two distinct settings.

c. Existing parity harness: re-run
   `backend/internal/app/backtest/runner_warmup_parity_test.go` and
   any sharded-vs-legacy parity suites; expect them to still pass
   under the new path.

d. Manual reproduction: re-run pass-1 tune
   (`avwap_v4_equity_tune` workspace style but for whale_pullback);
   expect the exit-related parameters to start moving the objective.

## 8. Verification commands (post-implementation)

- `cd backend && go test ./internal/app/backtest/... ./internal/app/strategy/...`
- `cd backend && go test ./internal/app/backtest -run TestSlicePipelineParity`
- Single-strategy 1-month backtest of whale_pullback, compare
  trade-log line counts before vs after for `atr_stop_mult=1.0`
  vs `atr_stop_mult=100.0`. Different => fixed.

## 9. Blast radius

- Backtest sharded path only.
- Affects every strategy that uses the PendingEntry -> PositionSide
  pattern: confirmed users today are `whale_pullback_v1`,
  `break_retest_v1`, `avwap_v1`, plus any new strategy following
  the same handshake.
- Tuning runs: numbers will change. Existing tuned configs (avwap_v4
  equity, whale_pullback pass-1) should be re-validated; expect the
  exit-knob axes to start producing real gradients.
- Live trading: untouched. Risk sizer + execution remain bus-driven
  in paper/live; only backtest shards add the inline path.

## 10. Risks and mitigations

- Risk: double-counting fills (broker simulates fill twice if
  replayFlat re-publishes OrderIntentCreated).
  Mitigation: replayFlat publishes only `FillReceived` from the
  buffer for in-shard-simulated intents; defensive
  `pre-filled` guard in `execution.handleIntent` as belt-and-braces.

- Risk: cash/equity/position drift between sharded and legacy paths.
  Mitigation: parity test (Section 7c) gates any merge.

- Risk: ordering bugs across shards (e.g., shard A fill must be
  visible to shard B's exit logic on the same tick).
  Mitigation: simbroker is global and mu-locked; shard exits run
  inside the same shard goroutine for the same symbol, so cross-
  symbol ordering is irrelevant. Cross-symbol position reads (rare:
  hedging strategies) already use the shared simbroker view.

- Risk: subtle regressions in `signal_tracker` / `pnl_aggregator` if
  they expected to see `OrderIntentCreated` before `FillReceived`.
  Mitigation: audit subscriptions; both are confirmed
  `FillReceived`-only consumers today (per `signal_tracker.go:47`
  and `ledger_writer.go`).

## 11. Out of scope

- Per-strategy fallback patches to whale_pullback / break_retest /
  avwap (they will Just Work after the engine fix).
- Live trading path changes.
- Slip / spread / market-impact modeling changes.
- Re-tuning whale_pullback (separate follow-up after the engine fix
  lands).

## 12. Acknowledgment requested

This is an engine-level change with measurable blast radius on every
backtest. Asking for explicit go-ahead before touching code, plus
confirmation that the recommended approach (Section 4) is preferred
over the strategy-level workaround (Section 5 / Option B).

## Postscript -- 2026-05-06

PR #89 landed the engine fix. whale_pullback_v1 was re-backtested
against live exits and shelved (PF 0.388 train / 0.450 holdout with
no recoverable parameter slice). See
_workspace/whale_pullback_v1_sunset_note.md for the post-fix verdict.
The fill-event fix itself is orthogonal infrastructure: it benefits
every strategy going forward.
