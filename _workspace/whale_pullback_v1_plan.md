# whale_pullback_v1 — Implementation Plan

Author: ridopark
Date: 2026-05-06
Status: awaiting sign-off

## Goal

Add a new builtin equity strategy `whale_pullback_v1` based on Parker
Brooks' three-step rule: confirm direction with session VWAP, wait for
retracement to EMA(N), confirm clear path on the volume profile.
Paper-only on first ship.

## Strategy spec

5-minute US equities, RTH only, LONG+SHORT.

1. Direction filter (session VWAP, resets at RTH open)
   - require `min_trend_bars` consecutive closes on the trend side of
     VWAP
   - the latest qualifying bar must close >= `vwap_break_atr × ATR`
     away from VWAP — large-candle break, also serves as the
     sideways-chop guard
2. Pullback entry — EMA(N), N tunable (default 9)
   - bar wick comes within `pullback_touch_atr × ATR` of the EMA
   - confirmation bar body closes on the VWAP-trend side of the EMA
   - emit entry signal
3. Volume-profile veto (RTH-only profile from prior `vp_lookback_days`
   sessions)
   - bin width = `vp_bin_bps`
   - HVN = bin volume >= `vp_hvn_threshold_pct` of POC
   - reject signal if any HVN exists within `vp_clear_atr × ATR`
     ahead of current price in trade direction (only when
     `vp_required = true`)

Exit: 2 consecutive bar bodies close on the opposite side of EMA(N) OR
price hits `entry ± atr_stop_mult × ATR` OR EOD flatten via standard
`EOD_FLATTEN` exit rule.

## Final params (after quant-analyst review)

ATR-relative thresholds replace bps-absolute to avoid breaking on
$30 vs $800 names.

    ema_period            = 9
    pullback_touch_atr    = 0.15
    min_trend_bars        = 3
    vwap_break_atr        = 0.5
    vp_lookback_days      = 5
    vp_bin_bps            = 10
    vp_hvn_threshold_pct  = 80.0
    vp_clear_atr          = 0.6
    vp_required           = true
    vp_rth_only           = true
    atr_stop_mult         = 1.75
    exit_body_closes      = 2
    cooldown_seconds      = 1800
    max_trades_per_day    = 3
    allowed_hours_start   = "09:35"   # skip first 5m bar of session
    allowed_hours_end     = "15:30"
    allowed_hours_tz      = "America/New_York"

Universe: copy from `avwap_v4_equity.toml` — 34 large/mega-cap
equities + ETFs.

Lifecycle: PaperActive, paper_only = true.

## Decisions deliberately deferred

