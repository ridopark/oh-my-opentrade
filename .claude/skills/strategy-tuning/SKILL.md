---
name: strategy-tuning
description: "Multi-strategy DNA parameter tuning with automated backtest loop. Works with any strategy (AVWAP, ORB, etc.) following schema_version = 2. Triggers on 'tune', 'tweak', 'optimize', 'parameter', 'DNA', 'TOML config' keywords. Does NOT trigger for simply reading or explaining a TOML file."
---

# Strategy Tuning Workflow

Autonomous tune→backtest→evaluate loop for oh-my-opentrade strategy DNA (schema_version = 2).

## Critical Rules

1. **Save results to files, extract metrics with python.** Save full results to `_workspace/` for quant analysis. Extract summary metrics with python (not jq):
   ```bash
   curl -s http://localhost:8080/backtest/$BT_ID/results > _workspace/{strategy}_{variant}.json
   python3 -c "
   import json
   d = json.load(open('_workspace/{strategy}_{variant}.json'))
   print(f'PF: {d[\"profit_factor\"]:.3f} | WR: {d[\"win_rate_pct\"]:.1f}% | P&L: \${d[\"total_pnl\"]:.0f} | Trades: {d[\"trade_count\"]} | DD: {d[\"max_drawdown_pct\"]:.2f}%')
   "
   ```
2. **Consult quant-analyst after EVERY backtest.** Launch the `quant-analyst` agent to analyze results. Especially critical for the baseline — get structural analysis BEFORE parameter sweeping. Provide the saved results file path so the quant can analyze trade-level data.
3. **Use caller's backtest params.** If the user specifies symbols, date range, timeframe, equity, slippage, max_positions, or max_per_group — use those exact values for every backtest. Only fall back to defaults below when the caller doesn't specify.
4. **Skip baseline if provided.** If the caller gives baseline metrics (PF, WR, DD, etc.), record them and go straight to tuning. Don't waste a backtest re-running what's already known.
5. **Poll every 10 seconds.** At max speed, backtests finish in 60-90 seconds for 7-month / 34-symbol runs. Use 10s poll intervals, not 30s.
6. **Send Discord notifications.** Every significant event must be posted to Discord. See the Discord Notifications section below.
7. **TOML inline tables only.** Never use `[params.subtable]` syntax — it terminates the `[params]` section. Use inline syntax: `param = { key = "value" }`.
8. **Structural before parametric.** Always try timing window and regime filters before entry quality params. The order matters due to slot backfill effects.

## Discord Notifications

Post updates to the Discord webhook for every significant event. This keeps the team informed without needing to watch the terminal.

**Webhook URL:** Read from `references/discord_webhook.txt` (one line, no trailing newline).

**When to send:**
1. **Tuning started** — strategy name, date range, baseline metrics
2. **Each backtest result** — param changed, direction, PF/WR/DD/trades, accept/reject verdict
3. **Engine change started** — what the quant recommended, what go-architect is implementing
4. **Engine change result** — build/test status, backtest result, accept/revert
5. **Pass checkpoint** — full comparison table (baseline vs current best)
6. **Tuning complete** — final metrics, all accepted changes, commit hash

**How to send — use a bash curl with Discord embed:**

```bash
curl -s -X POST "$DISCORD_WEBHOOK" \
  -H "Content-Type: application/json" \
  -d '{
    "embeds": [{
      "title": "TITLE_HERE",
      "description": "BODY_HERE",
      "color": COLOR_INT,
      "fields": [
        {"name": "Field1", "value": "val1", "inline": true},
        {"name": "Field2", "value": "val2", "inline": true}
      ],
      "footer": {"text": "oh-my-opentrade strategy tuner"}
    }]
  }'
```

**Color codes:** `3066993` (green = improvement/accept), `15158332` (red = regression/reject), `3447003` (blue = info/started), `16776960` (yellow = engine change in progress).

**Message templates by event type:**

### 1. Tuning Started
- Title: `🔧 Tuning Started: {strategy_id}`
- Color: blue
- Fields: Symbols (count), Date Range, Timeframe, Baseline PF, Baseline DD, Baseline Trades

### 2. Backtest Result
- Title: `📊 {param}={value} → PF {pf}` or `📊 Baseline: PF {pf}`
- Color: green if accepted, red if rejected
- Description: `{param} changed {old}→{new} | Verdict: ✅ ACCEPTED` or `❌ REJECTED`
- Fields: PF, Win Rate, Max DD, Trades, Avg Win, Avg Loss (all inline)

### 3. Engine Change
- Title: `⚙️ Engine Change: {name}` (started) or `⚙️ Engine Change Result: {name}`
- Color: yellow (started), green/red (result)
- Description: implementation details, build/test status, backtest comparison

