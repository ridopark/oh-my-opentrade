# Known-Good Strategy Snapshot — 2026-04-14

This directory is a point-in-time snapshot of the three active strategy DNA
files at the end of the 2026-04-14 tuning session. Keep these as the
fallback baseline if an active config needs to be reverted.

## Files

| File | Strategy ID | Description |
|------|-------------|-------------|
| `avwap_v4.toml` | `avwap_v4` | Anchored VWAP confluence-weighted options (Pass 2 tuned) |
| `macd_only_v1.toml` | `macd_only_v1` | 5m MACD crossover options with DP Z conditioning |
| `overnight_z_v1.toml` | `overnight_z_v1` | Late-session DP buy_ratio Z-score bias, open→MOC |

All three are the `PaperActive`, `paper_only = true` configs that production
is deploying as of this snapshot.

## Tuning Context

The session started because `avwap_v4` was underperforming after the
session-weight feature commit (`937998c`) replaced the binary `allowed_hours`
gate with a graduated weight system. The feature commit introduced
"TEST: open for today" session weights that effectively loosened the trading
window. Removing those weights and reverting to the binary gate restored the
baseline edge, and two tuning passes plus an engine-level spread audit
improved it further.

`macd_only_v1` and `overnight_z_v1` were verified against a user-supplied
known-good snapshot and required no changes — they match their last tuned
commits (`a5c5b5d` and `1e33d26` respectively).

## avwap_v4 — Tuning Results

Three changes accepted over two passes, each validated with split-half,
outlier, and slippage stress tests.

### Accepted changes

1. **Added `PREMIUM_STOP` exit rule** (`threshold = 0.10`)
   Caps premium-side loss magnitude; symmetric with the +6% target.
   Largest loss dropped from -$4,402 to -$1,345. DD dropped 10.4pp.

2. **`allowed_hours_end` 10:00 → 09:50**
   09:50+ entries were breakeven; the 09:35-09:49 window carries the edge.
   Trade count drop -25%, PF +0.09.

3. **`volume_mult` 1.5 → 2.5**
   Monotonic PF improvement to 2.5; 3.0 overfiltered. Quality > quantity.

4. **`target_pct` 0.10 → 0.06** (Pass 2)
   Tighter profit target dominates 8% and 10% at every spread assumption.
   See the Spread Sensitivity section below.

### Rejected this session

| Change | Reason |
|--------|--------|
| Remove PLTR from symbols | Slot backfill — freed slots filled with worse trades |
| Stagnation 60m (from 90m) | Clipped recovering trades; premium stop already caps loss |
| `min_slope_bps` 0.75 / 1.0 | DD improved but Sharpe regressed |
| `hold_bars` 3 | Too much confirmation delay on 5m bars |
| `min_confluence_score` 9 | Confluence 9-11 band is worse than 8 (quant verified) |
| `premium_stop` threshold 0.05 | Sharpe 6.46 — simulation artifact from unrealistically tight stop |
| `stop_bps` 75 | Underlying stop never fires; all exits go through premium rules |

## avwap_v4 — Backtest Metrics

Universe: 34 symbols. Period: 2025-04-14 → 2026-04-14. Timeframe: 5m.
Initial equity: $100k. Slippage: 10bps. `max_positions = 6`, `max_per_group = 3`.

### Solo (avwap_v4 only)

| Config | PF | WR | P&L | Trades | DD | Sharpe |
|--------|-----|-----|------|--------|----|--------|
| Known-good baseline (pre-session) | 1.159 | 62.2% | $65,904 | 1,305 | 17.84% | 1.777 |
| End of Pass 1 | 1.604 | 49.4% | $133,150 | 992 | 7.44% | 3.974 |
| End of Pass 2 (mult=1.0, optimistic) | **1.996** | 58.1% | $178,559 | 999 | 4.84% | 5.696 |
| End of Pass 2 (mult=2.0, realistic) | **1.749** | 58.2% | $145,341 | 999 | 5.75% | 4.626 |

### Pair (all 3 strategies deployed together)

| Config | PF | P&L | DD | Sharpe | Trades |
|--------|-----|------|----|--------|--------|
| Pass 1 result | 1.471 | $190,838 | 7.89% | 4.013 | 1,760 |
| Pass 2 (mult=1.0) | 1.633 | $233,181 | 5.59% | 5.250 | 1,840 |
| **Pass 2 (mult=2.0, realistic)** | **1.424** | **$168,733** | 7.09% | 3.760 | 1,833 |

## Spread Sensitivity — Final Decision Matrix

After Pass 2, the quant flagged the result as suspicious because the R:R
arithmetic didn't reconcile: `58.1% × 6% - 41.9% × 10% = -0.70%` expected
premium per trade, yet dollar PF was 2.0. An engine audit found that the
simbroker was applying exit spread via hardcoded tiers but skipping spread
on entries entirely — a systematic asymmetry.