Crowding gate against AVWAP (quant flagged correlation as #1 risk).
Shipping v1 without it — paper trading is the validation environment
for that question. Add as v1.1 if confirmed in paper data.

## Generalized primitives (promoted to `domain/strategy/`)

Policy override: rather than inlining reusable indicator code in
`builtin/`, promote anything that could serve future strategies as
its own typed, tested primitive. Follows the existing convention
established by `session_vwap.go`, `anchored_vwap.go`,
`weekly_anchor.go` — exported types in the same package, one file per
primitive, independently unit-tested.

Promoted

1. `domain/strategy/volume_histogram.go`
   - `VolumeHistogram` with `New(binBps, anchor float64) *VolumeHistogram`,
     `Accumulate(Bar)`, `POC() float64`, `HVNBins(thresholdPct) []int`,
     `HasHVNInRange(low, high, thresholdPct float64) bool`
   - Implements uniform-across-range distribution (volume split
     proportionally across every bin a bar's `[Low, High]` touches)
   - Bin alignment via fixed `anchor` + bin_width = `anchor × binBps × 1e-4`
   - NOT marshalable. Per existing convention (`SessionVWAP`,
     `AnchoredVWAP`), indicator primitives are tagged `json:"-"` in
     strategy state and rebuilt from raw bars via `ReplayOnBar`. The
     warmup window provides the bars; the strategy reaccumulates
   - The whale_pullback state holds N of these (one per session in the
     lookback window) and a merged frozen view; the merge logic stays
     strategy-local because the lookback policy is a strategy concern

2. `domain/strategy/ema_rolling.go`
   - `EMARolling` with `New(period int)`, `Update(price float64)`,
     `Value() float64`, `IsReady() bool`
   - NOT marshalable. Same convention as VolumeHistogram — rebuilt via
     `ReplayOnBar` during warmup
   - Existing `IndicatorData.EMA9/EMA21/EMA50` are fixed slots and
     `EMAFast/EMASlow` are owned by MACD via `RegisterEMAConfig` —
     strategies needing a strategy-local tunable EMA(N) currently roll
     their own (`crypto_revert_v1.go`, `crypto_tsm_v1.go`). This
     primitive becomes the shared building block; future cleanup can
     migrate those callers onto it (out of scope here)

NOT promoted (strategy-specific, stays in `builtin/whale_pullback_v1.go`)

- The FSM (Idle → Trending → PullbackArmed)
- Rule combination (VWAP break × EMA pullback × VP veto sequencing)
- Exit body-close counter
- TOML knobs that combine primitives with strategy semantics:
  `pullback_touch_atr`, `vwap_break_atr`, `vp_clear_atr`,
  `vp_lookback_days`, `vp_required`, `vp_rth_only`,
  `min_trend_bars`, `exit_body_closes`, `atr_stop_mult`,
  cooldowns, allowed-hours

Primitive constructor knobs (set by strategy at Init, NOT in TOML)

- `VolumeHistogram(binBps, anchor)` — `binBps` from `vp_bin_bps`,
  `anchor` = close of the first bar of the oldest kept session
  (recomputed by the strategy on each window roll)
- `EMARolling(period)` — period from `ema_period`

## Files to touch

1. CREATE `configs/strategies/whale_pullback_v1.toml`
   - schema_version = 2
   - `[strategy]`, `[lifecycle]`, `[routing]`, `[screening]`,
     `[params]`, `[hooks]`, `[[exit_rules]]` sections
   - `[hooks]` `signals = { engine = "builtin", name = "whale_pullback_v1" }`
   - `[[exit_rules]]`: MAX_LOSS by ATR (computed at engine via
     `atr_stop_mult`), STAGNATION_EXIT (60min default), EOD_FLATTEN
   - the body-close-opposite-EMA exit is in-strategy, not a rule

2. CREATE `backend/internal/domain/strategy/volume_histogram.go` and
   `backend/internal/domain/strategy/volume_histogram_test.go`
   - exported `VolumeHistogram` type per spec above
   - tests cover: bin alignment, uniform-across-range distribution,
     POC computation, HVN set, range query, marshal round-trip,
     edge cases (single-bin bar, halted bar with H==L, zero ATR
     equivalent)

3. CREATE `backend/internal/domain/strategy/ema_rolling.go` and
   `backend/internal/domain/strategy/ema_rolling_test.go`
   - exported `EMARolling` type per spec above
   - tests cover: warmup until period bars seen, smoothing constant
     correctness, marshal round-trip

4. CREATE `backend/internal/app/strategy/builtin/whale_pullback_v1.go`
   - struct `WhalePullbackStrategy` with `Meta()`, `WarmupBars()` = 60
     (covers `vp_lookback_days × bars_per_day` for 5 sessions of 5m
     RTH bars = 5 × 78 = 390; clamp to a reasonable per-strategy
     warmup since the EquitySpec already gives 800)
   - struct `WhalePullbackState` carries: rolling EMA(N), session-
     bucketed prior-day bar buffers (rolling 5 sessions of RTH bars),
     volume histogram rebuilt at each new RTH open, FSM phase,
     PrevBar, exit-counter for consecutive opposite body closes,
     PendingEntry/PositionSide bookkeeping mirroring break_retest
   - FSM: Idle → Trending → PullbackArmed → (signal emitted)
   - implements `ReplayableStrategy` from day one
   - inline volume histogram primitive — NOT a domain type yet
   - Uses `IndicatorData.VWAP`, `IndicatorData.ATR` from runner;
     EMA(N) is local because IndicatorData only exposes 9/21/50
   - cooldown + max_trades_per_day + allowed_hours gate same as
     break_retest_v1
   - Marshal/Unmarshal for state persistence

5. CREATE `backend/internal/app/strategy/builtin/whale_pullback_v1_test.go`
   - reuse `testContext` from `orb_v1_test.go`
   - tests:
     - Meta (id, version, name, author)
     - WarmupBars
     - Init_Fresh
     - ImplementsInterface (Strategy + ReplayableStrategy)
     - LongEntry_HappyPath: trend established → pullback wick + body
       on trend side → entry signal
     - ShortEntry_HappyPath
     - VolumeProfileVeto: HVN within `vp_clear_atr × ATR` blocks
       otherwise-valid signal
     - SidewaysOscillation_NoEntry: candles bouncing across VWAP
       under `vwap_break_atr × ATR` produce no entry
     - VWAPBreakBelowATR_NoEntry: trend side reached but no
       large-candle break
     - PullbackTooFar_NoEntry: wick stays farther than
       `pullback_touch_atr × ATR` from EMA
     - BodyClosesOppositeEMA_OneBar_NoExit (1 < `exit_body_closes`)
     - BodyClosesOppositeEMA_TwoBars_Exit
     - ATRStopExit
     - TunableEMAPeriod: ema_period=21 changes pullback target
     - OnEvent_FillConfirmation
     - OnEvent_EntryRejection
     - MarshalUnmarshal preserves FSM, prevBar, oppositeBodyCount,
       trendBars, position bookkeeping (NOT EMA / histogram —
       those rebuild via ReplayOnBar)
     - CooldownPreventsEntry
     - WindowRoll_BinReKeying: feed bars across a session-roll
       boundary, assert veto decisions are stable for a fixed query
       price (the merged histogram should align under the new anchor)
     - EMATiming_TPredecessor: assert pullback rule evaluates against
       EMA(t-1), not EMA(t); construct a bar that would touch under
       EMA(t) but miss under EMA(t-1) and assert no signal
     - VPRequiredFalse_DoesNotBlockOnEmptyProfile: fresh symbol with
       empty histogram and `vp_required=false` allows entry
     - HaltedBar_NoDivideByZero: bar with H==L accumulates into single
       bin without panic
     - AllowedHoursBoundary: 09:35:00 ET allowed, 09:34:59 ET blocked,
       15:29:59 ET allowed, 15:30:00 ET blocked
     - ReplayThenLive_ParityWithLiveOnly: feed N bars via ReplayOnBar
       then M bars via OnBar; compare indicator state to feeding all
       N+M bars via OnBar from the start. Byte-equal histogram and
       EMA values

6. EDIT `backend/internal/app/bootstrap/strategy.go` line ~136
   - append `builtin.NewWhalePullbackStrategy(),` after
     `builtin.NewCopytradeStrategy(),`

## Design principles (SOLID + KISS audit)

The strategy file uses small private collaborator types instead of
piling everything into the State struct. One responsibility per type.
Tests exercise them through the strategy's public OnBar/ReplayOnBar
surface — no separate test files per collaborator (KISS, no test
surface bloat).

Private collaborators (in `whale_pullback_v1.go`)

- `trendDetector` — owns the VWAP-side / break rule.
  - input: bar, VWAP, ATR
  - state: trendBars counter, lastDir
  - output: `(trendDir string, qualifiedBreak bool)`
- `pullbackDetector` — owns the EMA-touch + body-confirm rule.
  - input: bar, EMA, ATR, trendDir
  - state: armed flag, lastTouchBarIdx
  - output: `(armed bool, confirmed bool)`
- `vpVeto` — owns lookback maintenance + clear-path query.
  - input: bar (for accumulation), session-roll signal, query(price,
    atr, trendDir)
  - state: per-session VolumeHistograms, frozen merged HVN set,
    current session date
  - output: `(blocked bool, reason string)`
- `exitController` — owns 2-consecutive-opposite-body-close counter
  and ATR stop check.
  - input: bar, position side, EMA, entry price, ATR
  - state: oppositeBodyCount
  - output: `(exit bool, reason string)`

The strategy `OnBar` becomes a thin orchestrator: gate checks →
`updateStructure(...)` (shared with ReplayOnBar) → exit check (if
positioned) → entry check via collaborators → emit signal.

Anchor simplification (KISS)

Volume histogram `anchor` was originally specified as median close of
lookback window. Replaced with `close of the first bar of the OLDEST
kept session`. Same downstream behavior (it's just a coordinate
offset), no sort, deterministic. Recomputed only on window roll.

Shared structure update helper (KISS / parity)

Both `OnBar` and `ReplayOnBar` call a single `updateStructure(state,
bar, ind)` private function that advances all collaborators. Prevents
the duplicated-logic drift hazard documented in
`project_warmup_window_parity`.

EMA timing convention

The pullback rule "wick within `pullback_touch_atr × ATR` of EMA"
compares bar `t`'s wick against the EMA value as of bar `t-1`
(pre-update). Sequence inside `updateStructure`: read EMA value
first → run pullback rule against it → only then call `EMARolling.Update(close)`.
Otherwise the current bar pulls the EMA toward itself before being
judged against it, which drifts the touch threshold and breaks
determinism between live and backtest. Same convention applies to the
exit rule's "body closes opposite EMA" check.

OCP/DIP/ISP

- Strategy depends on `IndicatorData` (interface-shaped struct) and
  `Context` (interface). No adapter or infrastructure imports.
- All behavior knobs in TOML, parsed once at Init.
- Implements `Strategy` + opt-in `ReplayableStrategy`. No new
  interfaces introduced.

## Volume profile calculation (in-strategy, RTH only)

Inputs
- prior `vp_lookback_days` = 5 RTH sessions of 5m bars for this symbol
- `vp_bin_bps` = 10 (bin width as bps of price)
- `vp_hvn_threshold_pct` = 80.0
- `vp_clear_atr` = 0.6

Bin definition
- Bin width in dollars = max(0.01, anchor × vp_bin_bps × 1e-4)
- `anchor` is the close of the first bar of the OLDEST kept session in
  the rolling window — deterministic, no sorting, recomputed only when
  the window rolls
- Bin index for price p is `floor((p - anchor_floor) / bin_width)`,
  where `anchor_floor = anchor − 5000 × bin_width` (gives ample range
  on both sides; keeps int indices small)
- Bins live in a `map[int]float64` (bin_index → cumulative volume).
  Sparse map avoids preallocating a wide range

Volume distribution per bar
- A 5m bar contributes its full `Volume` distributed UNIFORMLY across
  the bins it touches in `[Low, High]`. Each touched bin gets
  `Volume × (overlap_in_bin / (High − Low))`. Single-bin bars get the
  full volume in one bin
- Rationale: assigning all volume to the close (typical-price method)
  underweights wide-range bars and produces a noisier histogram.
  Uniform-across-range is the standard "volume profile" approximation
  when tick data is unavailable

Window maintenance
- State keeps `[vp_lookback_days]sessionHistogram`, indexed by
  session-start-date string ("YYYY-MM-DD" in America/New_York).
  Each session histogram stores its own `map[int]float64` bins keyed by
  the SAME anchor (recomputed only when the window rolls)
- At the first bar of a new RTH session:
  1. drop oldest session bucket
  2. recompute `anchor` = close of the first bar of the new oldest
     kept session, and `anchor_floor = anchor − 5000 × bin_width`
  3. RE-KEY all kept session bin maps under the new anchor — every
     bin index is recomputed because `anchor_floor` shifted. Iterate
     each kept session's prior bin → centroid price (using the OLD
     anchor) → new bin index. This is required: without re-keying,
     merging old-anchor bins with new-anchor bins produces misaligned
     keys and corrupts the merged histogram
  4. rebuild merged histogram = sum of all kept session bins (now in
     the new keyspace)
  5. recompute `POC = max(merged[bin] over bin)` and the HVN set =
     `{bin : merged[bin] >= POC × vp_hvn_threshold_pct/100}`
- Within a session, bars accumulate into the current session's bucket
  but the merged histogram and HVN set used for VETO queries are
  FROZEN at the session-open snapshot. Today's volume does not
  influence today's clear-path check — the profile is "what came
  before"
- This implies a one-session lag for new symbols added to the universe
  (a known limitation; vp_required can be set false for bootstrap)

Clear-path query (long, at signal time)
- price_floor = current_close
- price_ceiling = current_close + vp_clear_atr × ATR
- bin_lo = floor((price_floor − anchor_floor) / bin_width)
- bin_hi = floor((price_ceiling − anchor_floor) / bin_width)
- if any bin in [bin_lo, bin_hi] is in the HVN set → veto
- short side is symmetric (price_floor = current_close −
  vp_clear_atr × ATR; price_ceiling = current_close)

Replay parity
- `ReplayOnBar` runs the SAME accumulation and session-roll logic as
  `OnBar` (minus signal emission), so the histogram converges to the
  same byte-equal state regardless of replay vs live entry path. This
  matches the warmup parity invariant established in
  `project_warmup_window_parity`

Persistence
- Histogram bin maps and HVN set are NOT marshaled (convention: indicator
  primitives are `json:"-"`). On restart, the warmup window feeds prior
  bars through `ReplayOnBar` and the histogram is rebuilt deterministically
- Strategy's marshalable scalar state (FSM phase, prevBar, counters,
  cooldown, position bookkeeping, current session date) survives across
  restarts — only the indicator primitives rebuild

