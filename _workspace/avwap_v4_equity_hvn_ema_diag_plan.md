# Plan: avwap_v4_equity HVN + EMA tag-only diagnostic

Date: 2026-05-07
Status: Plan, awaiting Phase 0 build verification before factor wiring.
Driver: two indicator primitives (VolumeHistogram, EMARolling) survived
the whale_pullback_v1 sunset as reusable domain. avwap_long_v1 plan
section 11 lists both as deferred ENGINE_CHANGEs (HVN clear-path veto;
EMA-pullback-after-VWAP-break entry type). This plan picks the cheapest
predecessor: ship both as tag-only diagnostic fields so quant can
correlate against existing avwap_v4_equity trade logs without engine
behavior change. Promotion to active wiring is OUT OF SCOPE here.

## 1. Context

avwap_v4_equity is the live-tuned long/short equity strategy
(PaperActive, priority 80). Its confluence stack already has 7+ factors
plus cofire_veto in shadow. Two candidate features remain unreached:

1. HVN clear-path veto (VolumeHistogram). Hypothesis: fading INTO an
   HVN zone within ~0.5x ATR of entry hurts both LONG and SHORT
   entries; HVNs absorb momentum and clip MFE before chandelier arms.
2. EMA-pullback-after-VWAP-break (EMARolling). Hypothesis: pullback to
   a faster EMA on the new side of a broken AVWAP is a continuation
   trade with a tighter stop than the existing AVWAP-pullback (which
   was disabled in v4_equity for low intraday WR).

Both are flagged ENGINE_CHANGE because neither has a path through the
existing TOML knobs. Active wiring (veto wrapper for HVN, new entry
evaluator for EMA) is meaningful Go work (~200 LOC each plus parity
tests). Before paying that cost we want evidence the signals add edge.

The cheapest predecessor: emit each indicator's value as a TAG on every
entry signal that fires today. Tags flow through `newEntrySignal` into
the trade log. Quant can then run forward-return correlation against
the existing v10 tuning trade log (n=551) and a fresh symbol-stratified
holdout, without any engine behavior change.

## 2. Constraints (locked)

- Tag-only. No vetos. No new entry types. No score-affecting factors.
- Default OFF: both diagnostic blocks `*_diag_enabled = false`. Engine
  behavior is bit-identical to today when disabled.
- One source file edited in code path: `backend/internal/app/strategy/builtin/avwap_v1.go`.
- One new test file: `backend/internal/app/strategy/builtin/avwap_v1_diag_parity_test.go`
  to lock byte-level live/backtest parity on the warm-on-restart path
  (model on whale_pullback's HVNFingerprint test).
- One TOML edited: `configs/strategies/avwap_v4_equity.toml` (new
  knobs declared with diag enabled = true so the live PaperActive run
  starts emitting tags immediately).
- ReplayOnBar IS wired (warm-on-restart from day one). Both indicators
  exit warmup hot so the very first post-restart entry signal carries
  populated tags. Diverges from the inducement/cofire cold-warm
  pattern; new parity test backstops the change.
- No changes to `warmup.EquitySpec` (5m provides 800 bars; EMA(9)
  needs ~36, HVN at lookback=5 sessions needs ~390 — comfortable
  headroom). No changes to `computeConfluence` signature, no changes
  to entry priority chain.
- Quant's preferred sidecar harness (the omo-signal-corr fork that
  reads bars from TimescaleDB and joins to trade entries) is OUT OF
  SCOPE here. It's the natural follow-up but does not depend on this
  PR landing. The tags this PR emits will be available to either
  workflow.

## 3. Honest data position

What we have:
- avwap_v4_equity trade logs from prior tuning (e.g. v10_maxloss_0012.json,
  n=551, PF 0.83, WR 51.4%) and assorted full-window runs.
- Both indicator primitives in tree with their own unit tests
  (ema_rolling_test.go, volume_histogram_test.go).
