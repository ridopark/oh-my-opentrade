# Observability: Prometheus & Grafana

oh-my-opentrade ships with a full observability stack: **Prometheus** for metrics collection and alerting, **Grafana** for dashboards.

## Quick Start

```bash
# Start everything (including Prometheus + Grafana)
docker compose -f deployments/docker-compose.yml up -d
```

## Access Points

| Service | URL | Credentials |
|---------|-----|-------------|
| **Prometheus** | http://localhost:9090 | none (open) |
| **Grafana** | http://localhost:3001 | admin / admin (or `GRAFANA_ADMIN_PASSWORD` from `.env`) |
| **Raw metrics** | http://localhost:8080/metrics | omo-core exposes this endpoint directly |

## Architecture

```
omo-core :8080/metrics ──── scraped every 5s ────► Prometheus :9090
                                                        │
                                                        ▼
                                                   Grafana :3001
                                                   (auto-provisioned datasource)
```

- **Prometheus** scrapes `omo-core:8080/metrics` every 5 seconds (configured in `deployments/monitoring/prometheus.yml`)
- **Grafana** has Prometheus auto-provisioned as the default datasource via `deployments/monitoring/grafana/provisioning/datasources/prometheus.yml`
- A pre-built **"Trading Ops"** dashboard is auto-loaded from `deployments/monitoring/grafana/dashboards/trading-ops.json`
- Alert rules are defined in `deployments/monitoring/alerts.yml`

## Pre-built Dashboard: Trading Ops

The auto-provisioned dashboard includes:

### System & Health Row
- **Build Version** — current `omo_build_info` label
- **Uptime** — `time() - process_start_time_seconds`
- **Heap Alloc (MB)** — `go_memstats_alloc_bytes`
- **Goroutines** — `go_goroutines`

### HTTP API Row
- **Request Rate** — `rate(omo_http_requests_total[1m])` by method/route
- **Latency** — p50/p95/p99 from `omo_http_request_duration_seconds`
- **Status Codes** — `omo_http_requests_total` grouped by status code

### Trading Metrics
- Order flow, fill rates, reject rates
- Strategy signal counts
- P&L and equity tracking
- WebSocket connection health

## All Available Metrics

### Bars (Ingestion)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_bars_received_total` | counter | symbol | Total bars ingested |
| `omo_bar_processing_latency_seconds` | histogram | symbol | Time to process each bar |
| `omo_bars_dropped_total` | counter | symbol, reason | Bars rejected (e.g., Z-score anomaly) |
| `omo_bar_pipeline_queue_depth` | gauge | — | Current event queue depth |

### Strategy

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_strategy_signals_total` | counter | strategy, type, side | Signals emitted by each strategy |
| `omo_strategy_trades_total` | counter | strategy, side | Trades executed per strategy |
| `omo_strategy_loop_duration_seconds` | histogram | strategy, phase | Strategy processing latency |
| `omo_strategy_state` | gauge | strategy, state | Current lifecycle state per strategy |

### Orders (Execution)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_orders_total` | counter | broker, strategy, side, type, status | Orders submitted |
| `omo_order_submit_latency_seconds` | histogram | broker, strategy, type | Time from intent to broker submission |
| `omo_order_fills_total` | counter | broker, strategy, side, status | Fill events received |
| `omo_order_fill_latency_seconds` | histogram | broker, strategy | Time from submission to fill |
| `omo_order_rejects_total` | counter | broker, strategy, reason | Orders rejected (risk, slippage, etc.) |

### P&L

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_pnl_realized_usd` | gauge | — | Cumulative realized P&L |
| `omo_pnl_unrealized_usd` | gauge | — | Current unrealized P&L |
| `omo_pnl_day_usd` | gauge | — | Today's net P&L |
| `omo_drawdown_day_usd` | gauge | — | Today's max drawdown |
| `omo_equity_usd` | gauge | — | Current account equity |

### Risk

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_risk_circuit_breaker_trips_total` | counter | — | Circuit breaker trip events |
| `omo_risk_circuit_breaker_active` | gauge | — | 1 if circuit breaker is engaged |
| `omo_risk_checks_total` | counter | result | Risk validation outcomes |
| `omo_positions_shares` | gauge | symbol | Open position size per symbol |
| `omo_exposure_usd` | gauge | symbol | Dollar exposure per symbol |