### 4. Pass Checkpoint
- Title: `📋 Pass {n} Complete — PF {baseline}→{current}`
- Color: green if improved, red if no improvement
- Description: Markdown table of all metrics (baseline vs current vs delta)
- Fields: Params Changed (list), Backtests Run, Engine Changes (if any)

### 5. Tuning Complete
- Title: `✅ Tuning Complete: {strategy_id}` or `⛔ Tuning Converged (no more improvement)`
- Color: green
- Description: Final summary with all accepted changes
- Fields: Final PF, Final DD, Total Backtests Run, Commit hash (if committed)

**Important:** Read the webhook URL from the file at the start of each tuning session:
```bash
DISCORD_WEBHOOK=$(cat .claude/skills/strategy-tuning/references/discord_webhook.txt)
```

Keep message bodies under 2000 characters (Discord limit). Use `\n` for newlines in the JSON description field. Escape special JSON characters in metric values.

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

### Poll Status (every 10s, timeout 10min)
```bash
curl -s http://localhost:8080/backtest/{id}/status
# Returns: {"status": "running|completed|failed", "progress": {"pct": 50, "bars_processed": N, "total_bars": M}}
```
At max speed, 7-month / 34-symbol backtests finish in ~60-90 seconds. Use 10s poll intervals.

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
5. Ask the quant to classify each recommendation as either:
   - **PARAM_CHANGE** — a TOML parameter tweak (handle in the normal tuning loop)
   - **ENGINE_CHANGE** — requires Go code modification (triggers the Engine Change Pipeline below)
6. Ask for: accept/reject recommendation, next parameter suggestion, structural concerns

### What to do with the analysis
- If the quant recommends **reject** and you were about to accept → reconsider
- If the quant identifies a **structural concern** → flag to user before continuing
- If the quant suggests a **parameter not in your queue** → add it to the queue
- If the quant recommends an **ENGINE_CHANGE** → execute the Engine Change Pipeline (below)
- The quant's recommendations are advisory — the pass thresholds are the final arbiter

## Engine Change Pipeline

When parameter tuning converges (no pass improves) but the quant identifies **ENGINE_CHANGE** recommendations (new filters, new exit rules, signal engine modifications), this pipeline automatically implements, verifies, and validates them.

### When to trigger
- A tuning pass converges with no improvement AND the quant has pending ENGINE_CHANGE recommendations
- The quant explicitly recommends a code-level change (e.g., "add retest quality filter", "implement ADR exhaustion filter")
- Multiple consecutive parameter tweaks show no PF improvement, suggesting the parameter ceiling is reached

### Pipeline Steps

```
for each ENGINE_CHANGE recommendation (ranked by quant's expected impact):
  1. SPEC     — Write a spec file to _workspace/
  2. IMPLEMENT — Launch go-architect agent to implement
  3. BUILD    — go build ./... (must pass)
  4. TEST     — go test ./internal/... (must pass)
  5. WIRE     — Add new TOML params if needed (with disabled-by-default defaults)
  6. BACKTEST — Run full backtest with the new feature ENABLED
  7. COMPARE  — Compare to current best metrics
  8. DECIDE   — Accept (keep code + enable param) or Revert (git checkout the changed files)
```

### Step 1: SPEC — Write the change specification

Create `_workspace/engine_change_{name}.md` with:
```markdown
## Change: {name}
**Type:** new_filter | new_exit_rule | signal_modification | other
**Quant Rationale:** {why this change is expected to improve PF}
**Expected Impact:** {e.g., "win rate +2-3pp by filtering weak retests"}

### Implementation Requirements
- Files to modify: {list}
- New TOML params: {param_name} = {default_value} (disabled by default)
- Architecture layer: {domain | app | adapter}
- New structs/functions needed: {list}

### Acceptance Criteria
- Build passes: go build ./...
- Tests pass: go test ./internal/...
- Backtest PF improves by >= 0.02 (or DD improves by >= 1pp)
- Trade count does not drop > 20%
```

### Step 2: IMPLEMENT — Launch go-architect

Use the Agent tool with `subagent_type: "go-architect"` and `model: "opus"`. Provide:
- The spec from Step 1
- The current strategy TOML for context
- Explicit instruction to follow hexagonal architecture (go-hexagonal skill patterns)
- Instruction to add new TOML params with defaults that DISABLE the feature (so existing behavior is preserved)

Example prompt structure:
```
Implement {change_name} in the ORB strategy engine.

## Spec
{paste spec content}

## Architecture Rules
- Follow hexagonal architecture: domain types in domain/, logic in app/, no domain→app imports
- New config params go in ORBConfig struct in orb_tracker.go
- Wire params in NewORBConfigFromDNA with default 0 or false (disabled)
- Add the filter/logic at the appropriate point in the signal generation flow

## Files Context
- ORB signal engine: backend/internal/app/monitor/orb_tracker.go
- Exit rules: backend/internal/domain/exit_rule.go + backend/internal/app/positionmonitor/evaluators.go
- Strategy service: backend/internal/app/strategy/service.go
- Setup detector: backend/internal/app/monitor/setup_detector.go

## Verification
After implementation, run:
- go build ./...
- go test ./internal/...
Both must pass.
```

