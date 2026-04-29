# LLY 850P "premium exhausted" + $84K phantom P&L — fix plan (2026-04-28)

## Incident

`omo-core` notification stream, 12:04 ET 2026-04-28:

    [12:04:01] Exit Submitted: CLOSE_LONG LLY260508P00850000 @ $824.56
      Rationale: PREMIUM_STOP: premium exhausted (entry=25.66, est=0.00, threshold=10%)
    [12:04:12] Exit Submitted: CLOSE_LONG LLY260508P00850000 @ $24.01
      Rationale: PREMIUM_STOP: repeg 1/1
    [12:04:24] Exit Submitted: CLOSE_LONG LLY260508P00850000 @ $869.37
      Rationale: PREMIUM_STOP: escalate-to-market
    [12:04:24] P&L: +$84,335.30 (+3241.55%) | Entry: $26.02 | Held: 4m

Three exit submissions in 23s, two of them carrying the LLY *underlying* spot
price as if it were the option premium, plus a fictitious +$84,335 P&L.

The position closed (probably profitably or near-flat). What's broken is
(a) the spurious PREMIUM_STOP trigger, (b) the underlying-price leak into the
option order's limit price, and (c) the fictitious P&L recorded in the
ledger.

## Root cause chain (verified from code)

### Layer 1 — `EstimatedPremium` returns 0 because `delta_at_entry` missing

`backend/internal/domain/exit_rule.go:390-451`

```go
entryPremium, ok1 := mp.CustomState["option_premium"]
delta, ok2          := mp.CustomState["delta_at_entry"]
if !ok1 || !ok2 || entryPremium <= 0 {
    return 0
}
```

The early-return requires `delta_at_entry` even when the full BSM input set
(`strike`, `expiry_unix`, `iv_at_entry`, `is_call`) is present. `delta` is
only consumed by the legacy delta-linear fallback at line 446.

`bootstrap.go:170-197` (engine restart with open option positions): restores
`option_premium` and `EntryPrice` only — **never** restores `delta_at_entry`,
`strike`, `expiry_unix`, `iv_at_entry`, or `is_call`. After every restart,
every still-open option position falls through to `return 0`.

### Layer 2 — `evaluatePremiumStop` misfires on `est == 0`

`backend/internal/app/positionmonitor/evaluators.go:1079-1083`

```go
estPremium := pos.EstimatedPremium(currentPrice, now)
if estPremium <= 0 {
    return true, "premium_stop: premium exhausted (entry=%.2f, est=0.00, threshold=%.0f%%)"
}
```

Treats "BSM unavailable" identically to "option went to zero". After a
restart this fires on the very first tick for every options position.

### Layer 3 — `triggerExit` silently uses the underlying as the order price

`backend/internal/app/positionmonitor/exit_eval.go:736-754`

```go
priceForOrder := currentPrice           // underlying spot
if pos.InstrumentType == domain.InstrumentTypeOption {
    if est := pos.EstimatedPremium(currentPrice, now); est > 0 {
        priceForOrder = est
    }
    // est == 0: priceForOrder STAYS as the underlying spot
}
exitPrice, orderType, tif := s.exitOrderParams(rule.Type, priceForOrder, ...)
```

When BSM returns 0, `priceForOrder` stays at the underlying spot. The
underlying then flows into `exitOrderParams` and becomes the "limit price"
for the option order.

### Layer 4 — `exitOrderParams` 5%-buffer fallback runs on the underlying

`backend/internal/app/positionmonitor/exit_eval.go:1058-1063`

```go
return currentPrice * (1 - buf), "limit", optionTIF("ioc")
```

When the option NBBO quote is missing or rejected (stale, blown spread, etc),
the fallback multiplies "currentPrice" by 0.95. With the underlying leaking
in: `$868 * 0.95 = $824.56` — exactly Event 1's displayed price.

### Layer 5 — `handleExitTimeout` retries with `livePos.EntryPrice`

`backend/internal/app/positionmonitor/exit_eval.go:457, 497`

```go
s.triggerExit(livePos, rule, reason, livePos.EntryPrice, s.nowFunc())
```

Re-peg and escalate paths pass `livePos.EntryPrice` as `currentPrice`.
For options, `EntryPrice` is the **underlying** entry price (the option
premium is in `CustomState["option_premium"]`). So the retry path also
hands an underlying-magnitude number to `triggerExit` — and with BSM still
broken, the same Layer 3 leak repeats.

For market escalate, `exitOrderParams` returns `currentPrice` directly
(line 1042). That's how Event 3 surfaces $869.37 as the order's limit
price even though `OrderType=market`.

