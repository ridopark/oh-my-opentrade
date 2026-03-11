# Operations & Testing Instructions

## AI Pre-Market Screener

The AI screener dynamically fetches the full Alpaca tradeable universe, applies hard numeric filters (Pass 0), then uses free OpenRouter LLMs to score and rank symbols per-strategy.

### Prerequisites

```bash
# Required in .env
STRATEGY_V2=true
AI_SCREENER_ENABLED=true
LLM_ENABLED=true
LLM_BASE_URL=https://openrouter.ai/api
LLM_API_KEY=sk-or-...   # OpenRouter key (free models work)
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
- **Numeric screener (Pass 1)**: 8:00 ET
- **AI screener (Pass 2)**: 8:35 ET

Both skip weekends and NYSE holidays.

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

After each run, a summary is sent to all configured notification channels (Discord, Telegram, etc.):

```
🔬 AI Pre-Market Screener
⏰ 08:35 ET | Duration: 5m 34s
🌐 Universe: 12638 → Snapshots: 486 → Pass0: 67

✅ avwap_v1 — 20 scored | stepfun/step-3.5-flash:free | 32062ms
✅ break_retest_v1 — 20 scored | stepfun/step-3.5-flash:free | 25936ms
❌ crypto_ai_scalping_v1 — all models failed
📊 4/7 strategies succeeded
```

No extra configuration needed — uses the same Discord/Telegram already set up for trade notifications.

### Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `ai screener not enabled` on curl | Missing env vars | Set `AI_SCREENER_ENABLED=true` and `STRATEGY_V2=true`, restart |
| `no strategies with screening descriptions` | TOMLs missing `[screening]` section | Check `configs/strategies/*.toml` for `[screening]` with `description` field |
| `no symbols survived pass0` | Filters too strict or market closed | Pre-market volume is 0 outside hours; lower `Pass0MinVolume` or test during pre-market (4-9:30 ET) |
| `all models failed` | OpenRouter rate limit or key issue | Check `LLM_API_KEY` is valid; free models have rate limits |
| `model failed, trying next` (warning) | One model unavailable | Normal — fallback chain handles it automatically |
| Results in DB but no effective symbols update | Symbol router not consuming AI events | Check logs for `ai screener: effective symbols resolved` |

### Configuration Defaults

These are applied automatically if not overridden in YAML:

| Setting | Default | Description |
|---------|---------|-------------|
| Models | qwen3-80b, step-3.5-flash, llama-3.3-70b (all `:free`) | LLM fallback chain |
| AI run time | 8:35 ET | When AI screener fires |
| Pass0 min price | $5 | Minimum stock price |
| Pass0 min volume | 10,000 | Minimum pre-market volume |
| Pass0 min gap% | 0% | Minimum absolute gap percentage |
| Max candidates/call | 20 | Symbols per LLM request |
| Top N per strategy | 10 | Best scores to keep |

Override via `configs/config.yml`:

```yaml
ai_screener:
  enabled: true
  models:
    - "qwen/qwen3-next-80b:free"
  ai_run_at_hour_et: 8
  ai_run_at_minute_et: 35
  pass0_min_price: 5.0
  pass0_min_volume: 10000
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
