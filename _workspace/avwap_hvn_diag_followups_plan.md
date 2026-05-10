# Plan: avwap HVN+EMA diag follow-ups (refactor + enhancements + correlation harness)

Date: 2026-05-07
Status: Plan, awaiting acknowledgment.
Driver: PR #91 (HVN + EMA tag-only diagnostics on avwap_v4_equity) shipped
to main (commit 13469fe6) on 2026-05-07. Three follow-ups were filed in
the PR review and the post-merge quant consultation; they need their own
plan to land:

  Track A. Refactor: extract a shared `hvnTracker` + `fingerprintBinSet`
  collaborator from the duplicated HVN volume-profile logic in
  `whale_pullback_v1.go` and `avwap_v1.go`. ~80 LOC removed. No
  behavior change. Both files have parity tests that protect the change.

  Track C. Diagnostic surface enhancements: enrich the EMA tag set with
  bar-touch booleans + bars-since-AVWAP-cross + max-breach-since-cross
  (the quant-flagged structural signals the original 7-key set could not
  capture); widen the diag emission to `avwap_v4` (options) so SHORT
  coverage is in the data the harness will eventually grade. Tag-only,
  default-off in parser, opted in by both spec TOMLs.

  Track B. Phase 2 of the diag plan: a forked `omo-signal-corr` tool that
  consumes the new HVN/EMA tag set on entries and grades them by
  forward-return PF/WR. Without an owner + 14-day revisit, the tags
  accumulate without ever feeding a decision (cofire_veto pattern --
  shipped 2026-04-20, still in shadow 17 days later as of plan-draft).

Tracks A and C are independent of each other. Track B can run on the
post-PR-91 tag set OR wait for Track C's richer tags; the latter gives a
better grading signal but pushes the 14-day decision window out by
however long Track C takes to ship + accumulate data.

## 1. Context

