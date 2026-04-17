# oh-my-opentrade

**Algorithmic trading, built like infrastructure.**

A broker-agnostic, hexagonal-architecture trading system for US equities, options,
and crypto. Three equity/options strategies (AVWAP, MACD, Overnight Z) plus a crypto
mean-reversion (crypto_revert_v1) run on the hot path, eight gates enforce risk, and
every order intent is journaled before the broker sees it so the system can recover
cleanly from a hard crash.

- Single Go binary, Next.js dashboard, TimescaleDB, NATS event bus
- Alpaca + Interactive Brokers adapters; IBKR options trade live daily in paper
- Dark-pool and 13F whale accumulation confluence scoring
- LLM-augmented Bull/Bear/Judge debate enriches every entry signal
- Broker-authoritative equity curve sampled every minute to match IBKR's own NetLiq
- Crypto uses IBKR warm market-data subscriptions to keep the uscrypto farm alive
  for slippage checks between infrequent signals

See [docs/PRD.md](docs/PRD.md), [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md),
and [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md) for deeper context.

## What it does

**Data**
- Real-time 1m/5m/15m bar streaming from Alpaca WebSocket with 4-sigma sanitization
- Historical backfill from Alpaca REST + YahooFinance
- Dark-pool print aggregation from SIP trade feed (5m bars: volume, buy/sell,
  large prints, VWAP)
- Late-session DP Z-score indicator (14:00-15:30 ET buy ratio, 20-day rolling
  normalization) for next-day directional bias
- Whale accumulation scoring from SEC 13F filings
- Historical options chains sourced from DoltHub
- Earnings calendar from Finnhub (daily refresh, 90-day lookahead)

**Strategies (3 active + 1 crypto + pipeline)**
- **AVWAP** — anchored VWAP mean reversion from 5m/15m regime extremes, with
  late-session dark-pool Z-score entry suppression (Sharpe +0.39)
- **MACD** — crossover with swing stops, dark-pool block-flow veto, and inverted
  Z-score regime filter that blocks entries in losing reversal regimes (PF +0.08)
- **Overnight Z-Score Bias** — daily-horizon strategy using late-session DP buy_ratio
  Z-score as a next-day directional signal. Equity shares only, no options. Staged
  for paper validation.
- **crypto_revert_v1** — BTC/ETH/SOL mean reversion on IBKR spot, 5m bars, uses
  warm IBKR market-data subscriptions so the slippage guard never waits on the
  uscrypto farm cold-start.
- **MFT crypto pipeline** — four additional designs (pairs, funding-timer,
  basis-carry, xsec-momo) documented in `docs/MFT-crypto-strategies/` with
  shared-infra and engine-change plans.
- Confluence layer combines technical, dark-pool, and 13F signals into a 0-100 score
  that gates every entry
- **Late-session DP Z conditioning** — the 14:00-15:30 ET dark-pool buy ratio,
  normalized over 20 trading days, predicts next-day returns (IC=-0.039, t=-3.92).
  AVWAP and MACD respond oppositely: AVWAP benefits from low-Z days (mean-reversion
  tailwind), MACD benefits from high-Z days (momentum continuation). Each strategy
  applies the signal in its own direction.

**AI-augmented decision layer**
- **Bull/Bear/Judge debate** — every entry signal is enriched by a structured
  adversarial LLM debate. A Bull agent argues the long thesis, a Bear agent
  argues the opposing case, and a Judge agent rules with JSON-structured output:
  `direction`, `confidence (0-1)`, `risk_modifier (TIGHT|NORMAL|WIDE)`, and
  `rationale`. The Judge can veto the trade outright.
- **Signal enrichment port** — `SignalEnrichment` is a first-class domain type
  with explicit status (`ok | timeout | error | skipped | vetoed`). When the
  LLM is unreachable or times out, strategies fall back to deterministic rules
  — AI never blocks the hot path.
- **Provider-agnostic** — OpenAI-compatible endpoint works with OpenRouter,
  Anthropic, Ollama, and vLLM. Switch providers with a config change, no code.
- **Context injection** — functional options let the pipeline attach option
  chains, recent news (Finnhub), strategy performance history, and signal
  metadata to the debate prompt without coupling the debate to any strategy.
- **LLM anchor selection** — Anchored-VWAP candidates (swing highs, volume
  rotations, weekly opens) are ranked by an LLM with confidence scores before
  the numerical AVWAP engine computes the band. The LLM picks *which* anchor
  matters; the deterministic engine decides *when* to fire.
- **Privacy boundary** — the LLM sees only public market data (RSI, Stoch,
  VWAP, EMA, regime type, confluence score). Strategy DNA, parameters, and
  position sizing never cross the port.
- **Risk modifier feedback** — the Judge's `TIGHT|NORMAL|WIDE` ruling flows
  back into the risk engine to tighten or loosen stops and size, giving the
  LLM a graduated voice rather than a binary veto.

