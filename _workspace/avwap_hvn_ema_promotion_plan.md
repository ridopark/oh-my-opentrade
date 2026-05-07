# Plan: HVN + EMA factor active-promotion (avwap_v4_equity ONLY)

Date: 2026-05-07
Status: Plan, awaiting acknowledgment.

Promotion target: **avwap_v4_equity** (PaperActive, LONG-only equity,
priority 80, $100k initial equity). avwap_v4 (options) is OUT OF
SCOPE for engine wiring; its diag opt-in shipped in PR #92 continues
emitting tags but is not load-bearing for this plan's grading,
shadow rollout, or active promotion.

Driver: PR #92 (HVN+EMA diag follow-ups) merged to main 2026-05-07 as
commit bbcace49. The harness (`omo-signal-corr-hvn-ema`) is live, the
4 EMA-structural tags are emitting on every avwap_v4_equity entry,
but the verdict on both factors is currently `INSUFFICIENT_DATA` --
the equity sample at n=82 over 3 months is too thin to clear a
defensible decision rule, with zero entries in the near-HVN bucket
on either holdout.

This plan covers four phases:

  Phase 0 (data path, mandatory): unblock the harness verdict.
  Phase 1 (wiring shells, parallel): land the engine wiring for both
  factors in shadow-only mode (no engine effect; tag-emission and
  factor-decision-log only). Bit-identity gate is the contract.
  Phase 2 (shadow rollout, EMA first): 10-trading-day live shadow
  window with hard halt criteria.
  Phase 3 (active promotion, EMA first): risk-reduced canary universe
  flip; auto-revert halt criteria; widen on clean run.
  Phase 4 (same shape for HVN): unblocked when EMA reaches active-
  stable OR shelve verdict.

Tracks A and B of the just-merged plan delivered the code; this plan
is the operational rollout that turns those tags into capital
decisions while NOT repeating the cofire shadow pattern (shipped
2026-04-20, still in shadow at this plan's draft date 17 days later
because no owner closed the loop).

## 1. Context

**What just shipped (PR #92, merged bbcace49):**
- 7 HVN/EMA tags from PR #91 (`hvn_dist_atr`, `hvn_density_above|below`,
  `poc_dist_bps`, `ema_value`, `ema_dist_bps`, `ema_dist_atr`).
- 4 new EMA-structural tags (`ema_low_below_ema`, `ema_high_above_ema`,
  `bars_since_avwap_cross_<anchor>`, `avwap_cross_breach_max_atr_<anchor>`).
- Cross-tracking state on `AVWAPState` (`AVWAPCrossBarsSince`,
  `AVWAPCrossLastSign`, `AVWAPCrossBreachMaxATR`) preserved across
  prior-restore.
- Diag opt-in widened from avwap_v4_equity to avwap_v4 (options) so
  SHORT-bias entries (puts on a bidirectional book) are in the
  grading sample.
- `omo-signal-corr-hvn-ema` harness binary at
  `backend/cmd/omo-signal-corr-hvn-ema/main.go`.

**Current harness verdict on v4_equity (`/tmp/scc_v4eq/pf_lift.txt`,
3-month 2026-02-01..2026-05-06 backtest, n=82):**

- HVN: time-OOS n_near=0 (all entries fired with hvn_dist_atr > 0.5);
  symbol-OOS n_near=3 (degenerate inf PF). Verdict
  `INSUFFICIENT_DATA` on both holdouts.
- EMA: same shape, n_near=0 on both holdouts.

The v4 options 3-month run produced 846 entries, but those are out
of scope for this plan -- promotion target is equity-only.

**Why we cannot wait for live PaperActive accumulation alone:** ~5
entries/day on avwap_v4_equity = ~50 entries in 10 trading days,
~150 in a month. To clear an n_near >= 50 floor (proposed below) on
BOTH the time-OOS AND symbol-OOS slices, we need a fat batch from a
longer backtest replay; live drip is too slow. A year-long replay
is the path that gets to enough n.

## 2. Constraints (locked)

These are non-negotiable across all four phases:

- **No promotion without a harness PASS** at n_near >= 50 per holdout,
  on BOTH holdouts (time-OOS AND symbol-OOS), signs agree, |lift| >=
  0.10 absolute. Single-holdout verdicts never advance a factor.
- **TOML-knob rollback only.** No code-level feature flags requiring
  rebuild. Knob changes hot-reload (or take effect on the next
  omo-core restart at most). Code reverts are the right tool ONLY
  when the bug is a panic/NaN that the knob cannot disable.