### Step 3: BUILD — Verify compilation

```bash
cd backend && go build ./... 2>&1
```
If build fails → send error back to go-architect agent via SendMessage for fix (max 2 retries). If still failing after retries → revert all changes and skip this engine change.

### Step 4: TEST — Run test suite

```bash
cd backend && go test ./internal/... 2>&1
```
If tests fail → send failures to go-architect for fix (max 1 retry). If still failing → revert and skip.

### Step 5: WIRE — Enable the new feature in TOML

Edit `configs/strategies/{strategy_id}.toml` to set the new param to its **enabled** value. Example:
```toml
# Was: retest_quality_min_body_hold = 0 (disabled)
retest_quality_min_body_hold = 0.50  # Enable: require 50% body hold on retest
```

### Step 6: BACKTEST — Validate with full backtest

Run the standard backtest (same params as the tuning loop — symbols, dates, equity, slippage). Poll and report progress as usual.

### Step 7: COMPARE — Check against current best

Compare the new backtest results to the current best metrics. The engine change is accepted if:
- PF improved by >= 0.02, OR
- Max DD improved by >= 1.0 percentage point, OR
- Net P&L improved by >= 5%
AND none of the hard constraints are violated (DD worsened > 1.5pp, trade count < 40, trade count dropped > 20%).

### Step 8: DECIDE — Accept or Revert

**Accept:** Keep the code changes and the enabled TOML param. Update the "current best" metrics. Log the change in the pass checkpoint report under "Engine Changes Applied".

**Revert:** Revert TOML param to disabled (default). Revert Go code changes:
```bash
git checkout -- backend/internal/app/monitor/orb_tracker.go  # etc.
```
Log in the report: "Engine change {name} reverted — PF {before} → {after} (no improvement)".

### After all engine changes are processed

If any engine changes were accepted, re-enter the parameter tuning loop (Step 3 of the main workflow) because the new code may have opened new parameter optimization headroom. Start from Pass 1 with the updated baseline.

### Engine Change Examples

**Retest Quality Filter:**
- Type: new_filter
- TOML param: `retest_quality_min_body_hold = 0.50`
- Implementation: In onAwaitingRetestBar, after detecting touch, check that the retest candle holds above 50% of the breakout candle's body
- Files: orb_tracker.go (ORBConfig + filter logic)

**ADR Exhaustion Filter:**
- Type: new_filter  
- TOML param: `adr_exhaustion_pct = 0.60`
- Implementation: Before emitting signal, compute how much of the 20-day ADR has been consumed. Skip if > 60%.
- Files: orb_tracker.go (ORBConfig + filter in onRangeSetBar), may need daily ATR data in IndicatorSnapshot

**New Exit Rule (e.g., R-Multiple Stop):**
- Type: new_exit_rule
- TOML param: `[[exit_rules]] type = "R_MULTIPLE_STOP"` with params
- Implementation: New evaluator function in evaluators.go, new type in exit_rule.go, dispatch case in Evaluate()
- Files: exit_rule.go, evaluators.go, spec_loader.go (isKnownExitRuleParamKey)

## Tuning Workflow

### Step 1: Setup
1. Read target DNA: `configs/strategies/{strategy_id}.toml`
2. Back up: `cp configs/strategies/{id}.toml configs/backups/{id}_pre_tune_$(date +%Y%m%d_%H%M).toml`
3. If baseline metrics provided by caller → record and skip to Step 2
4. Otherwise → run baseline backtest, poll, record metrics

### Step 1b: Quant Baseline Analysis (MANDATORY)
After baseline, **launch quant-analyst agent** with the saved results file. Ask for:
- Breakdown by regime, direction, time of day, symbol
- Biggest P&L bleeders
- Recommendations classified as PARAM_CHANGE or ENGINE_CHANGE
- Priority ordering

**Never skip this.** Blind parameter sweeping misses structural issues.

### Step 2: Prioritize Parameters
Read `[params]` and `[[exit_rules]]` sections. **Revised priority order** (learned from AVWAP v4 tuning — structural filters have more impact than entry quality):

1. **Engine fixes** (highest impact) — bugs in evaluators, signal flow, bar counting
2. **Structural filters** — allowed_hours_end, allow_regimes, regime_blocked_directions
3. **Entry quality** — min_confluence_score, min_rvol, min_confidence, volume_mult
4. **Exit rules** — stop_bps, max_loss pct, stagnation minutes, trail_pct
5. **Profit targets** — first_tier_rr, atr_multiplier, time_partial minutes
6. **Options** — delta range, DTE, spread limits (tune last, risky to overfit)

