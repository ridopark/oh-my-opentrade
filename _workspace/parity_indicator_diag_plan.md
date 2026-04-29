# Parity indicator diagnostic patch

Purpose: extend the Phase 2 EntryGated diagnostic payload with raw indicator
inputs (VolumeSMA, raw bar volume, MACD line/signal/histogram) so any future
live-vs-backtest gate divergence can be attributed via SQL diff to the
specific indicator value that disagreed, not just the gate outcome.

Owner: ridopark
Started: 2026-04-29
Status: Draft. Prereq audits complete (see below).

## Background — 2026-04-29 divergence

Three independent gate divergences observed on today's session
(account=default, env_mode=Paper) where backtest fired entries that live
blocked at the same (symbol, 5m bar):

1. avwap_v4 breakout vol gate. Live volRatio 0.41–0.75x; backtest fired
   the same 09:30 5m bar implying ratio >= 2.5x. bar.Volume from
   `market_bars` matches both sides, so the divergence is in
   `Indicators.VolumeSMA`. Today's payload exposes only the ratio
   (`indicators.volumeRatio`), not the numerator/denominator
   independently.
2. avwap_v4 confluence sub-score. META/AAPL/MSFT/LLY blocked at score
   7 < 8 in live; backtest fired same bars. Components show DP fired but
   `ScoreDarkPool` returns a 0-10 sub-score driven by `DPRatioZScore`,
   `DPBuyRatio`, `DPLargePrintPct`. Today's payload exposes the firing
   booleans but not the underlying numeric inputs.
3. macd_only_v1 RBLX. Live emitted `crossover: no MACD crossover
   detected` at every 5m bar from 09:34 to 10:29. Backtest fired RBLX
   at 09:34 with strength=0.88 — implying the MACD line / signal line
   crossed in backtest but not in live for the same bar. No payload
   field exposes MACDLine / MACDSignal at the EntryGated moment.

The 2026-04-28 byte-level IndicatorSnapshot parity claim (parity plan
"Warmup parity FIXED") is contradicted by today's evidence. We cannot
locate which indicator drifted without raw values on the blocked-row
payload.

## Pre-flight — what's been ruled out

DP aggregator semantics. Re-aggregated `market_trades` for 2026-04-29
09:30-10:55 ET vs `darkpool_bars` rows livedarkpool wrote: 84 (symbol,
5m bucket) pairs, all four numeric fields (dp_ratio, dp_volume,
total_volume, large_print_volume) match to 4 decimals. So the
livedarkpool `AddTrade` / `windowToBar` math is correct — the residual
DP-related divergence (item 2 above) must come from upstream
DPRatioZScore or DPBuyRatio computation, not the aggregator output.

Phase 5(d) full audit can stay deferred. This patch is the cheaper
diagnostic path.

## Approach

Stamp the raw inputs that drive each gate into the EntryGated payload at
emit time. SQL-diff workflow then becomes:

    SELECT a.payload->'indicators'->>'volumeSMA',
           b.payload->'indicators'->>'volumeSMA'
    FROM strategy_signal_events a
    JOIN strategy_signal_events b USING (symbol, ts)
    WHERE a.env_mode = 'Paper'
      AND b.payload->>'tag' LIKE 'backtest_%'
      AND a.symbol = 'AMD' AND a.ts = '2026-04-29 13:34:59...'

For this to work, an in-process backtest run must also emit EntryGated
rows with the same payload fields. The current code path already does
this (instance.go:EmitDomainEvent fires for both live and backtest
runners — Phase 2 confirmed); only the field set is missing.

## Files and changes

### 1. domain/event.go — extend payload schema

`backend/internal/domain/event.go:313` — `EntryGatedIndicators`. Add:

    Volume       float64 `json:"volume"`         // bar.Volume at gate eval
    VolumeSMA    float64 `json:"volumeSMA"`      // 20-bar 1m SMA at gate eval
    MACDLine     float64 `json:"macdLine"`
    MACDSignal   float64 `json:"macdSignal"`
    MACDHistogram float64 `json:"macdHistogram"`
    EMA21        float64 `json:"ema21"`          // for regime gate
    EMA50        float64 `json:"ema50"`
    StochK       float64 `json:"stochK"`
    StochD       float64 `json:"stochD"`

