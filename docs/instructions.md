# Operations & Testing Instructions

## AI Pre-Market Screener

The AI screener dynamically fetches the full Alpaca tradeable universe, applies hard numeric filters (Pass 0), then uses paid LLM models via OpenRouter to score and rank symbols per-strategy. Ticker symbols are anonymized before being sent to the LLM to prevent brand bias.

### Prerequisites

```bash
# Required in .env
STRATEGY_V2=true
AI_SCREENER_ENABLED=true
LLM_ENABLED=true
LLM_BASE_URL=https://openrouter.ai/api
LLM_API_KEY=sk-or-...   # OpenRouter key (paid models)
```

The screener reuses the same `LLM_BASE_URL` and `LLM_API_KEY` as the debate system.

### Database Migration

Run once (or verify the table exists):

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade < migrations/019_create_ai_screener_results.up.sql
```

Verify:

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "\d ai_screener_results"
```

### Automatic Schedule

The screener runs daily on trading days:
- **AI screener**: 8:35 ET
- Skips weekends and NYSE holidays
- **Catch-up on restart**: If the system restarts after the scheduled time and no screen has run for today, it automatically runs a catch-up screen

### Bootstrap on Restart

On every restart, the screener bootstraps from the latest DB results:
- Loads the most recent screening results per strategy
- Publishes `EventAIScreenerCompleted` events so the symbol router picks them up
- This ensures screened symbols survive restarts without re-running the LLM
- Bootstrap does NOT send Discord notifications (only fresh screen runs do)

### Watchlist Mode

All 7 strategies use `watchlist_mode = "replace"` — screener-picked symbols completely replace the static TOML symbols. The symbol router consumes `EventAIScreenerCompleted` events and updates effective symbols accordingly.

### Manual Trigger (Debug Endpoint)

Trigger a run at any time without waiting for the schedule:

```bash
curl -X POST http://localhost:8080/debug/ai-screener/run
```

Returns `{"status":"started","as_of":"..."}` immediately. The run executes asynchronously.

### Checking Logs

Watch the backend logs for the AI screener flow:

```bash
# If running via tmux
tmux attach -t omo-core

# Key log messages to look for:
# "ai screener: pass0 complete"     → universe=N snapshots=M pass0_survivors=K
# "ai screener completed"           → strategy, model, candidates, scored, latency_ms
# "ai screener: effective symbols resolved" → symbol router consumed the AI event
# "model failed, trying next"       → fallback chain activated (warning, not error)
# "equity WS: queued symbols for subscription" → equity symbols queued before WS connects
# "equity WS: draining pending subscriptions"  → queued symbols subscribed after WS connects
```

Via Loki:

```bash
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "ai.screener"' \
  --data-urlencode 'limit=50' \
  --data-urlencode 'direction=backward'
```

### Checking Results in DB

```bash
# Latest AI screener results (scores + rationale)
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT strategy_key, symbol, score, rationale, model,
       latency_ms, as_of AT TIME ZONE 'America/New_York' as as_of_et
FROM ai_screener_results
ORDER BY as_of DESC, strategy_key, score DESC
LIMIT 30;
"

# Summary: how many symbols scored per strategy, per run
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT run_id, strategy_key, model, count(*) as scored,
       round(avg(score), 1) as avg_score,
       max(score) as max_score,
       max(latency_ms) as latency_ms,
       as_of AT TIME ZONE 'America/New_York' as as_of_et
FROM ai_screener_results
GROUP BY run_id, strategy_key, model, as_of
ORDER BY as_of DESC
LIMIT 20;
"
```

### Notifications

After each fresh screen run, per-strategy Discord notifications are sent:

1. **Header message**: Summary with universe size, pass0 count, success/fail count, duration
2. **Per-strategy messages**: Each strategy gets its own message with symbols, scores (1-5), and rationale for each pick

Example header:
```
AI Pre-Market Screener
08:35 ET | Duration: 35s
Universe: 12638 → Snapshots: 486 → Pass0: 67
7/7 strategies succeeded
```

Example per-strategy:
```
orb_break_retest (4588ms)
• CRUS [4/5] — Strong gap-up with high RVOL, breaks prior resistance
• SAFE [4/5] — Clean breakout setup above VWAP with volume confirmation
...
```

Bootstrap loads do NOT send Discord notifications — only fresh screen runs do.

### Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `ai screener not enabled` on curl | Missing env vars | Set `AI_SCREENER_ENABLED=true` and `STRATEGY_V2=true`, restart |
| `no strategies with screening descriptions` | TOMLs missing `[screening]` section | Check `configs/strategies/*.toml` for `[screening]` with `description` field |
| `no symbols survived pass0` | Filters too strict or market closed | Pre-market volume is 0 outside hours; lower `Pass0MinVolume` or test during pre-market (4-9:30 ET) |
| `all models failed` | OpenRouter rate limit or key issue | Check `LLM_API_KEY` is valid |
| `model failed, trying next` (warning) | One model unavailable | Normal — fallback chain handles it automatically |
| Results in DB but no effective symbols update | Symbol router not consuming AI events | Check logs for `ai screener: effective symbols resolved` |
| Crypto strategies get equity candidates | Missing `asset_classes` in TOML `[routing]` | Add `asset_classes = ["CRYPTO"]` to crypto strategy TOMLs |

### Configuration Defaults

