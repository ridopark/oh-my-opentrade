---
name: strategy-tuning
description: "Multi-strategy DNA parameter tuning with automated backtest loop. Works with any strategy (AVWAP, ORB, etc.) following schema_version = 2. Triggers on 'tune', 'tweak', 'optimize', 'parameter', 'DNA', 'TOML config' keywords. Does NOT trigger for simply reading or explaining a TOML file."
---

# Strategy Tuning Workflow

Autonomous tune→backtest→evaluate loop for oh-my-opentrade strategy DNA (schema_version = 2).

## Critical Rules

1. **Never read the full results JSON.** Always pipe through `jq` to exclude the trades array — it can be 500KB+ and will blow up your context:
   ```bash
   curl -s http://localhost:8080/backtest/{id}/results | jq '{
     initial_equity, final_equity, total_return_pct, total_pnl,
     trade_count, win_count, loss_count, win_rate_pct,
     max_drawdown_pct, sharpe_ratio, profit_factor,
     avg_win, avg_loss, largest_win, largest_loss
   }'
   ```
2. **Use caller's backtest params.** If the user specifies symbols, date range, timeframe, equity, slippage, max_positions, or max_per_group — use those exact values for every backtest. Only fall back to defaults below when the caller doesn't specify.
3. **Skip baseline if provided.** If the caller gives baseline metrics (PF, WR, DD, etc.), record them and go straight to tuning. Don't waste a backtest re-running what's already known.
4. **Run variants in parallel.** When testing TIGHTER vs LOOSER for a parameter, run both backtests simultaneously (edit config → run → revert → edit other direction → run). Compare both when done.
5. **Report on every poll.** When polling a running backtest, always print the progress to the user so they can see activity.

## Strategy Discovery

DNA files: `configs/strategies/{id}.toml`

```bash
ls configs/strategies/*.toml
```

| Strategy ID | Engine | Description |
|-------------|--------|-------------|
| `avwap_v4` | `avwap` | Anchored VWAP confluence-weighted entries |
| `orb_break_retest` | `orb_break_retest` | Opening Range Breakout — break & retest |
| *(new)* | *(read hooks.signals.name)* | Any future schema_version = 2 strategy |

## DNA File Structure

```toml
schema_version = 2
[strategy]     # id, version, name, description
[lifecycle]    # state, paper_only
[routing]      # symbols, timeframes, priority, asset_classes
[params]       # algorithm parameters — THIS IS WHAT YOU TUNE
[hooks]        # signal engine binding
[dynamic_risk] # dynamic risk adjustment
[[exit_rules]] # array: TIERED_TP, TIME_PARTIAL, STAGNATION_EXIT, EOD_FLATTEN, MAX_LOSS, DTE_FLOOR
[options]      # options trading config
```

## Backtest API

### Default Params (use only when caller doesn't specify)
```json
{
  "symbols": ["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"],
  "from": "$(date -d '1 year ago' +%Y-%m-%d)",
  "to": "$(date +%Y-%m-%d)",
  "timeframe": "5m",
  "initial_equity": 100000,
  "slippage_bps": 10,
  "no_ai": true,
  "speed": "max",
  "max_positions": 6,
  "max_per_group": 2
}
```

### Run Backtest
```bash
curl -s -X POST http://localhost:8080/backtest/run \
  -H "Content-Type: application/json" \
  -d '{ ...params..., "strategies": ["{strategy_id}"] }'
# Returns: {"backtest_id": "bt-xxx", "status": "pending"}
```

### Poll Status (every 30s, timeout 10min)
```bash
curl -s http://localhost:8080/backtest/{id}/status
# Returns: {"status": "running|completed|failed", "progress": {"pct": 50, "bars_processed": N, "total_bars": M}}
```
Always report to user: `"Backtest {id}: {pct}% complete"`. Suppress if pct unchanged.

### Get Results (ALWAYS use jq filter)
```bash
curl -s http://localhost:8080/backtest/{id}/results | jq '{
  initial_equity, final_equity, total_return_pct, total_pnl,
  trade_count, win_count, loss_count, win_rate_pct,
  max_drawdown_pct, sharpe_ratio, profit_factor,
  avg_win, avg_loss, largest_win, largest_loss
}'
```

## Quant Analysis After Each Backtest

After every backtest result is collected, **launch the `quant-analyst` agent** to analyze it. This is mandatory — never skip.

### When to invoke
- After the baseline backtest completes
- After each parameter variant backtest completes (before deciding accept/reject)
- At each pass checkpoint (summarize the full pass)

### How to invoke
Use the Agent tool with `subagent_type: "quant-analyst"`. Provide:
1. The strategy name and current config summary (key params only)
2. The backtest metrics (PF, WR, DD, trade count, avg win, avg loss)
3. What parameter was changed and in which direction
4. The baseline metrics for comparison
5. Ask for: accept/reject recommendation, next parameter suggestion, structural concerns

