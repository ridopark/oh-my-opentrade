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

---

## Developer Workflow (Worktrees + Scripts)

Daily workflow for developing, shipping, and running omo-core locally. Three primary tools handle everything:

| Tool | What it does | When to use |
|------|--------------|-------------|
| `scripts/cc` | Create/reuse a git worktree and launch Claude Code in it | Start a new feature or experiment in isolation |
| `scripts/start.sh` + `scripts/shutdown.sh` | Stop and start `omo-core` + `omo-dashboard` tmux sessions | Bring services up/down manually |
| `/rebuild-commit-restart` skill | Build, commit, shutdown, restart in one shot | Ship a finished change to the running services |

### Worktree model: primary vs sandbox

**Exactly one primary worktree:**
```
/home/ridopark/src/oh-my-opentrade        ← main branch, live omo-core runs here
```

**All other worktrees are sandboxes:**
```
~/src/omo-worktree/<name>                 ← feature branches, edit-only
```

#### Rules

- **Primary only runs live omo-core.** It's the one with the active IBKR client_id=2 connection, port 8080 HTTP server, `omo-core` tmux session, and TimescaleDB writes.
- **Sandboxes are edit-only.** You can run `go build`, `go test`, backtests via HTTP, code refactors — but not `./scripts/start.sh`.
- **`start.sh` and `shutdown.sh` refuse to run from sandboxes by default.** They detect non-primary worktrees and exit with a helpful error. Override with `OMO_ALLOW_SANDBOX_START=1` or `OMO_ALLOW_SANDBOX_SHUTDOWN=1` if you know what you're doing.
- **`.env` is symlinked from primary to each sandbox on first use** (automatic via `cc` or `start.sh`). Edits to `.env` in primary propagate everywhere instantly.

#### Why the guard matters

If you run `./scripts/start.sh` from a sandbox with the live session running, you collide on:
- IBKR client_id (single-connection limit per id)
- HTTP port 8080
- Shared tmux session name `omo-core`
- TimescaleDB writes to the same `trades` / `orders` / `order_intents` tables

The safety guard in both scripts prevents this accident by refusing to run from non-primary worktrees.

---

### `scripts/cc` — new Claude Code session in a worktree

#### Setup (one time)

Add `scripts/` to your `PATH` so you can run `cc` from anywhere:

```bash
# in ~/.zshrc or ~/.bashrc
export PATH="/home/ridopark/src/oh-my-opentrade/scripts:$PATH"
```

Or alias it:
```bash
alias cc='/home/ridopark/src/oh-my-opentrade/scripts/cc'
```

#### Usage

```bash
cc                          # anonymous timestamped worktree
cc sprint-5                 # named worktree, create or reuse
cc sprint-5 --base main     # branch off a specific ref (default: main)
cc --list                   # list existing worktrees
cc --remove sprint-5        # remove a worktree (branch survives)
cc --shared                 # launch claude in current dir, no worktree
cc -- --continue            # pass everything after -- to claude
cc sprint-5 -- --continue   # create/reuse worktree + pass args to claude
cc --help                   # full help
```

#### What `cc <name>` does under the hood

1. Detects the primary worktree via `git worktree list --porcelain`
2. If `~/src/omo-worktree/<name>` exists → reuses it
3. Otherwise:
   - If branch `<name>` exists → `git worktree add` on that branch
   - Otherwise → `git worktree add -b <name> ... <base>` (default base: `main`)
4. Symlinks `.env` from primary if not already present
5. `cd` into the worktree
6. `exec claude` (replaces the shell process)

#### Typical daily pattern

```bash
# Start of day: what am I working on?
cc sprint-5                 # creates/resumes sprint-5 worktree + claude session

# ... work on sprint 5 ...

# End of session: worktree stays; branch stays; claude exits, shell returns

# Next day, same feature
cc sprint-5                 # resumes — same worktree, same branch

# Quick experiment
cc                          # timestamped scratch worktree

# When done, clean up
cc --remove sprint-5        # removes worktree, keeps branch
git branch -d sprint-5      # delete branch when truly done
```

---

### `/rebuild-commit-restart` skill — ship a change live

The idempotent orchestrator. Runs **build → commit → shutdown → start → verify**.

#### Trigger phrases

