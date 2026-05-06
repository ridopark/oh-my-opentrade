# Equity Backtest LMT Exit Haircut Fix

## Problem

simbroker prices equity exit fills at the LIMIT price submitted by exit_eval, not at the actual market. exit_eval at backend/internal/app/positionmonitor/exit_eval.go:1237-1242 builds a buffered limit of `currentPrice * 0.95` (long close) or `currentPrice * 1.05` (short close) as a guaranteed-fill cushion. simbroker at backend/internal/adapters/simbroker/fill_models.go routes LMT orders through Optimistic, Realistic, and Pessimistic models, all of which return `Price = c.LimitPrice` when the limit triggers. Every non-forced equity exit in backtest therefore reports a fill price 5% adverse to current, regardless of bar OHLC.

Live is unaffected: Alpaca reports actual `filled_avg_price` near top-of-book, so the cushion is harmless. The asymmetry is backtest-only.

Affected exits, all sharing `triggerExit -> exitOrderParams -> simbroker LMT`:
- CHANDELIER spot branch (evaluators.go:180-213, rationale prefix `chandelier_trail:` with no suffix)
- STAGNATION_EXIT (evaluators.go:793-845)
- STEP and SWING equity exits

Options exits are NOT affected: simbroker bypasses FillModel for options at backend/internal/adapters/simbroker/broker.go:464-516 (BSM + participation impact + slippage).

The bug shipped because `TestEvaluateChandelierTrail` and siblings assert evaluator trigger logic only, never the resulting fill price.

## Out of Scope

- Same-bar vs next-bar exit look-ahead bias (separate concern, see docs/AVWAP_CHANDELIER_REALISM_ASSESSMENT_2026-04-14.md).
- Equity exit re-pegging (exit_eval.go:1388 returns 0 budget for non-options, intentional).
- omo-replay `UpdatePrice` vs `UpdateBar` degenerate-Bar issue at omo-replay/main.go:1429 (handled by `fix-omo-replay-fill-model-parity` worktree).
- Broader options/equity FillModel architectural asymmetry.

## Constraints

- Run from a fresh feature branch off main. Do NOT ride on `fix/omo-replay-fill-model-parity` (unrelated in-flight work).
- Hexagonal architecture: adapter logic stays in simbroker; no app-layer fill behavior.
- Skip-worktree files (`CLAUDE.md`, `configs/backups/loose/macd_only_v1.toml`, `deployments/ib-gateway/ibc-config.ini`) are tolerated only if `git ls-files -v | grep '^S'` confirms their flag.

## Phase 1: simbroker equity LMT fill correction

Files:
- backend/internal/adapters/simbroker/fill_models.go (primary edit)
- backend/internal/adapters/simbroker/fill_models_test.go (regression coverage)

Approach:
- For equity LMT orders triggering in OptimisticFillModel, RealisticFillModel, and PessimisticFillModel: stop returning `c.LimitPrice` unconditionally. Fill at bar.Close clamped favorably by the limit.
  - Long close (selling): fill at `min(bar.Close, c.LimitPrice)` only when bar.Low <= limit (limit was actually reachable). Otherwise no fill.
  - Short cover (buying): fill at `max(bar.Close, c.LimitPrice)` only when bar.High >= limit.
- Where `NextBar` is available, Realistic and Pessimistic prefer `NextBar.Open` with identical clamping. Optimistic stays same-bar.
- Preserve current options-LMT behavior (options bypass FillModel, but audit before editing to confirm no overlap).

TDD: yes. Behavior change is unit-testable at the adapter boundary.

Phase 1 success criteria (verbatim):
- [ ] Unit test: equity LMT long-close with limit = currentPrice * 0.95, bar.Close = currentPrice, bar.Low <= limit -> fill returns Price equal to bar.Close (exact equality, not limit). Test runs across all three FillModel variants.
- [ ] Unit test: equity LMT short-cover with limit = currentPrice * 1.05, bar.Close = currentPrice, bar.High >= limit -> fill returns Price equal to bar.Close. All three FillModel variants.
- [ ] Unit test: equity LMT long-close where bar.Low > limit -> returns no-fill (not a fabricated price). All three FillModel variants.
- [ ] `go test ./backend/internal/adapters/simbroker/...` passes.
- [ ] `go build ./...` passes.
- [ ] No existing simbroker test changes its expected price unless that change is documented in the PR description as a fix to the same bug.

Phase 1 halt conditions:
- More than 5 existing simbroker tests need their expected fill prices updated. Indicates wider behavioral change than scoped; halt and reassess.
- `go build` fails after edits. Investigate root cause before iterating.

## Phase 2: end-to-end regression test for equity CHANDELIER fill

Files:
- backend/internal/app/positionmonitor/exit_eval_test.go (extend) or new test file under same package.

Approach:
- Construct a MonitoredPosition for an equity long, drive `Service.tick` through ticks that arm CHANDELIER, set the peak, then trigger an exit at a known currentPrice and bar.Close.
- Capture the resulting fill via the syncFill execution path used in backtest.
- Assert fill price ~= bar.Close (within 0.1%), NOT 0.95 * bar.Close.

TDD: yes. Regression test at the level the bug was missed.

Phase 2 success criteria (verbatim):
- [ ] New test exercises evaluateChandelierTrail spot branch -> triggerExit -> simbroker fill -> handleFillWithPrice and asserts recorded fill price is within 0.1% of bar.Close.
- [ ] Test FAILS when run against the pre-Phase-1 code (verify by `git stash` of Phase 1 edits, run test, expect failure, then `git stash pop`).
- [ ] Test PASSES on post-Phase-1 code.
- [ ] `go test ./backend/internal/app/positionmonitor/...` passes.

Phase 2 halt conditions:
- End-to-end scaffolding required exceeds 200 net LOC. Escalate via Discord yellow and downgrade to a tighter integration test at the simbroker -> handleFillWithPrice boundary instead.

## Phase 3: re-run suspect tuning baseline

Files: none (data only).

Approach:
- Re-run the equity strategy backtest that produced the suspect baseline.
- Compare PF, WR, expectancy, max DD, and trade count to the pre-fix baseline JSON.

Phase 3 success criteria (verbatim):
- [ ] Backtest completes without runtime error.
- [ ] PF, WR, and expectancy each shift in the favorable direction vs pre-fix baseline (the bug systematically reduced exit prices; post-fix should improve, not regress).
- [ ] Trade count within +/- 5% of pre-fix baseline (trigger logic unchanged, only fill price changes).
- [ ] At least one CHANDELIER spot exit in the post-fix run has rationale prefix `chandelier_trail:` and a recorded fill price > 0.97 * bar.Close at exit time (sanity check that the new fill model is in effect on the actual code path).

Phase 3 halt conditions:
- Trade count diverges by more than 5% from pre-fix. Indicates trigger logic was inadvertently changed; halt and investigate.
- Any backtest run errors. Halt and investigate.

## Global halt conditions

- 3 failed iterations of the same phase.
- Any push to main attempted.
- Any force-push attempted.
- Working tree dirty at invocation with files NOT in the project's documented skip-worktree set.
