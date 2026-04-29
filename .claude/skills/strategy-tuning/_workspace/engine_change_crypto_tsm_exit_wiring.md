## Change: Wire crypto_tsm_v1 strategy SignalExit to actually fire

**Type:** signal_routing_fix
**Quant Rationale:** Strategy's own OnBar exit signals (trailing_stop, signal_reversal, hard_stop, vol_spike, time_stop) are emitted but never result in closed positions. All 26 exits in a recent backtest came from position_monitor exit rules (22 MAX_HOLDING_TIME at exactly 14 days, 4 MAX_LOSS at >=8% loss). Winners with 20%+ pullback from MFE and losers with daily-close below -5% all exited via position_monitor, not strategy.

**Expected Impact (per quant):** PF 2.408 → 2.8-3.2. Letting winners book profit on trend weakness via trailing_stop should add $2-4k gross. Hard_stop cutting losses earlier should shrink avg loss from $823 toward ~$500. DD expected to stay under 2.5%.

### Evidence

Results file: `.claude/skills/strategy-tuning/_workspace/crypto_tsm_v1_z095.json` (PF 2.408, 26 trades).

Exit reason counts (from trade rationale field):
- MAX_HOLDING_TIME: 22
- MAX_LOSS: 4
- trailing_stop (strategy): 0
- signal_reversal (strategy): 0
- hard_stop (strategy): 0
- vol_spike (strategy): 0
- time_stop (strategy): 0

Specific trades that should have triggered strategy exits but didn't:
- Winner $3095 with MFE 31.34% / MAE -2.22% → trailing_stop (2.5 ATR) should fire on pullback
- Winner $2152 with MFE 20.11% → trailing_stop should fire
- Winner $1623 with MFE 16.58% → trailing_stop should fire
- Losers with MAX_HOLDING exit and MAE -7% to -11% → hard_stop (5%) should have fired first

### Root Cause Hypothesis (needs verification)

Per Explore agent investigation, SignalExit signals ARE routed through the pipeline:
1. Strategy OnBar returns SignalExit → runner.go:1716
2. Signal enrichment skipped for SignalExit → signal_debate_enricher.go:127-153
3. Risk sizer converts SignalExit → OrderIntent → risk_sizer.go:579-727
4. Execution service submits order

BUT: the OrderIntent arrival at the broker may be outpaced by position_monitor's evaluator (which fires on every bar tick). On 1d timeframe, OnBar fires once at daily close. If position_monitor fires first on the same daily close, its OrderIntent submits first, strategy's SignalExit arrives at a flat position and is rejected.

Alternative hypothesis: SignalExit with `direction="LONG"` on a LONG position is misrouted — risk_sizer expects SignalExit to carry `exit_direction` tag or infer CLOSE_LONG from position side. If the strategy's SignalExit is emitted with `SideSell` but no position-side metadata, the reconciler/sizer may drop it as "nothing to close" if broker reconcile hasn't propagated yet.

Third hypothesis: the signal_reconciler or a gate silently drops exits for strategies with exit_rules defined in TOML. Logic might be "if strategy has MAX_HOLDING_TIME, don't honor OnBar exits to avoid duplicates".

### Implementation Requirements

1. **Investigate and identify exact root cause.** Read `runner.go`, `risk_sizer.go`, `signal_reconciler.go`, `positionmonitor/exit_eval.go`. Trace the code path for a SignalExit emitted on a daily bar. Add debug logging if needed to confirm.

2. **Fix so strategy SignalExit wins the race OR coexists correctly with position_monitor.** Options (ordered by preference):
   - (a) Prioritize strategy-emitted exits over position_monitor rules when both fire on same bar (strategy has more context)
   - (b) Gate position_monitor evaluator to not fire if strategy emitted an exit same bar
   - (c) Merge both exit paths — strategy exits AND position_monitor rules both evaluated, first-to-fire wins at a deterministic priority

3. **Ensure the fix is scoped narrowly** to not regress avwap_v1, macd_v1, crypto_revert_v1 behavior. Those strategies may already depend on position_monitor-only exits. Probably the safest approach is a new TOML flag: `strategy_exits_priority = true` (default false), and only crypto_tsm_v1 opts in.

4. **Preserve exit rationale attribution.** The trade log `rationale` field should clearly state which system exited the position: `strategy:trailing_stop` vs `exit_monitor:MAX_HOLDING_TIME`. Today strategy exits produce generic rationales; position_monitor exits produce `exit_monitor:<rule>`.

### Files likely to modify

- `backend/internal/app/backtest/runner.go` — OnBar signal handling, exit rule evaluator invocation
- `backend/internal/app/strategy/risk_sizer.go` — SignalExit → OrderIntent conversion (line 579-727)
- `backend/internal/app/strategy/signal_reconciler.go` — exit signal handling
- `backend/internal/app/positionmonitor/exit_eval.go` — check if strategy exit pending before firing rule
- `backend/internal/app/strategy/builtin/crypto_tsm_v1.go` — may need to tag exit signals with origin for reconciler

### Acceptance Criteria

1. `go build ./...` passes
2. `go test ./internal/...` passes
3. After rebuild + restart, backtest crypto_tsm_v1 on same config (BTC/ETH, 2023-01-01 → 2026-04-14, 1d, 100k, 10 bps):
   - Strategy-emitted exits (trailing_stop, hard_stop, signal_reversal) appear in trade rationales for AT LEAST 5 of 26 trades
   - PF does not regress below 2.0 (we currently sit at 2.408; net improvement expected)
   - DD does not exceed 4% (currently 2.16%)
4. avwap_v1 backtest (34-symbol, 5m, 1yr, default DNA) does NOT regress — PF within ±5% of pre-change.

### Out of scope

- Do NOT modify crypto_tsm_v1.go's signal logic itself (entry rules, exit conditions are correct)
- Do NOT touch the z-score / VWTSM signal computation
- Do NOT add new exit rule types to [[exit_rules]]