### What to do with the analysis
- If the quant recommends **reject** and you were about to accept → reconsider
- If the quant identifies a **structural concern** → flag to user before continuing
- If the quant suggests a **parameter not in your queue** → add it to the queue
- The quant's recommendations are advisory — the pass thresholds are the final arbiter

## Tuning Workflow

### Step 1: Setup
1. Read target DNA: `configs/strategies/{strategy_id}.toml`
2. Back up: `cp configs/strategies/{id}.toml configs/backups/{id}_pre_tune_$(date +%Y%m%d_%H%M).toml`
3. If baseline metrics provided by caller → record and skip to Step 2
4. Otherwise → run baseline backtest, poll, record metrics

### Step 2: Prioritize Parameters
Read `[params]` and `[[exit_rules]]` sections. Rank by expected impact on the **specific problem** the caller identified (e.g., "avg loss too high" → focus exits first):

1. **Exit rules** (cut losers) — stop_bps, max_loss pct, stagnation minutes, trail_pct
2. **Entry filters** (fewer bad trades) — min_rvol, min_confidence, breakout_confirm_bps, max_retest_bars
3. **Profit targets** (let winners run) — first_tier_rr, atr_multiplier, time_partial minutes
4. **Timing** — allowed_hours, max_signals_per_session
5. **Options** — delta range, DTE, spread limits (tune last, risky to overfit)

### Step 3: Multi-Pass Coordinate Descent

Run up to **3 passes**. Each pass sweeps all priority parameters.

```
for pass = 1 to 3:
  improved_this_pass = false
  for each parameter in priority order:
    1. Save current config state
    2. Edit TOML → try TIGHTER value → run backtest A
    3. While A runs: revert TOML → edit → try LOOSER value → run backtest B
    4. Poll both A and B (alternate, 30s each)
    5. When both complete: compare A, B, and current-best
    6. Accept whichever clears the pass threshold; revert if both fail
    7. If accepted → run split-half validation
    8. If split-half fails → revert, move to next parameter
    9. If validated → improved_this_pass = true, update current-best

  # Correlated pair mini-grids (after sweep)
  for each (param_a, param_b) in CORRELATED_PAIRS:
    Run 3x3 grid: 9 backtests (can run sequentially — they're fast at max speed)
    Accept best combo if it clears threshold + split-half

  if NOT improved_this_pass → converged, stop
  else → report pass summary to user, ask continue/stop
```

#### Pass Thresholds (tighter each pass to prevent overfitting)
| Pass | PF delta | DD delta | Net P&L delta | Sharpe delta |
|------|----------|----------|---------------|--------------|
| 1    | >= 0.10  | >= 2.0pp | >= 10%        | >= 0.20      |
| 2    | >= 0.15  | >= 3.0pp | >= 15%        | >= 0.30      |
| 3    | >= 0.20  | >= 4.0pp | >= 20%        | >= 0.40      |

A parameter change is **accepted** if it improves ANY threshold metric without violating hard constraints.

#### Hard Constraints (reject if ANY violated)
- Max drawdown worsened by > 1.5 percentage points vs baseline
- Trade count below 40 (absolute floor)
- Trade count dropped > 20% vs baseline

#### Step Sizes
Use **absolute, parameter-specific steps** from the Parameter Guides below. Do NOT use percentage-based steps. Do NOT halve steps — with 30-200 trades, finer resolution is noise.

#### Split-Half Validation
For each accepted change:
1. Backtest first half of date range
2. Backtest second half of date range
3. **Reject if improvement is negative in either half**
This catches regime-specific overfitting. Costs 2 extra backtests per accepted change.

#### Correlated Pair Mini-Grids
Run 3x3 grids for known interacting pairs (defined per strategy below).
Only 9 backtests per pair. Accept best combo if it clears threshold + split-half.

### Step 4: Report & Checkpoint

Present this format after each pass:

```
**Backtest Context:** {N} symbols | {from} → {to} | {tf} timeframe
**Equity:** ${equity} | **Slippage:** {slippage} bps | **Pass:** {n}/3
**Backtests Run This Pass:** {count} | **Total:** {cumulative}

| Metric        | Baseline | Current | Delta   |
|---------------|----------|---------|---------|
| Profit Factor |          |         |         |
| Win Rate      |          |         |         |
| Net P&L       |          |         |         |
| Trade Count   |          |         |         |
| Max Drawdown  |          |         |         |
| Sharpe        |          |         |         |
| Avg Win       |          |         |         |
| Avg Loss      |          |         |         |

### Parameter Changes
| Parameter | Before | After | Rationale |
|-----------|--------|-------|-----------|

### Code Fixes (if any)
- `file:line` — description
```

Then ask: `"Pass {n} complete. {k} params improved. Continue to pass {n+1}?"`

### Step 5: On User Decision
- **Continue** → next pass
- **Stop** → keep improved DNA, suggest commit message

## Parameter Guides