Ask Claude any of these:
- `/rebuild-commit-restart`
- `rebuild and restart`
- `ship it`
- `deploy local`
- `RCR`

#### Workflow

1. **Build** — `cd backend && go build -o bin/omo-core ./cmd/omo-core`. Stops on failure.
2. **Commit** — Claude drafts the message (lead with **why**, not what), stages, commits. Pre-commit hook `/simplify` reviews the staged diff.
3. **Shutdown** — `./scripts/shutdown.sh`. Sends Ctrl-C, waits **up to 45 seconds** for Sprint 1 drain.
4. **Restart** — `./scripts/start.sh`. Rebuilds, kills port 8080, launches `omo-core` + `omo-dashboard` tmux sessions.
5. **Verify** — checks both tmux sessions exist; tails logs if either is missing.

#### When to use

- You've finished and tested a Go change and want it running live
- You want to capture "this works" as a commit + active service in one motion
- **Only from the primary worktree.** The safety guard in `start.sh`/`shutdown.sh` blocks it from sandboxes.

#### When NOT to use

- You haven't tested the change — run `go test ./...` or a backtest first
- Market is open and you have an active position in a volatile state — the 30-40s shutdown window might coincide with a stop-loss trigger
- The change is in a sandbox — merge to main first, then `/rcr` from primary

#### Timing expectations

| Shutdown state | Total duration |
|----------------|----------------|
| No open orders (weekend, pre-market) | ~5–10 seconds |
| 1–2 orders that cancel quickly | ~10–20 seconds |
| Orders IBKR won't immediately cancel | ~35–45 seconds |
| Build fails | ~2 seconds (early abort) |

---

### `scripts/start.sh` — bring services up

Run from the **primary worktree only**:

```bash
./scripts/start.sh
```

Does:
1. Worktree safety check (refuses if not primary, override: `OMO_ALLOW_SANDBOX_START=1`)
2. `.env` symlink bootstrap (only creates if missing)
3. Kills zombie `omo-core` processes holding Alpaca WS connections
4. Kills anything on port 8080 and port 8000
5. Builds `backend/bin/omo-core`
6. Starts `omo-core` in tmux session
7. Starts `omo-dashboard` (`npm run dev`) in tmux session

Skips with a warning if either tmux session already exists — expects a clean slate. To restart from a running state, use `/rebuild-commit-restart` or manually run `shutdown.sh` first.

#### Useful tmux commands after start

```bash
tmux attach -t omo-core         # view backend logs (Ctrl-b d to detach)
tmux attach -t omo-dashboard    # view dashboard logs
```

---

### `scripts/shutdown.sh` — bring services down

Run from the **primary worktree only**:

```bash
./scripts/shutdown.sh
```

Does:
1. Worktree safety check (refuses if not primary, override: `OMO_ALLOW_SANDBOX_SHUTDOWN=1`)
2. Stops any Docker `omo-core` container
3. Kills anything on port 8080
4. Sends Ctrl-C to `omo-core` tmux session
5. **Waits up to 45 seconds** for graceful shutdown (Sprint 1 drain up to 30s + 5s HTTP + 10s buffer)
6. Logs progress at 15s and 30s so you know it isn't hung
7. Kills the tmux session after the process exits (or after 45s hard deadline)
8. Same sequence for `omo-dashboard`
9. Leaves monitoring stack (Grafana, Prometheus, Loki, Fluent Bit) running

#### Why 45 seconds matters

Sprint 1 Phase B added an order drain that polls `ib.OpenTrades()` for up to 30 seconds to give IBKR fill callbacks time to land before the socket closes. The pre-Sprint-3 shutdown waited only 15 seconds — `tmux kill-session` would SIGHUP the still-draining process and corrupt in-flight order state. **The 45s wait preserves the drain and prevents silently losing fills across restart.**

---

### `scripts/start-infra.sh` — monitoring stack

Separate from the main services:

```bash
./scripts/start-infra.sh        # start Grafana, Prometheus, Loki, Fluent Bit
./scripts/stop-infra.sh         # stop the monitoring stack
```

Normally left running 24/7 — only start/stop if you need to free resources.

---

### Common tasks — cheatsheet

