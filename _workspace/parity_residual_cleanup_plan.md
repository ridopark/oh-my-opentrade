# Parity residual cleanup plan

Synthesized 2026-04-28 from three agent consultations after commits
`dc6c8a47` / `038f66ca` / `fb31fb38` / `3dc1d49b` closed live's in-process
5m parity (monitor vs runner_htf byte-identical across all 10 indicator
fields × 27,336 (sym, ts) bars × 808 UTC moments). Three remaining items
from the original plan in `_workspace/warmup_parity_5m_plan.md`.

## Outstanding gaps

1. **omo-replay HTF warmup boot+1 dedup gap** — replay's calc receives
   the boot+1 5m bar twice (warmup + runtime aggregator); live receives
   it once (runtime only). Causes cumulative EMA200 drift between live
   and replay across 800-bar warmup window.

2. **Runner-side calc discriminator** — symmetric to the monitor-side
   `TagBacktest` shipped in `fb31fb38`/`3dc1d49b`. The runner's
   `htfCalcs` use `Label="runner_htf"` regardless of process; an
   in-process `/backtest/run` pollutes live parity-diag validation when
   filtering by `"calc":"runner_htf"`.

3. **Strategy re-validation under the new spec** — the original plan
   defined hard rollback gates but the year-long backtest matrix has
   not been run since `dc6c8a47` shipped.

## Phase A — Small fixes (parallel, ~30 min total)

### A1: omo-replay HTF warmup → drop TrimWithBoot1

**Root cause** (verified by code investigator agent):
- `backend/cmd/omo-replay/main.go:895` calls
  `warmup.TrimWithBoot1(spec, htfTF, raw, warmupEnd)` for HTF warmup.
