---
name: monitor-omo-services
description: Continuously monitor Loki logs and database consistency for omo-core. Use when the user asks to monitor logs, watch for errors, keep an eye on the system, check DB health, or detect anomalies. Triggers on phrases like 'monitor logs', 'watch logs', 'keep monitoring', 'check loki', 'anything odd', 'check db', 'monitor'.
---

# Log & DB Monitor

Continuously query Loki logs and run database consistency checks. Report anomalies until the user says to stop.

Monitor both **System Health** (is the binary running and receiving data?) and **Economic Health** (are strategies behaving correctly and P&L within bounds?).

## Prerequisites

Verify all services are reachable before starting:

```bash
curl -s http://localhost:3100/ready && echo "Loki: OK" || echo "Loki: DOWN"
docker exec omo-timescaledb pg_isready -U opentrade -d opentrade 2>/dev/null && echo "DB: OK" || echo "DB: DOWN"
source /home/ridopark/src/oh-my-opentrade/.env && curl -s -o /dev/null -w "%{http_code}" https://paper-api.alpaca.markets/v2/account \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | xargs -I{} echo "Alpaca: {}"
```

If any service is down, warn the user. Proceed with whichever is available.

---

## Step 0: Detect Last Restart Time (ALWAYS run first)

All monitoring queries should use the last restart time as their default window, not a fixed "last N minutes" interval. This ensures the first monitoring cycle catches everything since the service came up, and subsequent cycles don't miss events in gaps between invocations.

```bash
SINCE_START=$(curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "starting omo-core|omo-core started|signal debate enricher subscribed|risk sizer subscribed"' \
  --data-urlencode 'limit=5' \
  --data-urlencode 'direction=backward' | python3 -c "
import json, sys
data = json.load(sys.stdin)
results = data['data']['result']
ts_list = []
for stream in results:
    for ts, msg in stream['values']:
        ts_list.append(int(ts))
if ts_list:
    latest = max(ts_list)
    print(latest)
else:
    import time
    print(int((time.time() - 600) * 1e9))
") && echo "SINCE_START=$SINCE_START"
```

Use `$SINCE_START` as the `start` parameter for ALL subsequent Loki queries (replacing hardcoded `2 minutes ago`). For DB queries, compute the equivalent interval:

```bash
UPTIME_MINUTES=$(python3 -c "import time; print(int((time.time()*1e9 - $SINCE_START) / 60e9))")
echo "Uptime: ${UPTIME_MINUTES} minutes"
```

For the report header, compute a human-readable start time:

```bash
START_TIME=$(python3 -c "
from datetime import datetime, timezone, timedelta
ts = $SINCE_START / 1e9
ct = datetime.fromtimestamp(ts, tz=timezone(timedelta(hours=-6)))
print(ct.strftime('%I:%M %p CT'))
")
```

---

## Part 1: Loki Log Monitoring

### Step 1: Query for errors and warnings (since restart)

```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "error|ERROR|panic|fatal|FATAL"' \
  --data-urlencode 'limit=100' \
  --data-urlencode 'direction=forward' \
  --data-urlencode "start=$SINCE_START" | python3 -c "
import json, sys
data = json.load(sys.stdin)
results = data['data']['result']
lines = []
for stream in results:
    for ts, msg in stream['values']:
        outer = json.loads(msg) if msg.startswith('{') else {'log': msg}
        log_line = outer.get('log', msg)
        lines.append((ts, log_line))
lines.sort(key=lambda x: x[0])
if not lines:
    print('No errors since restart.')
for ts, line in lines:
    print(line.rstrip())
"
```

### Step 2: Query all warnings since restart (categorized)

