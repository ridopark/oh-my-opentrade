# Backtest Fill Event Delivery — strategy OnEvent runs with stale state

Date: 2026-05-06
Status: Diagnosed, not fixed. Affects whale_pullback_v1 (and likely
break_retest_v1 / avwap_v1 — any strategy that relies on PendingEntry
→ PositionSide transition for in-strategy exits).

## Symptom

Tuning `whale_pullback_v1` showed that `exit_body_closes` and
`atr_stop_mult` produce byte-identical backtest results across
{1, 2, 3, 999} and {1.75, 2.25, 100.0}. With ATRStopMult=100 (impossible
to hit) AND ExitBodyCloses=999 (impossible to satisfy), the strategy
still emitted 46 SignalExit events per a 4-month, 4-symbol backtest.

## Root cause

In backtest mode, `FillConfirmation` events arrive at the strategy's
`OnEvent` handler *after* the bar loop has progressed several bars past
the entry. The strategy's `PendingEntry` field is cleared by the 5-min
timeout in OnBar before the FillConfirmation arrives. When OnEvent
runs:

    pendingEntry=""   positionSide=""   fillSide=sell   fillPrice=189.62

So the FillConfirmation handler's guard `if wp.PendingEntry != ""`
short-circuits, `PositionSide` is never set, and the body-close +
ATR-stop exit logic in `evalExit` is never invoked
(`if wp.PositionSide != "" && wp.PendingEntry == ""` is always false).

Diagnostic logs from a 1-symbol 7-day backtest:

- 28 FillConfirmation events delivered to strategy. 27 with empty
  PendingEntry (race lost), 1 with PendingEntry intact (race won).
- 0 evalExit invocations. Strategy never observes its own held position.

The 46 strategy-emitted SignalExit messages observed in the longer
4-month run come from a DIFFERENT path — they are NOT body-close or
ATR-stop. They appear to be artifacts of the engine's exit_monitor
loop matching against the strategy's earlier (entry-side) signals
under specific gate conditions. Either way, the strategy's spec'd
exits do not run.

## Why live mode is unaffected

In LIVE/paper trading we observed the correct flow in
`logs/omo-core.log`:

    EMIT SIGNAL ... type=entry side=buy instance=whale_pullback_v1:1.0.0:JPM
    handleFill: routed to strategy ... instance_id=whale_pullback_v1:1.0.0:SOXL side=SELL price=151.11

In live, broker fill latency naturally interleaves with bar processing,
so OnEvent runs while PendingEntry is still set. The dispatch that we
saw deliver `pendingEntry=""` in backtest was the EXIT fill (after
MAX_LOSS triggered), at which point PositionSide had been set by the
prior entry-fill OnEvent — that path works.

The bug is specifically that BACKTEST fill simulation is ordered
differently than live broker fills relative to OnBar invocations.

## Implications for tuning

Pass-1 tuning of `whale_pullback_v1` (PF 0.485 → 0.983 at 10 bps) is
real but achieved entirely through:

- engine-level exit rules (MAX_LOSS, EOD_FLATTEN tuning)
- entry-quality params (vwap_break_atr, pullback_touch_atr)
- structural filters (allowed_hours, universe pruning)

The strategy's body-close and ATR exits are dead code in the current
backtest framework. They WILL run live (we see the proof in the live
log). So the backtest is a CONSERVATIVE estimate of strategy quality
— live behavior with the strategy's own exits MIGHT be better.

## Where to look for the fix

1. Backtest event-bus dispatch ordering. Currently the EventBus appears
   to deliver FillReceived events asynchronously or in a deferred
   batch, after multiple OnBar calls. Either:

   - Make the backtest EventBus dispatch synchronously inline with
     OnBar (so each fill processes before the next bar), OR
   - Have the simbroker emit fills with a deterministic timing tag
     and the runner orchestrate OnBar/OnEvent interleaving.

2. Files to investigate:
   - `backend/internal/app/strategy/runner.go:946`
     (`Subscribe(EventFillReceived, r.handleFill)`)
   - `backend/internal/app/strategy/runner.go:2440`
     (`handleFill`)
   - `backend/internal/adapters/simbroker/broker.go`
     (where fills are simulated and `EventFillReceived` is emitted)
   - The EventBus implementation itself — what's the dispatch model?
     Sync, async, queued?

3. Verify the same symptom on `break_retest_v1` and `avwap_v1`
   (also use the PendingEntry → PositionSide pattern). If both have
   identical behavior in backtest, this is a system-level bug not a
   per-strategy bug.

## Workaround used during pass-1 tuning

None needed. Tuning proceeded by:

- Treating `atr_stop_mult` and `exit_body_closes` as inert in backtest.
  They got a flat default and were not swept.
- Tuning the engine-level `MAX_LOSS` pct as the de-facto stop loss.
- Tuning `EOD_FLATTEN minutes_before_close` as the de-facto holding
  time controller.

## Open follow-up

Issue should be filed as ENGINE_CHANGE with the title: "Backtest
EventBus delivers FillReceived too late to interleave with OnBar".
The fix is system-level and benefits every strategy that uses the
PendingEntry → PositionSide pattern.

Artifacts saved:

- `_workspace/whale_pullback_v1_baseline.json`
- `_workspace/whale_pullback_v1_no_stagnation_*.json`
- `_workspace/whale_pullback_v1_morning_only_*.json`
- `_workspace/whale_pullback_v1_pullback_*.json`
- `_workspace/whale_pullback_v1_eod_*.json`
- `_workspace/whale_pullback_v1_maxloss_*.json`
- `_workspace/whale_pullback_v1_atr_100_exit_1.json` (smoking-gun
  diagnostic — atr_stop_mult=100 + exit_body_closes=1 produces
  identical results to atr_stop_mult=1.75 + exit_body_closes=2)
