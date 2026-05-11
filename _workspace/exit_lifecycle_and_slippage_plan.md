# Exit lifecycle + simbroker slippage fixes

Three independent fixes, each its own commit, ordered so the smallest blast
radius ships first.

## Phase B-revised — Exit lifecycle: clear ExitPending atomically on terminal fill

### Context
The partial-qty preservation fix shipped in commit 844065e5 lets copytrade
STCs / tiered-TP / time-partial exits correctly size to less than the full
position. SimBroker fills the partial intent qty in one shot. But
`processFill` (`backend/internal/app/positionmonitor/service.go:707-728`)
keeps `ExitPending=true` per a comment that assumes the broker order is
still working for the remaining quantity — wrong when the broker order
was deliberately partial-sized.

Symptom: "exit cancel never reached terminal — manual intervention
required" warnings on every partial. Subsequent STCs hit
`prior exit in flight — rejecting` in `handlers.go:246`. Position closes
via escalate ladder, not the strategy's intended STC sequence.

### Why the simpler in-processFill fix vs a new event
qa-inspector flagged an actor-ordering hazard in the original plan (emit
`EventExitOrderTerminal` from `runFillFinalization`): the position-monitor
`runTickLoop` select drains channels non-deterministically. If
exitTerminal lands before the matching fillReceived, `ExitPending` would
clear while `pos.Quantity` is still full — the tick loop could fire
another CLOSE_LONG (SOFI-1605 phantom-short pattern).

Simpler fix: thread `broker_order_id` (already in the FillReceived
payload at `service.go:2051`) through `fillMsg` and let `processFill`
clear `ExitPending` atomically with the qty decrement. Ordering-invariant.
No new event. No double-emit risk against the Alpaca REST reconcile path
flagged by code-reviewer.

### Files to change
- `backend/internal/app/positionmonitor/service.go` — add
  `BrokerOrderID string` to `fillMsg` (around line 115).
- `backend/internal/app/positionmonitor/handlers.go` — parse
  `broker_order_id` from FillReceived payload, pass through to fillMsg
  (around line 57-73).
- `backend/internal/app/positionmonitor/service.go` — in `processFill`
  partial-close branch (after `pos.Quantity -= fill.Quantity`, currently
  line ~707): if `fill.BrokerOrderID != ""` and
  `pos.PendingExitOrderIDs[fill.BrokerOrderID]` exists, delete it. If
  `len(pos.PendingExitOrderIDs) == 0`: clear `ExitPending=false`; if
  `pos.ExitOrderID == fill.BrokerOrderID`, also clear `ExitOrderID=""`;
  call `s.positionGate.ClearInflightExit` when `positionGate != nil`.
  Peer-working case (PendingExitOrderIDs still has other entries) leaves
  ExitPending untouched — preserves the SOFI-1605 single-ExitPending
  invariant.