**Key insight:** Timing window and regime gating consistently outperform entry quality tuning. Always try structural filters first. Entry quality params like confluence score interact with time window due to **slot backfill** — narrow the window FIRST.

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
- Trade count below 200 (absolute floor for statistical significance)
- Trade count dropped > 30% vs baseline

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

#### Structural Filters (tune FIRST — highest impact)
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `allowed_hours_end` | "10:00"–"15:45" | 30min | **Highest impact.** Morning-only (11:00) dramatically improves quality |
| `allow_regimes` | subset of TREND_UP/TREND_DOWN/BALANCE | — | Drop losing regimes (TREND_UP longs get faded) |
| `regime_blocked_directions` | inline table | — | Block direction per regime. `{ BALANCE = "LONG" }` was biggest single improvement |

#### Entry Quality (tune AFTER structural filters)
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `min_confluence_score` | 5–8 | 1 | Higher = selective. **Interacts with time window** (slot backfill) |
| `volume_mult` | 1.5–3.0 | 0.25 | Higher = fewer, higher-quality trades |
| `hold_bars` | 3–8 | 1 | Stronger breakout confirmation |
| `min_slope_bps` | 0.1–1.0 | 0.1 | Strong trends only |

#### Exit Rules
| Parameter | Range | Step | Effect |
|-----------|-------|------|--------|
| `stop_bps` | 50–150 | 10 | Minor impact if swing stop dominates exits |
| `stagnation minutes` | 30–90 | 15 | Sweet spot ~45 min. Lower or higher both worse |
| `avwap_stop_bars` | 1–3 | 1 | Faster AVWAP-based stop |
| `max_loss pct` | 0.01–0.05 | 0.005 | Hard stop per trade |
| swing stop `min_bars` | 3–15 | 2 | Real bars (time-based). 6 bars = 30 min on 5m = profitability inflection |

**AVWAP-specific notes:**
- SHORT side (TREND_DOWN) is the profit driver. LONG side is structurally weaker.
- Swing stop min_bars is in real bars (time-based), not eval calls. Fixed 2026-04-06.
- Confluence filtering interacts with time window due to slot backfill. Narrow window FIRST.
- `regime_blocked_directions = { BALANCE = "LONG" }` blocks breakout longs in ranging markets.

**Correlated pairs:** `allowed_hours_end` × `min_confluence_score`, `stop_bps` × `min_slope_bps`

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

## Known Bug Classes

Before tuning, scan for these known issues:

### Counter-Based Bar Counting (CRITICAL)
**Symptom:** `CustomState["key"]++` in an evaluator to count bars.
**Bug:** In backtest mode, EvalExitRules is called N times per bar (once per symbol with pending aggregator). Counter increments N× per real bar, causing min_bars to fire too early.
**Fix:** Use `int(now.Sub(pos.EntryTime) / barDur)` instead. Deduplicate ring buffer writes with `last_bar_idx`.
**Status:** Fixed for `evaluateSwingStop` (2026-04-06). Other evaluators already time-based.

### TOML Subtable Scope Termination (CRITICAL)
**Bug:** `[params.subtable]` terminates `[params]` — all subsequent params become children of the subtable.
**Fix:** Use inline table: `param = { key = "value" }` within the `[params]` section.

## Slot Backfill Effect

Removing trades via entry filters can backfill freed position slots with worse replacement trades, causing net PF decrease.
**Mitigation:** Apply structural filters (time window, regime) FIRST to constrain the replacement pool, THEN tighten entry quality within the narrower pool.

## Rebuild & Restart After Engine Changes

```bash
cd /home/ridopark/src/oh-my-opentrade/backend
go build -o /home/ridopark/src/oh-my-opentrade/backend/bin/omo-core ./cmd/omo-core
kill $(pgrep -f "bin/omo-core$" | head -1) 2>/dev/null
sleep 2
tmux send-keys -t omo-core "set -a; source /home/ridopark/src/oh-my-opentrade/.env; set +a; /home/ridopark/src/oh-my-opentrade/backend/bin/omo-core 2>&1 | tee -a /home/ridopark/src/oh-my-opentrade/logs/omo-core.log" Enter
sleep 10
```

## Overfitting Detection

Flag as suspect if:
- Trade count < 200 (absolute floor for statistical significance)
- PF > 3.0
- WR > 60% AND PF > 2.0
- Backtest period < 6 months
- Split-half divergence (improvement in one half, regression in other)
- PF jumps > 0.3 from a single parameter tweak (suspect unless engine fix)

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