```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "WARN|warn"' \
  --data-urlencode 'limit=200' \
  --data-urlencode 'direction=forward' \
  --data-urlencode "start=$SINCE_START" | python3 -c "
import json, sys, re
from collections import Counter
data = json.load(sys.stdin)
results = data['data']['result']
lines = []
for stream in results:
    for ts, msg in stream['values']:
        outer = json.loads(msg) if msg.startswith('{') else {'log': msg}
        log_line = outer.get('log', msg)
        lines.append((ts, log_line))
categories = Counter()
for ts, line in lines:
    if 'veto gate' in line: categories['veto_gate_blocks'] += 1
    elif 'pre-LLM veto' in line: categories['pre_llm_veto'] += 1
    elif 'entry rejected' in line: categories['entry_rejected'] += 1
    elif 'exit cooldown' in line: categories['exit_cooldown'] += 1
    elif 'trading_window' in line: categories['trading_window'] += 1
    elif 'spread_guard' in line or 'slippage' in line: categories['spread_slippage'] += 1
    elif 'position_gate' in line: categories['position_gate'] += 1
    elif 'AI direction gate' in line: categories['ai_direction_gate'] += 1
    elif 'revaluation gate' in line: categories['revaluation_gate'] += 1
    elif 'websocket' in line.lower(): categories['websocket'] += 1
    elif 'reconnect' in line.lower(): categories['reconnect'] += 1
    elif 'notional too small' in line: categories['notional_too_small'] += 1
    elif 'no builtin implementation' in line: categories['legacy_hook_warn'] += 1
    else: categories['other'] += 1
print(f'Total warnings since restart: {sum(categories.values())}')
for cat, cnt in categories.most_common():
    print(f'  {cat}: {cnt}')
"
```

### Step 3: Frozen Bot Detection

A frozen bot (alive but not receiving data) is more dangerous than a dead one. Check that bars are being processed for all expected symbols:

```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "bar processed"' \
  --data-urlencode 'limit=50' \
  --data-urlencode 'direction=backward' \
  --data-urlencode "start=$(date -u -d '3 minutes ago' +%s)000000000" | python3 -c "
import json, sys, re
from datetime import datetime, timezone, timedelta
data = json.load(sys.stdin)
results = data['data']['result']
symbols_seen = set()
for stream in results:
    for ts, msg in stream['values']:
        outer = json.loads(msg) if msg.startswith('{') else {'log': msg}
        log_line = outer.get('log', msg)
        m = re.search(r'symbol=(\S+)', log_line)
        if m:
            symbols_seen.add(m.group(1))
expected_crypto = {'BTC/USD','ETH/USD','SOL/USD','AVAX/USD','DOGE/USD','PEPE/USD'}
expected_equity = {'AAPL','MSFT','GOOGL','AMZN','TSLA','SOXL','U','PLTR','SPY','META'}
# Check if we're during RTH (9:30-16:00 ET, Mon-Fri)
et = datetime.now(timezone(timedelta(hours=-5)))  # ET = UTC-5 (EST) or UTC-4 (EDT)
is_rth = et.weekday() < 5 and 930 <= et.hour * 100 + et.minute <= 1600
if is_rth:
    all_expected = expected_crypto | expected_equity
    label = 'all 16 symbols (RTH active)'
else:
    all_expected = expected_crypto
    label = '6 crypto symbols (outside RTH)'
missing = all_expected - symbols_seen
if missing:
    print(f'WARNING: No bars in last 3 min for: {sorted(missing)}')
else:
    print(f'All {len(all_expected)} {label} active (last 3 min)')
if not is_rth:
    equity_seen = expected_equity & symbols_seen
    if equity_seen:
        print(f'Note: equity symbols also active outside RTH: {sorted(equity_seen)}')
"
```

During equity market hours (9:30-16:00 ET, Mon-Fri), all 16 symbols should be active. Outside RTH, only the 6 crypto symbols are expected — missing equity symbols outside RTH is normal and should NOT be reported as a warning.

### Log Classification Rules

