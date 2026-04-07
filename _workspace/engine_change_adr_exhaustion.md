## Change: ADR Exhaustion Filter
**Type:** new_filter
**Quant Rationale:** When price has already consumed >60% of its typical daily range before the ORB breakout, the remaining follow-through potential is limited. These late-range breakouts tend to fail because the move is already exhausted. Filtering them should improve win rate.
**Expected Impact:** Win rate +2-3pp, PF +0.02-0.05

### Implementation Requirements
- Files to modify: `backend/internal/app/monitor/orb_tracker.go`
- New TOML params: `adr_exhaustion_pct = 0` (default 0 = disabled)
- Architecture layer: app (monitor)

### Logic
Before emitting the setup signal (right before the SetupCondition is constructed), compute:
1. Get the current session's high and low so far (from today's bars)
2. Get the 14-day ATR from the IndicatorSnapshot (snap.ATR is already available)
3. Compute session range consumed: `sessionRange = sessionHigh - sessionLow`
4. Compute exhaustion ratio: `exhaustion = sessionRange / ATR`
5. If `exhaustion > adr_exhaustion_pct` → reject the signal, log and return nil

### Where to add
The session high/low during the day can be tracked on the ORBSession struct:
- Add `SessionHigh float64` and `SessionLow float64` to ORBSession
- Update them in OnBar for every bar processed during the session
- Check the exhaustion ratio right before constructing SetupCondition

### Data Available
- `snap.ATR` — 14-period ATR already computed by the indicator pipeline
- `bar.High`, `bar.Low` — available on every bar
- The session resets daily (cycleToRangeSet clears session state)

### Acceptance Criteria
- Build passes: go build ./...
- Tests pass: go test ./internal/...
- Backtest PF improves by >= 0.02 (or DD improves by >= 1pp)
- Trade count does not drop > 20% (from 2184 baseline)