This is the layer that turns OMO from a deterministic system into an
opinionated one. The numeric confluence score tells us *whether* the setup is
clean; the debate tells us *whether the story makes sense right now*.

**Execution (8 gates)**
- short_direction, exposure_guard, portfolio_guard, risk_engine, slippage_guard,
  trading_window, spread_guard, buying_power_guard
- Write-ahead order journal survives crashes
- Startup reconciliation matches broker open orders against the journal before any
  new decision — protective stops are never blindly cancelled
- SystemD watchdog + Docker HEALTHCHECK for auto-restart

**Slippage control on exits**
- **Spread-aware exit pricing** — option exits read live bid/ask and price the
  limit at `mid − k·spread`, where `k` scales with days-to-expiry (0.25 ≥14d,
  0.35 5-14d, 0.45 <5d). Replaces the old blind 5%-off-mid formula that landed
  below the bid on sub-$3 premiums and forced dust-sweep fallback. Blown-spread
  guard (`spread/mid > 0.25`) falls back to a fixed-bps cap.
- **Asymmetric timeout + re-peg toward mid** — stops timeout fast (10s, 1 re-peg)
  to protect capital; targets get 30s and up to 3 re-pegs that tighten toward
  mid by one tick each attempt. Wall-time capped at 120s, with a no-re-peg
  override in the last 15 min to close for deterministic EOD liquidation.
- **Cancel-await-terminal** — broker cancel is awaited to terminal status and
  the exit-pending gate clears only after confirmation. Eliminates the
  cancel/resubmit race that previously produced `position_gate: no_position_to_exit`
  rejections and forced dust-sweep fallback mid-attempt.
- **Marketable-limit dust sweep** — the last-resort sweep submits a marketable
  limit at `max(bid − tick, bid·(1 − 150bps))` with a spread-adaptive floor
  (`max(150bps, spread/2)`), then falls back to true market after 15s if
  unfilled. Halt detection (`bid==0`) and near-close override skip the limit
  phase to avoid OCC exercise-by-exception on 0DTE ITM contracts.
- **Compliance-safe attribution** — dust-sweep fills keep `Strategy="dust_sweep"`
  on the raw ledger row (SEC 17a-4 / FINRA 4511 immutability), but the origin
  strategy is threaded through the rationale and the `FillReceived` event
  payload. Per-strategy P&L queries credit the origin; audit queries still see
  the raw broker-authoritative record.
- **Single-attempt circuit breaker accounting** — re-pegs under one exit attempt
  collapse into one unit for the exit-failure breaker so multi-attempt fills
  don't inflate failures 4x and false-trip the symbol lock.

**Backtest**
- Full event-bus backtester with isolated SimBroker
- Parameterized bar aggregation, per-symbol session tracking
- Black-Scholes options pricing with tiered bid-ask spread, IV calibration,
  and dynamic IV adjustments (VIX-beta scaling, time-of-day seasonality,
  earnings IV ramp from Finnhub calendar)
- Per-trade, per-day, per-symbol P&L; Sharpe, Sortino, max drawdown
- **86% faster** after a 5-phase optimization sprint (see below)

**Operations**
- TimescaleDB hypertables for bars, trades, equity snapshots
- Prometheus metrics, Loki structured logs
- Discord/Telegram webhooks for trades and risk alerts
- **Broker-authoritative equity curve** — a per-minute sampler writes
  `equity_curve` rows from live IBKR NetLiquidation, so the Portfolio page's
  chart matches IBKR's own statement (no OMO-internal accounting on top).
  Attribution P&L lives separately in `daily_pnl`.
- **Gap detector** — omo-data runs a startup scan that diffs expected vs
  persisted bars per (symbol, timeframe) using the domain calendar (NYSE
  sessions, early closes, crypto 24/7), surfacing silent coverage holes
  before they poison backtests or live replay.

## Backtest performance

A five-phase optimization sprint reduced wall-time by **86%** across all workloads while
preserving deterministic signal-count parity:

| Workload | Before | After | Reduction |
|---|---|---|---|
| 30 symbols / 1 year | ~130 s | **18 s** | -86% |
| 8 symbols / 1 year | 31.6 s | **5.0 s** | -84% |
| 8 symbols / 3 months | 10.1 s | **1.9 s** | -81% |

Each phase was gated by a benchmark — we only advanced when the prior phase wasn't enough:

1. **Phase 1 — Direct dispatch.** Replaced the pub/sub event bus with direct function calls
   (`ingestion.ProcessBar → monitor.HandleMarketBar → runner.HandleBarDirect`), shrunk
   indicator windows from 250 → 60 bars, and reordered struct layouts for cache locality.
2. **Phase 2 — Shard infrastructure.** Cloned monitor + runner per shard so each goroutine
   owns disjoint mutable state. This phase missed its gate (per-tick barriers cost more than
   the work they parallelized) but delivered the sharding scaffolding Phase 3 needed.