- `backend/cmd/omo-core/warmup.go:477` (live's HTF warmup) calls
  `warmup.Load(...)`, which uses plain `Trim` — does NOT append a
  boot+1 bar.
- After replay's `TrimWithBoot1`, calc state is seeded through the
  boot+1 bar (e.g. 19:35 UTC = 14:30 ET 5m bar). Then the runtime
  1m stream's first ~5 bars (19:36..19:40) feed the now-initialized
  5m aggregator and close a 5m bucket whose `Time` equals the boot+1
  timestamp. The aggregator emit then re-fires `calc.Update` for the
  same UTC moment.
- Net: replay double-feeds the boot+1 5m bar; live single-feeds.
  EMA200 cumulatively drifts by ~0.18 across 800 bars.

**Fix**:
- One-line change at `backend/cmd/omo-replay/main.go:895`:
  `warmup.TrimWithBoot1(spec, htfTF, raw, warmupEnd)` →
  `warmup.Trim(spec, htfTF, raw)`.
- Drop unused `warmupEnd` reference at the HTF site (still needed
  above for the fetch range).
- Leave 1m warmup at `main.go:852` unchanged — 1m has different
  semantics: live's `warmup.Load` for 1m DOES include the bar at
  `warmupEnd-1m` because the half-open `time < time.Now()` window
  naturally captures it; replay needs `TrimWithBoot1` to reproduce
  that since its half-open `time < firstBarTime[sym]` would otherwise
  skip it. The 1m runtime in replay starts at `firstBarTime[sym]`,
  so no overlap.

**Tests**:
- Existing `loader_test.go` tests for `TrimWithBoot1` unchanged.
- Add: regression test under `backend/cmd/omo-replay/` (or a warmup
  parity harness) asserting replay's WarmUp + first replay-window 5m
  close fires `calc.Update` exactly the same count as live's
  WarmUpNative + first runtime 5m close, for a fixed boot moment
  between two 5m boundaries.

**Files touched**: `backend/cmd/omo-replay/main.go` (1 line), one new
test file.

### A2: Runner-side calc discriminator

**Design** (designed by go-architect agent, mirrors monitor's
`TagBacktest`):

Add to `backend/internal/app/strategy/runner.go`:
- New field on `Runner` struct (near line 43, beside `htfCalcs`):
  ```go
  htfLabelSuffix string // empty in live; "_backtest_<id>" in backtest
  ```
- New method (placed after `NewRunner`, ~line 563):
  ```go
  func (r *Runner) TagBacktest(backtestID string) {
      r.mu.Lock()
      defer r.mu.Unlock()
      r.htfLabelSuffix = "_backtest_" + backtestID
  }
  ```
- Modify lazy `htfCalc.Label` assignment at `runner.go:1636` and
  `runner.go:1947`:
  ```go
  htfCalc.Label = "runner_htf" + r.htfLabelSuffix
  ```

Wire from `backend/internal/app/bootstrap/strategy.go` after
`NewRunner` at line 275 (mirrors `BuildMonitor`/`TagBacktest` at
`ingestion.go:63-64`):
```go
if deps.BacktestID != "" {
    runner.TagBacktest(deps.BacktestID)
}
```

`StrategyDeps.BacktestID` already exists (line 43). Live path:
`deps.BacktestID == ""`, `TagBacktest` never called, label stays
literal `"runner_htf"`. Zero behavior change.

**Lifecycle correctness**: `htfCalcs` is created empty in `NewRunner`
(line 538). First lazy population happens in `handleBarCore` (runtime,
~1633) or `WarmUpTF` (warmup, ~1943). Both fire strictly after
`NewRunner` returns and after bootstrap finishes wiring. `TagBacktest`
called immediately after `NewRunner` is well before any `htfCalc` is
constructed.

**Tests**:
- `runner_test.go`: subtest verifying after `runner.TagBacktest("abc")`,
  a synthesized HTF bar produces
  `htfCalcs[key].Label == "runner_htf_backtest_abc"`.

**Files touched**: `backend/internal/app/strategy/runner.go`,
`backend/internal/app/bootstrap/strategy.go`. ~12 LOC + ~30 LOC test.

## Phase B — Strategy re-validation (~3-4 h sequential, ~90 min parallel)

### Baselines (from `project_warmup_window_parity` memory)
Year-long 2025-04-22 to 2026-04-25, 34-symbol universe:
- AVWAP_v4: PF=1.60, win_rate=59.9%, trades=3754, return=360%
- MACD_only_v1: PF=1.13, win_rate=65.2%, trades=981, return=26%

These baselines were captured AFTER `27aa7575` (canonical-spec
WarmUpNative) but BEFORE `dc6c8a47` (double-feed gate). The double-feed
mostly affected live; backtest path may have been already correct, so
re-running may reproduce baselines within noise.

### Run matrix (8 backtests)
- Strategies: AVWAP_v4, MACD_only_v1
- Commits: HEAD~N (pre-`dc6c8a47`) → `baseline_local`; HEAD (post-fix)
  → `current`
- Windows: 1 annual + 3 non-overlapping quarters (Q1 2025-Q4,
  Q2 2026-Q1, Q3 2026-Q2 partial)

Walk-forward not warranted — the fix is microstructural, not
regime-dependent. Quarterly slices distinguish "uniform 1-2% drift"
from "5% drop concentrated in low-vol regime" cheaply.

### Sanity check
Compare `baseline_local` against the stored memory baselines. They
should match within ~0.5% PF; greater drift indicates environment
change (data updates, library bumps) and the local baseline is the
authoritative reference for the post-fix comparison.

### Hard rollback gates (auto-revert if any trips)
- PF < 0.95 × baseline_local
- Trades < 0.75 × baseline_local

### Soft tripwires (investigate, don't auto-revert)
- max_DD worsens > 15% absolute
- Sharpe drops > 10%
- Expectancy/trade drops > 8%
- Win rate shifts > 3 pts

### PASS criteria (all four required)
- (a) Annual PF within ±5% of `baseline_local`
- (b) Trades within ±10%
- (c) No quarter shows > 10% PF degradation
- (d) max_DD within +15% absolute

### FAIL response ladder
- **5-10% PF drop, trades stable**: diff per-trade exit prices between
  `baseline_local` and `current`; expect drift at ATR-stop ties.
  Retune `atr_stop_mult` ±0.1 before considering revert.
- **> 10% PF drop OR trades drop > 25%**: revert `dc6c8a47`, file bug.
  Don't retune through a correctness regression.
- **DD worsens > 15% but PF holds**: ship anyway, log for monitoring;
  sizing may need tightening.

### Runtime estimate
~25-40 min per year-long backtest, ~7 min per quarterly slice.
Full matrix: 16 runs (2 strategies × 2 commits × 4 windows), ~3-4 h
sequential, parallelizable to ~90 min.

## Phase C — Consolidate (after B passes)

- Update `project_warmup_window_parity` memory with post-fix baselines
  as new reference.
- Optional: lift the `live_size_multiplier=0.5` cap from the original
  plan once one full RTH session of post-fix paper trading shows clean
  PF/trades vs baseline.
- Original Phase 3a cleanup remains open: remove dead
  `Runner.WarmUpHTF` (genuinely unused at boot path now that
  `warmup.go:543` calls `PrimeAggregators` instead). Other callers
  (backtest, replay) may still reference it — verify before removal.

## Sequencing

- A1 and A2 are independent, both can ship in one commit pair.
- B is independent of A (runs against backtest path, not replay).
- A1 closes the live↔replay diagnostic gap so future post-deploy
  validation reads cleanly.
- A2 closes the parity-diag pollution for `runner_htf` if anyone runs
  an in-process backtest during validation.
- C is gated on B passing.

## Trigger to start

A1+A2: ready any time, well-scoped and reversible.

B: requires explicit approval (long-running) and a clean baseline
commit reference.

C: gated on B's PASS verdict.

## Cross-references

- `_workspace/warmup_parity_5m_plan.md` — original plan, Phase 0/1/2a
  shipped, Phase 2b/3 deferred
- `_workspace/parity_observability_followup.md` — long-term promotion
  of parity-diag ad-hoc emits to a real `parity_observations`
  hypertable + Prom gauges. Not gated on this plan.
- Memory: `project_warmup_window_parity.md` — baseline metrics and
  commits up through `dc6c8a47`'s prerequisites.