- Inducement (Factor 7) is the closest analog for "tag-only telemetry
  with optional active gating" patterns in avwap_v1.go.
- Cofire is the closest analog for "veto wrapper threaded after
  evaluator chain". Relevant if a follow-up promotes HVN to veto.

What we do NOT have:
- Bar-context attached to trade-log entries. Tags emitted at entry
  time will give (HVN distance, density, POC distance) and (EMA value,
  EMA distance bps, EMA distance ATR) AT THE ENTRY BAR but not the
  forward-return path. The sidecar harness is what closes that loop.
- Evidence either signal correlates with winners or losers on
  avwap_v4_equity specifically. This PR is the instrument that lets us
  collect that evidence in production trade logs over time.

## 4. Phase 0 - build + parity sanity

Goal: confirm the new state additions to AVWAPState and the tag-emission
code do not break existing tests, snapshot serialization, or
indicator-snapshot byte parity between live and backtest.

Steps:

1. Add new state fields to `*AVWAPState`:
   - For HVN: `hvnSessions []*sessionHist`, `hvnMerged map[int]float64`,
     `hvnSet map[int]struct{}`, `hvnAnchor float64`, `hvnBinBps float64`.
     All `json:"-"` (rebuilt via warmup; consistent with whale_pullback
     pattern).
   - For EMA: `EMA *start.EMARolling` (`json:"-"`), `EMAValue float64`,
     `EMAReady bool` (last two serialize for sanity; on Init-from-prior
     the EMA pointer is reset to nil and ReplayOnBar re-warms it from
     the warmup bar feed -- same defensive pattern whale_pullback uses,
     avoids partial-state-restoration bugs).
2. Add new TOML knobs (parsed in `parseAVWAPConfig`):
   ```
   hvn_diag_enabled, hvn_lookback_days, hvn_bin_bps,
   hvn_threshold_pct, hvn_rth_only
   ema_diag_enabled, ema_diag_period
   ```
   Defaults below. Each diag-enabled defaults to FALSE in the parser,
   then the v4_equity TOML overrides to TRUE on the live spec only.
3. Wire per-bar updates into BOTH OnBar (after `updateCofireVetoState`,
   before regime gate) AND ReplayOnBar (immediately after the existing
   AVWAP / ring-buffer / AboveCount/BelowCount updates). The two call
   sites must invoke the same updater methods on `*AVWAPState` so the
   warmup feed and the live feed produce identical state at any given
   bar. HVN session-rollover detection uses date-of-bar in
   `cfg.AllowedHoursTZ` (lift from whale_pullback verbatim) so warmup
   bars assigned the right session index.
4. Add tag fields to `entryTelemetryTags`:
   - HVN: `hvn_dist_atr`, `hvn_density_above`, `hvn_density_below`,
     `poc_dist_bps`. Emitted only when `hvn_diag_enabled && len(hvnSet) > 0`.
   - EMA: `ema_value`, `ema_dist_bps`, `ema_dist_atr`. Emitted only
     when `ema_diag_enabled && EMAReady`.
5. Add new parity test file
   `backend/internal/app/strategy/builtin/avwap_v1_diag_parity_test.go`:
   - `TestHVNFingerprintParity_LiveVsWarmup` - feed the same N bars
     through OnBar (live path) and through ReplayOnBar (warmup path)
     against fresh AVWAPState instances; assert byte-equal
     fingerprints (sorted bin index -> volume map). Model on
     whale_pullback_v1's HVNFingerprint test.
   - `TestEMAValueParity_LiveVsWarmup` - same fixture; assert EMAValue
     equal to ulp at the end of the bar feed.
   - `TestHVNState_InitFromPrior_ResetsAndRewarms` - Init with a prior
     state, confirm pointer is nil and merged is empty after Init, then
     feed warmup bars via ReplayOnBar and confirm hvnSet populates.
   - `TestEMAState_InitFromPrior_ResetsAndRewarms` - same shape for EMA.