Cost
- Each bar: O(bins_touched) updates, typically 1-5
- Each session roll (once per RTH open): O(total_bars_in_window)
  rebuild, typically < 400 bars × 5 sessions = 2000 ops. Trivial
- Veto query: O(bins_in_clear_range), typically 5-15 bins for an
  ATR-sized lookahead

Edge cases
- Insufficient warmup (< 1 session in window): if `vp_required = true`,
  treat as veto (no entries until profile is populated). Documented as
  expected first-day behavior on a freshly added symbol
- ATR == 0 (degenerate bar): treat as veto on entry attempt; no signal
  should fire under degenerate conditions anyway
- Bar with High == Low (e.g. halted): assign full volume to the
  single bin containing that price; do not divide by zero

## Architecture confirmations (from go-architect review)

- `warmup/spec.go` is shared infrastructure — DO NOT edit. Per-strategy
  `WarmupBars()` is what gates replay→live flip at
  `app/strategy/instance.go:147`.
- `domain/strategy/volume_profile.go` is a rotation-breakout anchor
  detector, NOT an HVN/POC histogram lookup primitive. Inline a small
  histogram in state for v1; promote to
  `domain/strategy/volume_histogram.go` only if a second strategy
  needs it (CLAUDE.md: no speculative abstractions).