### WebSocket (Market Data)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_ws_connected` | gauge | — | 1 if WS is connected, 0 if not |
| `omo_ws_reconnects_total` | counter | — | Total reconnection attempts |
| `omo_ws_messages_total` | counter | type | Messages received by type |
| `omo_ws_message_processing_duration_seconds` | histogram | type | Message handling latency |
| `omo_ws_last_message_timestamp_seconds` | gauge | — | Unix timestamp of last message |

### HTTP

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_http_requests_total` | counter | method, route, code | Total HTTP requests |
| `omo_http_request_duration_seconds` | histogram | method, route | Request latency |
| `omo_http_in_flight_requests` | gauge | — | Currently processing requests |

### Build / Runtime

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `omo_build_info` | gauge | version, commit, branch | Always 1; labels carry build metadata |
| `omo_runtime_info` | gauge | strategy_v2 | Always 1; labels carry feature flags |

## Pre-configured Alerts

Defined in `deployments/monitoring/alerts.yml`:

| Alert | Condition | Severity | Description |
|-------|-----------|----------|-------------|
| **CircuitBreakerActive** | `omo_risk_circuit_breaker_active == 1` | critical | Daily loss circuit breaker tripped — all trading halted |
| **WebSocketDisconnected** | `omo_ws_connected == 0` for 30s | critical | Market data feed is down |
| **NoBarsReceived** | `rate(omo_bars_received_total[5m]) == 0` for 5m | warning | No market data flowing through ingestion |
| **HighOrderRejectRate** | Reject rate >5% over 10m | warning | Too many orders failing validation |
| **HTTP5xxElevated** | 5xx rate >2% over 5m | warning | Backend errors spiking |

## Useful PromQL Queries

### System Health

```promql
# Is omo-core running?
omo_build_info

# Uptime
time() - process_start_time_seconds

# Memory usage
go_memstats_alloc_bytes / 1024 / 1024
```

### Market Data

```promql
# Bar throughput (bars/sec)
rate(omo_bars_received_total[1m])

# WebSocket connected?
omo_ws_connected

# Seconds since last WS message
time() - omo_ws_last_message_timestamp_seconds

# Dropped bars
rate(omo_bars_dropped_total[5m])
```

### Trading Activity

```promql
# Signals per minute by strategy
rate(omo_strategy_signals_total[5m])

# Order flow
rate(omo_orders_total[5m])

# Fill rate
rate(omo_order_fills_total[5m])

# Reject rate
rate(omo_order_rejects_total[5m])

# Order submit latency (p95)
histogram_quantile(0.95, rate(omo_order_submit_latency_seconds_bucket[5m]))
```

### P&L Monitoring

```promql
# Current equity
omo_equity_usd

# Day P&L
omo_pnl_day_usd

# Max drawdown today
omo_drawdown_day_usd

# Realized P&L
omo_pnl_realized_usd
```

### Risk

```promql
# Is circuit breaker active?
omo_risk_circuit_breaker_active

# Total exposure
sum(omo_exposure_usd)

# Positions by symbol
omo_positions_shares
```

## File Reference

```
deployments/
  docker-compose.yml                              # Prometheus + Grafana services
  monitoring/
    prometheus.yml                                 # Scrape config (5s interval)
    alerts.yml                                     # Alert rules
    grafana/
      provisioning/
        datasources/prometheus.yml                 # Auto-provision Prometheus datasource
        dashboards/default.yml                     # Dashboard file provider config
      dashboards/
        trading-ops.json                           # Pre-built Trading Ops dashboard

backend/internal/observability/metrics/
  metrics.go                                       # Registry + build/runtime info
  bars.go                                          # Bar ingestion metrics
  strategy.go                                      # Strategy signal/trade metrics
  orders.go                                        # Order execution metrics
  pnl.go                                           # P&L and equity metrics
  risk.go                                          # Risk and circuit breaker metrics
  ws.go                                            # WebSocket connection metrics
  http.go                                          # HTTP request metrics
```

## Configuration

### Prometheus
- **Scrape interval**: 5s (configurable in `deployments/monitoring/prometheus.yml`)
- **Retention**: 30 days (`--storage.tsdb.retention.time=30d`)
- **Reload**: Hot-reload enabled via `--web.enable-lifecycle` (POST to `http://localhost:9090/-/reload`)

### Grafana
- **Anonymous access**: Enabled as Viewer (no login needed to browse dashboards)
- **Admin password**: Set via `GRAFANA_ADMIN_PASSWORD` env var (default: `admin`)
- **Dashboard auto-refresh**: 30s scan interval for new/updated JSON files