6. Run existing tests:
   - `go test ./backend/internal/app/strategy/builtin/...`
   - `go test ./backend/internal/app/bootstrap/...`
   - `go test ./backend/internal/adapters/strategy/store_fs/...`
7. Run a backtest of avwap_v4_equity over the v4 holdout window
   (2026-02-01 -> 2026-05-06, 5m, 10 bps, 34-sym universe) with diag
   knobs OFF. Compare metrics against the latest baseline from
   `_workspace/avwap_v4_equity_tune/baseline.json` (or whichever JSON
   captures the pre-tag baseline). Required: PF / trade count / final
   equity match exactly (this proves zero-impact when diag is off).
8. Run the same backtest with diag knobs ON. Required: PF / trade count
   / final equity match the OFF run exactly (tags are emit-only; even
   though indicators now warm hot in ReplayOnBar, they do not influence
   selection or scoring). Inspect 10 random trades in the result JSON
   and confirm:
   - New tag keys present on every entry (not just post-cold-warm).
   - HVN distances within 10% of `hvn_lookback_days * day_atr` magnitude.
   - EMA values within 5% of entry-bar close.
   - The first entry of the backtest carries non-empty HVN tags
     (warm-on-restart works; no first-N-sessions cold blackout).

Decision rule (Phase 0):
- All existing tests pass AND new parity tests pass AND OFF-vs-baseline
  parity holds AND ON-vs-OFF parity holds AND tags are present-and-
  plausible from the FIRST entry -> proceed to Phase 1.
- Any test fails OR PF/trade-count delta when diag is off -> stop,
  root-cause, fix.
- Tags missing on first entry (cold-warm slipped through) OR
  HVNFingerprint live-vs-warmup byte-equality fails -> stop. Either
  the ReplayOnBar wiring is wrong or the session-rollover anchor logic
  diverges between the two paths.
- Tags present but implausible (HVN density always 0, EMA ready never
  fires) -> stop, fix the wiring.

Phase 0 cost: ~220 LOC implementation (200 src + 20-line parity test
helpers) + ~150 LOC test file + 2 backtests (~5 minutes each).

## 5. Phase 1 - ship the tagged build to PaperActive

1. Commit Phase 0 changes. Single commit, single file diff (plus the
   TOML knob additions).
2. /rebuild-commit-restart so the PaperActive run starts emitting tags.
3. Verify the next live entry signal in `logs/omo-core.log` carries
   the new tag keys.

Phase 1 cost: ~5 minutes wall-clock after the build.

## 6. Phase 2 - sidecar correlation (DEFERRED, separate plan)

Quant's preferred follow-up, not part of this PR:
- Fork `backend/cmd/omo-signal-corr/main.go` to a new tool that reads
  trade entries (or live trade-log rows once the tagged PaperActive
  run accumulates them) and computes forward-return PF/WR split by
  HVN-near-entry true/false and by EMA-distance bucket.
- Stratify by LONG vs SHORT, by anchor (session_open / pd_high / pd_low),
  by hour bucket, by symbol. Require both time-split and symbol-split
  holdout per `feedback_factor_validation.md`.
- Decision: PF lift >= 0.10 absolute on BOTH splits with directional
  agreement -> draft the active-promotion plan (HVN veto wrapper, or
  EMA tag-only-but-tighter-confluence-gate, or shelve).

This plan does NOT block on Phase 2. The tags shipped in Phase 1 are
the input the harness needs.

## 7. Defaults

Code defaults (set in `parseAVWAPConfig`):
- `hvn_diag_enabled = false`
- `hvn_lookback_days = 5`
- `hvn_bin_bps = 10`
- `hvn_threshold_pct = 80.0`
- `hvn_rth_only = true`
- `ema_diag_enabled = false`
- `ema_diag_period = 9`