| Pattern | Severity | Action |
|---------|----------|--------|
| `panic`, `fatal`, `FATAL` | CRITICAL | Report immediately, suggest restart |
| No bars for a symbol >3 min (crypto) or >5 min (equity during RTH) | CRITICAL | Frozen bot — report immediately |
| `error` + strategy name | HIGH | Report with context |
| Order rejection spike (>3 in 60s) | HIGH | May indicate account-level issue |
| `websocket error` or `reconnect` | HIGH | Report — verify reconnection succeeded within 5s |
| `PARTIAL_FILL` status lingering >60s | HIGH | Report — may need cancel-and-replace |
| `trading_window` rejection | NORMAL | Suppress unless new pattern |
| `slippage` / `spread_guard` rejection | NORMAL | Suppress unless excessive (>5/min) |
| `position_gate: already_in_position` | NORMAL | Suppress |
| `position_gate: no_position_to_exit` | LOW | Note if recurring |
| `fill received for unknown order` | LOW | Note — usually dust sweep related |
| `bar repaired by adaptive filter` | INFO | Suppress |
| `429` / `too many requests` | LOW | Note — if >5 in 1h, investigate polling frequency |
| `notional too small` / `weight/notional` | LOW | Risk sizer issue — note if recurring |

### Known Pre-Existing Warnings (suppress)

- `orb_break_retest` "no builtin implementation for hook" — legacy config, harmless
- `PaperOnly` lifecycle state errors — old config format, harmless after restart
- Slippage/spread rejections on altcoins (DOGE, PEPE, AVAX) during off-hours — expected

---

## Part 2: Database Consistency Checks

Run these on each monitoring cycle. All queries target `omo-timescaledb` container.

### Check 1: Position Drift (OMO DB vs Broker)

This is the most important check. Compare net position in trades table against broker positions.

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT symbol,
       SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END) AS net_qty,
       MAX(time) AS last_trade
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '7 days'
GROUP BY symbol
HAVING ABS(SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END)) > 0.0001
ORDER BY symbol;
"
```

Cross-reference against broker:

```bash
source /home/ridopark/src/oh-my-opentrade/.env && curl -s https://paper-api.alpaca.markets/v2/positions \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -c "
import json, sys
positions = json.load(sys.stdin)
if not positions or isinstance(positions, dict):
    print('No broker positions')
    sys.exit(0)
