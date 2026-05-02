# Options Pipeline Fixes (post PR #67 live observation)

Status: DRAFT — pending acknowledgment
Drafted: 2026-05-01
Branch target: main
Related: PR #67 (indicator-single-driver fix), today's live trading session.

## Two independent bugs surfaced today

After PR #67 closed the 0-trade backtest regression and live restarted on the
merged binary, today's RTH produced 9 entry signals. Only 2 (SOXL ×3, MRVL)
reached `executed`. The other 7 were rejected upstream of IBKR at the
risk_sizer contract-selection step. Trace dump in `logs/omo-core.log`
between 08:35 and 09:10 CDT (= 09:35-10:10 ET).

Two of the 7 rejections + 1 exit reject expose latent bugs that are
**pre-existing** (not introduced by PR #67) but worth closing now that the
indicator parity gate is healthy and we can see them:

1. **Bug A** — Alpaca options chain fetch is **not retried** on transient
   network errors. AFRM avwap signal at 09:00 CDT today was lost to a single
   `read tcp ...: read: connection reset by peer` from data.alpaca.markets.
2. **Bug B** — Strategy-emitted **exit** signals on options strategies are
   not translated from the underlying symbol to the open contract symbol.
   risk_sizer's options-translation logic (entry-only) creates an
   `OrderIntent` with `Symbol = underlying` for exits, position_gate looks
   up positions by symbol and finds nothing because the open position is
   keyed by the option contract, exit drops with `position_gate:
   no_position_to_exit`. Live MRVL macd_only_v1 hit this at 10:10 ET today.

Both rejections are silent in the sense that no operator-facing alert fires
— they show up only in the strategy log + signal-rejected dashboard column.

## Non-goals

- Tightening the risk_sizer delta-band filters to better match real chains
  (separate quant-led tuning, not a bug fix).
- Closing the synthetic-vs-real options chain realism gap (architectural
  conversation; out of scope here).
- Re-routing position-monitor exit rules (they already work via option
  contract symbol; only strategy-side exits are broken).

## Bug A — options chain fetch retry

### Investigation finding

`backend/internal/adapters/alpaca/options_rest.go:163`:

    return nil, fmt.Errorf("alpaca: fetch option snapshots: %w", err)

The HTTP error from `httpDo` (line ~157) is wrapped and returned to
risk_sizer. risk_sizer treats the error as `errOptionsChainFailed`, falls
through to the "no equity fallback" branch at risk_sizer.go:642 (because
avwap_v4 has `asset_classes = ["OPTION"]`), and drops the signal entirely.
Today's exact error:

    read tcp 172.26.160.198:57956->34.86.145.125:443: read: connection reset by peer

This is a TCP RST from Alpaca's server side mid-response. Classic transient
network failure — should retry, not fail-hard.

### Fix shape

Add retry-with-backoff inside the Alpaca options snapshots fetch path so
transient errors don't drop the signal. Keep the failure path as-is for
non-transient errors.

Files:
- `backend/internal/adapters/alpaca/options_rest.go` — wrap the HTTP call
  at the snapshot fetch site (currently single attempt around line 157-168
  for the chain-aware path, and around line 252 for the direct path).

Approach:
- Three attempts total. Backoff: 100ms, 300ms, 900ms (jittered ±10%).
- Retry on: `net.OpError`, `io.EOF`, connection reset (`syscall.ECONNRESET`),
  context-deadline-exceeded, and HTTP 502/503/504. Do NOT retry on 4xx
  (bad request, no retry will help) or context-canceled.
- Total worst-case latency on three retries ≈ 1.3s + per-call timeout.
  Acceptable: signal evaluation already burns 0.5-2s on the chain fetch.
- Log each retry attempt at INFO with the attempt number and wait time so
  we can observe transient-failure rate in Loki.
- Add a `transient_retry_total` counter (component=alpaca, gauge type) so a
  Grafana panel can flag flaky periods.

Tests:
- New unit test in `backend/internal/adapters/alpaca/options_rest_test.go`
  that uses a `httptest.Server` returning RST on the first 1-2 calls and
  200 on the 3rd. Asserts that the call ultimately succeeds and the
  retry counter incremented twice.
- Existing options_rest tests stay green (single-shot success path
  unaffected).

Rejected alternative — caching: I considered caching the most recent
successful chain per symbol with a 30s TTL so a transient failure could
fall back to a slightly-stale chain. Adds state, adds staleness risk
(option deltas can shift across 30s in fast markets), and the retry path
already covers the common case. Skip.

### Risks

- Retries add latency on the path. For a strategy whose entry depends on
  the bar that just closed, an extra ~1s on a fail-then-succeed path is
  fine because the strategy's pending-entry timer is 5min. Three serial
  retries with backoff stay within that budget.
- A persistent Alpaca outage means we burn 3× the attempts before logging
  the final fail. Acceptable — we still log the original error and
  drop the signal exactly as today.

### Acceptance criteria

- [ ] New unit test passes.
- [ ] Existing alpaca tests green.
- [ ] After deploy, Loki query
      `{service="omo-core"} |= "options chain fetch failed"` shows the
      transient-retry counter is non-zero on a typical RTH day (proves the
      retry is firing in production).
- [ ] On a planned manual test (kill the network during a fetch via
      `tc qdisc add ... loss 100% `for 200ms then remove), at least one
      retry succeeds.

## Bug B — strategy exit signals don't translate to option contract

### Investigation finding

`backend/internal/app/strategy/risk_sizer.go:625-655`:

    if sigRef.SignalType == start.SignalEntry.String() &&
       spec != nil && spec.Options != nil && spec.Options.Enabled &&
       rs.optionsMarket != nil {
        err := rs.handleOptionsSignal(...)   // builds OrderIntent with contract symbol
        ...
    }

The options-translation branch is **gated on `SignalEntry`**. Exit signals
fall through to the equity path at line 671:

    intent, err := domain.NewOrderIntent(
        intentID,
        event.TenantID, event.EnvMode,
        domain.Symbol(sigRef.Symbol),   // <— underlying symbol
        direction,                       // CloseLong/CloseShort
        ...
    )

The intent reaches position_gate at execution/position_gate.go:54 with
`intent.Symbol = MRVL`. position_gate filters positions by
`p.Symbol == intent.Symbol` (line 85). The actual open position is
`MRVL260508C00162500`, never matches, gate returns `ErrNoPositionToExit`,
intent is rejected.

The position-monitor exit rules (PREMIUM_STOP, CHANDELIER_TRAIL,
STAGNATION_EXIT, EOD_FLATTEN) are NOT affected — they emit intents
already-keyed by contract symbol because position_monitor tracks open
positions by their actual contract symbol. Only the strategy-side exits
that come through risk_sizer's exit-signal handling are broken.

The strategies that emit underlying-bar exits today: avwap_v4
(price-action exits), macd_only_v1 (`bb_macd_stop_hit`). Both are options
strategies. Both currently lose their strategy-side exits silently in
live; option positions stay open until a position-monitor rule fires
(typically EOD_FLATTEN if no premium move triggers earlier).

### Fix shape

Mirror the entry-side options translation for exits. When risk_sizer
receives an exit signal and the strategy has options enabled, look up the
open option positions for that (tenant, envMode, underlying) tuple, and
emit OrderIntents keyed by the **contract** symbol.

Files:
- `backend/internal/app/strategy/risk_sizer.go` — add a parallel
  `handleOptionsExit` function. Hook it into the existing dispatch at
  the options-branch gate by relaxing the `== SignalEntry` predicate
  and routing entries to `handleOptionsSignal` and exits to
  `handleOptionsExit`.
- `backend/internal/ports/strategy.go` (or wherever `PositionLookup` is
  defined) — confirm the lookup function exposes underlying→contract
  resolution. If not, extend the port. Most likely `PositionMonitor`
  already tracks `Position.Underlying` in its in-memory map; expose a
  `LookupOpenContractsByUnderlying(symbol)` method.

Approach:
1. risk_sizer's options-exit branch calls a new
   `LookupOpenOptionPositionsByUnderlying(tenantID, envMode, underlying)`
   on the PositionLookup port.
2. For each open contract returned, emit one OrderIntent with
   `Symbol = contract.Symbol` and `Direction = CloseLong` (or
   CloseShort if the contract was a short position — equity options
   strategies today are all long contracts, so default to CloseLong).
3. Carry forward the strategy's exit rationale tag so the trade log
   shows `strategy:bb_macd_stop_hit` rather than `exit_monitor:*`.
4. If no open positions match, log once at INFO ("strategy exit signal
   for underlying X: no open option positions to close") and drop. This
   is the legitimate "stale exit signal" case.
5. Idempotency: tag the intent's idempotency key with
   `<entry_intent_id>-strategy-exit` so multiple bars firing the same
   exit signal don't double-close. Mirror what position_monitor does.

Tests:
- Unit test in
  `backend/internal/app/strategy/risk_sizer_options_exit_test.go` (new):
  - Setup: open option position MRVL260508C00162500 in a fake
    PositionLookup keyed by `Underlying = "MRVL"`.
  - Drive: SignalEnriched event type=exit symbol=MRVL.
  - Assert: OrderIntent emitted with `Symbol = MRVL260508C00162500`,
    `Direction = CloseLong`.
  - Negative case: no open position → no OrderIntent, single info log
    message, no error.
- Integration test (in `backend/internal/app/backtest/`) extending an
  existing slice-pipeline test: open an option position, fire a
  strategy-side exit, assert the contract closes. (May need a small
  fake position-monitor since the existing setup uses real exit rules.)

### Risks

- Multiple open contracts under one underlying: `cooldown_seconds=3600`
  on avwap_v4 plus `max_trades_per_day=2` make this rare in production
  but possible. Default behavior: emit close intent for ALL open
  contracts under that underlying. Reviewer-checked: this matches the
  live-broker semantics of "close all positions for symbol X."
- Race with position-monitor exits: if a CHANDELIER_TRAIL also fires the
  same bar, two close intents arrive for the same contract.
  position_gate's `TryMarkInflightExit` (already present) deduplicates.
  Verified at execution/position_gate.go:139.
- Equity fallback: avwap_v4 has `asset_classes = ["OPTION"]`. For a
  strategy that does have EQUITY in `asset_classes`, the existing
  equity-path code at line 671 stays correct. The new options-exit
  branch only fires when `spec.Options.Enabled` AND
  `optionsMarket != nil`. Equity-side exits unaffected.

### Acceptance criteria

- [ ] New unit + integration tests pass.
- [ ] Live MRVL macd_only_v1 `bb_macd_stop_hit` exit on the next live
      occurrence routes to the open contract and closes it (instead of
      `position_gate: no_position_to_exit`).
- [ ] No regression in position_monitor exit rules (PREMIUM_STOP,
      CHANDELIER_TRAIL, etc.) — those still emit contract-keyed intents
      directly and bypass the new branch.
- [ ] Live trade log on a future strategy-exit shows
      `rationale = strategy:bb_macd_stop_hit` (not `exit_monitor:*`),
      proving the exit-origin tagging carries through.

## PR plan

**PR 1: alpaca options chain retry** (~100 LOC + test)
- Single-file change to `options_rest.go` plus the unit test.
- Smallest possible scope; no protocol changes; no other component edits.
- Ship first because it's risk-free relative to PR 2 (bus / strategy
  routing untouched).

**PR 2: strategy-exit options translation** (~150-200 LOC + tests)
- `risk_sizer.go` (options-exit branch + dispatch update).
- `ports/strategy.go` or equivalent (extend PositionLookup with
  underlying→contracts resolver).
- `positionmonitor` adapter implementation of the new lookup method.
- Unit + integration tests.
- Land after PR 1 is green in live for 1 RTH session.

## Rollout

- Merge PR 1 → rebuild + restart live.
- Monitor: after 1 RTH session, confirm `transient_retry_total > 0` (the
  retry path actually fired) and no regression in other Alpaca calls.
- Merge PR 2 → rebuild + restart live.
- Monitor: next time a strategy-side exit fires (bb_macd_stop_hit or
  AVWAP price-action exit), confirm it closes the contract instead of
  rejecting at position_gate.
- If either fix surfaces a regression, revert via single-commit revert.

## Estimated effort

- PR 1: ~2 hours including tests.
- PR 2: ~5 hours including tests + manual integration verification.

## Open questions

1. For PR 2, when multiple open contracts exist under one underlying
   (rare today, but possible), do we close all or only the contract
   matching the strategy instance that emitted the exit? Default in
   plan is "close all" — confirm this matches your trading policy.
2. Should the retry counter from PR 1 also fire on Alpaca SIP equity
   fetches (same client, same vulnerability), or scope strictly to
   options snapshots? Default in plan is options-only; widening is a
   later PR.
