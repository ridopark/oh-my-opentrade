# oh-my-opentrade

Professional-grade, broker-agnostic algorithmic trading ecosystem.

## Project Structure

```
backend/
  cmd/omo-core/       - Single binary MVP entry point
  internal/
    domain/           - Pure business logic, entities, events, value objects
    ports/            - Interface definitions (hexagonal boundaries)
    app/              - Application services (orchestrate domain + ports)
    adapters/         - Port implementations (Alpaca, TimescaleDB, etc.)
migrations/           - TimescaleDB SQL migration files
apps/dashboard/       - Next.js 15 frontend
deployments/          - Docker Compose & Dockerfiles
configs/              - App config & strategy DNA TOML files
```

## Architecture

This project follows **Hexagonal Architecture** (Ports & Adapters):

- **Domain layer** (`internal/domain/`) contains pure business logic with zero external dependencies
- **Ports** (`internal/ports/`) define interfaces that the domain needs
- **Adapters** (`internal/adapters/`) implement ports for specific technologies (Alpaca API, TimescaleDB, etc.)
- **Application services** (`internal/app/`) orchestrate domain logic through ports

## Code Standards

- **Backend**: Go with strict linting, hexagonal architecture patterns
- **Frontend**: Next.js 15, TypeScript, React
- **Testing**: `go test ./...` for backend, standard Jest/Vitest for frontend
- **Build**: `go build -o bin/omo-core ./cmd/omo-core` for backend

## Debugging

Loki is the primary log store for runtime debugging. Always check Loki logs when investigating production/paper trading issues (missed fills, unexpected behavior, order failures).

```bash
# Check Loki is running
curl -s http://localhost:3100/ready

# Query recent logs (plain text filter)
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "SEARCH_TERM"' \
  --data-urlencode 'limit=30' \
  --data-urlencode 'direction=backward'

# Trace a specific order by intent ID or broker order ID
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "ORDER_ID_HERE"' \
  --data-urlencode 'limit=50' \
  --data-urlencode 'direction=forward'
```

Logs are structured JSON. Parse with `python3 -c "import json, sys; ..."` to extract fields. Key components to filter: `execution`, `alpaca`, `position_monitor`, `risk_sizer`, `signal_debate_enricher`.

## Checking Current Positions & P&L

Query Alpaca paper API directly for live positions with unrealized P&L:

```bash
source /home/ridopark/src/oh-my-opentrade/.env && curl -s https://paper-api.alpaca.markets/v2/positions \
  -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
  -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -c "
import json, sys
positions = json.load(sys.stdin)
if not positions or isinstance(positions, dict):
    print(positions); sys.exit(0)
print(f'{\"Symbol\":<10} {\"Side\":<6} {\"Qty\":>12} {\"Avg Entry\":>12} {\"Current\":>12} {\"Mkt Value\":>14} {\"Unrealized P&L\":>16} {\"P&L %\":>8}')
print('-' * 92)
total_cost = total_mkt = total_pnl = 0
for p in sorted(positions, key=lambda x: abs(float(x.get('unrealized_pl', 0))), reverse=True):
    sym, side, qty = p['symbol'], p['side'], float(p['qty'])
    avg_entry, cur_price = float(p['avg_entry_price']), float(p['current_price'])
    mkt_val, pnl = float(p['market_value']), float(p['unrealized_pl'])
    pnl_pct, cost = float(p['unrealized_plpc']) * 100, float(p['cost_basis'])
    total_cost += cost; total_mkt += mkt_val; total_pnl += pnl
    print(f'{sym:<10} {side:<6} {qty:>12.6f} {avg_entry:>12.2f} {cur_price:>12.2f} {mkt_val:>14.2f} {pnl:>+16.2f} {pnl_pct:>+7.2f}%')
print('-' * 92)
total_pnl_pct = (total_pnl / total_cost * 100) if total_cost else 0
print(f'{\"TOTAL\":<10} {\"\":<6} {\"\":>12} {\"\":>12} {\"\":>12} {total_mkt:>14.2f} {total_pnl:>+16.2f} {total_pnl_pct:>+7.2f}%')
"
```

Cross-reference with OMO's trade DB to check for orphaned records:

```bash
# Net positions from OMO trade history (should match broker)
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
SELECT symbol,
       SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END) as net_qty,
       SUM(CASE WHEN side='BUY' THEN quantity*price ELSE 0 END) /
         NULLIF(SUM(CASE WHEN side='BUY' THEN quantity ELSE 0 END), 0) as avg_entry
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '30 days'
GROUP BY symbol
HAVING SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END) > 0.0001
ORDER BY symbol;
"
```

If OMO shows positions the broker doesn't have (orphaned records), insert reconciliation SELL trades to zero them out:

```bash
docker exec -i omo-timescaledb psql -U opentrade -d opentrade -c "
INSERT INTO trades (time, account_id, env_mode, trade_id, symbol, side, quantity, price, commission, status, strategy, rationale) VALUES
  (NOW(), 'default', 'Paper', gen_random_uuid(), 'SYMBOL', 'SELL', QTY, PRICE, 0, 'FILLED', 'reconciliation', 'cleanup: orphaned BUY with no broker position');
"
```

Symptom to look for in Loki: `bootstrap: OMO trade found but no broker position — skipping` from `position_monitor`.

## Key References

- [docs/PRD.md](docs/PRD.md) — Product Requirements Document
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — Detailed architecture documentation
- [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) — Implementation plan

## Development Commands

```bash
# Backend tests
cd backend && go test ./...

# Backend build
cd backend && go build -o bin/omo-core ./cmd/omo-core

# Run backend
cd backend && go run ./cmd/omo-core/

# Frontend dev server
cd apps/dashboard && npm run dev

# Docker Compose
docker compose -f deployments/docker-compose.yml up
```
