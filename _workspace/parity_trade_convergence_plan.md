# Parity follow-up: trade-level live↔backtest convergence

## Goal
Make today's backtest trades resemble today's live trades on the same
universe, same strategies (avwap_v4 + macd_only_v1), same date. The
parity_fix_plan.md PR closed the bias-loop regression, so gating-
decision distributions now share shape — but the actual *trades*
still diverge widely (1 of 5 backtest trades has even a loose live
analog). Universe is identical (verified: 34/34 underlyings shared,
0 symmetric diff). The remaining gap is gate-by-gate asymmetry on
shared symbols + option-leg selection.

## Verification baseline (2026-04-29, avwap_v4 only)
| Reason class | Live | Backtest | Delta |
|---|---:|---:|---:|
| slope | 1563 | 1772 | +209 |
| entry_specific | 526 | 145 | -381 |
| **hours** | **323** | **1898** | **+1575 (5.87x)** |
| regime | 263 | 235 | -28 |
| confluence | 105 | 184 | +79 |
| cooldown | 51 | 132 | +81 |
| max_trades | 0 | 78 | +78 |

Live trades today: 4 distinct round trips (QQQ@09:55, OXY@12:00,
MRVL@14:20, SNOW@15:45). Backtest: 5 round trips, 1 of which (OXY
midday) overlaps loosely (different strike, 6-min entry skew).

## Approach (sequential phases — stop-on-failure)
Each phase has a pass criterion. Blocking criterion failure halts the
plan; we do not stack hypothetical fixes.

### Phase 0: Joinable per-bar decision instrumentation
The bias-loop investigation almost mis-diagnosed itself because the
plan's premise was guessed, not measured. This phase invests one
session in proper instrumentation so subsequent phases work on data,
not hypotheses.

- Emit one row per `(symbol, bar.Time, strategy, env)` with: bar.Time
  in UTC, all gating inputs as a flat key=value bundle (atr, slope_bps,
  regime, confluence_count, minute_of_session, allowed_hours_mode,
  current_position, cooldown_remaining), and the first-fired block
  reason (or `accepted` for entries).
- Live: re-use `--emit-gated-diag`'s plumbing, but with `tag = ''`
  (untagged) so live untagged rows already in `strategy_signal_events`
  carry the same payload schema as backtest.
- Verify: a single SQL diff query produces a per-bar mismatch table
  for any chosen `(symbol, bar.Time)` window. No re-instrumentation
  needed for subsequent phases.

Pass: SQL `SELECT ... FROM strategy_signal_events e1 FULL OUTER JOIN
strategy_signal_events e2 ON (symbol, bar.Time) WHERE e1.reason !=
e2.reason` returns useful rows for at least 80% of today's bars where
both sides evaluated. Today's known coverage gap (live bar.Time started
mid-day after commit 942d244b deployed) means full-day coverage is
only available starting tomorrow's session.

Files: `internal/observability/parity/` (extend the existing diag
emitter), `internal/app/strategy/runner.go` (publish on the same
hook live uses).

### Phase 1: Hours gate alignment (suspect #1, +1575 blocks)
The `hours` reason fires 5.87x more in backtest than live on the same
symbols / same date. Three plausible causes:

a) **Timezone drift**: backtest evaluates 1m bars stamped UTC, live
   evaluates 1m bars stamped UTC, but the hours-gate compares against
   ET-derived `minute_of_session`. If backtest's bar.Time is offset
   (e.g. CDT-localized at log time) vs live, the gate's allowed-window
   shifts.
b) **Pre-market evaluation**: backtest runs from `--from 2026-04-29
   00:00Z` so bars from 08:30 ET (pre-market) reach the hours gate.
   Live only sees RTH bars because the upstream RTH filter drops
   pre-market before strategy evaluation.
c) **Session-time weighting mode mismatch**: per project memory
   `project_session_time_weighting.md`, AVWAP migrated to a graduated
   multiplier from a binary `AllowedHours`. If one binary still hard-
   blocks while the other soft-multiplies, that's the divergence.

Investigation order: (b) first (cheapest test — check the bar feed
for pre-market entries blocked by `hours`), then (a), then (c).

Fix per cause:
- (b): wire the same RTH-only filter live uses into the omo-replay /
  backtest bar feed. Pre-market bars should still update warmup state
  but not reach the hours gate.
- (a): canonicalize bar.Time to UTC at the boundary where both paths
  meet, before `minute_of_session` derivation.
- (c): port the canonical implementation; the live path is the source
  of truth.

Pass: backtest `hours` count within +/-25% of live's count
(323 +/- 81, so 242-404) on a re-run of today's full session.

Files (depends on cause): `internal/app/backtest/runner.go`,
`cmd/omo-replay/main.go`, `internal/app/strategy/builtin/avwap_v1.go`.