**Track A code surface today** (post PR #91 merge):

`whale_pullback_v1.go`
- Type `sessionHist` (line ~130). Already package-level; the new
  `avwap_v1.go` HVN code already references it directly.
- State fields on `WhalePullbackState`:
    `sessions []*sessionHist` (json:"-")
    `merged map[int]float64` (json:"-")
    `hvnSet map[int]struct{}` (json:"-")
    `currentAnchor float64` (json:"-")
    `currentBinBps float64` (json:"-")
    `SessionDate string` (json-serialized)
- Helpers (free functions): `rolloverIfNewSession`, `accumulateInCurrentSession`,
  `rebuildMerged`, `vetoByVP` (HVN-aware veto, only consumer of merged set).
- Methods: `(s *WhalePullbackState) HVNContainsPrice(low, high, thresholdPct)`,
  `(s *WhalePullbackState) HVNFingerprint() string`.
- Test parity: `TestWhalePullback_WindowRoll_BinReKeying`,
  `TestWhalePullback_ReplayThenLive_ParityWithLiveOnly`.

`avwap_v1.go` (added in PR #91)
- State fields on `AVWAPState`:
    `hvnSessions []*sessionHist` (json:"-")
    `hvnMerged map[int]float64` (json:"-")
    `hvnSet map[int]struct{}` (json:"-")
    `hvnAnchor float64` (json:"-")
    `hvnBinBps float64` (json:"-")
    `hvnSessionDate string` (json:"-")
- Helpers (free functions, near-byte-identical to whale's): `hvnRolloverIfNewSession`,
  `hvnAccumulateInCurrentSession`, `hvnRebuildMerged`.
- Methods: `(s *AVWAPState) updateHVN(bar)`, `(s *AVWAPState) HVNFingerprint() string`.
- Test parity: `TestAVWAP_HVNFingerprintParity_LiveVsWarmup`,
  `TestAVWAP_HVNState_InitFromPrior_ResetsAndRewarms`.

The duplication is structural, not stylistic. Same algorithm, same
imports (`crypto/sha256`, `encoding/hex`, `sort`, `strconv`), same
session-rollover key (date-of-bar in `cfg.AllowedHoursTZ`), same
re-key-and-merge math.

**Track B context**:

PR #91 shipped 7 new tag keys on every avwap_v4_equity entry signal:
`hvn_dist_atr`, `hvn_density_above`, `hvn_density_below`, `poc_dist_bps`,
`ema_value`, `ema_dist_bps`, `ema_dist_atr`.

Backtest verification (109 entries on 2026-02-01..2026-05-06, 5m, 34 sym)
shows tags populated from the first post-warmup entry.

Live PaperActive started emitting on 2026-05-07 06:19 CDT after the
rebuild-commit-restart in PR #91. At ~5 entries/day live, two weeks
gives ~50 fresh entries. That is too thin for stratified analysis on
its own; backtest-replay over the full available history is the path
that gets to enough n.

The closest precedent is the cofire_veto launch (shipped 2026-04-20):
shadow mode + a decision rule (PF lift >= 0.10 absolute on both
time-split and symbol-split holdouts). That precedent worked because
quant ran the omo-signal-corr harness against the data within days of
shipping. This plan codifies the same handoff for HVN/EMA.

## 2. Constraints (locked)

Track A:
- Refactor only. No behavior change. No new tests beyond what already exists.
- Extracted type lives in the same `builtin` package; no new package or
  internal/domain primitive is introduced.
- Existing parity tests in `whale_pullback_v1_test.go` AND
  `avwap_v1_diag_parity_test.go` must pass byte-equal pre-and-post.
- Backtest of `avwap_v4_equity` over the same 2026-02-01..2026-05-06
  window must reproduce PF=0.7338, trades=109, final_equity=$97471.06
  exactly (the bit-identical reference from PR #91).
- whale_pullback_v1 is currently `state = "Deactivated"` per
  `configs/strategies/whale_pullback_v1.toml`. Refactor MUST NOT change
  whale's veto math; if any whale parity test diverges, stop.

Track C:
- Tag-only. No vetos. No new entry types. No score-affecting factors
  (same lock as PR #91).
- Default OFF in parser; only avwap_v4_equity AND avwap_v4 (options)
  TOMLs opt in. Other strategies built on avwap_v1.go (if any) MUST
  remain default-off; verify via grep.
- New EMA-structural keys are emit-only; no existing key is renamed or
  removed. The redundant `ema_dist_bps` stays for backward compatibility
  with any consumer of the live trade-log stream from PR #91 onward.
- Cross-tracking state (`AVWAPCrossBarsSince`, `AVWAPCrossLastSign`,
  `AVWAPCrossBreachMaxATR`) MAY serialize (json `omitempty`) so
  snapshot inspection works; runtime parity is the protection.
- avwap_v4_equity backtest must reproduce PF=0.7338, trades=109,
  final_equity=$97471.06 EXACTLY against the PR #91 reference.
- avwap_v4 (options) OFF-vs-ON parity must be bit-equal; we are not
  adding ANY engine effect, only new tag content.

Track B:
- Read-only consumer of trade-log JSON. No engine changes. No new
  tag keys. No tag-key renames.
- Tool location: `backend/cmd/omo-signal-corr-hvn-ema/main.go` (new
  binary, clean fork from `backend/cmd/omo-signal-corr/main.go`).
  Existing `omo-signal-corr` binary stays as-is for the inducement /
  cofire / MACD harness it already serves.
- Decision rule (locked, copied from PR #91 plan section 6):
    PF lift >= 0.10 absolute on BOTH time-split and symbol-split
    holdouts AND directional agreement -> draft active-promotion plan.
    Either split fails, or signs disagree across splits -> shelve.
- Holdout discipline per `feedback_factor_validation.md`: time-split
  AND symbol-split, not just one.

## 3. Track A: hvnTracker refactor

### 3.1 Target shape

```go
// backend/internal/app/strategy/builtin/hvn_tracker.go (new file)
package builtin

type hvnTracker struct {
    sessions    []*sessionHist
    merged      map[int]float64
    hvnSet      map[int]struct{}
    anchor      float64
    binBps      float64
    sessionDate string
}

// Update advances the tracker by one bar. Caller passes the config knobs
// inline so the tracker has no dependency on AVWAPConfig or
// WhalePullbackConfig (and does not need to know which strategy owns it).
func (t *hvnTracker) Update(bar start.Bar, lookbackDays int, binBps, thresholdPct float64, rthOnly bool, tz string)

// Reset clears all session state. Called by Init prior-restore and by
// whale_pullback's existing `scalars.* = nil` block.
func (t *hvnTracker) Reset()

// Fingerprint is the existing sha256-over-sorted-bin-set helper.
func (t *hvnTracker) Fingerprint() string

// Read-only accessors for tag emitters (avwap appendHVNDiagTags) and
// veto consumers (whale vetoByVP).
func (t *hvnTracker) HVNSet() map[int]struct{}
func (t *hvnTracker) Merged() map[int]float64
func (t *hvnTracker) Anchor() float64
func (t *hvnTracker) BinBps() float64
func (t *hvnTracker) HVNContainsPrice(low, high float64) bool

// fingerprintBinSet is the package-level helper used by Fingerprint.
// Exposed separately so future bin-set-bearing collaborators can reuse it.
func fingerprintBinSet(set map[int]struct{}) string
```

### 3.2 Migration steps

1. Create `backend/internal/app/strategy/builtin/hvn_tracker.go` with
   the type, constructor (or zero-value usability), `Update`, `Reset`,
   `Fingerprint`, accessors, `fingerprintBinSet`. Lift the math from
   the existing `avwap_v1.go` helpers verbatim (they are already the
   cleaner of the two copies after PR #91).

2. Update `AVWAPState`:
   - Replace 6 fields (`hvnSessions`, `hvnMerged`, `hvnSet`, `hvnAnchor`,
     `hvnBinBps`, `hvnSessionDate`) with one field: `hvn hvnTracker`
     (json:"-"). Zero-value usable.
   - Replace `(s *AVWAPState) updateHVN(bar)` body with
     `s.hvn.Update(bar, cfg.HVNLookbackDays, cfg.HVNBinBps, cfg.HVNThresholdPct, cfg.HVNRTHOnly, cfg.AllowedHoursTZ)`.
     Same disabled-knob early-return.
   - Replace `(s *AVWAPState) HVNFingerprint() string` body with
     `s.hvn.Fingerprint()`.
   - Update `appendHVNDiagTags` to read from `s.hvn.Merged()`,
     `s.hvn.HVNSet()`, `s.hvn.Anchor()`, `s.hvn.BinBps()`.
   - Update `Init` prior-restore to call `st.hvn.Reset()` then
     `st.hvn.binBps = cfg.HVNBinBps` (or absorb the binBps init into
     `Reset(binBps)`).
   - Drop the four imports added in PR #91 that are now hvn_tracker.go's
     concern: `crypto/sha256`, `encoding/hex`, `sort`, `strconv`.

3. Update `WhalePullbackState`:
   - Replace 5 fields (`sessions`, `merged`, `hvnSet`, `currentAnchor`,
     `currentBinBps`, `SessionDate`) with one field: `hvn hvnTracker`
     (json:"-"). NOTE: whale's `SessionDate` is currently json-serialized
     (used for `TradesToday` reset on date-change). Splitting concerns:
     keep `SessionDate string` as json on whale (for `TradesToday`),
     have `hvn.sessionDate` mirror it (the tracker's own internal date
     for HVN session-rollover). Two scalars stored, one tracker-internal,
     one for whale's day-counter logic. Acceptable; alternative is to
     promote the date-tracking responsibility to the tracker AND retain
     a `TradesTodayResetOn string` on whale, which is uglier.
   - Replace `rolloverIfNewSession`/`accumulateInCurrentSession`/`rebuildMerged`
     calls with one `wp.hvn.Update(bar, cfg.VPLookbackDays, cfg.VPBinBps, cfg.VPHVNThresholdPct, cfg.VPRTHOnly, cfg.AllowedHoursTZ)`.
   - Update `vetoByVP` to read from `wp.hvn.Merged()`, `wp.hvn.HVNSet()`,
     `wp.hvn.Anchor()`, `wp.hvn.BinBps()`. Same math, different source.
   - Update `(s *WhalePullbackState) HVNContainsPrice(...)` to delegate
     to `s.hvn.HVNContainsPrice(...)`. Or remove the method entirely
     and have callers (currently just tests) call `wp.hvn.HVNContainsPrice(...)`
     directly.
   - Update `(s *WhalePullbackState) HVNFingerprint() string` to delegate.
   - Drop the `import "crypto/sha256"`/`"encoding/hex"`/`"sort"`/`"strconv"`
     in whale_pullback_v1.go now that the hash lives in hvn_tracker.go.
   - Update `Init` prior-restore: replace the five `scalars.* = nil`
     lines with `scalars.hvn.Reset()`.

4. Run all parity tests:
   - `go test ./backend/internal/app/strategy/builtin/... -run 'TestAVWAP_HVN|TestWhalePullback_WindowRoll|TestWhalePullback_ReplayThenLive'`
   - All four must pass byte-equal pre-and-post the refactor.

5. Run avwap_v4_equity backtest over 2026-02-01..2026-05-06, 5m, 10 bps,
   34-sym universe with diag ON. Required: PF=0.7337945435175424,
   trades=109, final_equity=$97471.06117476482 (exact bit match against
   PR #91's reference). If PF differs by even one ulp -> stop, root-cause.

6. Run whale_pullback_v1 unit + integration tests in full:
   - `go test ./backend/internal/app/strategy/builtin/... -run TestWhalePullback`
   - All must pass. whale_pullback is Deactivated so we cannot validate
     against a live PnL stream, but the full unit suite covers the veto
     math the refactor touches.

### 3.3 Decision rule

- Both parity tests AND the bit-identical avwap_v4_equity backtest pass
  -> ship the refactor PR.
- Any divergence in either parity test OR the backtest -> stop, do not
  ship. Diagnose whether the tracker abstraction has a bug or whether
  the strategies differ in a subtle way the abstraction missed (e.g.
  whale's `vp_required` veto on first day vs avwap's no-veto).

### 3.4 Defaults

None new. The tracker's `Update` signature is already config-driven by
the caller.

### 3.5 Files touched (Track A)

- `backend/internal/app/strategy/builtin/hvn_tracker.go` (new file).
  ~120 LOC.
- `backend/internal/app/strategy/builtin/avwap_v1.go`. Net -50 LOC
  (delete 5 helpers + 6 state fields, replace with tracker calls).
- `backend/internal/app/strategy/builtin/whale_pullback_v1.go`. Net -50
  LOC (similar shape).
- No test files touched.
- No TOMLs touched.

Net: ~120 LOC added in new file, ~100 LOC deleted across the two
strategy files. Net -approximately-zero LOC; the win is the
single-source-of-truth for HVN math, not raw line count.

### 3.6 Blast radius (Track A)

- avwap_v4_equity (PaperActive, priority 80, on live PaperActive run):
  bit-identical backtest is the gate.
- whale_pullback_v1 (Deactivated): unit tests are the gate. No live
  PnL exposure.
- No other strategies use the HVN math.
- No JSON-snapshot schema change (all hvn fields are json:"-" in both
  strategies post-refactor; whale's `SessionDate` stays serialized).
- No new dependency on internal/domain.

### 3.7 Risks (Track A)

- whale's `vp_required` first-day-veto behavior at `vetoByVP:608`
  (`if wp.merged == nil { return true }`) depends on the EXACT shape
  of the merged map being nil on cold start. The tracker's `Reset`
  must produce a state where `Merged()` returns a nil map (not an
  empty non-nil map) for this to keep working. Concrete mitigation:
  the existing `TestWhalePullback_VPRequired_FirstDayVeto` test (or
  equivalent if the test name differs; grep `vp_required.*test`) is
  the protection. If no such test exists, write one before refactoring.
- `SessionDate` split between tracker and state on whale: easy to
  silently break `TradesToday` reset if the two dates drift. Concrete
  mitigation: keep both updates inside one method (whale OnBar's
  existing date-detection block), and the existing
  `TestWhalePullback_RTHSessionRoll_TradesTodayReset` test catches it.
- Refactor lands on a Friday or weekend with no live trades to verify
  -> mitigated by the backtest gate; no need to wait for live PnL.

## 4. Track C: diagnostic surface enhancements

### 4.1 Motivation

Quant review of PR #91 flagged three concrete weaknesses in the shipped
tag set. Status of each on landing this plan:

1. EMA tags lack the structural signals the "pullback to a faster EMA on
   the new side of a broken AVWAP is a continuation trade" hypothesis
   actually requires. Specifically: (a) directionality of the pullback,
   (b) recency / depth of the AVWAP break that preceded the pullback,
   (c) whether price actually wicked the EMA on this bar or just sits
   near it. CURRENT TAG SET cannot test the hypothesis -- the harness
   can split returns by EMA proximity but cannot identify the
   "pullback after break" pattern.

2. avwap_v4_equity is LONG-ONLY (`allowed_directions = ["LONG"]` in the
   TOML). The HVN hypothesis ("fading INTO an HVN within ~0.5x ATR of
   entry hurts BOTH LONG and SHORT entries") is bidirectional, so the
   SHORT half of the question cannot be tested on v4_equity. avwap_v4
   (options book) trades both sides and uses the same `avwap_v1.go`
   strategy code, so the diag knobs already work on it -- the
   enhancement is purely a TOML opt-in.

3. Phase 2 needs an owner + 14-day revisit to avoid the cofire shadow
   pattern. ALREADY ADDRESSED in this plan: see Track B section 5.6.

Items 1 and 2 land in this track. Item 3 is closed by Track B.

A fourth quant ask -- "drop `ema_dist_bps` as redundant with
`ema_dist_atr`" -- is INTENTIONALLY NOT INCLUDED. Verified after merge:
both keys are signed (`(close - ema) / ema * 1e4` vs
`(close - ema) / atr`); the latter is the cross-symbol comparable
quantity but the former is human-readable. Removing a tag key already
streaming into trade logs would invalidate any historical analysis that
queries by it. Keep both; document the redundancy in a comment.

### 4.2 New EMA structural tags (item 1)

Add four new keys to `appendEMADiagTags` (or split into a sibling
`appendAVWAPCrossDiagTags` if cross-tracking grows):

- `ema_low_below_ema` -- "1" if `ec.bar.Low <= s.EMAValue`, else "0".
  Boolean (encoded as "0"/"1" string per existing tag style) for "did
  this bar wick the EMA". One line.

- `ema_high_above_ema` -- mirror of above; "1" if `ec.bar.High >= s.EMAValue`.
  Together the two booleans encode pullback-touch direction without
  ambiguity (both true = bar straddles EMA; both false = bar entirely
  one side; one of each = pullback approached one side).

- `bars_since_avwap_cross_<anchor>` -- per-anchor count of 5m bars since
  the most recent close-vs-AVWAP sign change, for each active anchor in
  `cfg.Anchors` (typically `session_open`, `pd_high`, `pd_low`). New
  state on `AVWAPState`:
  ```go
  AVWAPCrossBarsSince map[string]int    `json:"avwap_cross_bars_since,omitempty"`
  AVWAPCrossLastSign  map[string]int    `json:"avwap_cross_last_sign,omitempty"` // -1, 0, +1
  ```
  Updated in OnBar AND ReplayOnBar in the same loop that already updates
  `AboveCount`/`BelowCount` (sign change detection is one extra branch
  per anchor). Emitted only when `cfg.EMADiagEnabled` AND the count is
  finite (skip first-bar / never-crossed cases). Tag key uses the anchor
  name suffix because v4_equity has 3 anchors and the harness needs to
  know which.

- `avwap_cross_breach_max_atr_<anchor>` -- per-anchor max
  `|close - avwap| / ATR` magnitude observed since the most recent cross,
  reset on each cross. New state:
  ```go
  AVWAPCrossBreachMaxATR map[string]float64 `json:"avwap_cross_breach_max_atr,omitempty"`
  ```
  Updated in the same loop. Emitted as a single tag per anchor.

State growth: 3 new map fields on `AVWAPState`. All json-serialized
under `omitempty` so snapshots are unaffected for OFF strategies.

### 4.3 Widen to avwap_v4 options (item 2)

Edit `configs/strategies/avwap_v4.toml` to add the same diag knob block
shipped in v4_equity:

```
hvn_diag_enabled = true
hvn_lookback_days = 5
hvn_bin_bps = 10.0
hvn_threshold_pct = 80.0
hvn_rth_only = true
ema_diag_enabled = true
ema_diag_period = 9
```

avwap_v4 already uses the avwap_v1 strategy engine (same Go code), so
no Go changes needed for this widening. Confirm via grep that no other
TOMLs reference `avwap_v1` -- if any other strategies are paper-active
on avwap_v1 today, they MUST stay diag-OFF to keep their snapshot bytes
unchanged (parser default is OFF, so this is automatic).

### 4.4 Migration steps

1. Add the 4 new tag-emission lines to `appendEMADiagTags`. Bar-touch
   booleans are local computations (no state needed); cross-tracking
   reads the new state fields populated in step 2.

2. Add cross-tracking state + update logic. Insert into the
   `AboveCount`/`BelowCount` loop in BOTH OnBar and ReplayOnBar (same
   pattern as PR #91). Each iteration, for each anchor:
   - Compute `sign := sign(bar.Close - avwapValue)`.
   - If `sign != AVWAPCrossLastSign[anchor]` AND `AVWAPCrossLastSign[anchor] != 0`:
     reset `AVWAPCrossBarsSince[anchor] = 0` and
     `AVWAPCrossBreachMaxATR[anchor] = 0`.
   - Else increment `AVWAPCrossBarsSince[anchor]++` and
     update `AVWAPCrossBreachMaxATR[anchor] = max(prev, |bar.Close - avwapValue| / ATR)`.
   - Set `AVWAPCrossLastSign[anchor] = sign`.

3. Update `Init` prior-restore branch to either preserve or reset these
   maps (same pattern as the existing `AboveCount`/`BelowCount` -- they
   are PRESERVED across restart because they reflect long-running
   indicator state, NOT diagnostic-only ephemeral state). Decision:
   PRESERVE on prior-restore so the cross-tracking is continuous across
   omo-core restarts. Initialize to empty maps when `prior == nil`.

4. Edit `configs/strategies/avwap_v4.toml` per section 4.3.

5. Add new parity tests in `avwap_v1_diag_parity_test.go`:
   - `TestAVWAP_AVWAPCrossBarsSince_LiveVsWarmup` -- feed bars across a
     known cross, assert per-anchor count matches across replay-only and
     replay-then-live paths.
   - `TestAVWAP_AVWAPCrossBreachMax_LiveVsWarmup` -- same fixture, assert
     max-breach values match.
   - `TestAVWAP_BarTouchTags_BoundaryCases` -- assert
     `ema_low_below_ema` / `ema_high_above_ema` correctly encode bar
     positions: entirely above EMA, entirely below, straddling, exactly
     touching one side.

6. Run the bit-identical backtest gate from PR #91:
   `/tmp/omo-replay --backtest --strategies avwap_v4_equity --symbols <34 syms>
    --from 2026-02-01 --to 2026-05-06 --timeframe 5m --slippage-bps 10
    --output-json /tmp/diag_on_v3.json`
   Required: PF=0.7337945435175424, trades=109, final_equity=$97471.06117476482.
   Cross-tracking is a new computation but must NOT affect the trade
   selection -- it only contributes new tag content.

7. Run an avwap_v4 (options) backtest as a smoke test:
   `/tmp/omo-replay --backtest --strategies avwap_v4 --symbols <v4 syms>
    --from 2026-02-01 --to 2026-05-06 --timeframe 5m --slippage-bps 10
    --output-json /tmp/avwap_v4_diag_on.json`
   Compare PF / trade count / final equity against the SAME run with
   the diag knobs reverted to false in the v4 TOML. Required: bit-equal.
   This is the OFF-vs-ON parity check for the newly-opted-in spec.

8. Inspect 10 random LONG entries and 10 random SHORT entries from the
   v4 ON run. Verify:
   - All 4 new EMA-structural keys present.
   - `bars_since_avwap_cross_<anchor>` is a small integer (typically 1-50)
     for the active anchor; absent or 0 for never-crossed anchors.
   - `avwap_cross_breach_max_atr_<anchor>` is a non-negative float in a
     plausible range (0 to ~5 ATR).
   - Bar-touch booleans correctly reflect bar high/low vs ema_value.
   - At least one SHORT entry has tags populated (the SHORT-coverage
     deliverable for item 2).

### 4.5 Decision rule (Track C)

- All existing tests pass AND new parity tests pass AND bit-identical
  v4_equity backtest holds AND OFF-vs-ON parity holds for v4 options
  AND tag presence/plausibility verified on both LONG and SHORT entries
  -> ship Track C.
- Any divergence in v4_equity bit-identity -> stop, root-cause. Most
  likely cause: cross-tracking mutates state in a way that affects
  AboveCount/BelowCount via aliased map.
- Any divergence in v4-options OFF-vs-ON parity -> stop. Same diagnosis.
- Tag-presence missing on any entry -> wiring bug, fix before ship.

### 4.6 Files touched (Track C)

- `backend/internal/app/strategy/builtin/avwap_v1.go`. ~60 LOC added
  (state fields + cross-tracking loop hook + 4 new tag emissions).
- `backend/internal/app/strategy/builtin/avwap_v1_diag_parity_test.go`.
  ~80 LOC added (3 new tests).
- `configs/strategies/avwap_v4.toml`. 7 lines added (diag opt-in block).
- `configs/strategies/avwap_v4_equity.toml`. NO CHANGE (already opted in
  via PR #91; the new tags emit automatically because the strategy code
  is shared).

### 4.7 Blast radius (Track C)

- avwap_v4_equity (PaperActive, priority 80, live PaperActive run): new
  tags emit; bit-identical engine behavior gated by section 4.4 step 6.
- avwap_v4 options: not currently PaperActive in tree (verify via the
  `lifecycle.state` field; if it IS PaperActive, the diag opt-in starts
  emitting on the next live entry). If avwap_v4 is in `Inactive` state,
  the TOML change is a backtest-only enablement.
- Cross-tracking state ADDS to AboveCount/BelowCount work per-bar by
  one extra branch per anchor (3 anchors x ~78 5m bars/day x 34 symbols
  x 2 strategies if avwap_v4 is also active = ~16k extra branches/day).
  Negligible.
- Snapshot byte growth: +3 small map fields on AVWAPState, encoded as
  `omitempty`. Empty for diag-OFF strategies (zero bytes). For diag-ON,
  3-anchor v4_equity: ~120 bytes per snapshot per symbol. Across 34
  symbols x 2 snapshots/min: ~16 MB/day persisted. Watch for storage
  growth in the snapshot retention monitor.
- Other strategies using avwap_v1.go: must verify no third strategy
  uses the engine. If exactly two (v4 + v4_equity), no broader blast.

### 4.8 Risks (Track C)

- Cross-tracking state ALIASES the per-anchor maps on Init prior-restore
  (Go maps are reference types). If the Init code paths are not careful,
  a `st.AVWAPCrossBarsSince = avwapPrior.AVWAPCrossBarsSince` assignment
  would alias and make a future Reset on `st` mutate the prior state's
  map. Mitigation: use explicit per-key copies, OR use the existing
  `AboveCount`-style direct assign (which is already in the code with the
  same aliasing risk; if it has not bitten in production, the new fields
  follow the same convention).
- avwap_v4 options uses the same engine but with `enabled = true` in
  `[options]` and a different exit-rule set. The diag tags are
  computed PRE-confluence, so options-vs-equity exit-rule differences
  do not affect tag emission. Confirmed via code inspection: the
  HVN/EMA wiring is in `OnBar` before any options-vs-equity branching.
- Adding cross-tracking to ReplayOnBar might subtly change the warm-up
  state for v4_equity if the warmup feed sees more cross transitions
  than the live feed would (e.g. extended-hours bars in warmup but not
  live). Same risk noted in PR #91 review for HVN/EMA. Mitigation:
  cross-tracking on `bar.Close vs avwap_value` does not depend on RTH
  filtering; the AboveCount/BelowCount loop already runs on every
  warmup bar with the same logic. Existing parity is the protection.
- Bar-touch booleans on a halted bar (`bar.High == bar.Low`) collapse
  to "either both true or both false" depending on EMA position.
  Acceptable; that is the correct encoding for a halted bar.

## 5. Track B: omo-signal-corr-hvn-ema fork

### 5.1 Target shape

New binary at `backend/cmd/omo-signal-corr-hvn-ema/main.go`. Fork of
the existing `backend/cmd/omo-signal-corr/main.go` (1303 LOC) but
LIGHTER: the existing tool builds synthetic "would-have-fired" signals
from raw bars, which is heavy. The fork consumes EXISTING entries from
the trade-log JSON (which already carry the tags AND the realized PnL),
so it skips the bar-load + signal-rebuild path entirely.

### 5.2 Input/output contract

Input flags:
- `--trade-log` (one or more, comma-separated): paths to backtest result
  JSONs (e.g. `_workspace/avwap_v4_equity_tune/v10_maxloss_0012.json`)
  OR a directory of them. Each file is a `backtest.Result`-shaped JSON
  with a `trades` array.
- `--strategy` (default: `avwap_v4_equity`): only entries with
  `tags.setup` matching the strategy id and `direction == "LONG"|"SHORT"`
  (entry rows, not exits) are included.
- `--out` (default: `_tmp/signal_corr_hvn_ema/<timestamp>/`): output dir
  for per-bucket CSVs.
- `--time-holdout-cutoff` (default: 30 days before max trade date):
  trades on/after this date go into the OOS bucket; before -> IS bucket.
- `--symbol-holdout-fraction` (default: 0.30): 30% of symbols (by entry
  count, smallest-first to avoid clipping the most-active symbols) are
  reserved as a held-out symbol set.

Output:
- `summary.txt`: one-page Markdown table.
  - Per HVN bucket (by `hvn_dist_atr` quantile, e.g. <0.5, 0.5-1.0,
    1.0-2.0, 2.0-5.0, >5.0) AND per `hvn_density_above`/`below` ratio
    bucket, report: n, win_rate, profit_factor, avg_pnl, total_pnl.
  - Per EMA bucket (by `ema_dist_atr` signed quantile), report: same.
  - Each row reported THREE TIMES: full sample, time-OOS only,
    symbol-OOS only.
- `pf_lift.txt`: the decision-rule view.
  - For each HVN bucket vs the overall sample, compute PF lift
    (bucket_pf - overall_pf). Same for each EMA bucket. Mark each row
    with PASS/FAIL on the (>= 0.10 abs lift, both holdouts agree on
    sign) rule.
- `per_trade.csv`: every entry with all input tags + the bucket
  assignments + realized PnL. Auditable; lets the next person re-bucket
  without re-running.

### 5.3 What it does NOT do

- Does not load bars from TimescaleDB. The trade log already carries
  realized PnL (the consequence of all the engine's exit logic), which
  is the cleanest available "forward return" for an entry.
- Does not synthesize alternative signals. We are grading the entries
  the strategy actually fired, not "what if we had also fired here".
- Does not write to the database. Read-only on file inputs.
- Does not produce an active-promotion plan. The output of this tool
  IS the input to the human (or the next planning skill) who decides
  whether to draft one.

### 5.4 Migration / build steps

1. Copy `backend/cmd/omo-signal-corr/main.go` to
   `backend/cmd/omo-signal-corr-hvn-ema/main.go`. Strip the bar-load,
   the signal-rebuild, the MACD/stretch/inducement-edge functions
   (~700 LOC removed; the fork is much smaller than the original).

2. Write the trade-log loader: parse the JSON, filter entries by
   strategy id and direction, project tags into typed buckets.

3. Write the bucket logic. Quantile cuts come from the data, not
   hard-coded thresholds (so the harness adapts to whatever the live
   distribution looks like).

4. Write the holdout split: sort trades by `filled_at` for time-OOS,
   sort symbols by entry-count ascending and take the first N% for
   symbol-OOS.

5. Write the PF-lift table + decision-rule formatter.

6. Smoke test against the existing
   `_workspace/avwap_v4_equity_tune/v10_maxloss_0012.json` (n=551).
   Note: this trade log was generated BEFORE the HVN/EMA tags shipped,
   so it will have ZERO entries with the new tags and the harness will
   correctly report "no qualifying entries". That is the test for the
   no-data path.

7. Re-run the avwap_v4_equity backtest over the longest available
   window with diag ON; pipe the output JSON to the harness. This is
   the first real grading run. Expected n: 500-1500 entries (year-long
   full backtest, ~109 entries on 3-month).

### 5.5 Decision rule (re-stated, locked)

For HVN as a near-entry veto candidate:
- Compute PF for entries WHERE `hvn_dist_atr <= 0.5` (the original plan's
  hypothesis range) vs entries WHERE `hvn_dist_atr > 0.5`.
- Compute the same split on the time-OOS bucket and on the symbol-OOS
  bucket independently.
- PASS if BOTH holdouts show PF lift (full PF - near-HVN PF) >= 0.10
  absolute AND both holdouts agree on sign.
- FAIL if either holdout fails the threshold OR the holdouts disagree
  on sign (the documented cofire reversal-factor failure mode).

For EMA as a tighter-confluence-gate candidate:
- Same shape, bucket on `ema_dist_atr` (signed) at the within-1-ATR vs
  outside-1-ATR threshold.

PASS on either factor -> draft a separate active-promotion plan
specifying the exact wiring (HVN veto wrapper a la cofire, OR EMA gate
a la dp_z_conditioning). FAIL on both factors -> shelve. The tags stay
in the trade log because removing them would invalidate any future
re-grading; they cost almost nothing and a future analyst may find
something this harness missed.

### 5.6 Owner + 14-day revisit

- Owner: quant-analyst sub-agent (the same agent that ran the cofire
  validation harness).
- Revisit date: 2026-05-21 (14 days from PR #91 merge on 2026-05-07).
- On the revisit date, EITHER:
    (a) The harness has been run and produced a verdict -> document the
    verdict and either draft the promotion plan (PASS) or shelve the
    factor (FAIL).
    (b) The harness has NOT been run -> escalate. The HVN/EMA tags are
    still streaming into PaperActive trade logs and consuming engine
    state (negligibly, but non-zero); they are dead-letter telemetry
    until graded.

This 14-day owner+revisit pair is the bit that prevents the cofire
pattern (shipped 2026-04-20, still in shadow 17 days later as of
2026-05-07).

### 5.7 Files touched (Track B)

- `backend/cmd/omo-signal-corr-hvn-ema/main.go` (new). ~600 LOC
  (forked-and-stripped from the 1303-line original).
- `backend/cmd/omo-signal-corr-hvn-ema/README.md` (new). One screen of
  usage + decision-rule recap. Pin the 2026-05-21 revisit date.
- No source-of-engine code changes.
- No TOML changes.
- No test changes; the harness is itself a one-shot tool, validated by
  smoke-running on the no-tag and tagged JSON inputs above.

### 5.8 Blast radius (Track B)

- Read-only on file inputs. Cannot move capital. Cannot modify trade
  logs. Cannot mutate the database.
- The new binary is 100% additive; existing `omo-signal-corr` is
  untouched and continues to serve inducement / cofire / MACD research.

### 5.9 Risks (Track B)

- Tagged trade-log accumulation is slow (~5 entries/day live across 34
  symbols on a long-only book). The harness will be data-starved until
  either a backtest-replay generates a fat batch OR several weeks of
  live data accumulate. Mitigation: smoke-test on the v10 trade log
  (no-tag path) AND on a fresh tagged backtest replay BEFORE waiting
  for live data.
- Bucketing-by-quantile means the bucket boundaries shift as more data
  arrives. For reproducibility, persist the boundaries used for each
  decision-rule evaluation (write them into `summary.txt`).
- Symbol-holdout that selects the smallest-N symbols may pick up
  symbols where the strategy has never fired (e.g. SOXL with 2 entries
  total). Mitigation: cap the symbol-holdout at 30% of symbols ONLY IF
  each held-out symbol has at least 5 entries; otherwise bump to the
  next smallest until the constraint is met.
- The v10 trade log was generated with a different config than current
  (e.g. `max_loss=0.012` came from sweep v10; current config matches).
  Re-grading old trade logs against current behavior is misleading.
  Mitigation: pin the harness to the post-PR-91 config window
  (everything on/after 2026-05-07) when running the decision-rule eval.
  Old trade logs are useful for the no-data smoke path only.

## 6. Independence + ordering

The three tracks are mostly independent. Soft ordering hints below; no
hard dependencies.

- Track A (refactor) and Track C (enhancements) BOTH touch
  `avwap_v1.go`. If they ship in parallel, the second-to-merge will
  rebase against the first. Easier to land in series: A first (it
  shrinks the file), then C (which adds new tag-emission lines and new
  state). Reverse order is fine; just one extra rebase.
- Track B (harness) is a fresh binary, no collision with A or C.
- Track B benefits from Track C: richer EMA structural tags give the
  harness a clean way to test the "pullback after AVWAP break"
  hypothesis. WITHOUT Track C, the harness can still grade the original
  7-key set, but the EMA verdict will be weak by construction.
- Track C's "widen to avwap_v4 options" adds SHORT entries to the tag
  stream. Track B's HVN bidirectional verdict is statistically valid
  only on data that includes SHORTs. So if you want a defensible HVN
  conclusion, Track C precedes Track B's grading run.

Recommended order if pursuing all three: A -> C -> Track B's first
grading run on the post-Track-C data. Track B's owner+revisit clock
(2026-05-21) starts on PR #91 merge regardless; if Track C is not in
by 2026-05-21, run Track B's first pass on whatever data exists and
note the EMA-verdict-weak / SHORT-coverage-missing caveats in the
report.

## 7. Out of scope

- Promoting HVN to a veto on avwap_v4_equity (or any other strategy).
  That is a follow-on plan triggered by Track B's PASS verdict.
- Promoting EMA to an entry-type or scoring factor. Same.
- Widening the HVN/EMA tag emission to strategies BEYOND avwap_v4_equity
  and avwap_v4 (options). macd_only_v1 has different entry physics and
  would dilute the attribution signal; out of scope until the avwap-side
  verdict is in.
- Removing or renaming `ema_dist_bps`. Quant flagged it as redundant
  with `ema_dist_atr`; we keep it to preserve backward compatibility
  with the trade-log stream that has already been emitting since PR #91.
- Changing the existing `omo-signal-corr` binary. The fork pattern
  preserves the existing tool's single-purpose contract.
- Migrating other duplicated math (e.g. `armPendingEntry`/`rollbackPendingEntry`
  in pending_entry.go is already shared; nothing else jumps out as
  parallel duplication).
- Touching the inducement detector or the cofire veto code paths. They
  have their own validation pipelines.
- Reworking warmup or snapshot infrastructure. All new state in Track C
  flows through the existing warmup + snapshot seams.

## 8. Acknowledgment requested

Three tracks, mostly independent:

- Track A: mechanical refactor (~120 LOC new + ~100 LOC deleted, parity
  tests + bit-identical backtest as the gate).
- Track C: diagnostic surface enhancements (~60 LOC engine + 80 LOC test
  + 7 lines TOML, OFF-vs-ON parity gate on two strategies).
- Track B: research-harness fork (~600 LOC new in a new binary, no engine
  changes, owner+revisit pinned to 2026-05-21).

Request explicit go-ahead to:

- proceed with all three tracks (suggested order: A -> C -> Track B's
  first grading run on the post-Track-C tag set), OR
- proceed with a subset (e.g. ship Track B immediately on the PR #91
  tag set and defer C, accepting the weaker EMA verdict and missing
  SHORT coverage), OR
- adjust scope first (e.g. extract `hvnTracker` to internal/domain
  instead of the builtin package; merge the harness fork into the
  existing omo-signal-corr binary as a subcommand; drop one or more of
  Track C's structural EMA tags if quant decides a subset is enough).
