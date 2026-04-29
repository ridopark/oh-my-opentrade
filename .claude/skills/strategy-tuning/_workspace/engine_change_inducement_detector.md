## Change: Inducement Detector (Factor 7 Confluence)

**Type:** new_filter / new_confluence_factor
**Spec source:** `~/.claude/projects/-home-ridopark-src-oh-my-opentrade/memory/project_inducement_detector.md`
**Depends on:** Phase 2B fill-model validation passed (otherwise we're measuring edge on suspect baseline).

### Quant Rationale
When price sweeps past a prior swing high/low (taking out stops) then reverses back, weak hands are shaken out — creating higher-conviction entries. Structural microstructure mechanism (liquidity sweep), orthogonal to the existing confluence basis (Fib/KeyLevel/Candle/Band/DP). Adds capacity to the effective confluence ceiling without reshuffling existing signals. Spec adapted from Waqar Asim's scalping concept, reframed as a confluence input to AVWAP rather than standalone.

### Expected Impact
- PF delta >= 0.3 for inducement-tagged trades vs untagged (memory spec target).
- Trade count approximately flat in scoring-only mode (entries still gated by `min_confluence_score`, new factor just adds to score).
- When combined with a widened hour gate (Phase 4), may make the temporal gate redundant — that's the key hypothesis to test.

### Implementation Requirements

Files to modify:
- `backend/internal/app/strategy/builtin/avwap_v1.go` — AVWAPConfig additions, AVWAPState additions, OnBar hook, Factor 7 in computeConfluence
- `backend/internal/domain/strategy/swing_detector.go` — reuse existing SwingDetector.Push (no change expected unless N differs)
- `backend/internal/app/strategy/builtin/avwap_v1_test.go` — new unit tests for detection edge cases
- `configs/strategies/avwap_v4.toml` — 8 new TOML params, default disabled

New TOML params (in `[params]` of avwap_v4.toml):
```toml
inducement_enabled = false           # master switch, default off
inducement_swing_n = 3               # SwingDetector N (reuse existing anchor detection if same N)
inducement_swing_depth = 8           # recent swings to track per side
inducement_max_age_bars = 60         # max swing age (60 bars = 5h at 5m)
inducement_breach_min_bps = 5        # min breach past swing to qualify
inducement_breach_max_bps = 80       # max breach (beyond = real breakout)
inducement_reversal_bars = 3         # close must return inside within N bars
inducement_volume_min_ratio = 1.2    # sweep bar volume >= ratio * VolumeSMA
```

### AVWAPConfig additions (avwap_v1.go:161+)
```go
// Inducement detector params (Factor 7 confluence)
InducementEnabled         bool
InducementSwingN          int
InducementSwingDepth      int
InducementMaxAgeBars      int
InducementBreachMinBps    int
InducementBreachMaxBps    int
InducementReversalBars    int
InducementVolumeMinRatio  float64
```

Keys to add to `NewAVWAPConfigFromDNA` (or equivalent factory).

### AVWAPState additions (avwap_v1.go:266+)
```go
// Inducement tracking
RecentSwingHighs  []SwingLevel       // ring buffer, cap = InducementSwingDepth
RecentSwingLows   []SwingLevel       // ring buffer, cap = InducementSwingDepth
PendingInducement *PendingInducement // active multi-bar reversal candidate, nil when none
InducementSwing   *start.SwingDetector // local detector instance for this strategy if spec N != shared anchor N
```

### New domain types
```go
// In domain/strategy/inducement.go (new file)
type SwingLevel struct {
    Time     time.Time
    Price    float64
    Side     SwingSide // SwingHigh / SwingLow
    BarAge   int       // bars since confirmed
}

type PendingInducement struct {
    Swing         SwingLevel
    BreachBar     time.Time
    BreachBPS     int
    VolumeRatio   float64
    BarsRemaining int // countdown; 0 = expired
}

type InducementSignal struct {
    Strength  string  // "strong" | "moderate" | "weak"
    Score     int     // 5 / 3 / 2
    Direction start.Side
    Tag       string  // "inducement_strong" etc.
}
```

### Detection Algorithm (new function `detectInducement` in avwap_v1.go)
Per spec, 4 steps:
1. **Candidate sweep**: For each recent swing, compute `breachBPS = |barExtreme - swingPrice| / swingPrice * 10000`. Qualify if `breach_min_bps <= breachBPS <= breach_max_bps` AND `swingAge <= max_age_bars`.
2. **Reversal confirmation**: Same-bar (strongest): `bar.High > swingHigh AND bar.Close < swingHigh` for sweep of high. Multi-bar: create PendingInducement with countdown = reversal_bars.
3. **Volume gate**: Sweep bar volume >= `volume_min_ratio * VolumeSMA`.
4. **Direction alignment**: Sweep of swing HIGH = bearish signal (short). Sweep of swing LOW = bullish signal (long). Score only when signal direction matches the entry direction being evaluated.

Edge cases:
- Multiple qualifying sweeps on same bar: use strongest (highest breach BPS with volume).
- Pending inducement + new sweep: new replaces pending.
- Gap open past swing + close inside on same bar: valid inducement.
- Both high AND low swept on same bar: return nil (volatility event, not directional).

### Scoring (max 5 pts, extends Factor 7 slot in computeConfluence)
| Condition | Score | Tag |
|---|---|---|
| Same-bar reversal + volume confirmed | 5 | `inducement_strong` |
| Multi-bar reversal + volume confirmed | 3 | `inducement_moderate` |
| Same-bar reversal, no volume confirm | 2 | `inducement_weak` |

Note: raises AVWAP's max confluence score from the current ceiling (Fib 3 + KeyLevel 3 + Candle 2 + Band 2 + DP 10 = 20; effective max was 23 per memory — some sources are cumulative) to ~28. That means current `min_confluence_score = 8` threshold is below the new ceiling, so enabling inducement doesn't mechanically block trades — it just adds ranking signal on top. A future sweep could test `min_confluence_score = 10 or 11` once inducement is firing.

### Integration Point in computeConfluence
Currently Factor 6 is Whale 13F. Add Factor 7 after that:
```go
// Factor 7: Inducement (+5)
inducementComp := start.ComponentScore{Name: "inducement", Group: "microstructure", Weight: 5}
if cfg.InducementEnabled && inducementSignal != nil {
    // only score when signal direction matches isLongEntry
    if (inducementSignal.Direction == start.SideLong) == isLongEntry {
        res.Score += inducementSignal.Score
        res.Factors = append(res.Factors, inducementSignal.Tag)
        inducementComp.Fired = true
        inducementComp.Value = float64(inducementSignal.Score)
    }
}
res.Components = append(res.Components, inducementComp)
```

The `inducementSignal` must be computed in OnBar (before computeConfluence is called) since it requires swing state maintenance and is bar-driven, not entry-driven.

### OnBar Changes
After indicators are available and before any emitEntry path, inside OnBar:
```go
// Update swing state and check for inducement
if cfg.InducementEnabled {
    swings := state.InducementSwing.Push(bar) // or reuse shared detector
    for _, sw := range swings {
        state.pushSwingLevel(sw) // appends to RecentSwingHighs/Lows ring buffer
    }
    // Age out stale swings
    state.pruneStaleSwings(cfg.InducementMaxAgeBars)
    // Check for inducement on this bar
    sig := detectInducement(bar, state.RecentSwingHighs, state.RecentSwingLows, &state.PendingInducement, cfg, indicators.VolumeSMA)
    state.LastInducementSignal = sig // consumed by computeConfluence
}
```

### TOML wiring in avwap_v4.toml
Place near the DP conditioning block (~line 200) since both are microstructure-driven:
```toml
# --- Inducement (liquidity sweep) — Factor 7 confluence ---
# Detects when price sweeps past a recent swing high/low then reverses.
# Classic "stop run" pattern: weak hands shaken out before real move.
# DISABLED by default until Phase 2B validates on realistic fill model.
inducement_enabled = false
inducement_swing_n = 3
inducement_swing_depth = 8
inducement_max_age_bars = 60
inducement_breach_min_bps = 5
inducement_breach_max_bps = 80
inducement_reversal_bars = 3
inducement_volume_min_ratio = 1.2
```

### Acceptance Criteria
- `cd backend && go build ./...` passes
- `cd backend && go test ./internal/...` passes (existing tests unchanged)
- New unit tests cover: same-bar reversal, multi-bar reversal, volume gate fail, age cap expiry, breach min/max bounds, both-sides-swept (returns nil), pending-replaced-by-new-sweep
- Enabled backtest on 34-sym / 2025-04-14 → 2026-04-14 / 10bps with `inducement_enabled = true`:
  - Trade count delta: within ±5% of baseline (scoring-only, no entry blocking change)
  - Per-trade decomposition (tagged vs untagged): PF of tagged trades > PF of untagged by >= 0.3
- If target not met, revert TOML enable flag (keep code for future iteration).

### Anti-Goals (explicitly NOT doing)
- Do not make inducement gate entries (block if absent). It is additive to confluence, not subtractive.
- Do not tune `min_confluence_score` as part of this change. Isolated single-variable test per skill rule #9.
- Do not touch the hour gate. That's Phase 4.
- Do not share the SwingDetector instance with the shared anchor resolver unless N matches. If spec N=3 and shared N differs per timeframe (5m=5, 1h=3, 1d=2 from ai_anchor_resolver.go), use an AVWAP-local detector to avoid coupling.

### Risk Assessment
- **Code surface**: ~450 LOC new in avwap_v1.go + ~100 LOC new domain/strategy/inducement.go + ~200 LOC tests. Medium.
- **Live behavior risk**: LOW. Feature is disabled-by-default via `inducement_enabled = false`. Existing behavior is byte-identical with the flag off (compiler dead-code-eliminates the gated paths). Main risk is build breakage from a typo — caught by CI.
- **Measurement risk**: MEDIUM. If Phase 2B hasn't normalized Sharpe, we can't distinguish "inducement adds real edge" from "inducement exploits the same fill-model inflation." Hence dependency.

### OOS Hardening (added 2026-04-18 per Phase 2B verdict)

Phase 2B found avwap_v4 has real but narrow-niche edge (hivol-fresh Sharpe 5.33 vs deployed 8.52 — ~60% inflation from temporal concentration + compounding, not curve-fit). To prevent Factor 7 from absorbing the residual symbol-selection bias, validation MUST use a pre-registered train/holdout split of the deployed 27-sym universe:

- **Train symbols (15, first half alphabetical of deployed 27)**: AAPL, AFRM, AMZN, AVGO, BA, CRM, GOOGL, HOOD, IWM, JPM, LLY, META, MRVL, MSFT, MU
- **Holdout symbols (12)**: NET, NVDA, OXY, PLTR, QQQ, RBLX, RIVN, SMCI, SNOW, SOFI, SOXL, XOM

Validation sequence:
1. With `inducement_enabled = false` (baseline): run A@train-15 and A@holdout-12 separately. Record baseline PF/Sharpe per set.
2. With `inducement_enabled = true` (scoring-only, no parameter tuning): re-run both sets. Compute delta for train and holdout independently.
3. Accept criterion: holdout delta >= 0 AND holdout inducement-tagged trades show PF >= untagged + 0.3. If holdout degrades even while train improves, Factor 7 has absorbed symbol-selection bias — REVERT.
4. Bias-guard: the `inducement_breach_min_bps`, `inducement_breach_max_bps`, `inducement_reversal_bars` defaults MUST be set a priori and never tuned on the holdout.

### Sequencing vs Phase 4 (hour gate revisit)
Phase 4 asks: can inducement subsume the hour gate? That test requires:
1. Inducement enabled (this change, default true after acceptance)
2. Run variant D (no hour gate) again on 34-sym DoltHub-complete data
3. Compare tagged-trade PF by time-of-day bucket vs untagged
4. If tagged trades maintain PF > 2.0 across ALL time buckets (including the dead midday zone), the gate is obsolete. If tagged trades still decay in midday, keep the gate AND the inducement (additive).
