## Change: Retest Quality Filter
**Type:** new_filter
**Quant Rationale:** Weak retests that immediately reverse through the breakout level have low follow-through. Requiring the retest candle's close to hold above a fraction of the breakout candle's body filters out these weak retests, improving win rate by 2-3pp.
**Expected Impact:** Win rate +2-3pp by filtering weak retests → PF +0.02-0.05

### Implementation Requirements
- Files to modify: `backend/internal/app/monitor/orb_tracker.go`
- New TOML params: `retest_quality_min_body_hold = 0` (default 0 = disabled)
- Architecture layer: app (monitor)
- New logic: In the AWAITING_RETEST → RETEST_CONFIRMED transition, after detecting the touch AND hold confirmation, add an additional check: the retest candle's close must hold above `retest_quality_min_body_hold` fraction of the breakout candle's body range. Specifically:
  - For LONG: retest candle close >= breakout_level - (breakout_body * (1 - min_body_hold))
  - For SHORT: retest candle close <= breakout_level + (breakout_body * (1 - min_body_hold))
  - Where breakout_body = abs(breakout_candle.Close - breakout_candle.Open)
  - And breakout_level = ORBHigh (for long) or ORBLow (for short)
- The breakout candle data needs to be stored in the ORBSession when transitioning from RANGE_SET → AWAITING_RETEST

### Acceptance Criteria
- Build passes: go build ./...
- Tests pass: go test ./internal/...
- Backtest PF improves by >= 0.02 (or DD improves by >= 1pp)
- Trade count does not drop > 20%
