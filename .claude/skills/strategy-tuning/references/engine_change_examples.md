# Engine Change Examples

Reference examples for the Engine Change Pipeline. Read this when implementing engine changes.

## Retest Quality Filter
- **Type:** new_filter
- **TOML param:** `retest_quality_min_body_hold = 0.50` (default 0 = disabled)
- **Implementation:** In `onAwaitingRetestBar` (orb_tracker.go), after detecting touch, check that the retest candle's close holds above 50% of the breakout candle's body. If not, reject the retest.
- **Files:** orb_tracker.go (ORBConfig struct + filter logic in retest phase)
- **Rationale:** Weak retests that immediately reverse through the breakout level have low follow-through. Requiring the retest candle to hold above half the breakout body filters these out.

## ADR Exhaustion Filter
- **Type:** new_filter
- **TOML param:** `adr_exhaustion_pct = 0.60` (default 0 = disabled)
- **Implementation:** Before emitting signal, compute how much of the 20-day ADR (Average Daily Range) has been consumed by the current session's move. Skip if > 60%.
- **Files:** orb_tracker.go (ORBConfig + filter in onRangeSetBar), may need daily ATR data from IndicatorSnapshot
- **Rationale:** Late-range breakouts (price already moved >60% of typical daily range) have lower follow-through because the move is already exhausted.

## New Exit Rule (R-Multiple Stop)
- **Type:** new_exit_rule
- **TOML param:** `[[exit_rules]] type = "R_MULTIPLE_STOP"` with params `r_multiple`, `min_hold_bars`
- **Implementation:**
  1. Add `ExitRuleRMultipleStop ExitRuleType = "R_MULTIPLE_STOP"` to exit_rule.go
  2. Add case to `NewExitRuleType()` validation
  3. Implement `evaluateRMultipleStop()` in evaluators.go — triggers when price moves N×R against entry (R = entry-to-stop distance)
  4. Add dispatch case in `Evaluate()`
  5. Update `isKnownExitRuleParamKey()` in spec_loader.go if per-symbol overrides needed
- **Files:** exit_rule.go, evaluators.go, spec_loader.go
- **Rationale:** R-multiple stops scale with the trade's initial risk, unlike fixed BPS stops. A 2R stop on a tight setup is tighter than on a wide setup.