`VolumeRatio` already exists; keep it for UI compatibility but make it
secondary — the diff target is `Volume / VolumeSMA`, computed at query
time.

Add to `EntryGatedComponent` (event.go:293) the numeric inputs that
drive each scorer's sub-score, since the boolean `Fired` and weight
slot don't show why DP scored low:

    SubScore int `json:"subScore"`   // actual ScoreDarkPool / ScoreFib / ... return value
    Inputs   map[string]float64 `json:"inputs,omitempty"`
    // For darkpool: {"dpRatio", "dpRatioZScore", "dpBuyRatio", "dpLargePrintPct"}.
    // For fib: {"distancePct", "level"}. Etc.

### 2. strategy/instance.go — stamp at emit boundary

`backend/internal/app/strategy/instance.go` (the `EmitDomainEvent` path
that produces the `EntryGated` event). At the stamp site, copy from the
state's `IndicatorData` (already passed through to the gate code via
`s.Indicators` — see avwap_v1.go:1710 `s.Indicators.VolumeSMA`). Mirror
the existing `AVWAPState` snapshot pattern: read once, copy into the
payload struct, no extra computation.

### 3. confluence.go — return Inputs from each scorer

`backend/internal/domain/strategy/confluence.go:280` (`ScoreDarkPool`)
already has the inputs in scope (`ind.DPRatio`, `ind.DPRatioZScore`,
`ind.DPBuyRatio`, `ind.DPLargePrintPct`). Add a populated `Inputs` map
on the returned `ConfluenceResult.Components[0]`. Same for `ScoreFib`,
`ScoreVolume`, `ScoreCandle`, `ScoreBand`, `ScoreInducement`,
`ScoreWhale`. Mechanical change; no logic change.

### 4. backtest tag