Two knobs were added to the backtest request (commit `6425ce7`):

- `option_spread_multiplier` (float64, default 1.0) — scales exit tiers
- `option_entry_spread_enabled` (bool, default false) — adds entry half-spread

Defaults preserve prior behavior byte-for-byte.

### Sensitivity matrix — avwap_v4 solo, target 6% vs 8%

Entry spread always on. Multiplier applied to both entry and exit tiers.

| mult | target=6% PF | target=6% P&L | target=6% Sharpe | target=8% PF | target=8% P&L | target=8% Sharpe |
|------|--------------|---------------|-------------------|--------------|---------------|-------------------|
| 1.0  | 1.996 | $178,559 | 5.696 | 1.750 | $152,436 | 4.757 |
| 2.0  | **1.749** | $145,341 | 4.626 | 1.532 | $117,093 | 3.637 |
| 2.5  | 1.626 | $126,606 | 4.010 | 1.423 | $96,844  | 3.027 |
| 3.0  | 1.512 | $107,739 | 3.388 | 1.336 | $79,966  | 2.500 |

**Target 6% dominates target 8% at every spread assumption** by roughly
+0.2 PF / +$28k P&L. Ship 6%. `mult=2.0` is the realistic planning baseline;
`mult=1.0` numbers are optimistic and should not be cited externally.

## Validation Evidence

- **Split-half stability (target 6%, mult 1.0)**: H1 PF 2.034 / H2 PF 1.962.
  Both halves positive and consistent.
- **Outlier resistance (target 6%, mult 1.0)**: PF degrades gracefully as
  top winners are removed:
  - Top 1 removed: 1.940
  - Top 5 removed: 1.791
  - Top 10 removed: 1.672
  - Top 20 removed: 1.574
  Edge is distributed, not carried by a handful of outliers.
- **Slippage robustness (target 6%, mult 1.0)**: 3x backtest slippage (30bps
  underlying) gave PF 1.919 — only -0.08 drop. Real edge, not a fill artifact.
- **Entry simulation audit (commit `6425ce7`)**: added option entry spread
  cost to simulator. At realistic mult=2.0 the Sharpe drops from 5.7 → 4.6,
  putting it in defensible territory for an options breakout strategy.

## Live Paper Monitoring Thresholds

Track weekly against the mult=2.0 backtest baseline. **Kill the strategy if
any threshold breaches.**

| Metric | Expected | Warn | Kill |
|--------|----------|------|------|
| Rolling 30-trade PF | ~1.75 | < 1.4 | < 1.2 after 60+ trades |
| Hit rate on +6% target | ~58% | > 15pp below backtest | — |
| Realized entry slippage vs mid | < 0.6% per leg | > 1.0% sustained | — |
| Live Sharpe / backtest Sharpe ratio | 0.6 - 0.8 | — | < 0.4 after 100 trades |
| Avg loser size | ≤ 1.3× backtest avg loser | > 1.5× | — |

If realized entry slippage sustains > 1.0% per leg, re-baseline the backtest
to `mult=2.5` (quant's bad-fill regime) before making any further tuning calls.

## Reference Commits

| Commit | Change |
|--------|--------|
| `02aa46f` | fix(avwap): restore morning-only session weights |
| `9708620` | tune(avwap): PF 1.16→1.60 via premium stop + tighter window + volume filter |
| `6425ce7` | tune(avwap): target 10%→6% after option spread-cost audit |

## Reference Backtest Files

Saved to `_workspace/` during the session:

- `avwap_v4_known_good_baseline.json` — pre-tuning baseline
- `avwap_v4_pstop10_final.json` — end of Pass 1
- `avwap_v4_p2_target6.json` — end of Pass 2 (mult=1.0)
- `avwap_v4_p2_target6_h1.json`, `_h2.json` — split-half validation
- `avwap_v4_p2_target6_slip30.json` — 30bps slippage stress test
- `avwap_v4_t6_mult2.json`, `_mult25.json`, `_mult3.json` — spread sensitivity
- `avwap_v4_t8_mult20.json`, `_mult25.json`, `_mult30.json` — target 8% comparison
- `avwap_v4_final_pair_realistic.json` — final pair backtest (mult=2.0)

## Restoration Instructions

If an active config needs to be reverted to the known-good state:

```bash
cp configs/backups/strategies-good-20260414/avwap_v4.toml configs/strategies/
cp configs/backups/strategies-good-20260414/macd_only_v1.toml configs/strategies/
cp configs/backups/strategies-good-20260414/overnight_z_v1.toml configs/strategies/
```

Then rebuild and restart omo-core so the strategy service reloads the DNA:

```bash
cd backend && go build -o bin/omo-core ./cmd/omo-core
tmux kill-session -t omo-core && ./scripts/start.sh
```
