# Option Exit Re-Peg Ladder — 2026-04-24

Commits: `126e851e` (fix) + `e115b3fc` (cleanup).

## The problem

When an option position triggers an exit, the position monitor submits a
limit order priced near the NBBO mid. If it doesn't fill in 10s (stops) or
30s (targets), the monitor cancels and re-submits — the "re-peg". If the
re-peg still doesn't fill, we escalate to a market order.

Before this change, the re-peg used the **same DTE-scaled k factor** as the
first attempt, just with a fresh quote. So the ladder looked like:

    t=0    LMT @ mid - k*spread    (k=0.45 for short-dated options)
    t=10s  CANCEL, LMT @ mid - k*spread (same k, new quote)
    t=21s  CANCEL, MKT IOC        <-- crosses to bid, full spread lost

The re-peg was essentially hoping the book would come up to our level.
When it didn't, we gave up the entire spread via the market fallback.

Observed on 2026-04-23 live paper trades:
- COIN 197.5P exit: first limit 15.23, re-peg 15.19, then market filled
  at 14.45 — ~$0.25/contract below mid.
- PLTR 150P exit: first limit 7.999, re-peg 8.088, then market at 7.80.
- NVDA 207.5C exit: first limit 2.67, re-peg 2.60, filled 2.62 (re-peg
  only worked because underlying moved favorably during the wait).

The pattern: the re-peg and market escalation together were bleeding the
full spread on most stop-category exits.

## The fix

Widen k by +0.25 on each re-peg, capped at 1.0. The `bid+tick` floor
already in `buildExitLimitPrice` is the true hard bound.

New ladder for stop-category exits (short-dated, DTE < 5, k0 = 0.45):

    t=0    LMT @ mid - 0.45*spread
    t=10s  LMT @ mid - 0.70*spread  <-- tightens toward bid
    t=21s  MKT IOC (last resort)

For targets (3 re-pegs allowed) the k sequence is:
`0.35, 0.60, 0.85, 1.00` — the last re-peg is effectively a marketable
limit at `bid+tick` before the real market order.

The floor clamp means we never post below the bid, so at worst the re-peg
sits at `bid+tick` and has a chance of catching price improvement in the
10s window. Wall time is unchanged (21s for stops, 91s for targets). The
market endpoint is unchanged. Only the intermediate re-peg prices shift.

## How it flows through the code

The counter was already on the position — just not threaded through the
pricing functions.

    triggerExit (exit_eval.go:777)
      -> reads pos.ExitRetryCount AND pos.ExitRepegCount
      -> calls exitOrderParams(..., retryCount, repegCount, ...)

    exitOrderParams (exit_eval.go:1022)
      -> on forced exit or retryCount>=1, returns MKT IOC (unchanged)
      -> otherwise calls buildExitLimitPrice(..., repegCount, ...)

    buildExitLimitPrice (exit_pricer.go:87)
      -> calls kForDTE(dte, repegN)

    kForDTE (exit_pricer.go:40)
      -> base k by DTE bucket
      -> k += 0.25 * repegN
      -> cap at 1.0

The re-peg counter lifecycle lives in `handleExitTimeout` (exit_eval.go):
- First attempt: ExitRepegCount = 0.
- After first timeout: increments to 1 before re-firing triggerExit.
- After re-peg budget exhausted (1 for stops, 3 for targets): increments
  ExitRetryCount instead, which routes straight to market on next call.

## Blast radius

- Files touched: 4 (2 code + 2 test).
- No event schema changes, no DB changes, no config changes.
- No callsites outside positionmonitor.
- First-attempt pricing (repegN=0) is bit-identical to the old code.
- Only the re-peg slot and target re-pegs 1/2/3 change — and they can
  only tighten, never widen. Existing bid+tick floor prevents below-bid
  pricing. Existing 5% exitBpsCap still bounds absolute discount magnitude.

## What this does NOT fix

- **Entry-side spread bleed.** Separate fix already shipped
  (`risk_sizer.go`: removed paper→market override, stale_cancel 120s).
- **Stale-quote fallback.** When the NBBO snapshot is >5s old or the feed
  returns blown spreads, `buildExitLimitPrice` rejects and we fall through
  to a 5%-below-mid formula (`exit_eval.go:1057`). That path is worse than
  the laddered quote path when it fires — tracked separately.
- **Stop timeout duration.** Still 10s. Could be extended to 20-30s for a
  less-aggressive escalation profile, but that belongs in a DNA tune, not
  a code change.

## Expected P&L effect

Per-exit bleed reduction depends on spread width. For the COIN case above
(~$0.40 spread), the new ladder prices the re-peg at bid+tick ($14.51
given bid $14.50). Even if the re-peg fills at the bid+tick floor, that's
a ~$0.06/contract improvement over the market-IOC outcome (~$14.45). Two
contracts = $12 recovered. Not life-changing per trade, but compounds
across every stop exit that used to market-escalate.

## Tests

`backend/internal/app/positionmonitor/exit_pricer_test.go`:
- `kForDTE` widens by 0.25/step, caps at 1.0.
- Tight spread relative to mid: bps cap binds; ladder still floors at
  bid+tick.
- Wide mid with tight spread: ladder demonstrably tightens p0 > p1 > p2.

`backend/internal/app/positionmonitor/service_test.go`:
- `exitOrderParams` with repegCount=1 prices strictly below repegCount=0
  for the same quote.

All existing tests updated with `repegCount=0` to preserve prior behavior.