for p in positions:
    print(f\"{p['symbol']:<12} {p['side']:<6} qty={float(p['qty']):>12.6f}  pnl={float(p.get('unrealized_pl',0)):>+10.2f}\")
"
```

**Classification:**
- Any difference > 0 between DB and broker: **WARNING**
- Difference > 5% of position size: **CRITICAL**
- DB has position but broker doesn't (orphaned): **HIGH** — suggest reconciliation INSERT
- Broker has position but DB doesn't: **HIGH** — missed fill, check Loki for WebSocket gaps

### Check 2: Duplicate Trades

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT trade_id, COUNT(*) AS cnt
FROM trades
WHERE time >= NOW() - INTERVAL '1 day'
GROUP BY trade_id
HAVING COUNT(*) > 1
LIMIT 10;
"
```

**If found**: CRITICAL — duplicate trade IDs indicate a bug in fill handling. Should never happen.

### Check 3: Stuck Orders (open >10 minutes)

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT broker_order_id, symbol, side, status, time
FROM orders
WHERE status NOT IN ('filled', 'canceled', 'expired', 'rejected')
  AND time < NOW() - INTERVAL '10 minutes'
ORDER BY time DESC
LIMIT 10;
"
```

**If found**: HIGH — stale open orders may need manual cancellation via Alpaca API.

### Check 4: Market Bar Gaps

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT symbol, timeframe,
       MAX(time) AS last_bar,
       EXTRACT(EPOCH FROM (NOW() - MAX(time)))/60 AS minutes_stale
FROM market_bars
WHERE timeframe = '1m'
  AND time >= NOW() - INTERVAL '1 hour'
GROUP BY symbol, timeframe
HAVING EXTRACT(EPOCH FROM (NOW() - MAX(time)))/60 > 5
ORDER BY minutes_stale DESC;
"
```

**Classification:**
- Crypto symbol gap >5 min: **HIGH** — WebSocket likely disconnected
- Equity symbol gap during RTH >5 min: **HIGH**
- Equity symbol gap outside RTH: **NORMAL** — expected, suppress

### Check 5: Trade Activity & Runaway Detection (since restart)

Use `$UPTIME_MINUTES` from Step 0 to compute the interval. Fall back to 60 minutes if unavailable.

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT strategy, side,
       COUNT(*) AS trades,
       ROUND(SUM(quantity * price)::numeric, 2) AS notional,
       MIN(time) AS first_trade,
       MAX(time) AS last_trade
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '${UPTIME_MINUTES:-60} minutes'
GROUP BY strategy, side
ORDER BY strategy, side;
"
```

Also break down by symbol to spot concentration:

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT symbol, COUNT(*) AS trades,
       COUNT(*) FILTER (WHERE side='BUY') AS buys,
       COUNT(*) FILTER (WHERE side='SELL') AS sells,
       ROUND(SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END)::numeric, 6) AS net_qty
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '${UPTIME_MINUTES:-60} minutes'
GROUP BY symbol
ORDER BY trades DESC;
"
```

**Classification:**
- Strategy with >20 trades/hour: **WARNING** — possible runaway loop
- Strategy with >50 trades/hour: **CRITICAL** — likely a bug, suggest disabling
- Normalize by uptime: `trades_per_hour = trades / (UPTIME_MINUTES / 60)`

### Check 6: Portfolio Summary & Drawdown

```bash
source /home/ridopark/src/oh-my-opentrade/.env && curl -s https://paper-api.alpaca.markets/v2/account \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -c "
import json, sys
a = json.load(sys.stdin)
equity = float(a['equity'])
last_equity = float(a['last_equity'])
buying_power = float(a['buying_power'])
daily_change = equity - last_equity
daily_pct = (daily_change / last_equity * 100) if last_equity else 0
positions = int(a.get('position_market_value', '0') != '0')
print(f'Equity: \${equity:,.2f}  (daily: \${daily_change:+,.2f} / {daily_pct:+.2f}%)')
print(f'Buying power: \${buying_power:,.2f}')
print(f'Last equity: \${last_equity:,.2f}')
if daily_pct < -2:
    print(f'WARNING: Daily drawdown {daily_pct:.2f}% exceeds -2% threshold')
if daily_pct < -5:
    print(f'CRITICAL: Daily drawdown {daily_pct:.2f}% exceeds -5% hard stop threshold')
"
```

**Classification:**
- Daily loss >2%: **WARNING** — report to user
- Daily loss >5%: **CRITICAL** — recommend pausing all strategies

### Check 7: TimescaleDB Health (run every 5th cycle)

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT hypertable_name,
       pg_size_pretty(hypertable_size(format('%I.%I', hypertable_schema, hypertable_name)::regclass)) AS total_size,
       num_chunks
FROM timescaledb_information.hypertables
ORDER BY hypertable_size(format('%I.%I', hypertable_schema, hypertable_name)::regclass) DESC;
"
```

Report if any hypertable exceeds 10GB or has >1000 chunks.

---

## Part 3: Anomaly & Suspicion Detection (since restart)

After collecting raw data from Parts 1 and 2, actively look for patterns that aren't outright errors but are worth flagging to the user. These are the "hmm, that's odd" observations a senior engineer would notice.

### What to Look For

**Signal Flow Anomalies:**
- High veto rate on a specific symbol (>5 vetoes since restart) — strategy may be misconfigured for current regime
- Same symbol being repeatedly vetoed then NOT vetoed in alternation — regime oscillation, potentially unstable
- Strategy generating signals but zero fills — something is blocking between signal and execution
- Signals generated but all rejected (100% rejection rate) — gate misconfiguration
- AI debate consistently disagreeing with signals (high AI direction conflict rate) — strategy thesis may be stale

**Execution Anomalies:**
- Fills at prices far from limit price (slippage >0.5% on equities, >1% on crypto) — liquidity or timing issue
- Orders submitted then immediately canceled (not by exit rules) — possible race condition
- Burst of orders in <10 seconds — throttle or debounce might be missing
- Fill quantities consistently smaller than intended (partial fills becoming the norm)
- Same symbol churning: buy-sell-buy-sell in rapid succession (>3 round-trips since restart)

**Position & P&L Anomalies:**
- Position held longer than max_hold without exit triggering — exit rule may not be firing
- Net realized P&L trending consistently negative across all strategies since restart
- One strategy disproportionately responsible for losses (>80% of total loss)
- Position size much larger or smaller than typical (compare to recent history)

**System Behavior:**
- Log volume unusually high or low compared to uptime (expect ~5-15 log lines per minute per symbol)
- Component that should be logging is silent (e.g., no `position_monitor` logs while positions are open)
- Warmup took unusually long or short compared to normal (~30-60s expected)
- WebSocket reconnection without corresponding data gap (could indicate silent data loss)
- Startup log shows different number of strategy instances than expected

### How to Report Anomalies

For each anomaly found, report:
1. **What**: One-line description of what's odd
2. **Evidence**: The specific data point(s) that triggered the flag
3. **Possible cause**: Most likely explanation (don't speculate wildly)
4. **Suggested action**: "investigate", "monitor next cycle", or "likely benign"

Example:
```
PEPE/USD vetoed 8 times since restart (every signal blocked)
  → Strategy crypto_avwap_v2 has negative expectancy in BALANCE regime
  → Likely benign: veto gate working as designed. Consider disabling strategy for this symbol.
```

Only report things that are genuinely worth the user's attention. If everything is behaving exactly as expected, say so — don't manufacture concerns.

---

## Report Format

After all checks, produce a single concise report:

```
**Monitoring Report — [time] CT (since restart at [start_time], uptime: [N]m)**

**System Health:**
- Services: [Loki OK, DB OK, Alpaca OK]
- Symbols active: [N/16] (last 3 min)
- [any frozen/disconnected symbols]

**Logs (since restart):**
- Errors: [count] | Warnings: [count]
- [categorized breakdown: veto blocks, trading window, cooldown, etc.]

**DB Health:**
- Positions: [N] open, [match/mismatch] with broker
- Orphaned: [none / list]
- Stuck orders: [none / count]
- Duplicate trades: [none / count]
- Bar gaps: [none / list]

**Economic Health:**
- Equity: $[X] (daily: [+/-Y%])
- Trades since restart: [total] ([N]/hr rate) — [summary by strategy]
- [drawdown warning if applicable]

**Worth Investigating:**
- [anomaly 1 with evidence and suggestion]
- [anomaly 2...]
- (or "Nothing suspicious — all patterns normal")

**Verdict:** [clean / watch item / action needed]
```

---

## Monitoring Cadence

| Check | Frequency |
|-------|-----------|
| Loki errors/warnings | Every cycle (~2 min) |
| Frozen bot detection | Every cycle |
| Position drift | Every cycle |
| Stuck orders | Every cycle |
| Bar gaps | Every cycle |
| Trade activity | Every cycle |
| Portfolio drawdown | Every cycle |
| Duplicate trades | Every cycle |
| TimescaleDB health | Every 5th cycle (~10 min) |

- If verdict is **clean**: end response, repeat on next prompt (~2 min)
- If verdict is **watch item**: end response, note what to watch, repeat sooner (~1 min)
- If verdict is **action needed**: report findings and recommend specific action
- If user says **stop**: stop monitoring, give final summary of the session

---

## Alerting Thresholds Quick Reference

| Metric | Warning | Critical |
|--------|---------|----------|
| Position drift (DB vs broker) | Any difference > 0 | Diff > 5% of position |
| Order rejection rate | 5% of daily orders | >3 consecutive or 10% |
| Bar gap (crypto) | 1 missing bar | 5 consecutive bars (5 min) |
| Bar gap (equity, during RTH) | 1 missing bar | 5 consecutive bars |
| Daily P&L drawdown | -2% | -5% (hard stop) |
| Trade count per strategy/hour | >20 | >50 (runaway) |
| Order latency (paper) | >500ms | >2000ms |
| Open positions | >15 | >20 |
| Stuck orders (age) | >5 min | >10 min |
| Partial fill lingering | >30s | >60s |

---

## Alpaca-Specific Monitoring Notes

- **WebSocket Disconnects**: Alpaca `stream.alpaca.markets` can drop connections without TCP close frame. Monitor for `websocket error` and verify `reconnect` succeeds within 5s.
- **Paper API Latency**: Alpaca Paper is significantly slower than Live. Use 500ms as the baseline, not sub-100ms.
- **Crypto Minimums**: Alpaca has strict minimum notionals ($1-$10). Watch for `notional too small` in risk_sizer logs.
- **Rate Limits**: 200 requests/min for data API, separate limits for trading. Monitor for `429` status codes.

---

## Targeted Queries

Use these when investigating specific issues found during monitoring:

**Trace a specific strategy in Loki:**
```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "STRATEGY_NAME"' \
  --data-urlencode 'limit=30' \
  --data-urlencode 'direction=backward'
```

**Trace a specific symbol in Loki:**
```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "SYMBOL"' \
  --data-urlencode 'limit=30' \
  --data-urlencode 'direction=backward'
```

**Trace an order by broker ID in Loki:**
```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "ORDER_ID"' \
  --data-urlencode 'limit=50' \
  --data-urlencode 'direction=forward'
```

**Full trade history for a symbol:**
```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT time, side, quantity, price, status, strategy, rationale
FROM trades
WHERE symbol = 'SYMBOL' AND env_mode = 'Paper'
ORDER BY time DESC
LIMIT 20;
"
```

**Check broker open orders:**
```bash
source /home/ridopark/src/oh-my-opentrade/.env && curl -s "https://paper-api.alpaca.markets/v2/orders?status=open" \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -m json.tool
```

**Check broker account buying power:**
```bash
source /home/ridopark/src/oh-my-opentrade/.env && curl -s https://paper-api.alpaca.markets/v2/account \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -c "
import json, sys
a = json.load(sys.stdin)
print(f\"Equity: \${float(a['equity']):,.2f}\")
print(f\"Buying Power: \${float(a['buying_power']):,.2f}\")
print(f\"Day P&L: \${float(a['equity'])-float(a['last_equity']):+,.2f}\")
"
```

---

## Loki Log Format

Loki results are double-wrapped JSON. Each value is `[timestamp, message_string]` where `message_string` is a JSON object with a `"log"` key containing the actual structured log line:

```python
outer = json.loads(msg)        # {"log": "<actual log line>"}
log_line = outer.get("log", msg)
```

Some log lines are structured JSON (`{"level":"info",...}`), others are plain Go slog format (`2026/03/10 16:42:59 INFO ...`). Handle both.

## Key Components

| Component | What it does |
|-----------|-------------|
| `strategy_runner` | Routes bars to strategy instances, emits signals |
| `execution` | Validates and submits orders to broker |
| `position_monitor` | Tracks open positions, triggers exit rules |
| `risk_sizer` | Calculates position sizes, applies dynamic risk |
| `alpaca` | Broker API communication (REST + WebSocket) |
| `ingestion` | Bar sanitization and repair |
| `warmup` | Startup historical bar loading |
| `monitor` | Indicator calculation and regime detection |
| `ledger` | Trade recording and P&L tracking |

## Database Tables

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `trades` | Executed fills | time, symbol, side, quantity, price, strategy, status, trade_id |
| `orders` | Broker orders | time, account_id, env_mode, intent_id, broker_order_id, symbol, side, quantity, limit_price, stop_loss, status, filled_at, filled_price, filled_qty, strategy, rationale, confidence |
| `market_bars` | OHLCV bars | time, symbol, timeframe, open, high, low, close, volume |
| `daily_pnl` | Daily P&L snapshots | date, account_id, realized_pnl, unrealized_pnl |
| `equity_curve` | Equity over time | time, account_id, equity |
| `accounts` | Account config | id, env_mode, broker |
| `thought_logs` | AI debate reasoning | time, symbol, strategy, thought |
| `strategy_dna_history` | Config change audit | time, strategy_id, dna_toml |

## Important Notes

- Loki endpoint: `http://localhost:3100`
- TimescaleDB: `docker exec -i omo-timescaledb psql -U opentrade -d opentrade`
- Alpaca credentials: sourced from `/home/ridopark/src/oh-my-opentrade/.env`
- All commands run from project root: `/home/ridopark/src/oh-my-opentrade`
- Crypto trades 24/7; equity only during RTH (9:30-16:00 ET)
- After-hours crypto rejections (trading_window, spread_guard) are expected — suppress
- Alpaca Paper API is slower than Live — use 500ms as latency baseline, not sub-100ms
- The system uses paper trading — losses are not real money but should still be reported