`backend/internal/app/strategy/runner.go:570` (`TagBacktest`) already
sets a label suffix; verify the EntryGated payload also carries a
`tag` field set to `"backtest_<id>"` when `htfLabelSuffix != ""`. If
not, add one to `EntryGatedPayload` and stamp it from
`r.htfLabelSuffix`. This is the JOIN key for the SQL diff — without
it, live and in-process-backtest rows are indistinguishable in the
`strategy_signal_events` hypertable (per parity plan note "log
pollution from in-process backtest").

## Tests

Unit:
- `domain/strategy/confluence_test.go` — assert each `Score*` function
  populates `Components[0].SubScore` matching `ConfluenceResult.Score`
  and `Components[0].Inputs` carries the expected keys.
- `domain/strategy/contract_test.go` — already constructs an
  `IndicatorData` with `VolumeSMA` and `MACDLine`; extend a test that
  builds an `EntryGatedPayload` and asserts the new fields round-trip
  through JSON marshalling.

Integration:
- `app/strategy/instance_test.go` — existing tests that exercise
  `EmitDomainEvent` for `EntryGated`. Extend assertions to cover the
  new payload fields are stamped from the underlying `Indicators`
  field. Use a fixture `IndicatorData` so the assertion is exact-equal,
  not float-tolerant.

No new test files needed.

## Validation workflow

After ship, on the next divergent session:

1. Run the EOD backtest in-process with no_ai=true matching the live
   AI flag (parity plan Phase 1 setting).
2. Verify both write to `strategy_signal_events` with distinguishable
   `payload->>'tag'` values.
3. SQL diff for any (symbol, ts) pair where one side fired and the
   other blocked:

       WITH diverge AS (
         SELECT symbol, ts FROM strategy_signal_events
         WHERE ts >= '<session_start>' AND ts < '<session_end>'
         GROUP BY symbol, ts
         HAVING COUNT(DISTINCT status) > 1
       )
       SELECT
         d.symbol, to_char(d.ts AT TIME ZONE 'America/New_York','HH24:MI') et,
         live.payload->'indicators' AS live_ind,
         bt.payload->'indicators' AS bt_ind,
         live.payload->'confluence'->'components' AS live_comp,
         bt.payload->'confluence'->'components' AS bt_comp
       FROM diverge d
       JOIN strategy_signal_events live
         ON live.symbol=d.symbol AND live.ts=d.ts
        AND COALESCE(live.payload->>'tag','') = ''
       JOIN strategy_signal_events bt
         ON bt.symbol=d.symbol AND bt.ts=d.ts
        AND bt.payload->>'tag' LIKE 'backtest_%';

The first field that diverges is the root cause input. If
`indicators.volumeSMA` differs but `bar.volume` matches, the warmup
or rolling window has drifted. If `confluence.components[darkpool].inputs.dpRatioZScore`
differs but inputs.dpRatio matches, the dpRolling buffer state has
drifted. Etc.

## Blast radius

- Schema additive only. JSON-tagged `omitempty` on all new fields
  means existing dashboard/UI consumers ignore unknown keys.
- Storage cost: ~250 bytes per blocked row per evaluation. Today's
  load is ~850 rows/day. ~0.2 MB/day uncompressed; negligible after
  TimescaleDB compression.
- Hot path: zero new computation. Every field read is from already-
  populated `IndicatorData` / `ConfluenceResult` structs the gate
  code already consults. Only the marshalling cost increases by ~9
  scalar fields and one map per blocked emit.
- Backtest cost: same — backtest runner emits via the same instance
  context, so its EntryGated rows pick up the new fields automatically.

Estimated scope: ~80 LOC code + ~120 LOC tests across 4 files.

## Open questions

- Does `payload->>'tag'` already exist on EntryGated rows from the
  in-process backtest path? Earlier query `SELECT DISTINCT
  payload->>'tag' FROM strategy_signal_events WHERE ts >=
  '2026-04-29 00:00:00-05'` returned only empty string for 847 rows.
  Today's backtest run id `9fceaac6` was an HTTP-driven harness, not
  in-process via `TagBacktest` at the strategy runner — so this run
  did not write to `strategy_signal_events` at all (the strategies
  ran inside the backtest runner's makeSnapshotFn loop, not through
  the live runner's emit path). Confirm whether the SQL-diff
  workflow needs the backtest runner to emit EntryGated rows
  through the same path, or if it should write to a parallel
  `backtest_signal_events` table that joins on (run_id, symbol, ts).
- Should the `Inputs` map be a strongly-typed struct per scorer
  instead of a generic `map[string]float64`? Strong typing helps the
  UI but couples `domain/event.go` to `domain/strategy`. Accept the
  generic map for now; promote to typed structs if the UI needs
  them.

## Decision log

- 2026-04-29: Chose payload extension over a separate
  `parity_observations` hypertable (the option in
  `_workspace/parity_observability_followup.md`). The hypertable is
  the right shape for ongoing SLO monitoring, but for one-shot
  divergence triage the existing `strategy_signal_events` payload is
  faster to read, faster to ship, and reuses Phase 2 infrastructure.
  Promote to the dedicated hypertable when the diagnostic load
  justifies it.
- 2026-04-29: DP aggregator output ruled out as a divergence source
  via direct re-aggregation of `market_trades` (84/84 buckets match
  to 4 decimals). DPRatioZScore-style upstream divergence remains
  possible and is what this patch surfaces.

## Related files

- backend/internal/domain/event.go (EntryGatedPayload, EntryGatedIndicators, EntryGatedComponent)
- backend/internal/domain/strategy/contract.go (IndicatorData fields)
- backend/internal/domain/strategy/confluence.go (Score* functions)
- backend/internal/app/strategy/instance.go (EmitDomainEvent stamp site)
- backend/internal/app/strategy/runner.go (TagBacktest, htfLabelSuffix)
- backend/internal/app/monitor/indicators.go (VolumeSMA, MACDLine sources)
- backend/internal/app/strategy/builtin/avwap_v1.go (consumer of s.Indicators in gates)
- _workspace/backtest_live_parity_plan.md (Phase 2 background)
