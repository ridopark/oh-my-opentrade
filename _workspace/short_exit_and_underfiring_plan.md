# Short-Exit, Backtest-End Force-Close & Underfiring — Triple Fix Plan

Date: 2026-05-01
Trigger: 1-year backtest with `asset_classes = ["EQUITY"]` produced 12 trades. Same symbol/strategy flattens cleanly when LONG (QQQ #2) but rides 30 days to BACKTEST_END when SHORT (QQQ #10). 5 of 5 shorts leaked; total -$16.8K dominated by short losses.

This plan ships three fixes, in order, on one feature branch. Each fix has its own commit so failures roll back cleanly.

---

## Diagnosis recap

Verified symmetric (no side branch) by reading:

- `evaluators.go:456 evaluateEODFlatten` — only crypto + closed-session filters.
- `exit_eval.go:60 tick()` — iterates all positions, no side filter, both phases.
- `exit_eval.go:962 exitDirection` — emits `DirectionCloseShort` for shorts.
- `exit_rule.go:356 IsShort` — case-insensitive; "SELL" or "PUT" returns true.
- `simbroker/broker.go:523-534` — exit `default` branch infers BUY/SELL from its tracked `pos.side`.
- `execution/service.go:1494-1524, 2011-2042` — `fillPayload["direction"]` always set from `intent.Direction`.
- `positionmonitor/service.go:668-672` — distinguishes SHORT entries from exits via `Direction.IsExit()`.
- `backtest/collector.go:295-344` — matches `CloseShort` against `openSells`.

Confirmed asymmetric:

- `backtest/runner.go:2153-2159 signalPassthrough` — collapses ALL strategy-emitted exits to `DirectionCloseLong`. Comment claims "execution resolves position side from broker." This works in simbroker because line 528-533 looks up the tracked position, but it makes the entire exit pipeline depend on simbroker's local position ledger being populated correctly. Live brokers won't have this fallback.

The fact that QQQ same-day, same-strategy LONG flattens but SHORT doesn't, while every code path I read is symmetric, means the break is one of:

1. SHORT entries reach the collector (we see them in the trade log) but never reach the position monitor's `s.positions` map. Cause hypotheses: a side-conditional subscription path, or `processFill` short-circuiting on a side check we haven't found.
2. SHORT positions are registered but `tick()` skips them via a hidden gate (e.g. `IsShort()` check in some upstream filter we missed).
3. SHORT exit intents are emitted but rejected by execution's pre-checks (price-gate, position-gate) for an asymmetric reason.

One log line at the right boundary will tell us which. Fix B does that targeted instrumentation.

---

## Fix A — Replace BACKTEST_END with a final EOD-flatten tick

**Problem.** `collector.CloseOpenPositions` zeros open positions with synthetic trade records bypassing the simbroker, fees, exit reasons, and MFE/MAE accounting. That violates SRP (two systems doing exit pricing) and hides Fix B's symptom.

**Change.**

1. Add a public method on the position monitor:

       func (s *Service) TickAt(now time.Time)

   It is `tick()` with `s.nowFunc()` replaced by the passed `now`. No other behavior change.

2. In `backtest/runner.go`, just before `collector.CloseOpenPositions`:

       lastClose := domain.CalendarFor(domain.AssetClassEquity).SessionClose(r.lastBarTime)
       posMonitor.TickAt(lastClose)
       // drain the event bus so emitted CloseLong/CloseShort intents complete
       infra.EventBus.Drain(ctx)

3. Rename `Rationale: "BACKTEST_END"` to `Rationale: "BACKTEST_END_LEAK"` and add an ERROR-level log naming each leaked symbol/side/strategy/qty. After A+B, this log should never fire.

**Why this is SOLID/KISS.** Single new seam (TickAt), single call-site change in runner, no duplicated pricing logic, exit-chain stays the source of truth (SRP), monitor ticks on injected clock (DIP), the existing `tick()` body is reused (OCP — no rewrite).

**Tests.**

- `TestService_TickAt_FiresEODFlattenOnAllPositions` (positionmonitor): register one LONG and one SHORT MonitoredPosition with `EOD_FLATTEN` rule, call `TickAt(sessionClose)`, assert TWO `OrderIntentCreated` events fire — one `CloseLong`, one `CloseShort`. Mocks the event bus.
- `TestRunner_BacktestEndDrainsOpenPositions` (backtest): a 1-day backtest entering one LONG and one SHORT at 09:35 with EOD-only exit rules. Assert the trades table contains zero `BACKTEST_END_LEAK` rows.

**Files touched (~50 LOC):**
- `backend/internal/app/positionmonitor/exit_eval.go` — extract `tick()` body to `tickWithNow(now)`, expose `TickAt`.
- `backend/internal/app/backtest/runner.go` — call `TickAt` and drain before `CloseOpenPositions`.
- `backend/internal/app/backtest/collector.go` — rename rationale + add error log.
- two test files.

---

## Fix B — Find and close the short-side break

**Problem.** Even after Fix A, if shorts are still missing from `s.positions` at end-of-run (or are present but skipped by `tick()`), `BACKTEST_END_LEAK` will fire and Fix A becomes a louder symptom rather than a cure.

**Step 1 — instrument (1 commit, ~5 LOC).**

Add three log lines, all DEBUG except the last which is INFO so we don't have to flip log levels:

```go
// positionmonitor/service.go processFill — after line 668
s.log.Debug().
    Str("symbol", string(fill.Symbol)).
    Str("side", fill.Side).
    Str("direction", fill.Direction).
    Bool("is_exit", isExit).
    Msg("processFill side/direction trace")

// positionmonitor/service.go processFill — right before NewMonitoredPosition (line 722)
s.log.Info().
    Str("symbol", string(fill.Symbol)).
    Str("side", fill.Side).
    Msg("registering monitored position")

// positionmonitor/exit_eval.go tick — at the top of the position loop (line 75)
s.log.Debug().
    Str("symbol", string(pos.Symbol)).
    Str("side", pos.Side).
    Bool("is_short", pos.IsShort()).
    Bool("exit_pending", pos.ExitPending).
    Msg("tick evaluating position")
```

**Step 2 — run (no commit).**

Rerun the failing 1-year backtest config. Grep:

    grep "registering monitored position" backtest.log | grep SELL
    grep "tick evaluating position" backtest.log | grep "is_short=true"

Expected outcomes:

| SHORT registered? | SHORT in tick? | Fix surface |
|---|---|---|
| no | n/a | upstream of position monitor — likely `processFill` early-return on side mismatch, OR the simbroker→execution→positionmonitor SubscribeAsync chain drops short fills |
| yes | no | inside the actor — between `s.positions` and the loop's gate (most likely a hidden filter in `tick()` or its callers) |
| yes | yes (no exit emitted) | `triggerExit` or downstream — likely `exit_eval.go:962`'s `pos.IsShort()` branch isn't reached, or `OrderIntentCreated` for shorts gets rejected by execution |

**Step 3 — fix (1 commit, expected 5-30 LOC).**

Apply the targeted change at the surface Step 2 identifies. Single conditional removed, or single missing direction case added. NOT a rewrite. Add a regression test in the same package that exercises the exact short path.

**Step 4 — remove instrumentation (squashed into Step 3 commit).**

Strip the three log lines once the fix lands. Leave one INFO log for "monitored position registered" if it doesn't already exist — useful for live ops.

---

## Fix C — Underfiring (likely cured by B, verified or escalated)

**Hypothesis.** Per-symbol cooldown / risk-budget consumption was held by stuck short positions. Once shorts close cleanly, those slots free up, entry count rises naturally. 12 trades/year on a 34-symbol universe is 5x-10x below baseline expectation.

**Step 1 — re-baseline after A+B.**

Rerun the same 1-year config. Record:
- trade count
- entries per symbol per month (read from logs, not the trades table)
- entry-attempt vs entry-accepted ratio (from existing `entry_gated_writer` rejection logs)

**Step 2 — branch on result.**

If trade count ≥ 50/year and rejection ratio looks sane (most attempts pass): C is cured by B, ship it.

If trade count is still low, the most likely true causes (in order):

1. **Session-time weighting** killing entries (memory `project_session_time_weighting.md`). Check distribution of computed entry-strength values vs. threshold. Likely fix: lower threshold or relax the time-of-day curve. Touched files: `configs/strategies/*.toml`. Pure config change, no code.
2. **Universe loading.** Fewer than 34 symbols actually have bars in the backtest window. Grep bar-publish logs by symbol. Fix is in `backtest/runner.go` universe resolution if a path is dropping symbols silently.
3. **Risk-sizer rejecting entries.** `entry_gated_writer` rejection reasons distribution. If a particular reason dominates (e.g. "insufficient_buying_power" or "max_concurrent_positions"), tune the corresponding limit.

**Step 3 — fix or document.**

If a clear cause is identified, ship a focused fix (1 commit, likely TOML-only). If no cause is identified, write the findings into `_workspace/underfiring_diagnosis.md` and surface to user before touching code.

**Files touched in the common case (B cures C):** zero. Just a rerun and confirmation.

---

## Order of operations & branch hygiene

1. New branch off current main: `fix-short-exit-and-backtest-flatten`.
2. Commit 1 (Fix A): TickAt + runner wiring + collector rename + two tests. CI green.
3. Commit 2 (Fix B Step 1): instrumentation log lines.
4. Manual rerun → identify surface.
5. Commit 3 (Fix B Step 3+4): targeted fix + regression test + strip instrumentation.
6. Manual rerun for Fix C.
7. Commit 4 (Fix C): config tweak OR docs OR no-op (skip commit).
8. Open PR with all three commits, summary table of before/after trade-count and PnL.

## Out of scope

- Strategy parameter tuning beyond what Fix C may require for entry-strength threshold.
- AI/LLM debate (off everywhere).
- Live broker adapters. Fix A only adds a method to the position monitor and a call-site in backtest runner; live still uses wall-clock `tick()`.
- Trade-table date display (year missing). Frontend cosmetic, separate ticket if user cares.

## Risks

- Fix A's `EventBus.Drain` may not exist as-is. If not, replace with a synchronous publish + a polling loop on `len(openBuys)+len(openSells) == 0` with a 1s budget, OR refactor `tick()` to invoke `triggerExit` synchronously when called via `TickAt`. Both are KISS-acceptable; pick whichever the existing event bus supports.
- Fix B might surface a deeper architectural issue (e.g. shorts never registered because of some test-only seam). If the targeted fix balloons past 50 LOC, stop, write findings, and re-plan rather than expanding scope.
- Fix C's per-symbol cooldown hypothesis may be wrong — be ready to fall back to TOML-only tuning if rerun still shows underfiring.

## Acceptance criteria for the PR

- `BACKTEST_END_LEAK` appears zero times in the rerun.
- All shorts in the trade table close on entry day via `EOD FLATTEN` (or any non-LEAK reason).
- Year-long backtest produces ≥50 trades, with shorts and longs both represented.
- All new tests pass; existing tests in `positionmonitor/`, `backtest/`, `simbroker/` packages still pass.
- Net P&L recovers from -$16.8K to something interpretable (sign doesn't matter — the goal is "exits work," not "strategy is profitable").