#### "I want to start a new feature"
```bash
cc feat/my-new-feature
# claude launches in ~/src/omo-worktree/feat/my-new-feature
# .env is symlinked automatically; new branch created off main
```

#### "I want to continue a feature from yesterday"
```bash
cc feat/my-new-feature          # same command — reuses existing worktree
```

#### "I want to ship my changes to the running live omo-core"
From the **primary** worktree:
```
/rebuild-commit-restart
```

#### "I want to stop everything"
```bash
./scripts/shutdown.sh           # from primary
```

#### "I want to see what worktrees exist"
```bash
cc --list
```

#### "I want to clean up an old worktree"
```bash
cc --remove old-feature
git branch -d old-feature       # optional: delete the branch too
```

#### "I accidentally ran start.sh from a sandbox and it refused — now what?"
That's the safety guard working correctly. Two options:

1. **Recommended:** merge your changes to main, then `/rebuild-commit-restart` from primary.
2. **Escape hatch:** stop the primary's omo-core first, then from the sandbox:
   ```bash
   OMO_ALLOW_SANDBOX_START=1 ./scripts/start.sh
   ```
   You're responsible for ensuring no collisions (distinct client_id, distinct port, distinct tmux name). Rarely needed.

#### "Can I run two `omo-core` binaries at the same time?"
**Not by default.** They'll collide on IBKR client_id=2, HTTP port 8080, and tmux session name `omo-core`. To do it safely you'd need per-worktree `.env` overrides (distinct `IBKR_CLIENT_ID`, distinct `OMO_HTTP_PORT`) and a renamed tmux session, all managed manually. Not recommended unless you have a specific reason.

---

### Troubleshooting

#### `omo-core panicked on shutdown with alpaca SIGSEGV`
Already fixed in commit `5a8f6a8` (alpaca nil-guard in `adapter.go:Close`). If you see it, your binary is stale — rebuild via `/rebuild-commit-restart` or `./scripts/start.sh` (which rebuilds automatically).

#### `shutdown.sh hangs for 30+ seconds`
Working as intended — the Sprint 1 drain is waiting for IBKR fill callbacks on a working order. Progress logs at 15s and 30s confirm it's waiting, not hung. Full timeout is 45s.

#### `cc refuses to create a worktree because I'm not in a git repo`
Run `cc` from anywhere inside your primary checkout (or any existing worktree). It uses `git worktree list` to find the primary.

#### "The dashboard tmux session won't start"
Usually port 8000 is already in use. `scripts/start.sh` tries to free it via `kill_port 8000`, but if that fails, do it manually:
```bash
lsof -ti :8000 | xargs kill -9
./scripts/start.sh
```

#### "I want to see the live omo-core logs"
```bash
tail -f logs/omo-core.log       # from primary worktree
# or
tmux attach -t omo-core         # live view, Ctrl-b d to detach
```

#### "How do I check the order journal is writing during live trading?"
```bash
docker exec omo-timescaledb psql -U opentrade -d opentrade \
  -c "SELECT status, COUNT(*) FROM order_intents GROUP BY status ORDER BY 1;"
```
Expect `submitted` + `filled` during active sessions. Any `rejected` rows with `submit_error IS NOT NULL` deserve investigation.

---

### References

- [`docs/plans/ROADMAP.md`](plans/ROADMAP.md) — sprint index + what's next
- [`docs/plans/SPRINT_1_PLAN.md`](plans/SPRINT_1_PLAN.md) — robustness quick wins (shipped)
- [`docs/plans/SPRINT_2_PLAN.md`](plans/SPRINT_2_PLAN.md) — order journal + reconciliation (shipped)
- [`docs/plans/SPRINT_3_5_PLAN.md`](plans/SPRINT_3_5_PLAN.md) — flag removal (next)
- [`docs/plans/SPRINT_4_PLAN.md`](plans/SPRINT_4_PLAN.md) — risk management gates (planned)
- [`.claude/skills/rebuild-commit-restart/SKILL.md`](../.claude/skills/rebuild-commit-restart/SKILL.md) — the RCR skill
- [`scripts/cc`](../scripts/cc) — worktree launcher source
- [`scripts/start.sh`](../scripts/start.sh) — service start
- [`scripts/shutdown.sh`](../scripts/shutdown.sh) — service stop