| Setting | Default | Description |
|---------|---------|-------------|
| Models | `google/gemini-2.5-flash-lite`, `deepseek/deepseek-chat-v3`, `anthropic/claude-3.5-haiku` | LLM fallback chain (paid) |
| AI run time | 8:35 ET | When AI screener fires |
| Pass0 min price | $10 | Minimum stock price |
| Pass0 min volume | 50,000 | Minimum pre-market volume |
| Pass0 min gap% | 0% | Minimum absolute gap percentage |
| Max candidates/call | 20 | Symbols per LLM request |
| Top N per strategy | 10 | Best scores to keep |

Override via `configs/config.yml`:

```yaml
ai_screener:
  enabled: true
  models:
    - "google/gemini-2.5-flash-lite"
    - "deepseek/deepseek-chat-v3"
    - "anthropic/claude-3.5-haiku"
  ai_run_at_hour_et: 8
  ai_run_at_minute_et: 35
  pass0_min_price: 10.0
  pass0_min_volume: 50000
  max_candidates_per_call: 20
  top_n_per_strategy: 10
```

### Rollback

```bash
# Remove migration
docker exec -i omo-timescaledb psql -U opentrade -d opentrade < migrations/019_create_ai_screener_results.down.sql

# Disable
# Set AI_SCREENER_ENABLED=false in .env (or remove the line), restart
```

---

## Order Execution

### Order Type

All entries are submitted as **limit orders** (never stop_limit). The `StopLoss` field in `OrderIntent` is informational — used by the position monitor for post-fill risk management, NOT sent to the broker as a stop price.

### Slippage Guard

Before submitting, `SlippageGuard` checks: `ask > limitPrice + tolerance`. If the spread is too wide, the order is rejected.

Tolerance = `limitPrice * MaxSlippageBPS / 10000`

### Time-Aware Slippage (Crypto)

Crypto spreads widen significantly outside regular hours. The risk sizer reads offhours params from strategy DNA:

| Window | `limit_offset_bps` | `max_slippage_bps` | Total |
|--------|--------------------|--------------------|-------|
| RTH (08:00-17:00 ET weekdays) | 15 | 20 | 35 BPS |
| Off-hours / weekends | 30 | 40 | 70 BPS |
| Equity (always) | 5 | 10 | 15 BPS |

RTH is determined by `isCryptoRTH()` in `risk_sizer.go`: weekday + 08:00-17:00 ET.

Strategy DNA params (in `[entry]` section of TOML):
```toml
limit_offset_bps = 15
max_slippage_bps = 20
limit_offset_bps_offhours = 30
max_slippage_bps_offhours = 40
```

### Stale Order Reconciler

Orders pending > 2 minutes are automatically canceled by the reconciler (`execution/service.go` line ~1075). This is aggressive for paper trading — valid limit orders may be killed before filling. Consider increasing to 15-30 min if fill rates are too low.

### Exit Circuit Breaker

`PositionGate` tracks exit failures per symbol. After 3 consecutive exit failures for the same symbol, a 5-minute cooldown is applied before retrying. This prevents infinite exit retry loops (originally triggered by Alpaca paper refusing ETH/USD sells).

---

## AI Signal Debate (Bull/Bear/Judge)

### Per-Symbol Veto

The AI direction conflict check is **per-symbol** with a **7-day lookback**. Minimum confidence of 0.50 is required for the AI to veto a trade. Trades without enough AI history are allowed through (fail-open).

Exit sells do NOT go through AI discussion — the notification says "AI discussion skipped" for exit orders.

---

## Dynamic Symbol Activation

When the screener picks new symbols, the `PipelineActivator` and `Activation Service` handle:

1. Creating strategy instances for new symbols
2. Warming up indicators (1m, 1H EMA50, 1D EMA200)
3. Subscribing to WebSocket streams (equity symbols queue if WS not yet connected)
4. Replaying ORB data if market is open

**WS Subscription Timing**: Equity symbols requested before the equity WS connects are queued in `pendingSymbols`. When the WS connects, `drainPendingSubscriptions()` subscribes them automatically. No manual intervention needed.

---

## Universe Filtering

The Alpaca universe provider filters:
- **Crypto**: Only `/USD` pairs (filters out `/BTC`, `/EUR`, etc.)
- **Equity**: All active, tradeable stocks

Pass0 further filters by price ($10+), volume (50K+), and gap%.

---

## Monitoring & Debugging

### Check Positions vs DB

```bash
# Broker positions
source .env && curl -s https://paper-api.alpaca.markets/v2/positions \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -m json.tool

# OMO trade DB net positions (should match broker)
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT symbol,
       SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END) as net_qty
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '30 days'
GROUP BY symbol
HAVING SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END) > 0.0001
ORDER BY symbol;
"
```

If OMO shows positions the broker doesn't have (orphaned records), insert reconciliation trades:
```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
INSERT INTO trades (time, account_id, env_mode, trade_id, symbol, side, quantity, price, commission, status, strategy, rationale) VALUES
  (NOW(), 'default', 'Paper', gen_random_uuid(), 'SYMBOL', 'SELL', QTY, PRICE, 0, 'FILLED', 'reconciliation', 'cleanup: orphaned BUY with no broker position');
"
```

### Key Components in Loki

| Component | Filter | What to look for |
|-----------|--------|-----------------|
| `ai_screener` | `ai.screener` | Screen runs, model failures, pass0 results |
| `execution` | `execution` | Order submissions, cancellations, fills |
| `position_monitor` | `position_monitor` | Position tracking, exit triggers |
| `risk_sizer` | `risk_sizer` | Position sizing, slippage checks |
| `activation` | `activation` | New symbol warmup, WS subscriptions |
| `symbolrouter` | `symbolrouter` | Effective symbol updates |
