---
name: strategy-tuning
description: "Multi-strategy DNA parameter tuning with automated backtest loop. Works with any strategy (AVWAP, ORB, etc.) following schema_version = 2. Triggers on 'tune', 'tweak', 'optimize', 'parameter', 'DNA', 'TOML config' keywords. Does NOT trigger for simply reading or explaining a TOML file."
---

# Strategy Tuning Workflow

Systematic, automated workflow for tuning any oh-my-opentrade strategy DNA. This skill drives an autonomous tune→backtest→evaluate loop and checkpoints with the user when meaningful improvement is found.

## Strategy Discovery

All strategy DNA files live at `configs/strategies/{id}.toml` (schema_version = 2).

To find available strategies:
```bash
ls configs/strategies/*.toml
```

Known strategies and their signal engines:
| Strategy ID | Engine | Description |
|-------------|--------|-------------|
| `avwap_v4` | `avwap` | Anchored VWAP confluence-weighted entries |
| `orb_break_retest` | `orb_break_retest` | Opening Range Breakout — break & retest |
| *(new)* | *(read hooks.signals.name)* | Any future schema_version = 2 strategy |

## DNA File Structure

```toml
schema_version = 2
[strategy]     # id, version, name, description
[lifecycle]    # state (PaperActive|LiveActive|Disabled), paper_only
[routing]      # symbols, timeframes, priority, conflict_policy, asset_classes
[params]       # algorithm parameters — THIS IS WHAT YOU TUNE
[hooks]        # signal engine binding
[dynamic_risk] # dynamic risk adjustment
[[exit_rules]] # array: SWING_STOP, STAGNATION_EXIT, EOD_FLATTEN, MAX_LOSS, etc.
[options]      # options trading config
```

## Automated Tuning Workflow

### Step 1: Establish Baseline
1. Read the target DNA file: `configs/strategies/{strategy_id}.toml`
2. Back up: `cp configs/strategies/{id}.toml configs/backups/{id}_pre_tune_$(date +%Y%m%d).toml`
3. Run baseline backtest (see Backtest API below)
4. Record baseline metrics: PF, WR, Net P&L, Trade Count, Max DD

### Step 2: Prioritize Parameters
Read the `[params]` section and rank by expected impact:
1. **Entry filters** (highest impact) — volume thresholds, confidence scores, confirmation bars
2. **Exit rules** (risk management) — stop distances, stagnation timeouts, max loss
3. **Timing** (market structure) — entry windows, cooldowns
4. **Options** (position sizing) — delta range, DTE, spread limits

### Step 3: Autonomous Tuning Loop
```
for each parameter in priority order:
  1. Record current value
  2. Try TIGHTER value → run backtest → compare to baseline
  3. Try LOOSER value → run backtest → compare to baseline
  4. Keep whichever improved; revert if both degraded
  5. If meaningful improvement → STOP, report to user, ask continue/stop
  6. If no improvement → move to next parameter
```

**Meaningful improvement** = any of:
- PF improved ≥ 0.1 without trade count dropping > 20%
- Max DD decreased ≥ 2% without PF dropping
- Net P&L improved ≥ 10% with PF ≥ 1.2
- Sharpe improved ≥ 0.2

### Step 4: Report & Checkpoint
Always include the backtest context (symbols and date range) at the top, then present the comparison table:
```
**Backtest Universe:** {N} symbols — {list or "full 73-symbol universe"}
**Data Range:** {from} → {to} ({N months})
**Timeframe:** {tf} | **Initial Equity:** ${equity} | **Slippage:** {slippage} bps

| Metric        | Baseline | Current | Delta   |
|---------------|----------|---------|---------|
| Profit Factor |          |         |         |
| Win Rate      |          |         |         |
| Net P&L       |          |         |         |
| Trade Count   |          |         |         |
| Max Drawdown  |          |         |         |
```
List parameter changes made, then ask:
> "Meaningful improvement found. Continue tuning or accept these results?"

### Step 5: On User Decision
- **Continue** → resume loop from next parameter
- **Stop** → keep the improved DNA, suggest a commit message

## Backtest API

### Symbol Universe
ALWAYS use the FULL universe of 73 liquid US equities from `domain.KnownSymbols()`. Using a small subset (e.g. 12 symbols) risks overfitting to those specific tickers. The full list:

```
AAPL,ABBV,AFRM,AMD,AMZN,AVGO,BA,BAC,CAT,COIN,COST,CRM,CVX,DDOG,DE,DIA,F,FUBO,GM,GOOGL,GS,HIMS,HOOD,INTC,IWM,JNJ,JPM,LCID,LLY,MA,MARA,META,MRNA,MRVL,MSFT,MU,NET,NFLX,NIO,NVDA,ON,ORCL,OXY,PFE,PLTR,PYPL,QCOM,QQQ,RBLX,RIOT,RIVN,SLB,SMCI,SNOW,SOFI,SOXL,SPY,SQ,SQQQ,TGT,TQQQ,TSLA,U,UNH,UPS,UPST,V,WMT,XLE,XLF,XLK,XOM,ZS
```

If the list needs updating, check `backend/internal/domain/sector.go` → `KnownSymbols()`.