3. **Phase 3 — Slice-to-completion.** Each shard runs its full bar stream to completion with
   no per-tick synchronization, then a k-way merge replays signals in tick order. Eliminated
   240 k barrier wake-ups per year of data.
4. **Phase 4+5 — Allocation reduction.** Typed hot paths bypass `domain.Event` construction.
   Pre-sized slabs, zero-alloc ingestion, and eliminated idempotency-key string concatenation
   cut total allocations from 19 GB → 9.7 GB per run. GC CPU dropped from 12 s → 8 s.

See [docs/perf/README.md](docs/perf/README.md) for the full roadmap, pprof analysis, and
commit history.

## Strengths

- **Hexagonal architecture** — ports and adapters cleanly separate the Go core from
  every external system. Swapping Alpaca for IBKR, in-memory bus for NATS, or backtester
  for live is a two-hour adapter rewrite, not a refactor.
- **Persistence-first order model** — order intents are journaled *before* the broker
  API is called. Startup reconciliation guarantees zero orphaned stops on crash.
- **Dark-pool + 13F confluence** — trades only fire when technical, dark-pool, and
  institutional positioning agree. Empirically cuts churn versus single-indicator
  systems.
- **LLM debate as a second opinion** — adversarial Bull/Bear/Judge enrichment
  reviews every entry with structured JSON output (direction, confidence, risk
  modifier). Deterministic strategies produce the signal; the LLM stress-tests
  the thesis and can veto, tighten, or widen risk. Graceful fallback preserves
  hot-path reliability.
- **Late-session DP Z conditioning** — late-hour (14:00-15:30 ET) dark-pool flow
  predicts next-day returns. AVWAP and MACD each apply the signal in their own
  direction (mean-reversion vs momentum), validated across 13,204 trades.
- **Multi-timeframe regime detection** — 5m/15m anchors classify market state
  (TREND/BALANCE/REVERSAL) before 1m entries fire, filtering regime-mismatch churn.
- **Crash resilience** — order journal + broker reconciliation + position monitor
  tick + watchdog auto-restart. Recovery in under 30 seconds.

## Honest limitations

1. **Options IV model is first-order, not stochastic.** Multi-day holds use real
   historical bids (DoltHub). Same-day exits adjust IV via VIX-beta scaling (0.7),
   time-of-day seasonality (U-shape), and earnings ramp (Finnhub calendar). These
   capture ~60% of IV variance but miss idiosyncratic events, skew dynamics, and
   intraday VIX-stock decorrelation.
2. **IBKR live execution (funded) is still pending.** The adapter is fully
   implemented and runs a full paper session daily on IBKR — equity and options
   entries, exits, and dust-sweep reconciliation all execute end-to-end through
   the real IBKR gateway against a paper account. Funded-account validation is
   the remaining step.
3. **Universe is 34 hardcoded symbols.** Adding a symbol currently requires a code
   redeploy. Dynamic discovery is not on the roadmap yet.
4. **Single-broker dependency during outages.** Alpaca outages halt the system;
   multi-broker failover is not architected yet.

## Architecture

```
                           ALPACA
                             |
                SEC 13F  --- | ---  IBKR
                         \   |   /
                          +--+--+
                         /       \
              DOLTHUB --|  CORE   |-- TIMESCALE
                         \       /
                          +--+--+
                             |
                          NATS BUS

CORE = strategy engine + execution gates + order journal
Every edge is a port; every label is a swappable adapter.
```

## Quick start

```bash
# Run tests
cd backend && go test ./...

# Build the core binary
cd backend && go build -o bin/omo-core ./cmd/omo-core

# Run the full stack with Docker Compose
docker compose -f deployments/docker-compose.yml up

# Or run core + dashboard locally
cd backend && go run ./cmd/omo-core/
cd apps/dashboard && npm run dev

# Convenience scripts
./scripts/start.sh      # start everything
./scripts/shutdown.sh   # stop everything

# Logs
tmux attach -t omo-core       # backend logs
tmux attach -t omo-dashboard  # dashboard logs
```

The landing page lives at `/` in the dashboard. The signal monitor moved to `/signals`.

## Project structure

```
backend/
  cmd/omo-core/       Single binary entry point
  internal/
    domain/           Pure business logic, entities, events, value objects
    ports/            Interface definitions (hexagonal boundaries)
    app/              Application services (orchestrate domain + ports)
    adapters/         Port implementations (Alpaca, IBKR, TimescaleDB, ...)
migrations/           TimescaleDB SQL migrations
apps/dashboard/       Next.js 15 frontend
deployments/          Docker Compose + Dockerfiles
configs/              App config + strategy DNA TOML files
docs/plans/           Sprint plans and roadmap
```

## Roadmap

See [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md). Sprint 3.5 (journal flag
removal) is gated on 24h live validation. Sprint 4 (risk-management gates +
3-state kill switch) is queued next.
