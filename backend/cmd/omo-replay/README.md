# omo-replay

Historical market data replay and backtest engine for oh-my-opentrade.

`omo-replay` replays stored 1m bars from TimescaleDB through the same event-driven pipeline used by the live trading binary (`omo-core`). It operates in two modes: **replay** (signal-only observation) and **backtest** (full execution with SimBroker).

## Modes

### Replay Mode (default)

Replays bars through the strategy pipeline and logs generated signals and order intents **without executing trades**. Order intents are logged via a mock handler. Useful for validating strategy logic and indicator behavior on historical data.

```bash
go run ./cmd/omo-replay/ \
  --from 2025-06-02 --to 2025-06-03 \
  --symbols SPY \
  --env-file /path/to/.env
```

### Backtest Mode (`--backtest`)

Wires the **full execution pipeline** — SimBroker, risk engine, slippage guard, kill switch, daily loss breaker, position gate, position monitor, and trade collector — achieving pipeline parity with `omo-core`. Produces a performance report with trade-level results.

```bash
cd /home/ridopark/src/oh-my-opentrade/backend/cmd
go run ./omo-replay/ \
  --backtest \
  --from 2026-03-09 --to 2026-03-12 \
  --symbols AAPL,MSFT,GOOGL,AMZN,TSLA,SPY,META,NVDA \
  --initial-equity 100000 \
  --slippage-bps 5 \
  --no-ai \
  --config /home/ridopark/src/oh-my-opentrade/configs/config.yaml \
  --env-file /home/ridopark/src/oh-my-opentrade/.env \
  --output-json results.json \
  > test.log 2>&1


go run ./cmd/omo-replay/ \
  --backtest \
  --from 2025-06-02 --to 2025-06-03 \
  --symbols SPY \
  --initial-equity 100000 \
  --slippage-bps 5 \
  --no-ai \
  --output-json /tmp/results.json \
  --env-file /path/to/.env
```

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--symbols` | config file | Comma-separated symbols to replay (e.g. `SPY,QQQ`) |
| `--from` | *(required)* | Start time — `YYYY-MM-DD` or RFC3339 |
| `--to` | now | End time — `YYYY-MM-DD` or RFC3339. Use the **next day** for same-day replay |
| `--speed` | `max` | Replay speed: `max`, `1x`, `10x`, or any float (e.g. `2.5`) |
| `--config` | `configs/config.yaml` | Path to YAML config file |
| `--env-file` | `.env` | Path to `.env` file with DB credentials |
| `--backtest` | `false` | Enable backtest mode with SimBroker execution |
| `--initial-equity` | `100000` | Starting account equity for backtest |
| `--slippage-bps` | `5` | Slippage in basis points for SimBroker fills |
| `--output-json` | *(none)* | Write backtest results as JSON to this path |
| `--no-ai` | `true` | Disable AI signal debate enricher — uses passthrough instead |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `LOG_LEVEL` | Zerolog level: `debug`, `info`, `warn`, `error` |
| `LOG_PRETTY` | Set to `true` for human-readable log output |

## Pipeline Architecture

Both modes share the same startup sequence:

```
TimescaleDB ──► Bar Loading ──► Warmup ──► Replay Loop
```

### Warmup Phase

Before replay begins, the engine:

1. **Indicator warmup** — Loads bars from the previous RTH session (last 120 bars) to seed VWAP, RSI, EMA, Stochastic, and volume indicators
2. **Spike filter seeding** — Seeds the adaptive bar filter with historical data to avoid false rejections
3. **Strategy runner warmup** — Feeds warmup bars through each strategy's indicator calculator so strategies start with warm state

### Replay Loop

Bars are replayed in timestamp-synchronized order across all symbols:

```
For each timestamp group:
  1. Update mutable clock (atomic.Value) to bar time
  2. Reset MTFA aggregators on new trading day boundary
  3. Feed SimBroker the bar close price (backtest only)
  4. Publish MarketBarReceived event for each symbol
  5. WaitPending() — drain all async event handlers (backtest only)
  6. EvalExitRules() — check position exit conditions (backtest only)
  7. WaitPending() — drain exit-triggered events (backtest only)
  8. Apply speed delay (if not max speed)
```

### Backtest Event Chain

In backtest mode, the full event chain mirrors `omo-core`:

```
MarketBarReceived
  ├─► Ingestion (sanitize, filter)
  ├─► Monitor (indicators, regime, MTFA aggregation)
  ├─► Strategy Runner ──► SignalCreated
  │     └─► Signal Passthrough (--no-ai) ──► SignalEnriched
  │         └─► RiskSizer ──► OrderIntentCreated
  │             └─► Execution Service (validate → submit)
  │                 ├─► SimBroker (fill with slippage)
  │                 ├─► Risk Engine, SlippageGuard, KillSwitch
  │                 ├─► DailyLossBreaker, PositionGate
  │                 └─► Backtest Collector (aggregate results)
  └─► Position Monitor (price cache, exit rules)
```

### Key Design Decisions

- **Mutable clock**: An `atomic.Value`-based clock is updated per bar group and injected into all components. This ensures time-dependent logic (position monitor, risk engine) uses bar time, not wall clock.
- **WaitPending synchronization**: After each bar group, `eventBus.WaitPending()` drains all in-flight async handlers before proceeding, ensuring deterministic sequential execution.
- **Signal passthrough**: When `--no-ai` is set (default), a lightweight subscriber converts `SignalCreated` → `SignalEnriched` with `EnrichmentSkipped` status, bypassing the LLM debate enricher while keeping the downstream pipeline intact.
- **Shared bootstrap builders**: Both `omo-core` and `omo-replay` use the same `bootstrap` package to construct execution, strategy, position monitor, and ingestion services — ensuring pipeline parity by construction.

## Output

### Console Summary

Every run prints a summary:

```
=== REPLAY SUMMARY ===
Bars processed: 393
Timestamp groups: 385
Signals created: 3
Order intents created: 3

Events by type:
- FillReceived: 1
- MarketBarReceived: 393
- MarketBarSanitized: 491
- OrderIntentCreated: 3
- OrderIntentRejected: 2
- OrderIntentValidated: 1
- OrderSubmitted: 1
- RegimeShifted: 48
- SignalCreated: 3
- SignalEnriched: 3
- StateUpdated: 385
```

### JSON Output (backtest only)

With `--output-json`, a detailed report is written including:

- Per-trade entry/exit prices, P&L, and duration
- Aggregate metrics (total P&L, win rate, max drawdown)
- Equity curve data points

## Prerequisites

- **TimescaleDB** with historical bar data loaded
- **Strategy specs** in `configs/strategies/` (TOML files defining strategy parameters)
- **Config file** (`configs/config.yaml`) with database connection and symbol configuration

## Limitations

- **SimBroker fill model**: Fills are instant at close ± slippage. No next-bar fills, volume-aware sizing, or partial fills (explicitly deferred).
- **Single account only**: No multi-account orchestrator support.
- **No AI caching**: When `--no-ai=false`, LLM calls are made live — no deterministic debate replays.
