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

### Step 3: Multi-Pass Coordinate Descent
Run up to **3 passes** over all parameters. Multiple passes catch interaction effects (changing param A may open headroom for param B).

```
for pass = 1 to 3:
  improved_this_pass = false
  for each parameter in priority order:
    1. Record current value
    2. Try TIGHTER (current - step) → backtest → compare
    3. Try LOOSER (current + step) → backtest → compare
    4. Keep whichever clears the pass threshold; revert if both fail
    5. If accepted → run split-half validation (see below)
    6. If split-half fails → revert and move to next parameter
    7. If accepted and validated → improved_this_pass = true
  
  # Correlated pair mini-grids (after each pass)
  for each (param_a, param_b) in CORRELATED_PAIRS:
    Run 3x3 grid (current-step, current, current+step for each)
    Accept best combo if it clears pass threshold + split-half
  
  if NOT improved_this_pass → converged, stop
  else → report pass summary, ask user continue/stop
```

#### Pass-Specific Thresholds (~1.5x tighter per pass to combat overfitting)
| Pass | PF delta | DD delta | Net P&L delta | Sharpe delta |
|------|----------|----------|---------------|--------------|
| 1    | ≥ 0.10   | ≥ 2.0pp  | ≥ 10%         | ≥ 0.20       |
| 2    | ≥ 0.15   | ≥ 3.0pp  | ≥ 15%         | ≥ 0.30       |
| 3    | ≥ 0.20   | ≥ 4.0pp  | ≥ 20%         | ≥ 0.40       |

#### Hard Constraints (reject if ANY violated)
- Max drawdown worsened by > 1.5 percentage points
- Trade count below 40 (absolute floor)
- Trade count dropped > 20% relative to baseline

#### Step Sizes
Use **absolute, parameter-specific steps** from the parameter guides below. Do NOT use percentage-based steps. Do NOT halve steps on convergence — with 30-200 trades, finer resolution is noise.

#### Split-Half Validation
For each accepted parameter change:
1. Backtest on **first half** of date range
2. Backtest on **second half** of date range
3. Reject if improvement is negative in either half
Costs 2 extra API calls per accepted change but prevents regime-specific overfitting.

#### Correlated Pair Mini-Grids
After coordinate descent in each pass, run 3×3 grids for known interacting pairs:
- `stop_bps` × `atr_multiplier` (both control exit width)
- Entry score threshold × confidence threshold (both filter entries)
Only 9 backtests per pair. Define pairs per strategy in the parameter guides below.

### Step 4: Report & Checkpoint
Always include the backtest context at the top, then present the comparison table:
```
**Backtest Universe:** {N} symbols — {list or "full 73-symbol universe"}
**Data Range:** {from} → {to} ({N months})
**Timeframe:** {tf} | **Initial Equity:** ${equity} | **Slippage:** {slippage} bps
**Pass:** {pass_number}/3 | **Backtests Run:** {count}

| Metric        | Baseline | Current | Delta   |
|---------------|----------|---------|---------|
| Profit Factor |          |         |         |
| Win Rate      |          |         |         |
| Net P&L       |          |         |         |
| Trade Count   |          |         |         |
| Max Drawdown  |          |         |         |
| Sharpe        |          |         |         |
```

#### Parameter Changes
| Parameter | Before | After | Step | Rationale |
|-----------|--------|-------|------|-----------|
| ...       | ...    | ...   | ...  | ...       |

#### Code Fixes Applied (if any)
- `file:line` — description of fix

Then ask the user:
> "Pass {n} complete. {k} parameters improved. Continue to pass {n+1} or accept these results?"

### Step 5: On User Decision
- **Continue** → start next pass from top priority
- **Stop** → keep the improved DNA, suggest a commit message

## Backtest API

### Symbol Universe
Use the **active trading universe of 34 symbols** — these are the symbols configured in live strategies. Backtesting on symbols you don't trade dilutes signal and wastes time.

```
AAPL,AFRM,AMD,AMZN,AVGO,BA,COIN,CRM,GOOGL,HIMS,HOOD,IWM,JPM,LLY,META,MRNA,MRVL,MSFT,MU,NET,NFLX,NVDA,OXY,PLTR,QQQ,RBLX,RIVN,SMCI,SNOW,SOFI,SOXL,SPY,TSLA,XOM
```

If the list needs updating, check `configs/strategies/*.toml` → `[routing] symbols`.