- **Shadow window: hard 10 trading days OR 50 fresh tagged entries**
  (whichever comes first), with a one-time 5-day extension if the
  shadow window closes below the 50-entry floor. No second extension.
  Cofire shipped 2026-04-20 and was still in shadow at this plan's
  draft 17 days later because no owner closed the loop. This window
  closes the loop.
- **Promotion target is avwap_v4_equity ONLY.** avwap_v4 (options)
  stays diag-only for the lifetime of this plan; no factor TOML
  knobs are added to its spec, no engine wiring is enabled on it,
  no shadow or active rollout phases touch it. The PR #92 diag opt-
  in on v4 options continues emitting tags as inert telemetry but
  is not load-bearing for any phase here.
- **Single-factor-at-a-time promotion.** Never co-promote HVN and
  EMA on v4_equity simultaneously. EMA promotes first (data is
  closer to the n_near floor); HVN follows after EMA stabilizes
  active or shelves.
- **avwap_v4 options OFF-vs-ON parity is a continuous regression
  gate.** Even though we are not promoting on options, the avwap_v1.go
  engine is shared between v4_equity and v4 options; any wiring
  added in Phase 1 must keep v4 options bit-equal across the diag-
  only baseline (846 trades, $223,883.42, PF=2.0152 from PR #92
  validation). A leak there means a TOML-knob default is wrong and
  is a halt-and-revert event.
- **avwap_v4_equity bit-identity gate against PR #91 reference**
  (PF=0.7337945435175424, trades=109, final_equity=$97471.06117476482)
  must hold WITH the new wiring in shadow mode. Active-mode flip
  needs a SEPARATE backtest gate -- the active-mode delta must match
  the harness's predicted PF lift within tolerance.
- **Owner is mandatory and named in the plan.** Default owner: the
  user (ridopark) at this plan's draft date; reassignable to
  quant-analyst sub-agent on explicit transfer. The 14-day revisit
  cadence applies to every phase that puts capital at stake.

## 3. Phase 0: harness/data improvements (mandatory pre-wiring)

The harness verdict is `INSUFFICIENT_DATA` on every path today.
Phase 0 unblocks the verdict before any engine wiring is designed,
so we know whether either factor PASSes the locked decision rule
before we write code that assumes one will.

### 3.1 Phase 0 deliverables