### Phase 2: entry_specific gate alignment (-381 blocks)
Live fires 526 entry_specific blocks vs backtest's 145. Backtest
either reaches this gate less often (earlier gates absorb the bars)
or the gate's sub-reasons fire differently. Phase 0 instrumentation
should expose the sub-reason — `entry_specific` is a class-level tag
that wraps multiple internal block reasons.

Investigation: bucket `entry_specific` blocks by their full reason
string (not just the prefix) on both sides. Identify the dominant
sub-reason on each side and diff.

Fix: depends on sub-reason. Likely candidates: `entry_specific:
no_setup`, `entry_specific: weak_strength`, `entry_specific:
duplicate_signal`. Each has its own remediation.

Pass: per-sub-reason delta within +/-50% on top-3 sub-reasons.

Files: `internal/app/strategy/builtin/avwap_v1.go` (entry-gate logic).

### Phase 3: Option-strike selection
Live 12:00 OXY 59-CALL vs backtest 11:54 OXY 62-CALL — same direction,
same underlying, different strike. Both selections come from the
same strategy signal but different option-chain selection paths.

Investigation: pin the function on both sides that maps
`(underlying, signal.Side, signal.Strength)` to a specific contract.
Live uses live IBKR option-chain snapshots; backtest uses a
DoltHub-backed historical chain (see project memory
`project_dolthub_options.md` and `project_options_realism.md`).

Fix:
- If the strike-selection heuristic differs (e.g. ATM vs OTM-by-Z),
  port live's heuristic into backtest.
- If the heuristic matches but the chain itself differs (DoltHub
  monthlies vs live weeklies), accept the divergence and carve out a
  sub-rule: "weekly-targeted strategies (avwap_v4 with 3-7 DTE) score
  parity on the underlying-trade level, not option-trade level."

Pass: at least 60% of paired live+backtest trades on the same
underlying within +/-10% strike difference and same `option_right`.

Files: `internal/app/options/`, `internal/app/backtest/option_chain.go`
(or wherever the chain abstraction lives).

### Phase 4: Trade-level validation
After phases 1-3 land, re-run today's full-day backtest and compare:

- Number of distinct round trips: backtest within +/-50% of live's 4.
- Pairing rate: at least 50% of live's trades have a backtest analog
  on the same underlying within +/-15min entry-time skew, same
  direction (LONG/SHORT and CALL/PUT).
- Cumulative reason-class distribution: each class within +/-25% of
  live's count (was already 5.87x off on `hours`).

If pass: ship. If fail: re-investigate the largest residual delta.
After 3 distinct hypotheses fail, halt and re-scope.

## Files (cumulative across phases)
- `backend/internal/observability/parity/` — instrumentation extensions
- `backend/internal/app/strategy/runner.go` — bar.Time normalization,
  diag publish hook
- `backend/internal/app/strategy/builtin/avwap_v1.go` — hours gate,
  entry_specific gate
- `backend/internal/app/backtest/runner.go`, `cmd/omo-replay/main.go`
  — bar feed RTH filter alignment
- `backend/internal/app/options/` (TBD path) — strike selection

## Risks / blast radius
- **Live impact**: phases that modify shared code (avwap_v1.go,
  runner.go) must verify zero behavioral change for live by re-running
  the parity SQL diff and confirming the live-side reason-class
  distribution is unchanged within +/-2%.
- **Historical backtests**: changing the backtest hours filter or
  strike selection re-prices every prior backtest run. Pin the diff
  to a held-out date range (e.g. last 30 days) and report PF / trade-
  count delta before merging.
- **Instrumentation overhead**: per-bar diag rows multiply DB write
  load. Phase 0 should sample-or-throttle if measured load > 5x baseline.

## Out of scope
- Tick-vs-1m intrabar resolution. Live decides at sub-bar granularity;
  backtest decides at 1m close. Closing this gap requires a synthetic
  tick model — bigger than this plan.
- MACD post-warmup drift (~0.09 max diff, 14/78 pairs > 0.01) —
  separate ticket from the original parity_fix_plan.md.
- Sentiment / news / halt screening if they don't appear as universe
  divergences (already verified identical for 2026-04-29).
- AI re-enable. All current evidence is from `--no-ai=true` runs.

## Reference data
- Verification baseline: live untagged rows + backtest run
  `0bfa6d56-ddd5-5cd9-a611-fcdf69dc24f0` for 2026-04-29.
- Original fix: PR #20, commits a5a7115e + b213ffd0 + 7357864a.
- Universe verified identical: `comm -23 /tmp/live_underlyings.txt
  /tmp/bt_universe.txt` returned zero rows.

## Halt conditions
- Phase 0 instrumentation produces malformed rows or > 5x DB load
  vs baseline.
- Any phase fails 3 distinct fix hypotheses without converging.
- Live-side reason-class distribution shifts > 2% during validation
  (signals an unintended live regression).