- IndicatorData has fixed EMA9/EMA21/EMA50 + dynamic EMAFast/EMASlow
  (already used by other strategies). Tunable EMA(N) lives in strategy
  state — this is consistent with `crypto_revert_v1.go` /
  `crypto_tsm_v1.go` precedent. Mutating IndicatorData per strategy
  would break warmup parity (see project_warmup_window_parity).
- Use `IndicatorData.VWAP` directly. Verified via existing equity
  strategies — VWAP for equity 5m is session-anchored at RTH open.
- Tests reuse `testContext` (orb_v1_test.go:16-36).

## Verification

Goal-driven success criteria:

1. New tests pass: `go test ./backend/internal/app/strategy/builtin/... -run WhalePullback -v` → all green
2. No regressions: `go test ./backend/internal/app/strategy/builtin/...` → all pre-existing tests still green
3. Build clean: `go build ./backend/...` → no errors
4. Spec loads: confirm `whale_pullback_v1.toml` is picked up at
   bootstrap by running existing spec-store tests, or one targeted
   integration test that exercises spec→registry resolution
5. Manual smoke (NOT automated in this PR): a backtest run command
   I'll show in the PR description, not execute, since this is
   paper-only on first ship and we want quant time to spot-check
   defaults before burning compute

## Out of scope

- Strategy tuning loop (no DNA sweeps in this PR)
- Live promotion (stays paper_only)
- Crowding gate against AVWAP (deferred to v1.1, decision documented
  above)
- Promoting volume histogram to domain primitive
- Dashboard wiring beyond what registration auto-provides
- Any indicator pipeline changes (no IndicatorData edits)

## Sequence

1. Write `domain/strategy/volume_histogram.go` + tests, run package
   tests
2. Write `domain/strategy/ema_rolling.go` + tests, run package tests
3. Write `whale_pullback_v1.toml`
4. Write `whale_pullback_v1.go` (state + FSM + ReplayOnBar +
   Marshal/Unmarshal) — uses the two new primitives
5. Write `whale_pullback_v1_test.go` for the test list above
6. Edit `bootstrap/strategy.go` to register
7. `go build ./backend/...` clean
8. `go test ./backend/...` all green (catches any unintended
   downstream impact from new domain types)
9. Commit
