# Parity follow-up: trade-level live↔backtest convergence (v2)

## Goal

Make today's backtest trades resemble today's live trades on the same
universe, same strategies (avwap_v4 + macd_only_v1), same date. As of
the 2026-04-29 baseline only 1 of 5 backtest trades has a loose live
analog, despite verified-identical universe (34/34 underlyings, 0
symmetric diff). Indicator-layer parity is now byte-identical (PRs
#22, #23, #24 closed sub-bugs A and B; sub-bug C empirically
refuted). The remaining gap is in entry-decision logic and
option-leg selection, not in indicator inputs.

## Supersedes

`_workspace/parity_trade_convergence_plan.md` (commit dropped Phase 1
based on the investigation below). Original Phase 1 ("hours gate
alignment") was empirically refuted on 2026-04-30: the 5.87x ratio
between backtest's 1898 hours-blocks and live's 323 was an
instrumentation-coverage artifact, not a correctness bug. See
"Phase 1 refutation" section.

## Phase 1 refutation (2026-04-30, removed from this plan)

The original plan claimed a 5.87x asymmetry on the `hours` block
class (1898 backtest vs 323 live, AAPL+34 syms 2026-04-29) and
proposed three causes: (a) pre-market evaluation reaching the gate,
(b) timezone drift on `minute_of_session`, (c) session-time
weighting mode mismatch.

Investigation findings:

1. Gate definition at `backend/internal/app/strategy/builtin/avwap_v1.go:3573-3585`
   is identical for live and backtest — same `cfg.AllowedHoursStart/End`
   from the same TOML, same `now.In(loc).Format("15:04")` comparison.
2. `now = bar.Time` on both paths (`runner.go:1633` for live 1m
   dispatch, `runner.go:1792` for HTF dispatch, `runner.go:1925` for
   ProcessBar). `cfg.AllowedHoursTZ = "America/New_York"` applied
   consistently. **Refutes (b).**
3. `session_weight_enabled` is not set in either active TOML
   (`configs/strategies/avwap_v4.toml`, `configs/strategies/macd_only_v1.toml`).
   `SessionWeightEnabled` defaults to false → both sides use the
   binary AllowedHours gate. **Refutes (c).**
4. `gateHTFEquity` (`runner.go:1660`) blocks non-RTH 1m → 5m
   aggregation on both paths (added in PR #22). 5m bars reaching
   avwap_v1 are RTH-only on both sides. **(a) is structurally
   prevented at the gate-evaluation layer.**

The 5.87x came from data-coverage asymmetry, not gate logic. AAPL
2026-04-29 backtest (`bt-41261bbb99e1d177`, completed 2026-04-30
~06:35 CDT, 5s wall time) emitted 833 hours-blocks; live emitted ~50.
The backtest's [from, to] window covers 04:00-20:00 ET (full bar
range in `market_bars`); live only sees bars when `omo-core` is
streaming (~10h coverage on 2026-04-29). Pre-market and post-RTH
5m bars emit "outside trading hours" no-ops on the backtest side
that live never evaluates. The blocks are bookkeeping noise — they
don't affect trade decisions during the 09:30-15:45 ET window where
both sides' state machines align.

Phase 1 is removed. The trade-list divergence is in subsequent
phases.

## Phase 0: Joinable per-bar diagnostic instrumentation

The bias-loop and parity investigations have repeatedly almost
mis-diagnosed because the bar-level diff was reconstructed from
incomplete data. This phase lands the instrumentation Phase 2/3
need to converge on data, not hypotheses.

### 0.1 Persist `bar.Time` and `barTime` discriminator on EntryGated rows

`emitEarlyGated` (`avwap_v1.go:2842`) constructs `domain.EntryGatedPayload`
with a `Bar` field that includes `bar.Time`, but the row's `ts`
column is set from `domain.NewEvent`'s OccurredAt = `time.Now()` at
emit time. On 2026-04-30 backtest data this produced rows where
`payload->'bar'->>'time'` was empty and `ts` reflected wall-clock
emit time, not bar time, making per-bar joins impossible.

Fix:
- `instance.go:389-407` (`instanceContext.EmitDomainEvent`) — when the
  payload is `EntryGatedPayload`, build the published event's
  OccurredAt from `payload.Bar.Time`, not `time.Now()`. This makes
  `strategy_signal_events.ts` semantically equal to bar.Time on both
  sides.
- `avwap_v1.go:2906-2908` — confirm `Bar.Time` is set; populate from
  the `bar` argument unconditionally (it currently is, but the field
  is dropped somewhere — probably JSON marshalling). Add a
  per-emit assertion that the Bar.Time field is non-zero before
  publish.
- Same for `emitEntryGated` (`avwap_v1.go:2916+`) and any other
  EntryGated-emit site.

Also confirm that `payload->>'tag'` is properly set to
`backtest_<run_id>` for backtest-driven emits and empty for
live-driven emits. The mechanism exists (`runner.BacktestTag()`
plumbed into `instance.go:396`) but verify on a fresh emit.

Pass: SQL
```sql
SELECT
  CASE WHEN payload->>'tag' LIKE 'backtest_%' THEN 'backtest' ELSE 'live' END AS env,
  payload->'bar'->>'time' AS bar_time,
  payload->>'blockingGate' AS gate,
  COUNT(*) AS n
FROM strategy_signal_events
WHERE ts >= '<test-window-start>' AND ts < '<test-window-end>'
  AND symbol = 'AAPL'
GROUP BY env, bar_time, gate
ORDER BY bar_time, env, gate
LIMIT 50;
```
returns rows with non-empty `bar_time` for both env values, and
matching `bar_time` keys appear on both sides for the RTH session
window (09:30-15:45 ET on 2026-04-29).

### 0.2 Surface env discriminator in routine queries

The original plan's `signal_id NOT LIKE '%backtest%'` filter does
not match this codebase's tagging scheme — `payload->>'tag'` is the
authoritative discriminator. Update verification SQL templates and
any dashboard queries that try to split live/backtest by signal_id
to use `payload->>'tag'`.

Files: `_workspace/*.sql` snippets, monitor/dashboard queries (TBD —
grep for `signal_id LIKE '%backtest%'`).

Pass: zero `signal_id LIKE '%backtest%'` matches in active SQL templates.

### 0.3 Trade-level pairing helper script

Adds `scripts/parity_pair_trades.sh` that takes `--date YYYY-MM-DD`
and produces a CSV of paired live↔backtest trades, columns:
`underlying`, `live_ts`, `bt_ts`, `direction`, `live_strike`,
`bt_strike`, `live_premium`, `bt_premium`, `entry_skew_min`,
`pair_status` (one of: paired, live_only, backtest_only). Pairing
rule from the plan baseline: same underlying, same direction
(LONG/SHORT and CALL/PUT), entry within ±15min.

This is the data the plan's Phase 4 validation reads, so it has to
work before any phase claims pass.

Pass: script runs on 2026-04-29 against current DB and produces a
non-empty CSV. Expected output (today): 1-paired, 4-live-only,
4-backtest-only.

## Phase 2: entry_specific gate alignment (the actual residual)

Live fires 526 entry_specific blocks vs backtest's 145 on the
2026-04-29 baseline (`-381` delta, biggest residual after Phase 1
removed). entry_specific is a class-level wrapper around multiple
internal block reasons; the dominant sub-reason on each side has
to be identified via Phase 0.1's bar-time-keyed query.

### 2.1 Bucket entry_specific by sub-reason

Phase 0.1 lands enough plumbing to run:
```sql
SELECT
  CASE WHEN payload->>'tag' LIKE 'backtest_%' THEN 'backtest' ELSE 'live' END AS env,
  payload->>'blockingDetail' AS sub_reason,
  COUNT(*) AS n
FROM strategy_signal_events
WHERE ts BETWEEN '2026-04-29 13:30:00+00' AND '2026-04-29 19:45:00+00'
  AND payload->>'blockingGate' = 'entry_specific'
GROUP BY env, sub_reason
ORDER BY env, n DESC;
```

Pass: returns the top 5 sub-reasons per env, ranked. This is the
data that drives Phase 2.2.

### 2.2 Diff dominant sub-reasons

For each sub-reason that differs by > 50% between live and backtest,
identify the gating code at the corresponding `emitEarlyGated`
call site. Likely candidates per the plan:
- `entry_specific: no_setup` — no breakout/bounce/pullback fired.
- `entry_specific: weak_strength` — confluence score below threshold.
- `entry_specific: duplicate_signal` — pending entry already active.

Each has its own remediation. The fix is sub-reason-specific; do
not stack hypotheses.

### 2.3 Apply fix and re-run

Fix the dominant divergence, re-run the 2026-04-29 backtest, and
re-execute the Phase 2.1 SQL. Each top-3 sub-reason within ±50%
between live and backtest.

Pass: per-sub-reason delta within ±50% on top-3 sub-reasons.

Halt: 3 distinct fix hypotheses applied without converging — stop
and re-investigate from sub-reason buckets.

Files: `internal/app/strategy/builtin/avwap_v1.go` (entry-gate
logic — exact paths depend on which sub-reason is dominant).

## Phase 3: Option-strike selection

Live `12:00 OXY 59-CALL` vs backtest `11:54 OXY 62-CALL` on the
2026-04-29 baseline. Same underlying, same direction, different
strike/expiry. Two orthogonal causes:

(a) Strike-selection heuristic differs (e.g. ATM vs OTM-by-Z).
(b) Option chain itself differs (live IBKR weeklies snapshot vs
    DoltHub historical monthlies).

### 3.1 Pin the selection function

Locate `(underlying, signal.Side, signal.Strength, asof) →
(option_symbol, strike, expiry, right)` mapper on each side.
Likely paths: `internal/app/options/`, `internal/app/backtest/option_chain.go`,
or wherever the chain abstraction lives.

Pass: identify exact functions, log their inputs and outputs for
the OXY 11:54-12:00 ET 2026-04-29 case on both sides.

### 3.2 Diff and decide

If (a): port live's heuristic to backtest, re-run, validate same
strike on the OXY case.

If (b): accept the chain divergence and carve out the validation
rule — "weekly-targeted strategies (avwap_v4 with 3-7 DTE) score
parity at the underlying-trade level, not the option-trade level."
Document the carve-out in `project_options_realism.md` memory.

Pass: ≥ 60% of paired live+backtest trades on the same underlying
with same `option_right` AND (same expiry-week OR within ±10%
strike difference).

Halt: chain divergence is structural and the carve-out is rejected
— reframe to a delta-stuck (not strike-stuck) parity comparison.

Files: `internal/app/options/` (TBD), backtest option chain
abstraction.

## Phase 4: Trade-level validation

After Phases 2-3 land, re-run today's full-day backtest and
compare via `scripts/parity_pair_trades.sh` from Phase 0.3:

- Number of distinct round trips: backtest within ±50% of live's
  trade count for the day.
- Pairing rate: ≥ 50% of live's trades have a backtest analog on
  the same underlying within ±15min entry-time skew, same
  direction.
- entry_specific class delta: ≤ 25% per env (post-Phase-2).
- All other class deltas (regime, confluence, cooldown, max_trades,
  slope) within ±25% — these were close enough at baseline but
  shouldn't regress.

Pass: ship. Fail: re-investigate the largest residual delta. After
3 distinct hypotheses fail, halt and re-scope.

## Files (cumulative across phases)

- `backend/internal/app/strategy/instance.go` — Phase 0.1 emit-time fix
- `backend/internal/app/strategy/builtin/avwap_v1.go` — Phase 0.1 (bar.Time
  populate verify) + Phase 2.x (entry-gate sub-reason fixes)
- `backend/internal/app/options/` (TBD path) — Phase 3 strike selection
- `scripts/parity_pair_trades.sh` — Phase 0.3 (new file)
- `_workspace/parity_trade_convergence_v2_plan.md` — this plan

## Risks / blast radius

- **Live impact**: Phase 0.1 changes EntryGated event OccurredAt
  semantics from emit-time to bar.Time. Downstream consumers (SSE
  clients, dashboard) may rely on emit-time ordering. Verify the
  dashboard's SSE EntryGated stream does not break ordering. Also
  the `signalProgressCache` (`runner.go:2356-2362`) keys on (Strategy,
  Symbol) so cache hit semantics don't change.
- **Historical backtests**: Phase 2/3 fixes re-price every prior
  backtest run. Pin the diff to a held-out date range (last 30 days)
  and report PF / trade-count delta before merging.
- **Phase 0 alone is purely additive**: instrumentation + helper
  script. No behavioral change. Land it standalone.

## Out of scope (parked)

- Tick-vs-1m intrabar resolution. Live decides at sub-bar
  granularity; backtest decides at 1m close. Closing this gap
  requires a synthetic tick model — bigger than this plan.
- MACD post-warmup drift (~0.09 max diff).
- AI re-enable. All current evidence is from `--no-ai=true` runs.
- SimBroker fill model differences.
- Late-session 16:00 ET hours-block delta — instrumentation noise
  per Phase 1 refutation.
- Sentiment/news/halt screening differences (already verified
  identical for 2026-04-29 universe).

## Halt conditions

- Phase 0.1 surfaces a fifth root cause for the bar.Time-not-set
  issue not in the EntryGated emit chain — halt and update plan.
- Phase 2 sub-reason bucket is dominated by a single sub-reason
  that differs > 90% between sides — likely a structural mismatch
  (strategy version drift, config divergence) rather than a logic
  bug. Halt and verify config + version equivalence first.
- Phase 3 chain divergence is structural and (b) carve-out is
  rejected — re-scope to delta-stuck parity comparison.
- Live-side reason-class distribution shifts > 2% during validation
  — signals an unintended live regression. Halt.
- Three distinct fix hypotheses fail in any phase without
  converging. Halt and re-scope.

## Reference data

- Plan v1: `_workspace/parity_trade_convergence_plan.md`. Phase 1
  removed in v2 based on 2026-04-30 investigation.
- Indicator-layer parity: PRs #20, #21, #22, #23, #24, #25.
- Universe verified identical 2026-04-29: `comm -23 /tmp/live_underlyings.txt
  /tmp/bt_universe.txt` returned zero rows.
- Hours-gate refutation backtest: `bt-41261bbb99e1d177`, 833
  hours-blocks for AAPL alone (vs ~50 live), all in pre-market or
  post-RTH minutes outside the 09:30-15:45 window.
- Tagging discriminator: `payload->>'tag' LIKE 'backtest_%'`. The
  v1 plan's `signal_id NOT LIKE '%backtest%'` filter does not work
  in this codebase.