**0a. Year-long backtest replay on avwap_v4_equity.**
Window: 2025-05-07..2026-05-07 (one year up to and including the day
PR #92 merged). `--strategies avwap_v4_equity --timeframe 5m
--slippage-bps 10` against the standard 34-sym universe. Estimate:
~436 entries (3-month sample of 109 extrapolated; conservative).
Output to `_workspace/v4eq_year_<run_date>.json`. Pre-run a 3-month
smoke window first (`--from 2025-05-07 --to 2025-08-07`) to confirm
historical-bar availability before committing to the full year.

**0b. Tighten harness decision rule with min-N gate (~10 LOC).**
File: `backend/cmd/omo-signal-corr-hvn-ema/main.go` `decide()`. Add:

```
const minNNear = 50
if timeOOS.nNear < minNNear || symOOS.nNear < minNNear {
    return fmt.Sprintf("INSUFFICIENT_DATA (n_near < %d on at least one holdout)", minNNear)
}
```

Rationale (`feedback_factor_validation.md`): cofire's overfit was
caught at n=144 held-out and missed at n=60 OOS; the floor at 50 is
the conservative side of that range and prevents the n=0/n=3 noise
we see today on v4_equity 3-month from triggering false-positive
promotion drafts. The threshold is a flag-tunable constant, not a
hard-coded one; future runs can investigate edge cases at lower
n with explicit override.

**0c. Run harness on year-long v4_equity output, capture verdicts.**
Commit `pf_lift.txt`, `summary.txt`, and `per_trade.csv` to
`_workspace/v4eq_year_<run_date>/` for audit. Repeat the smoke run
against `_workspace/avwap_v4_equity_tune/v10_maxloss_0012.json`
(pre-PR-91 trade log, n=551) to confirm the n_near floor still
correctly returns INSUFFICIENT_DATA on the no-tag path.

### 3.2 Phase 0 success criteria

- pf_lift.txt verdicts on v4_equity for both HVN and EMA factors,
  reported under the new n_near>=50 rule. Possible verdicts: `PASS`,
  `FAIL`, `INSUFFICIENT_DATA`.
- Smoke run on v10 trade log still reports INSUFFICIENT_DATA (the
  no-tag path stays graceful under the new floor).
- Phase 0 SHIPS regardless of verdict outcome -- the deliverable is
  the verdict file, not the verdict value. PASS/FAIL/INSUFFICIENT_DATA
  all gate Phase 1 differently (see 3.3 below).

### 3.3 Phase 0 -> Phase 1 gate

- Either factor PASS on v4_equity year-long -> proceed to Phase 1
  wiring shells, prioritize the PASS factor for Phase 2 sequencing.
- Both factors FAIL on v4_equity year-long -> shelve. Tags stay
  emitting (already shipped); no wiring goes in. Document the
  year-long verdict and revisit on 2026-09-07 (4-month re-test
  horizon, by which time live PaperActive will have ~600 fresh
  tagged entries to re-grade).
- Both factors INSUFFICIENT_DATA after the year-long replay (would
  mean v4_equity fires <50 near-bucket entries in a year) -> two
  options: (a) reduce the bucket threshold from 0.5 ATR (HVN) / 1.0
  ATR (EMA) toward more-inclusive bounds and re-run, or (b) accept
  the data doesn't support the original hypothesis at LONG-only
  equity scale. Decision logged in `_workspace/`.
- Mixed: one factor PASS, one INSUFFICIENT_DATA -> proceed to Phase 1
  for the PASS factor only; defer the INSUFFICIENT_DATA factor's
  wiring until live data accumulates or the bucket is re-tuned.

### 3.4 Phase 0 LOC budget

- `backend/cmd/omo-signal-corr-hvn-ema/main.go`: ~10 LOC added (the
  decide() floor; no regex/extraction since the equity-only path
  uses native ticker symbols throughout).
- One year-long backtest run + two harness invocations (one for
  v4_equity year-long, one for the v10 no-tag smoke regression).
  Cost is replay time, not LOC.
- `_workspace/v4eq_year_<run_date>/`: 3 files (`pf_lift.txt`,
  `summary.txt`, `per_trade.csv`).

### 3.5 Phase 0 risks

- **Year-long replay may have data gaps** (TimescaleDB historical
  bars; user has previously noted dark-pool backfill and option-chain
  backfill gaps; equity bars themselves should be complete but worth
  pre-checking). Mitigation: pre-run a 3-month smoke
  (`--from 2025-05-07 --to 2025-08-07 --backtest --strategies
  avwap_v4_equity`) to confirm data availability before kicking the
  full year.
- **The min-N=50 floor may be wrong for the v4_equity population.**
  If the year-long run produces 80 near-HVN entries on one holdout
  but 30 on the other, INSUFFICIENT_DATA fires. Manual review may
  decide the asymmetry is informative. Mitigation: the floor is a
  decision-rule arg (made flag-tunable in 0b), not a code constant;
  future runs can pass a lower value to investigate edge cases
  without code change.
- **v4_equity is LONG-only**, so the original bidirectional HVN
  hypothesis ("fading INTO an HVN HURTS BOTH sides") collapses to
  "fading INTO an HVN HURTS LONG entries near resistance." This is
  still testable on the equity sample but a directionally narrower
  question. The plan accepts this scope.

## 4. Phase 1: wiring shells (parallel; both factors, shadow-only)

Phase 1 lands the engine wiring for both HVN and EMA factors as
shadow-only code paths. The factors compute, log decisions, and tag-
emit; they do NOT modify entries, exits, or signal strength while
shadow=true. Active-mode flips happen in Phase 3.

Phase 1 ships regardless of which factor (if any) PASSed Phase 0.
Wiring shells with default-OFF knobs are zero-cost insurance: when
PASS arrives, promotion is a TOML edit, not a multi-day code task.

### 4.1 HVN factor wiring (cofire-cloned)

**Shape (the engineer agent's recommendation):** mirror `applyCofireVeto`
at `avwap_v1.go:2801`. Add `applyHVNVeto(ec, sig, err)` and chain it:

```
return s.applyHVNVeto(ec, s.applyCofireVeto(ec, sig, err))
```

**Reader extraction:** lift the "near-HVN at entry" boolean out of
`appendHVNDiagTags` (`avwap_v1.go:629-677`) into a small helper:

```
func (s *AVWAPState) hvnNearEntry(bar start.Bar, atr float64) (near bool, distATR float64, ok bool)
```

The diag emitter calls the reader and writes tags from the same fact
source the factor reads; this preserves the cofire-shadow invariant
(tag output identical whether the factor is shadow or active).

**Applier shape (mirrors cofire `applyCofireVeto` at lines 920-963):**

```
func (s *AVWAPState) applyHVNVeto(ec entryContext, sig *start.Signal, err error) (*start.Signal, error) {
    if err != nil || sig == nil {
        return sig, err
    }
    cfg := ec.cfg
    if !(cfg.HVNFactorEnabled || cfg.HVNFactorShadow) {
        return sig, err
    }
    // side-gate: per cfg.HVNFactorLongOnly (default true on v4_equity)
    if cfg.HVNFactorLongOnly && sig.Side != start.SideBuy {
        return sig, err
    }
    near, distATR, ok := s.hvnNearEntry(ec.bar, s.Indicators.ATR)
    if !ok {
        return sig, err
    }
    if !near {
        return sig, err
    }
    // shadow: log + tag, do not block
    if cfg.HVNFactorShadow && !cfg.HVNFactorEnabled {
        sig.Tags["hvn_factor_would_block"] = "1"
        sig.Tags["hvn_factor_dist_atr"] = fmt.Sprintf("%.3f", distATR)
        return sig, err
    }
    // enabled: block + tag the suppressed entry
    s.recordHoldReason(ec, "hvn_factor_veto")
    return nil, nil
}
```

**LOC budget:** ~50 LOC (40 applier + 5 chain wire-up + 5 reader
extraction).

### 4.2 EMA factor wiring (dp_z-cloned)

**Shape (engineer agent + quant agent converged):** hold-bars
conditioner at `evaluateBasicExit` between `avwap_v1.go:1694-1704`,
additive to the existing dp_z adjustment.

The EMA structural state (recent AVWAP cross + deep breach + price
near EMA on the new side) is a CONTINUATION signal; matches dp_z's
"favorable tailwind = be more patient" shape. Reads
`s.AVWAPCrossBarsSince[anchor]` and `s.AVWAPCrossBreachMaxATR[anchor]`
for the position's primary anchor.

**Wiring:**

```
// EMA factor conditioning: extend hold_bars when structural setup
// favors continuation (recent cross + deep breach).
if cfg.EMAFactorEnabled || cfg.EMAFactorShadow {
    anchor := cfg.Anchors[0]
    barsSince, hasSince := s.AVWAPCrossBarsSince[anchor]
    breachMax, _ := s.AVWAPCrossBreachMaxATR[anchor]
    favorable := hasSince && barsSince <= cfg.EMAFactorMaxBarsSinceCross &&
        breachMax >= cfg.EMAFactorMinBreachATR
    if favorable {
        if cfg.EMAFactorShadow && !cfg.EMAFactorEnabled {
            // shadow: log fact, do not modulate
            s.shadowEMAFactorFavorable = true
        } else if cfg.EMAFactorEnabled {
            effectiveHoldBars = int(float64(effectiveHoldBars) * cfg.EMAFactorHoldBarsMult)
            if effectiveHoldBars < 1 {
                effectiveHoldBars = 1
            }
        }
    }
}
```

A `shadowEMAFactorFavorable bool` field on AVWAPState (json:"-")
captures the shadow decision so the next entry signal's tags can
emit `ema_factor_would_extend = "1|0"`. Cleared after each entry-
tag emission.

**LOC budget:** ~30 LOC (25 conditioner + 5 shadow-state field +
clear).

### 4.3 TOML knob layout (each factor independent)

Mirror cofire's pattern from `avwap_v4_equity.toml:206-213`. Each
factor gets its own knob set. Mixing them invites the cofire reversal
bug at the per-factor level.

```
# --- HVN near-entry factor (default OFF; shadow first; promote only
# after harness PASS at n_near >= 50 per holdout, both signs agree) ---
hvn_factor_enabled = false
hvn_factor_shadow = false
hvn_factor_long_only = true
hvn_factor_near_atr_max = 0.5

# --- EMA continuation factor (hold-bars conditioning; same gating) ---
ema_factor_enabled = false
ema_factor_shadow = false
ema_factor_max_bars_since_cross = 12
ema_factor_min_breach_atr = 1.0
ema_factor_hold_bars_mult = 1.5
```

All defaults OFF/zero so existing v4_equity bit-identity is automatic.

### 4.4 Phase 1 tests (mandatory)

In `backend/internal/app/strategy/builtin/avwap_v1_factor_test.go`
(new file). All three are bit-identity-style gates plus behavioral
assertions:

1. **TestAVWAP_HVNFactor_Off_BitIdentity**: factor knobs default OFF.
   Run avwap_v4_equity backtest 2026-02-01..2026-05-06 against the
   PR #91 reference TOML. Required: PF=0.7337945435175424, trades=109,
   final_equity=$97471.06117476482 EXACTLY.
2. **TestAVWAP_HVNFactor_Shadow_TagParity**: same backtest with
   `hvn_factor_shadow=true, hvn_factor_enabled=false`. Required:
   same PF/trades/equity (shadow does not block); plus emitted tags
   include `hvn_factor_would_block` on every near-HVN entry.
3. **TestAVWAP_HVNFactor_Active_BehaviorChange**: synthetic fixture
   with two entries -- one near-HVN (would block), one far-HVN (would
   pass). With `hvn_factor_enabled=true`, near-HVN entry is blocked;
   far-HVN passes. Without enabled, both pass.
4. **TestAVWAP_EMAFactor_Off_BitIdentity**: same as #1 for EMA knobs.
5. **TestAVWAP_EMAFactor_Shadow_TagParity**: same as #2 for EMA;
   tag is `ema_factor_would_extend`.
6. **TestAVWAP_EMAFactor_Active_HoldBarsExtended**: fixture with a
   position open and EMA structural conditions favorable (recent
   cross + deep breach). With `ema_factor_enabled=true`,
   `effective_hold_bars` = original * 1.5 (rounded). Without enabled,
   value unchanged.
7. **TestAVWAP_HVNAndCofire_ChainOrder**: cofire blocks first; HVN
   never runs. Verify `applyHVNVeto` is not called when
   `applyCofireVeto` returned `(nil, nil)`. Pin the chain order
   (engineer agent flagged this).
8. **TestAVWAP_HVNFactor_PriorRestoreParity**: snapshot-restore parity
   on the new factor decision. With shadow=true on a fresh state,
   feed N bars then snapshot, restore, feed M more bars; assert the
   tag sequence on the M+N+1 entry signal matches a no-restart run.
   Same for EMA factor.
9. **TestAVWAP_HVNFactor_LongOnlyGate**: with `hvn_factor_long_only=true`
   (default), HVN factor does NOT fire on a synthetic SHORT entry.
   With `long_only=false`, it fires on both LONG and SHORT. Tests
   the side-gate code path even though only LONG-side firing is
   used by v4_equity in production.

### 4.5 Phase 1 success criteria

- All 9 unit tests above pass at `-count=10`.
- avwap_v4_equity bit-identical against PR #91 reference: PF, trades,
  final_equity match exactly.
- avwap_v4 options OFF-vs-ON parity holds (846 trades, $223,883.42,
  PF=2.0152) across diag-only and diag+factor-shadow modes.
- Both factors land in shadow mode on avwap_v4_equity TOML
  (`hvn_factor_shadow=true`, `ema_factor_shadow=true`,
  `*_enabled=false`) and the backtest is unchanged.
- Phase 0 verdicts in `_workspace/` (regardless of value).

### 4.6 Phase 1 LOC budget

- `backend/internal/app/strategy/builtin/avwap_v1.go`: ~80 LOC (50
  HVN + 30 EMA).
- `backend/internal/app/strategy/builtin/avwap_v1_factor_test.go`
  (new): ~250 LOC (9 tests).
- `configs/strategies/avwap_v4_equity.toml`: 9 lines added (knobs).
- `configs/strategies/avwap_v4.toml`: NO CHANGE in Phase 1; v4
  options stays in the diag-only config.

### 4.7 Phase 1 blast radius

- avwap_v4_equity (PaperActive, priority 80): factor wiring lands
  but stays in shadow mode (`*_enabled=false`); no entry/exit
  behavior change. Live PaperActive PnL is unaffected.
- avwap_v4 options (PaperActive): no TOML changes; no engine effect.
- whale_pullback_v1 (Deactivated): unaffected; HVN tracker still
  serves whale's existing vp_required veto. No factor wiring on
  whale (out of scope).
- No JSON-snapshot schema change. The factor-shadow state field
  (`shadowEMAFactorFavorable`) is `json:"-"`.
- No new dependency on `internal/domain` or `internal/adapters`.

### 4.8 Phase 1 risks

- **State aliasing on prior-restore.** The 3 cross-tracking maps
  added in Track C are direct-assigned at avwap_v1.go:1689-1691.
  Maps are reference types; the prior is discarded immediately
  post-Init under the established AboveCount pattern. The new EMA
  factor reads these maps but does NOT write to them (only the per-
  bar update loop in OnBar/ReplayOnBar writes). So no new aliasing
  class. Mitigation: add a comment at lines 1689-1691 documenting
  the discard-after-Init contract while we are touching Init.
- **Cofire chain order dependency.** If a future change to
  `evaluateEntries` reorders the chain, HVN factor fires before
  cofire. Mitigation: TestAVWAP_HVNAndCofire_ChainOrder pins the
  order; any reorder requires updating the test expectation.
- **Tag emission mismatch between shadow and active.** If the
  reader is called only in active mode, shadow-mode tag-emission
  paths drift from active. Mitigation: the reader is always called
  whenever `enabled OR shadow` is true; tag emission is unchanged
  by the active/shadow flip. Test #2 and #5 verify this.

## 5. Phase 2: shadow rollout (per factor; EMA first by data
   availability)

Phase 2 flips one factor at a time to `*_shadow=true` on
avwap_v4_equity. The factor logs decisions live; no capital is
deployed to the factor's recommendation.

### 5.1 EMA-first vs HVN-first

Quant agent recommended EMA first because n_near=94 already meets
the proposed n_near>=50 floor on the 3-month v4 options time-OOS
slice (the closest stand-in for v4_equity until the year-long replay
lands), suggesting EMA's data path is ahead of HVN's. HVN at
n_near=25 needs the year-long v4_equity replay to clear the floor,
which lands in Phase 0.

Risk argued for HVN first to keep attribution clean if the rollout
fails (HVN was the original hypothesis; failure is informative for
EMA design). Resolved in favor of quant's data-driven order, with
the caveat: if EMA shadow FAILS, stop and review the harness
methodology (per risk's framing) before starting HVN's clock.

### 5.2 Pre-shadow gates

Before flipping `ema_factor_shadow=true` on the live PaperActive
spec:
- Phase 0 produced an EMA PASS verdict on at least one strategy in
  the year-long replay. (If FAIL, skip shadow; go to shelve.)
- Phase 1 wiring has merged to main with all 9 unit tests green.
- avwap_v4_equity backtest with `ema_factor_shadow=true,
  ema_factor_enabled=false` reproduces PR #91 reference exactly
  (PF=0.7337945435175424).
- Pre-shadow snapshot of live PaperActive trade log saved to
  `_workspace/avwap_v4_equity_preshadow_<date>.json` for the
  attribution baseline.

### 5.3 Shadow window

- **Hard duration:** 10 trading days OR 50 fresh tagged entries on
  avwap_v4_equity, whichever comes first. One-time 5-day extension
  if the window closes below 50 entries; auto-shelve thereafter.
- **Daily check (automated via live-ops or manual):**
  - Shadow-flag rate (entries with `ema_factor_would_extend = "1"`)
    is between 5% and 50% of total entries on a 5-trading-day rolling
    window. Below 5%: signal too rare to grade; above 50%:
    indistinguishable from noise.
  - Tag-emission rate >= 95% on entries with
    `ema_factor_shadow=true` set in the spec at the bar's time. Below
    95% indicates a wiring regression.
  - Sign-flip check: among shadow-flagged entries, compute realized
    PnL of the entries the factor would have extended vs would not.
    First half of the window vs second half. If the sign of (extend-
    favorable PnL minus baseline PnL) flips between halves, halt.
    Same overfit signature that killed cofire reversal-factor.
  - Panic / NaN tag / snapshot serialization error attributable to
    the factor code path: immediate halt.

### 5.4 End-of-window verdict

At the end of the shadow window:
- **PASS** if (a) the shadow-flagged entries' realized PnL beats
  baseline-non-flagged by margin matching the harness prediction
  within ±20%, AND (b) sign-flip check passed. Advance to Phase 3.
- **FAIL** if (a) realized PnL on flagged entries is within noise
  band of baseline (no edge), OR (b) sign-flip check tripped. Revert
  TOML knob to `ema_factor_shadow=false`; document verdict; shelve.
- **INSUFFICIENT_N** if entry count < 50. Trigger one-time 5-day
  extension. After extension, if still < 50, auto-shelve.

### 5.5 Phase 2 deliverables

- `_workspace/ema_shadow_verdict_<date>.md`: 1-page verdict report
  with the specific halt or pass-criterion that fired and the
  realized PnL attribution.
- TOML revert (if FAIL) or advance to Phase 3 (if PASS).

## 6. Phase 3: active promotion (EMA first; canary universe; risk-reduced)

Phase 3 flips `ema_factor_enabled=true, ema_factor_shadow=false` on
a CANARY clone of avwap_v4_equity, with reduced risk knobs, for 10
trading days. Auto-revert on any halt criterion.

### 6.1 Pre-active gates

- Phase 2 produced a PASS verdict for the factor.
- Risk reduction TOML edit landed: `risk_per_trade_bps=250` (from
  500), `max_position_bps=750` (from 1500). Locked for first 10
  active-mode trading days; restored to 500/1500 on day 11 if all
  halt criteria green.
- Canary universe: temporary `avwap_v4_equity_canary.toml` clone
  (or a `routing.symbols` override knob if added) with 5 symbols:
  AAPL, MSFT, SPY, QQQ, JPM. Criterion: highest liquidity + tightest
  spreads + most-stable HVN profiles (large-cap, broad ownership,
  well-defined daily volume distributions). Minimizes HVN computation
  noise and per-trade slippage cost during validation.

### 6.2 Active-mode halt criteria (auto-revert to shadow)

Any ONE of the following triggers immediate revert to shadow mode
via TOML knob (`ema_factor_enabled=false, ema_factor_shadow=true`).
Revert is hot-reload at next omo-core spec re-read; no rebuild.

- **Drawdown:** strategy-level realized PnL drawdown > 1.5% of
  $100k initial equity ($1,500) over any 5-trading-day rolling window
  AND attribution shows >= 50% of the loss from entries the factor
  newly affected (allowed/blocked vs the prior shadow run baseline).
- **Trade count starvation:** trade count drops more than 40% vs the
  trailing 20-day pre-flip baseline (factor is over-vetoing or over-
  modulating; live signal volume is the canary).
- **Win-rate collapse:** win-rate falls below pre-flip baseline minus
  10 percentage points over 30+ trades.
- **Single-day loss spike:** any single-day loss > 2x the worst pre-
  flip single-day loss in the trailing 60 days.
- **Tag-emission rate drops below 95%** (engine wiring regression).
- **Panic / NaN / snapshot error** attributable to the factor code
  path.

Document every revert with the specific trigger condition that fired
in `_workspace/ema_active_revert_<date>.md`.

### 6.3 End-of-active-window verdict

At the end of the 10-trading-day active window with all halt criteria
green:
- **STABLE** -> restore risk_per_trade_bps to 500, max_position_bps
  to 1500. Widen universe from 5-canary to full 34. Continue
  monitoring at 14-day cadence.
- **REVERTED** -> document the halt cause; if it's a wiring bug,
  fix and re-shadow; if it's an edge-decay finding, shelve.

### 6.4 Phase 3 deliverables

- `configs/strategies/avwap_v4_equity_canary.toml` (new, removed
  after wide-rollout).
- `_workspace/ema_active_verdict_<date>.md`: verdict report.
- TOML edit on `avwap_v4_equity.toml` to widen universe (if STABLE).

### 6.5 Phase 3 LOC budget

Configuration edits only. No new code.

### 6.6 Phase 3 risks

- **Canary symbols are not representative.** If AAPL/MSFT/SPY/QQQ/JPM
  are unusually well-behaved on the factor, the wide-rollout reveals
  edge-cases on smaller-cap symbols. Mitigation: canary is the
  validation gate, not the certification; the 14-day post-widen
  monitoring catches symbol-class divergence.
- **Risk reduction masks the active-mode signal.** With
  risk_per_trade_bps cut in half, PnL impact per trade is half;
  smaller signal-to-noise on the validation. Acceptable trade-off:
  10 days at half-risk is the conservative side; restoration on day
  11 if green.
- **avwap_v4 options (untouched in Phase 1-3) regresses while
  v4_equity is being validated.** Continuous OFF-vs-ON parity gate
  on options remains a constraint. Pre-each-deploy regression test
  asserts options bit-equal across diag-only and diag-plus-shadow.

## 7. Phase 4: HVN (after EMA reaches active-stable OR shelves)

Same shape as Phase 2 + Phase 3 but for the HVN veto on
avwap_v4_equity. HVN is unblocked when EMA is either (a) ACTIVE-
STABLE on the wide universe for 14+ days, OR (b) shelved with a
documented verdict.

The HVN factor's specific differences from EMA on v4_equity:
- HVN is a binary BLOCK (cofire-cloned) not a hold-bars conditioner;
  the shadow-flag rate calibration is "would-block %" rather than
  "would-extend %".
- HVN's harness verdict at PR #92 merge time was sign-flipped from
  the original hypothesis on v4 options (near-HVN OUTPERFORMS by
  lift -1.095 on a thin sample). v4 options data is not used by this
  plan's grading (equity-only), so the sign-flip read does NOT carry
  over -- the year-long v4_equity replay produces its own verdict
  from scratch. If v4_equity year-long shows the same sign flip,
  re-design Phase 1 wiring (see 7.1). If it confirms the original
  direction, Phase 1's veto wrapper stands.
- HVN data on v4_equity may be insufficient even after year-long
  replay (v4_equity is LONG-only; near-resistance HVN entries fire
  only at the AVWAP-vs-prior-day-high anchor cluster). If HVN year-
  long verdict is INSUFFICIENT_DATA, defer indefinitely; do NOT
  substitute v4 options data (out of scope for this plan).

Phase 4's specific success/halt criteria mirror Phase 2/3 with
substitutions (`would_block` rate instead of `would_extend` rate;
trade-count starvation threshold tightens because a veto strictly
REDUCES trade count by design -- raise the Phase 3 threshold from
40% to 70% to allow the expected reduction).

### 7.1 Phase 4 sign-flip-aware wiring (only if v4_equity year-long
   confirms the sign flip)

If the v4_equity year-long replay shows near-HVN OUTPERFORMS (the
sign opposite the original hypothesis) at n_near >= 50 on both
holdouts with signs agreeing, Phase 1's `applyHVNVeto` flips to a
positive-strength wrapper instead of a veto. Pseudocode:

```
if cfg.HVNFactorBoostMode {
    if near {
        sig.Tags["hvn_factor_boost"] = "1"
        // confluence boost is per-call-site; insert at the 6 sites
        // engineer flagged in the consult (avwap_v1.go:1881, 1969,
        // 2057, 2172, 2236, 2316)
    }
}
```

This flips the wiring shape from veto (1 insertion point) to
booster (6 insertion points). LOC budget grows from ~50 to ~80.
Document the sign-flip decision in
`_workspace/hvn_signflip_<date>.md` with the year-long v4_equity
verdict that triggered it.

## 8. Out of scope

- **Promoting on avwap_v4 (options).** v4 options stays diag-only
  for the lifetime of this plan. Its diag opt-in from PR #92 keeps
  emitting tags as inert telemetry but is not used by any phase
  here. If a separate plan ever takes options to active, it must
  produce its own harness verdict, its own canary set, and its own
  shadow window -- the equity-only validation here does NOT
  transfer to the bidirectional options book.
- Promoting HVN or EMA factors on whale_pullback_v1 (Deactivated;
  out of scope until live).
- Promoting on macd_only_v1 or any non-AVWAP strategy. Different
  entry physics; tags would dilute the attribution signal.
- Augmenting the v4_equity harness sample with v4 options trade-log
  data (would require underlying-ticker extraction in the harness
  and direction-filtering on the options entries; deferred until
  a future plan needs it).
- Algorithmic auto-shelve based on live PnL alone (manual review at
  each verdict gate; automation is a Phase 5 follow-up).
- Touching cofire veto code paths.
- Reworking the harness's bucket boundaries (locked at hvn_dist_atr
  <= 0.5 / |ema_dist_atr| <= 1.0; future re-bucketing requires its
  own plan).
- Adding new tag keys (PR #92 shipped 11 keys total; no additions
  in this plan).
- Modifying snapshot retention or warmup parity infrastructure. New
  factor state flows through existing seams.

## 9. Owner + revisit

- **Phase 0 owner:** ridopark (user). Target completion: 2026-05-14
  (1 week from this plan's draft date 2026-05-07).
- **Phase 1 owner:** ridopark / Claude pair on the wiring. Target
  completion: 2026-05-19 (5 working days after Phase 0).
- **Phase 2 EMA shadow start:** depends on Phase 0 EMA verdict;
  earliest 2026-05-20.
- **Phase 3 EMA active start:** earliest 2026-06-03 (10 trading
  days after Phase 2 start, assuming PASS).
- **Phase 3 EMA wide-rollout:** earliest 2026-06-17 (10 trading
  days after canary active start, assuming all halt criteria green).
- **Phase 4 HVN shadow start:** earliest 2026-07-01 (after EMA
  reaches active-stable on wide universe for 14 days OR shelves).
- **Mandatory check-in cadence:** every 14 days from this plan's
  draft date. Each check-in updates the `_workspace/` verdict
  files with the current phase status and any halt-criterion that
  fired since the last check-in.

## 10. Acknowledgment requested

Four phases, mostly serial. Promotion target is avwap_v4_equity
ONLY; v4 options is out of scope.

- Phase 0: harness/data improvements (~10 LOC + year-long v4_equity
  replay, ~1 week, mandatory pre-Phase-1 gate).
- Phase 1: wiring shells for both HVN and EMA factors on v4_equity
  (~80 LOC engine + 250 LOC tests + 9 TOML lines, 5 working days,
  ships regardless of Phase 0 verdict).
- Phase 2-3 EMA shadow + active on v4_equity: ~3-4 weeks live
  validation; revert-to-shadow on any halt criterion.
- Phase 4 HVN on v4_equity: same shape after EMA settles; possibly
  sign-flipped wiring depending on Phase 0 v4_equity year-long
  verdict.

Request explicit go-ahead to:

- proceed with all four phases (sequencing: 0 -> 1 -> 2-EMA ->
  3-EMA -> 4-HVN), OR
- proceed with Phase 0 only first; pause for re-acknowledgment
  before Phase 1 once verdicts land, OR
- adjust scope first (e.g., raise/lower the n_near>=50 floor;
  pick a different wiring shape; defer HVN entirely until v4_equity
  has 6 months of live tagged data; widen Phase 0 to include v4
  options trade logs as an augmenting grading sample with the
  underlying-ticker extraction and direction-filtering enhancements).