Spec override in `configs/strategies/avwap_v4_equity.toml` (so the
PaperActive run emits tags but no other strategy is affected):
- `hvn_diag_enabled = true`
- `ema_diag_enabled = true`

## 8. Files touched

- `backend/internal/app/strategy/builtin/avwap_v1.go` (state, parser,
  per-bar update calls in OnBar AND ReplayOnBar, telemetry tags).
  ~220 LOC additive.
- `backend/internal/app/strategy/builtin/avwap_v1_diag_parity_test.go`
  (new file). HVN fingerprint parity, EMA value parity, Init-from-prior
  reset+rewarm. ~150 LOC.
- `configs/strategies/avwap_v4_equity.toml` (new diag knobs,
  diag-enabled = true).

That is the entire surface. The new parity tests are the price of
warm-on-restart; the primitives' own unit tests still cover the math.

## 9. Blast radius

- Engine behavior when both diag knobs are OFF: bit-identical to
  current. Verified by Phase 0 OFF-vs-baseline backtest.
- Engine behavior when diag knobs are ON: trade selection unchanged
  (no veto, no factor, no entry-type change), only entry-signal tags
  gain new keys. Verified by Phase 0 ON-vs-OFF parity check.
- AVWAPState size grows by ~5 fields. Snapshot serialization unaffected
  for json:"-" fields; EMAValue+EMAReady scalars add ~16 bytes per
  symbol-state. Negligible.
- Other strategies unaffected (config knobs only land on avwap_v4_equity
  TOML; defaults are off).
- Live trading impact: avwap_v4_equity is PaperActive only. Even a
  worst-case bug in the tag-emission path can't move capital.

## 10. Risks and mitigations

- New state field accidentally affects entry decisions -> Phase 0 OFF
  vs baseline parity catches it.
- HVN session rollover bug emits zero tags -> Phase 0 step 7 inspects
  10 random trades' tags for plausibility.
- EMA or HVN warm-on-restart diverges between live and backtest ->
  the new parity test file catches this. Whale_pullback already proved
  the pattern works for HVN; AVWAP's existing AVWAPCalc+AboveCount
  parity in ReplayOnBar proves the warmup feed is deterministic.
- HVN session-rollover detection picks the wrong anchor in the warmup
  feed (e.g. uses first warmup bar's close instead of session-open
  close) -> HVN fingerprint test fails; lift whale_pullback's date-
  detection logic verbatim to avoid re-deriving it.
- Tag bloat in trade-log JSONs -> 7 new keys per entry, ~120 bytes
  each. Trade logs already carry ~3KB tags. Negligible.
- Quant decides to skip the sidecar harness -> tags accumulate harmlessly.
  No cost.
- A future engine refactor changes the tag flow and silently drops the
  new keys -> follow-up integration test could lock the keys' presence
  on a synthetic entry. Not in scope here.

## 11. Out of scope for this iteration

- Any veto behavior using HVN or EMA.
- Any new entry type using EMA.
- Any change to `computeConfluence` signature or factor scoring.
- The omo-signal-corr fork / sidecar correlation harness (Phase 2 in
  this plan; needs its own plan once we have a week of tagged
  PaperActive trades or quant decides to backtest-replay for tags).
- Promoting either feature to active in any other strategy
  (avwap_v4 options, macd_only_v1, etc.). Wait for evidence on the
  equity variant first.
- Touching whale_pullback_v1 (Deactivated; out of scope).

## 12. Acknowledgment requested

Tag-only with warm-on-restart. One source file + one new test file +
one TOML. Default off in parser, default on only on the v4_equity
spec. Request explicit go-ahead to:
- proceed with Phase 0 (implement, run unit + parity tests, run two
  parity backtests, decide Phase 1 / stop), OR
- adjust scope first (e.g. skip the EMA half if quant wants HVN-only;
  defer ReplayOnBar wiring to a follow-up if the parity test cost
  feels excessive).