### Layer 6 — `insertFillLeg` falls back to `intent.LimitPrice` for the trade row

`backend/internal/app/execution/service.go:1694`

```go
legPrice := firstPositive(update.Price, update.FilledAvgPrice, po.intent.LimitPrice)
```

When the broker stream's per-fill price is empty (ibsync race, or fastPoll
fallback), the recorder substitutes the **intent's** limit price. With the
intent's limit price already corrupted to an underlying-magnitude number
(Layer 3+5), the trades table records ~$869 as the option's "fill price".

Same pattern at execution/service.go:1248-1259, :1396, :1424 — every
fallback writes intent.LimitPrice into the trade row.

### Layer 7 — `ledger_writer.processFill` multiplies by 100x

`backend/internal/app/perf/ledger_writer.go:265`

```go
fillPnL = (price - entryPrice) * sellQty * multiplier   // multiplier=100 for OCC
```

`($869.37 - $26.02) * 1 * 100 = $84,335.30` — exact match.

The actual broker fill almost certainly happened near $24 (IBKR ignores the
limit on a market order and fills at the inside). But the fill's `Price`
field came back zero/missing, so we wrote `intent.LimitPrice = $869.37`
into the trades row. Ledger then computes a fake +3241% P&L from the
contaminated row.

## Why $24.01 was correct on the middle event

Event 2 (`repeg 1/1`) shows $24.01 — a real option-premium-magnitude price.
That's because on repeg the option NBBO quote was available, and
`buildExitLimitPrice` ignores `currentPrice` entirely — it computes the
limit from `quote.Bid/Ask` directly. The only path that surfaces the
underlying contamination is the fallback path (no quote / stale / blown
spread).

So the Layer-3 leak is *latent* whenever the live quote is healthy. The
LLY case caught Event 1 and Event 3 in the no-quote / market-escalate
fallback windows.

## The fix — five surgical changes

All in existing files, no schema changes.

### Fix 1 — `EstimatedPremium`: drop the `delta_at_entry` precondition

`backend/internal/domain/exit_rule.go:390-451`

Make BSM the primary path, fallback to delta-linear only when BSM inputs
are incomplete *and* delta is present. Single early-return on
`option_premium <= 0`.

```go
entryPremium, ok := mp.CustomState["option_premium"]
if !ok || entryPremium <= 0 {
    return 0
}
spreadCost := spreadCostForPremium(entryPremium)

strike, hasStrike     := mp.CustomState["strike"]
expiryUnix, hasExpiry := mp.CustomState["expiry_unix"]
ivAtEntry, hasIV      := mp.CustomState["iv_at_entry"]
isCallVal, hasRight   := mp.CustomState["is_call"]

if hasStrike && hasExpiry && hasIV && hasRight && strike > 0 && ivAtEntry > 0 {
    // unchanged BSM path
}

// fallback: legacy delta-linear, requires delta
delta, ok := mp.CustomState["delta_at_entry"]
if !ok {
    return 0
}
underlyingMove := currentUnderlyingPrice - mp.EntryPrice
est := entryPremium + delta*underlyingMove - spreadCost
...
```

This keeps existing behaviour intact when both delta and BSM inputs are
present, and unblocks BSM when only delta is missing.

### Fix 2 — `bootstrap.go`: restore BSM inputs from the orders row

`backend/internal/app/positionmonitor/bootstrap.go:170-197`

After a restart, every still-open option position needs the full BSM input
set so `EstimatedPremium` works. The orders row already carries strike,
expiry, and option_right. IV and delta need a re-source — options are
priceable without delta now (Fix 1) but IV is essential for BSM.

Two-tier restore:

(a) From the orders row: `strike`, `expiry_unix`, `is_call`. These are
    already on the order (`orders.strike/expiry/option_right`).
(b) From the live IBKR options chain at boot, for each open contract:
    fetch IV. Falls back to a fixed default (e.g. 0.30) if the chain is
    unavailable. Tag `pos.CustomState["iv_at_entry"] = chainIV` and log a
    warning that IV was re-sourced post-restart.

For `delta_at_entry`: not strictly needed once Fix 1 lands. Optional to
re-source from the chain.

Sites to update: the existing bootstrap loop at `bootstrap.go:160-198`.
Add a `restoreOptionBSMInputs(pos, brokerOrder, optionsPort)` helper that
populates `CustomState` from order metadata + a one-shot chain fetch.

### Fix 3 — `triggerExit`: refuse to ship a limit when premium is unknown

`backend/internal/app/positionmonitor/exit_eval.go:736-778`