### Run
```bash
curl -s -X POST http://localhost:8080/backtest/run \
  -H "Content-Type: application/json" \
  -d '{
    "symbols": ["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"],
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

### Poll (every 60s, timeout 10min)
```bash
curl -s http://localhost:8080/backtest/{id}/status
# Returns: {"status": "running|completed|failed", "progress": {"pct": 50}}
```
**Report progress to the user on each poll:**
> "Backtest {id}: {pct}% complete ({elapsed}s elapsed)"
Suppress if pct hasn't changed since last poll.

### Results
```bash
curl -s http://localhost:8080/backtest/{id}/results
```

## Parameter Guides by Strategy

### AVWAP (`avwap_v4`)

#### Entry Filters
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `volume_mult` | 1.5–3.0 | 0.25 | Higher = fewer trades, higher quality |
| `min_confluence_score` | 5–8 | 1 | Higher = very selective entries |
| `hold_bars` | 3–8 | 1 | Stronger breakout confirmation |
| `min_slope_bps` | 0.1–1.0 | 0.1 | Strong trends only |

#### Exit Rules
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `stop_bps` | 50–150 | 10 | Tighter = less loss per trade |
| `avwap_stop_bars` | 1–3 | 1 | Faster AVWAP-based stop |
| `stagnation minutes` | 60–180 | 15 | Faster exit on stale trades |
| `max_loss pct` | 0.01–0.05 | 0.005 | Hard stop per trade |

#### Timing
| Parameter | Effect |
|-----------|--------|
| `allowed_hours_start/end` | Entry window (ET) |
| `midday_trap_shield` | 11:00–13:00 low-liquidity filter |
| `cooldown_seconds` | Re-entry cooldown per symbol |

**Correlated pairs for mini-grid:** `stop_bps` × `min_slope_bps`

### ORB Break & Retest (`orb_break_retest`)

#### Entry Filters
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `orb_window_minutes` | 5–30 | 5 | Shorter = tighter range, more breakouts |
| `min_rvol` | 1.0–3.0 | 0.25 | Higher = only high-volume sessions |
| `min_confidence` | 0.5–0.9 | 0.05 | Higher = more selective |
| `breakout_confirm_bps` | 3–15 | 2 | Bigger move needed to confirm |
| `touch_tolerance_bps` | 1–5 | 1 | How close to level counts as "touch" |
| `max_retest_bars` | 6–20 | 2 | Faster retest required |
| `retest_confirm_bars` | 1–4 | 1 | Bars to confirm retest hold |

#### Exit Rules
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `stop_bps` | 30–100 | 5 | Tighter risk per trade |
| `atr_multiplier` | 1.5–4.0 | 0.25 | Profit target as ATR multiple |
| `stagnation minutes` | 45–120 | 15 | Faster exit on stale trades |

#### Timing
| Parameter | Effect |
|-----------|--------|
| `allowed_hours_start/end` | Entry window (ET) |
| `max_signals_per_session` | Cap entries per day |

**Correlated pairs for mini-grid:** `stop_bps` × `atr_multiplier`, `min_confidence` × `min_rvol`

### Generic (unknown strategy)
For any new strategy, infer tuning ranges from:
- Current values: try ±20% as starting exploration
- Parameter names: `_bps` = basis points, `_mult` = multiplier, `_bars` = bar count
- Common sense: risk params → tighten first; filter params → loosen if trade count is too low

## Options Parameters (all strategies)
| Parameter | Range | Effect |
|-----------|-------|--------|
| `target_delta_low/high` | 0.30–0.55 | ATM (0.40–0.55) preferred for fill rate + liquidity |
| `min_dte/max_dte` | 7–60 | Shorter = higher gamma, faster decay |
| `max_spread_pct` | 0.05–0.15 | Liquidity filter |
| `max_contracts` | 1–10 | Position size cap |

### Fill-Friendliness Constraint
When tuning options parameters, **fill probability is a first-class metric**.
A signal that never fills is worth zero. Prioritize contracts that are:
- **ATM (delta 0.40–0.55)**: tightest bid-ask spreads, highest open interest
- **Adequate open interest (≥50-100)**: ensures market maker participation
- **Spread ≤ 8-10% of premium**: reject illiquid strikes
- **Avoid deep ITM (delta > 0.60)**: wide spreads, low volume, stale quotes

When evaluating tuning results, discount strategies that select ITM contracts
even if backtested P&L looks better — backtests assume fills at mid price,
but live ITM orders often sit unfilled. ATM gamma actually helps day trades
by accelerating gains on winning directional moves.

## Code Fix Policy
During tuning, you may encounter bugs or issues in strategy code, backtest runner, or signal engine:

- **Small fix** (< ~20 lines, one file, obvious bug): Fix autonomously. Report under "Code Fixes Applied" in checkpoint.
- **Big change** (new feature, multi-file refactor, behavior change): STOP and describe to user. Wait for approval.
- **Ambiguous**: Ask the user.

## Overfitting Detection

Flag results as suspect if:
- Trade count < 40 (absolute floor — below this, all metrics are noise)
- PF > 3.0
- WR > 60% AND PF > 2.0
- 3+ per-symbol overrides
- Backtest period < 6 months
- Sharp in-sample vs out-of-sample divergence (split-half check fails)
- Improvement only appears in one half of the data range

## Healthy Metric Ranges

| Metric | Minimum | Target | Red Flag |
|--------|---------|--------|----------|
| Profit Factor | 1.2 | 1.3–2.0 | > 3.0 (overfitting) |
| Win Rate | 30% | 35–50% | > 60% (suspicious) |
| Max Drawdown | — | < 10% | > 20% |
| Trade Count | 40+ | 60+ | < 30 |
| Sharpe Ratio | 0.5 | 1.0–2.0 | > 3.0 |
| Avg Win/Avg Loss | 1.5 | 2.0–3.0 | < 1.0 |

## References

- Strategy DNA files: `configs/strategies/*.toml`
- Backtest result interpretation: `backtest-analysis` skill
- Tuning history: `git log --oneline configs/strategies/`