### ORB Break & Retest (`orb_break_retest`)

#### Exit Rules (tune first when R:R is bad)
| Parameter | Location | Range | Step | Effect |
|-----------|----------|-------|------|--------|
| `stop_bps` | `[params]` | 30–100 | 5 | Tighter stop = smaller avg loss |
| `max_loss pct` | `[[exit_rules]] type="MAX_LOSS"` | 0.005–0.025 | 0.005 | Hard loss cap per trade |
| `trail_pct` | `[[exit_rules]] type="TIERED_TP"` | 0.005–0.025 | 0.005 | Tighter trail = lock profits faster |
| `first_tier_rr` | `[[exit_rules]] type="TIERED_TP"` | 1.0–3.0 | 0.5 | Higher = let first tier run further |
| `stagnation minutes` | `[[exit_rules]] type="STAGNATION_EXIT"` | 45–120 | 15 | Faster exit on flat trades |
| `stop_buffer_bps` | `[params]` | 5–20 | 5 | Buffer on swing stop |

#### Entry Filters
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `orb_window_minutes` | 5–30 | 5 | Shorter = tighter range, more signals |
| `min_rvol` | 0.0–3.0 | 0.5 | Higher = only high-volume sessions |
| `min_confidence` | 0.0–0.9 | 0.1 | Higher = more selective |
| `breakout_confirm_bps` | 2–15 | 2 | Bigger move to confirm |
| `touch_tolerance_bps` | 10–50 | 10 | How close to ORB level = "touch" |
| `max_retest_bars` | 6–25 | 3 | Faster retest required |
| `retest_confirm_bars` | 1–4 | 1 | Bars confirming retest hold |
| `displacement_min_body_pct` | 0.3–0.7 | 0.1 | Displacement candle quality |
| `displacement_min_range_pct` | 0.001–0.005 | 0.001 | Displacement candle size |

#### Timing
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `allowed_hours_end` | "12:00"–"15:00" | 1h | Narrower = avoid afternoon chop |
| `max_signals_per_session` | 1–3 | 1 | More = more attempts per day |

**Correlated pairs:** `stop_bps` x `atr_multiplier`, `min_confidence` x `min_rvol`

### AVWAP (`avwap_v4`)

#### Entry Filters
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `volume_mult` | 1.5–3.0 | 0.25 | Higher = fewer, higher-quality trades |
| `min_confluence_score` | 5–8 | 1 | Higher = very selective |
| `hold_bars` | 3–8 | 1 | Stronger breakout confirmation |
| `min_slope_bps` | 0.1–1.0 | 0.1 | Strong trends only |

#### Exit Rules
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `stop_bps` | 50–150 | 10 | Tighter = less loss per trade |
| `avwap_stop_bars` | 1–3 | 1 | Faster AVWAP-based stop |
| `stagnation minutes` | 60–180 | 15 | Faster exit on stale trades |
| `max_loss pct` | 0.01–0.05 | 0.005 | Hard stop per trade |

**Correlated pairs:** `stop_bps` x `min_slope_bps`

### Generic (unknown strategy)
Infer ranges from current values: try +/- 20%. Parameter naming conventions:
- `_bps` = basis points, `_mult` = multiplier, `_bars` = bar count, `_pct` = percentage

## Options Parameters (all strategies)
| Parameter | Range | Effect |
|-----------|-------|--------|
| `target_delta_low/high` | 0.30–0.55 | ATM (0.40–0.55) for fill rate + liquidity |
| `min_dte/max_dte` | 5–60 | Shorter = higher gamma, faster decay |
| `max_spread_pct` | 0.05–0.15 | Liquidity filter |

### Fill-Friendliness Constraint
Fill probability is a first-class metric. Prioritize ATM (delta 0.40–0.55) with adequate OI (>=100) and spread <= 10% of premium. Discount backtested results that select deep ITM (delta > 0.60) — live fills are much worse than backtest assumes.

## Overfitting Detection

Flag as suspect if:
- Trade count < 40
- PF > 3.0
- WR > 60% AND PF > 2.0
- Backtest period < 6 months
- Split-half divergence (improvement in one half, regression in other)

## Healthy Metric Ranges

| Metric | Minimum | Target | Red Flag |
|--------|---------|--------|----------|
| Profit Factor | 1.2 | 1.3–2.0 | > 3.0 |
| Win Rate | 30% | 35–50% | > 60% |
| Max Drawdown | — | < 10% | > 20% |
| Trade Count | 40+ | 60+ | < 30 |
| Sharpe Ratio | 0.5 | 1.0–2.0 | > 3.0 |
| Avg Win/Avg Loss | 1.5 | 2.0–3.0 | < 1.0 |

## Code Fix Policy
- **Small fix** (< 20 lines, one file, obvious bug): Fix autonomously, report in checkpoint.
- **Big change** (new feature, multi-file refactor): STOP, describe to user, wait for approval.