### Run
```bash
curl -s -X POST http://localhost:8080/backtest/run \
  -H "Content-Type: application/json" \
  -d '{
    "symbols": ["AAPL","ABBV","AFRM","AMD","AMZN","AVGO","BA","BAC","CAT","COIN","COST","CRM","CVX","DDOG","DE","DIA","F","FUBO","GM","GOOGL","GS","HIMS","HOOD","INTC","IWM","JNJ","JPM","LCID","LLY","MA","MARA","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NIO","NVDA","ON","ORCL","OXY","PFE","PLTR","PYPL","QCOM","QQQ","RBLX","RIOT","RIVN","SLB","SMCI","SNOW","SOFI","SOXL","SPY","SQ","SQQQ","TGT","TQQQ","TSLA","U","UNH","UPS","UPST","V","WMT","XLE","XLF","XLK","XOM","ZS"],
    "from": "2025-06-01",
    "to": "2026-03-27",
    "timeframe": "5m",
    "initial_equity": 100000,
    "slippage_bps": 10,
    "strategies": ["{strategy_id}"],
    "no_ai": true,
    "speed": "max",
    "max_positions": 6,
    "max_per_group": 2
  }'
```

- `max_positions`: caps simultaneous open positions (default 6, prevents over-exposure)
- `max_per_group`: caps positions per sector group (default 2, prevents sector concentration)

### Poll (every 15s, timeout 10min)
```bash
curl -s http://localhost:8080/backtest/{id}/status
```

### Results
```bash
curl -s http://localhost:8080/backtest/{id}/results
```

## Parameter Guides by Strategy

### AVWAP (`avwap_v4`)

#### Entry Filters
| Parameter | Range | Effect |
|-----------|-------|--------|
| `volume_mult` | 1.5–3.0 | Higher = fewer trades, higher quality |
| `min_confluence_score` | 5–8 | Higher = very selective entries |
| `hold_bars` | 3–8 | Stronger breakout confirmation |
| `min_slope_bps` | 0.1–1.0 | Strong trends only |

#### Exit Rules
| Parameter | Range | Effect |
|-----------|-------|--------|
| `stop_bps` | 50–150 | Tighter = less loss per trade |
| `avwap_stop_bars` | 1–3 | Faster AVWAP-based stop |
| `stagnation minutes` | 60–180 | Faster exit on stale trades |
| `max_loss pct` | 0.01–0.05 | Hard stop per trade |

#### Timing
| Parameter | Effect |
|-----------|--------|
| `allowed_hours_start/end` | Entry window (ET) |
| `midday_trap_shield` | 11:00–13:00 low-liquidity filter |
| `cooldown_seconds` | Re-entry cooldown per symbol |

### ORB Break & Retest (`orb_break_retest`)

#### Entry Filters
| Parameter | Range | Effect |
|-----------|-------|--------|
| `orb_window_minutes` | 5–30 | Shorter = tighter range, more breakouts |
| `min_rvol` | 1.0–3.0 | Higher = only high-volume sessions |
| `min_confidence` | 0.5–0.9 | Higher = more selective |
| `breakout_confirm_bps` | 3–15 | Bigger move needed to confirm |
| `touch_tolerance_bps` | 1–5 | How close to level counts as "touch" |
| `max_retest_bars` | 6–20 | Faster retest required |
| `retest_confirm_bars` | 1–4 | Bars to confirm retest hold |

#### Exit Rules
| Parameter | Range | Effect |
|-----------|-------|--------|
| `stop_bps` | 30–100 | Tighter risk per trade |
| `atr_multiplier` | 1.5–4.0 | Profit target as ATR multiple |
| `stagnation minutes` | 45–120 | Faster exit on stale trades |

#### Timing
| Parameter | Effect |
|-----------|--------|
| `allowed_hours_start/end` | Entry window (ET) |
| `max_signals_per_session` | Cap entries per day |

### Generic (unknown strategy)
For any new strategy, infer tuning ranges from:
- Current values: try ±20% as starting exploration
- Parameter names: `_bps` = basis points, `_mult` = multiplier, `_bars` = bar count
- Common sense: risk params → tighten first; filter params → loosen if trade count is too low

## Options Parameters (all strategies)
| Parameter | Range | Effect |
|-----------|-------|--------|
| `target_delta_low/high` | 0.30–0.75 | Closer to ATM = more expensive, tracks better |
| `min_dte/max_dte` | 7–60 | Shorter = higher gamma, faster decay |
| `max_spread_pct` | 0.05–0.15 | Liquidity filter |
| `max_contracts` | 1–10 | Position size cap |

## Overfitting Detection

Flag results as suspect if:
- Trade count < 30
- PF > 3.0
- WR > 60% AND PF > 2.0
- 3+ per-symbol overrides
- Backtest period < 6 months
- Sharp in-sample vs out-of-sample divergence

## Healthy Metric Ranges

| Metric | Minimum | Target | Red Flag |
|--------|---------|--------|----------|
| Profit Factor | 1.2 | 1.3–2.0 | > 3.0 (overfitting) |
| Win Rate | 30% | 35–50% | > 60% (suspicious) |
| Max Drawdown | — | < 10% | > 20% |
| Trade Count | 30+ | 50+ | < 20 |
| Sharpe Ratio | 0.5 | 1.0–2.0 | > 3.0 |
| Avg Win/Avg Loss | 1.5 | 2.0–3.0 | < 1.0 |

## References

- Strategy DNA files: `configs/strategies/*.toml`
- Backtest result interpretation: `backtest-analysis` skill
- Tuning history: `git log --oneline configs/strategies/`
