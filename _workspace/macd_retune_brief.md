# macd_only_v1 IS baseline (2025-10-01..2026-03-31) — quant brief

Results path: /home/ridopark/src/oh-my-opentrade/_workspace/macd_baseline_IS_results.json
OOS results path: /home/ridopark/src/oh-my-opentrade/_workspace/macd_baseline_OOS_results.json
Current config: /home/ridopark/src/oh-my-opentrade/configs/strategies/macd_only_v1.toml
Last known high-PF config (9a2ac6a, PF 1.16): git show 9a2ac6a:configs/strategies/macd_only_v1.toml

## IS headline
- Trades: 1414 (paired, 2828 rows)
- PF: 0.839  WR: 61.1%  Sharpe: -2.42
- Total PnL: -$50,850
- Max DD: 59.8%
- Avg win $306, Avg loss $574 (losses 1.87x wins)

## OOS headline (2026-04-01..04-20)
- Trades: 165  PF: 1.38  WR: 67.9%  PnL: +$8,037  DD: 7.5%  Sharpe: 6.85

## Exit reason breakdown (IS)
- STAGNATION_EXIT (90 min): 199 trades, WR 7.5%, -$120,174
- MAX_HOLDING_TIME (180 min): 159 trades, WR 5.0%, -$122,342
- EOD_FLATTEN: 237 trades, WR 30.0%, -$55,258
- PREMIUM_TARGET (15%): huge winners (+$1k to +$4k each)
- CHANDELIER_TRAIL(activate 5%, giveback 3%): many small wins, capped at +$200 avg per activation bucket

## Regime breakdown (IS)
- TREND_UP: n=297  pnl=+$6,200  WR 66.7%
- REVERSAL: n=88   pnl=-$3,724  WR 55.7%
- TREND_DOWN: n=226  pnl=-$20,897  WR 59.7%
- BALANCE: n=803  pnl=-$32,428  WR 60.0%   <-- despite ADX<20 gate!

## Direction (IS)
- CALL: n=976  pnl=-$37,442  WR 61.6%  avg $-38
- PUT:  n=438  pnl=-$13,408  WR 60.0%  avg $-31

## Top loser symbols
AFRM -$11,636, IWM -$9,681, SOXL -$9,397, SOFI -$7,946, SMCI -$7,530, QQQ -$5,464, GOOGL -$5,055, SPY -$4,715, RBLX -$4,695
## Top winner symbols
AMD +$11,300, CRM +$5,956, META +$5,549, MSFT +$5,146, XOM +$3,348, HOOD +$3,117, HIMS +$2,782

## Monthly PnL
2025-10: n=293, -$14,825, WR 61.8%
2025-11: n=203,  -$3,301, WR 61.1%
2025-12: n=225, -$17,299, WR 52.4%
2026-01: n=248,  +$2,436, WR 65.7%
2026-02: n=195, -$11,625, WR 59.5%
2026-03: n=250,  -$6,234, WR 64.8%
2026-04 OOS: n=165, +$8,037, WR 67.9%

## Worst single trades (all MAX_HOLDING or STAGNATION)
SOXL CALL BALANCE -$5,037 (entry $1.18, exit $0.01 — 99% premium loss)
SOFI CALL BALANCE -$3,994 (entry $0.85, exit $0.05)
SOXL CALL BALANCE -$3,948
NET  CALL TRENDDN -$3,795 (entry $6.51, exit $1.08)
...

## Config drift since 9a2ac6a (PF 1.16 baseline)
- min_confluence_score: 60 -> 65 (tighter)
- PREMIUM_TRAIL(trail 12%, activation 8%) -> CHANDELIER_TRAIL(activate 5%, giveback 3%)
- risk_per_trade_bps: 300 -> 500 (bigger size)
- max_position_bps: 1000 -> 2000 (bigger position cap)
- max_per_group: 3 -> 2 (tighter sector)
- max_contracts: 5 -> 50 (much bigger)
- added kill_switch, portfolio_heat_guard, sector_exposure_guard, directional_bias_guard, pdt_guard, reg_t_guard, earnings_blackout_gate, macro_event_gate
- added range_gate (BALANCE + ADX<20 block)
- added dp_z_conditioning
- signal engine: bollinger_macd -> macd (rename, same logic per f4577a3)

## Key commits since 9a2ac6a affecting config
- 9fb9e7d/0d2b296: loosen filters for live broker testing (likely the regression vector)
- 00ab10b: add BALANCE+ADX<20 gate
- 69f791a: wire 8 new gates to execution chain
- e73251a: DTE 5-14 -> 5-30 (stopgap; appears already reverted to 5-14 in current)
- a5c5b5d: mpg 3 -> 2
- 58aefd0: PREMIUM_TRAIL -> CHANDELIER_TRAIL(5%,3%)

## Open questions for quant-analyst
1. Root cause: is the IS regression driven by entry filter relaxation (looser gates), exit structure (no premium MAX_LOSS, trails too loose), or position-size inflation (risk_bps 300->500, max_position 1000->2000, max_contracts 5->50)?
2. The BALANCE regime loses $32k despite ADX<20 gate. Is the gate effective? Should we drop BALANCE entirely?
3. TREND_DOWN loses $21k but WR is 59.7% — so small wins, big losers. Does MACD work on the short side?
4. 61% WR + PF 0.84 means 1 big loser eats 5-6 small winners. Is this a stop-loss / MAX_LOSS problem, or a position-sizing problem (winners too small, losers too big)?
5. Should we revert to PREMIUM_TRAIL(12%, 8%)? Chandelier(5%, 3%) activates too rarely — 59 chandelier exit buckets total.
6. Classify each recommendation as PARAM_CHANGE vs ENGINE_CHANGE.