When `EstimatedPremium` returns 0 AND no live quote is available, do not
fall through to "currentPrice * 0.95". The current behaviour silently
sends a 30x-inflated limit to the broker. Three options ranked by safety:

(a) **Preferred**: jump straight to market on this attempt. Set
    `retryCount` so `exitOrderParams` returns `(currentPrice, "market",
    "ioc")` — and pass through a *premium-magnitude* price so the
    "limit_price" telemetry on the OrderIntent stays sane (use
    `pos.CustomState["option_premium"]` as the best estimate when
    EstimatedPremium fails — it's the entry premium, but at least it's
    in the right order of magnitude).

(b) Skip this attempt — log a warning and let the next tick re-evaluate.
    Risk: a real premium collapse could still happen and we'd be sitting
    on it.

(c) Hard-block submission with an error event. Risk: position stays
    exposed indefinitely if the failure mode is permanent.

I'd ship (a). Concretely:

```go
isOption := pos.InstrumentType == domain.InstrumentTypeOption
priceForOrder := currentPrice
quote := tryFetchQuote(...)  // unchanged

if isOption {
    est := pos.EstimatedPremium(currentPrice, now)
    switch {
    case est > 0:
        priceForOrder = est
    case quote != nil && quote.Bid > 0:
        priceForOrder = (quote.Bid + quote.Ask) / 2.0
    case pos.CustomState["option_premium"] > 0:
        // Last resort: use entry premium as the magnitude anchor and
        // force market routing so the limit doesn't matter.
        priceForOrder = pos.CustomState["option_premium"]
        forceMarket = true
    default:
        // No way to compute an option-magnitude price. Refuse to submit.
        s.log.Error().Str("symbol", string(pos.Symbol)).
            Msg("triggerExit: cannot price option exit — skipping attempt")
        pos.ExitPending = false
        return
    }
}
```

Add `forceMarket` to the `exitOrderParams` call and short-circuit to
market routing when it's set.

### Fix 4 — `evaluatePremiumStop`: distinguish "BSM unavailable" from "premium=0"

`backend/internal/app/positionmonitor/evaluators.go:1079-1083`

Without Fix 1, this evaluator misfires on every restart. Even with Fix 1,
defence in depth: if `EstimatedPremium` returns 0 *and* the BSM input set
is incomplete, treat as "no signal" rather than "premium exhausted".

```go
estPremium := pos.EstimatedPremium(currentPrice, now)
if estPremium <= 0 {
    if !pos.HasBSMInputs() {
        return false, ""  // can't tell — don't fire
    }
    return true, "premium_stop: premium exhausted ..."
}
```

Add `MonitoredPosition.HasBSMInputs()` returning true only when all of
`strike`, `expiry_unix`, `iv_at_entry`, `is_call` are present.

### Fix 5 — `insertFillLeg`: refuse intent.LimitPrice fallback when broker price is empty

`backend/internal/app/execution/service.go:1694`

```go
legPrice := firstPositive(update.Price, update.FilledAvgPrice, po.intent.LimitPrice)
```

For options specifically, `intent.LimitPrice` can be a contaminated
underlying-magnitude number. Three changes:

1. Drop `intent.LimitPrice` from the firstPositive chain. If both
   `update.Price` and `update.FilledAvgPrice` are zero, defer the leg
   record — request a `ReqFills` at next reconciler tick rather than
   commit a guessed price.

2. Add an order-of-magnitude sanity check: for option legs, if
   `legPrice > 5 * pos.CustomState["option_premium"]` (5x the entry as a
   conservative ceiling), reject the leg as suspicious and log an
   `EventCopytradeOrphanFill`-style alert. Premium can move 200-300%, but
   not 30x.

3. Apply the same to the four sister sites: service.go:1248-1259,
   :1396, :1424.

This is the structural backstop. Even if Layers 1-4 ever leak again, the
trade row and ledger stay intact.

## Files touched

| File | Change |
|---|---|
| `backend/internal/domain/exit_rule.go` | Fix 1 — drop `delta_at_entry` precondition; add `HasBSMInputs()` |
| `backend/internal/app/positionmonitor/bootstrap.go` | Fix 2 — restore strike/expiry/iv/is_call on bootstrap |
| `backend/internal/app/positionmonitor/exit_eval.go` | Fix 3 — refuse underlying-leak in triggerExit; add forceMarket path |
| `backend/internal/app/positionmonitor/evaluators.go` | Fix 4 — distinguish BSM-unavailable from premium=0 |
| `backend/internal/app/execution/service.go` | Fix 5 — drop intent.LimitPrice fallback in fill recorders + magnitude check |
| `backend/internal/app/positionmonitor/exit_eval_test.go` | Tests for Fixes 1, 3, 4 |
| `backend/internal/app/positionmonitor/bootstrap_test.go` | Test for Fix 2 |
| `backend/internal/app/execution/service_test.go` | Test for Fix 5 magnitude reject |