- New tests: `backend/internal/app/positionmonitor/exit_terminal_fill_test.go`
  - Partial fill of partial-intent (qty=17 of 50, broker_order_id matches
    ExitOrderID, no peer): asserts ExitPending=false, ExitOrderID="",
    positionGate cleared, pos.Quantity=33.
  - Partial fill WITH peer working (PendingExitOrderIDs has another id):
    asserts ExitPending stays true, peer id retained.
  - Partial fill not matching ExitOrderID (peer order's fill): asserts
    pos.Quantity decremented, ExitOrderID untouched, PendingExitOrderIDs
    entry for the fill's id removed.
  - Full close (qty=50 of 50): existing behavior unchanged (position
    deleted via the `pos.Quantity <= 1e-9` branch).

### SOLID/KISS/DRY notes
- Single-responsibility: processFill already owns the position-state
  transition on fill events. Adding the ExitPending clear here keeps
  one source of truth instead of splitting across two handlers.
- KISS: no new event types, no payload schema additions, no feature
  flag — the change is internal to the position monitor.
- DRY: reuses the existing `broker_order_id` field already in the
  FillReceived payload. No duplicate channel plumbing.

### Acceptance criteria
1. `go build ./...` clean.
2. `go test ./internal/app/positionmonitor/... ./internal/app/execution/...`
   all green.
3. Re-run copytrade replay:
   `/tmp/omo-replay-bin --backtest --copytrade-history services/discord-copytrade/state/history_90d.jsonl --strategies copytrade_v1 --symbols AAPL,AMZN,BABA,BIDU,ENPH,FSLR,GLD,GOOGL,INTC,IWM,KWEB,MARA,MSFT,NIO,NVDA,ORCL,PDD,QQQ,RKLB,SLV,SPY,TSLA,TSM --from 2026-01-27 --to 2026-04-23 --config configs/config.yaml --env-file .env > /tmp/replay_b_revised.log 2>&1`
   then `grep -c "exit cancel never reached terminal" /tmp/replay_b_revised.log` returns 0 or near-zero (was >100 pre-fix).

### Halt conditions
- New tests fail and root-cause indicates a SOFI-1605-style invariant
  violation (any test asserting "single exit pending" semantics) — halt,
  Discord red.
- "exit cancel never reached terminal" count does NOT drop materially
  after the fix — halt, Discord red.

---

## Phase C-1 — Simbroker fills at min(live_ask, cap), not always at cap

### Context
Quant-analyst measured +11.3% median BTO slippage in the copytrade
backtest. Root cause is structural, not market drift: risk sizer at
`backend/internal/app/strategy/risk_sizer.go:944-959` pins paper fills
to `priceCap = ref_premium * (1 + 0.10)` and simbroker honors that cap
as the fill price unconditionally. In live IBKR, fills hit the actual
ask (often below cap). Current backtest is ~5-8% more pessimistic than
live per BTO. Predicted recovery: ~$74k of -$234k loss (32% edge
recovery) on the 90-day fixture.

### Files to change
- `backend/internal/app/strategy/risk_sizer.go` — in the paper-pinned
  BTO entry path (around line 944-959), write the chain's `best.Ask`
  into `intent.Meta["live_ask"]` alongside the existing priceCap.
  `intent.LimitPrice = priceCap` unchanged (preserves live-trading
  semantics: don't pay above cap).
- `backend/internal/adapters/simbroker/broker.go` — in
  `computeOptionEntryPrice` (around lines 1442-1495): if
  `intent.Meta["live_ask"]` is present and > 0, use
  `min(live_ask, intent.LimitPrice)` as the entry mid (instead of
  always using `intent.LimitPrice`); apply the half-spread tier to
  that. If `live_ask > intent.LimitPrice`, reject the fill — the order
  wouldn't fill in live either; let the BTO miss rather than fabricate
  a fill.
- `configs/strategies/copytrade_v1.toml` — under `[options]`, add
  `paper_fill_at_ask = true`. Default at engine level is `false` for
  backwards compatibility; on for copytrade.
- New test: a focused file under
  `backend/internal/adapters/simbroker/` (e.g.
  `option_paper_fill_test.go`) covering:
  - `live_ask < cap` → fills at `live_ask + half_spread`.
  - `live_ask == cap` → fills at `cap + half_spread` (current behavior).
  - `live_ask > cap` → no fill emitted (rejection signal — TBD: test
    asserts SubmitOrder returns an error OR no fill callback fires;
    implementation choice TBD by go-architect).

### SOLID/KISS/DRY notes
- Interface segregation: live_ask travels via the existing
  `intent.Meta` map — no new field on `OrderIntent`, no new port
  method. Adapter reads optional metadata, defaults to existing
  behavior if absent.
- KISS: one TOML knob, one branch in simbroker, one risk_sizer stash.
- DRY: reuses the existing half-spread tier function in
  computeOptionEntryPrice.

### Acceptance criteria
1. `go build ./...` clean.
2. `go test ./internal/adapters/simbroker/... ./internal/app/strategy/...`
   all green.
3. Re-run replay with `paper_fill_at_ask = true`:
   - median BTO slippage drops from +11.3% to <= +3.0% (quant's
     prediction: "+1-3%").
   - profit factor moves measurably toward 1.0 (was 0.20).

### Halt conditions
- Median slippage does NOT drop below +5% — halt, Discord red (the
  fix didn't address the right channel; need re-investigation).
- Replay errors or panics in computeOptionEntryPrice — halt, Discord red.

---

## Phase C-3 — Stale-signal age veto in copytrade

### Context
Quant's tail analysis: worst slippage outliers (slip > +20%) are
pre-RTH or overnight messages where the underlying gapped before our
fill bar. A 30-minute message-age cutoff cleanly removes them.
Expected ~3-5 BTOs skipped on the 90-day fixture. Minor equity impact
(~$5-10k) but improves Sharpe materially by trimming the tail.

### Files to change
- `backend/internal/app/strategy/builtin/copytrade_v1.go` — add a
  `MaxSignalAgeSecs int` field to the strategy config struct (around
  cfg.PartialFractions area, line ~71-95). In the BTO entry path
  (around line 265, `case "BTO":`), compute
  `age := ctx.Now().Sub(sig.PostedAt)`; if
  `cfg.MaxSignalAgeSecs > 0 && age > Duration(cfg.MaxSignalAgeSecs)*time.Second`,
  log a warning and return early (no signal emitted).
- `configs/strategies/copytrade_v1.toml` — under `[params]`, add
  `max_signal_age_secs = 1800`.
- New test in
  `backend/internal/app/strategy/builtin/copytrade_v1_test.go`:
  - Stale BTO (PostedAt 31 min before ctx.Now()) → no signal emitted,
    state unchanged.
  - Fresh BTO (PostedAt 5 min before ctx.Now()) → signal emitted, state
    advanced normally.
  - Zero-value config (max_signal_age_secs = 0) → veto disabled,
    behavior unchanged.

### SOLID/KISS/DRY notes
- One config knob, one branch, one test. No new abstractions.
- KISS: `0` disables — explicit, no magic sentinel.

### Acceptance criteria
1. `go build ./...` clean.
2. `go test ./internal/app/strategy/builtin/...` all green.
3. Pre-implementation: count BTOs in
   `_workspace/copytrade_replay/fills.csv` with
   `ts_filled - posted_at > 1800s` — expect 3-5 (quant's prediction).
4. Re-run replay with veto enabled — fewer trades, BTO count drops by
   the expected 3-5; PF should tick up; max drawdown should improve.

### Halt conditions
- More than 10 BTOs skipped (would indicate the cutoff is too
  aggressive for the data; revisit) — halt, Discord yellow.
- PF degrades after veto — halt, Discord red (the skipped trades
  were profitable; re-examine quant's recommendation).

---

## Final coordination

- Use `go-architect` for the backend implementation. Brief on each
  phase with the file:line refs above.
- After each phase, run the relevant tests; only proceed to the next
  phase when the current phase's acceptance criteria are met.
- Three commits, one per phase, so they can be rolled back
  independently.
- The pre-existing local TOML modification
  (`risk_per_trade_bps: 1000 → 2500`) is intentional user state — leave
  it; the test failure (`TestLoadSpecFile_CopytradeV1_ParsesCleanly`)
  is unrelated to this work.
- The pre-existing dirty files (CSVs in `_workspace/copytrade_replay/`,
  `configs/config.yaml`, `configs/backups/*`) are user state — leave
  them.

### Overall halt conditions (applies to all phases)
- Working tree state at start of a phase shows unexpected modifications
  outside the planned files — halt, Discord yellow, do not proceed.
- Any push to main attempted — halt, Discord red.
- Any force-push attempted — halt, Discord yellow.