Estimated diff: ~250 LOC added, ~30 modified.

## Test plan

Unit tests:

1. `EstimatedPremium` with full BSM inputs but no `delta_at_entry` returns
   the BSM price (currently returns 0).
2. `EstimatedPremium` with only `option_premium` and `delta_at_entry`
   returns the legacy delta-linear estimate (no regression).
3. `bootstrap.restoreOptionBSMInputs` populates strike/expiry/is_call from
   a `BrokerOrder` row; `iv_at_entry` populated from a stub
   `OptionsPricePort`.
4. `evaluatePremiumStop` returns `(false, "")` when BSM inputs missing
   AND `est == 0`; returns `(true, ...)` only when BSM inputs are present
   and `est <= 0`.
5. `triggerExit` with options + `est == 0` + no quote: emits MARKET intent
   with `LimitPrice == option_premium` (sanity-magnitude), not
   `currentPrice` (underlying).
6. `insertFillLeg` with `update.Price == 0` and `update.FilledAvgPrice == 0`:
   skips the leg insert (no row written, warning logged).
7. `insertFillLeg` magnitude check: `legPrice > 5 * option_premium` →
   rejected, alert event published.

Integration smoke:

- Restart `omo-core` with at least one open paper option position.
  Confirm no PREMIUM_STOP fires on first tick. Verify
  `pos.CustomState["iv_at_entry"]` is set in the boot log.

DB sanity post-deploy:

```sql
SELECT time, symbol, side, price, quantity, rationale
  FROM trades
 WHERE option_symbol IS NOT NULL
   AND time >= NOW() - INTERVAL '14 days'
   AND price > 200
 ORDER BY price DESC LIMIT 50;
```

Pre-fix: rows with absurd prices like $824, $869 are reachable. Post-fix:
should be empty.

## Blast radius

- **Live order routing**: Fix 3 changes `triggerExit` semantics for the
  `est==0 + no-quote` branch — switches from "ship inflated limit" to
  "ship MARKET with sane limit_price stamp". This *improves* execution
  on the bad path; the good path (BSM works or quote available) is
  byte-identical.
- **Fill recording**: Fix 5 drops a fallback path. If both broker
  price fields are zero on a real fill (rare), we now skip the leg until
  the next ReqFills reconciliation. Slightly slower trade-row visibility
  on the affected order; no incorrect data written.
- **Bootstrap**: Fix 2 adds a single options-chain fetch at startup per
  open option position. Bounded — at most a few dozen contracts after
  any restart. Falls back to a default IV if the fetch fails.
- **No schema migration**, no event schema changes, no broker adapter
  changes, no UI changes.

## Order of operations

1. Fix 1 (smallest, one-file, no behaviour change for the happy path).
2. Fix 4 (defence in depth, depends on Fix 1).
3. Fix 5 (independent, structural backstop — ship even if Fixes 2-3 slip).
4. Fix 3 (behaviour change to triggerExit, requires care + tests).
5. Fix 2 (bootstrap restore, requires options-chain plumbing — biggest).

Fixes 1+4+5 can ship as one PR (~100 LOC) and would close the recorded
P&L corruption immediately. Fixes 2+3 (~150 LOC) close the upstream
trigger and can ship as a follow-up.

## Cleanup

- Audit historical `trades` rows for the magnitude pattern documented
  above (`price > 5 * option_premium`). One-shot SQL to identify
  candidates; manual verdict per row before any DELETE/UPDATE.
- The +$84,335 row in today's `daily_pnl` and `strategy_daily_pnl`
  needs a reversal entry, otherwise dashboards remain wrong. Same
  approach as the dual-write cleanup in
  `_workspace/dual_write_options_fix_plan.md` ("Historical-data cleanup").

## Out of scope

- The independent multi-fill recording bug (`fill_recording_multi_exec_fix_plan.md`)
  — orthogonal but Fix 5 will harmlessly compose with that fix when both
  ship.
- The dual-write reconciler bug (`dual_write_options_fix_plan.md`) —
  also orthogonal; touches different reconciler paths.
- Re-pricing the actual exit during the incident: the broker fills
  already happened. This plan is about preventing recurrence + closing
  the recording corruption.
