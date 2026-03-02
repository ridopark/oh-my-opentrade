# Product Requirements Document (PRD): oh-my-opentrade (v11.0 – Unified Architecture)

## 1. Product Vision: The Self-Evolving Multi-Tenant Quant Lab
**oh-my-opentrade** is a professional-grade, broker-agnostic algorithmic trading ecosystem. 

The platform operates on a continuous 24-hour cycle of:
1. Pre-Market Screening 
2. Deterministic, multi-account execution 
3. Post-Market AI Research & Strategy Evolution

**Core Principles:**
* AI is used for reasoning—not for high-frequency decisions. 
* Deterministic Go code is the source of truth for all real-time logic. 
* Adversarial AI agents debate only when triggered by a market state change. 
* Strict risk boundaries prevent AI hallucination from creating exposure. 
* Strategies evolve nightly using performance-driven "Strategy DNA." 

The system runs fully containerized on an Oracle Cloud ARM VM (4 OCPUs, 24 GB RAM) optimized for 24/7 reliability.

---

## 2. Technical Stack & Infrastructure

### Backend
* **Golang** (Hexagonal Architecture) 
* Deterministic "State Machine Monitor" 
* Native technical indicator & regime detection engine 
* **Yaegi** for hot-swappable TOML strategy DNA 
* Real-time Pub/Sub event bus (market → state → AI → execution)
* **Rate Limit Governor:** A pacing proxy queue to ensure REST requests (like startup hydration) never breach the broker's 200 req/min limits.

### Frontend
* **Next.js 15** (App Router) 
* TypeScript, Tailwind CSS, shadcn/ui, TanStack Query 
* Real-time visualizations: market states, debates, DNA changes, executions

### Database
* **TimescaleDB** (PostgreSQL) 
* Partitioned by `account_id` + `env_mode` (Paper/Live) 
* **Indexing Strategy:** Hypertable compression enabled for data older than 7 days to preserve VM disk space and query speed.
* Tables: `MarketBars`, `Trades`, `ThoughtLogs` (JSONB), `StrategyDNAHistory`

### AI Layer
* **OpenCode SDK** * Flagship reasoning models (Claude 3.7 / Gemini 3.0 Pro) 
* Adversarial agents (Bull, Bear, Judge)

---

## 3. The 24-Hour Autonomous Lifecycle

| Time (EST) | Phase | Description |
| :--- | :--- | :--- |
| **08:30 – 09:15** | **Screener** | AI scans universe for "Stocks in Play" (Gap %, RVOL, news). |
| **09:15 – 09:30** | **Approval** | User reviews overnight "Strategy DNA Upgrades." |
| **09:30 – 16:00** | **Execution** | Go backend executes MTFA strategies across all accounts. |
| **17:00 – Nightly** | **Evolution** | AI analyzes trades, ThoughtLogs, updates DNA, runs backtests (5 bps slippage). **Corporate Action Check:** Filters out tickers with upcoming splits/dividends to prevent math errors. |

---

## 4. Multi-Tenant Strategy Engine
Designed to run multiple trading accounts (Paper and Live) simultaneously with strict isolation.

### 4.1 Data Sanitization Layer
A deterministic Go module removes unreliable market data using a 4-Sigma Z-Score filter.
* If price movement exceeds 4 std dev from the 30-period mean **without matching volume**, the bar is flagged as "Suspect" and excluded.

### 4.2 Multi-Timeframe Analysis (MTFA)
* **Anchor (5m/15m):** Defines macro regime (trend, balance, reversal zones). 
* **Trigger (1m):** Identifies entries, exits, pullbacks, stop placement.

### 4.3 Pluggable Strategy Modules
1. **Opening Range Breakout (ORB) – Break & Retest** 2. **Anchored VWAP (AVWAP)** 3. **AI-Enhanced Scalping Signals** (RSI/Stoch mean-reversion aligned with 5m regime) 
4. Custom strategies hot-swapped using Yaegi.

---

## 5. The Dual-Layer AI Syndicate

### Phase 1: Deterministic Go Market Monitor
Go code continuously computes:
* Technical indicators (RSI, Stochastics, EMAs, VWAP) 
* Volume anomalies & Regime shifts 
* Breakout conditions & Retest structures 

When a valid condition emerges, it emits an **LLM Trigger Event**. This keeps API usage minimal, provides stable filtering, and prevents AI hallucination.

### Phase 2: Adversarial AI Reasoning (OpenCode SDK)
Triggered only when Phase 1 detects a valid setup.
* **Bull Agent:** Argues strongest long thesis based on structured JSON input.
* **Bear Agent:** Argues strongest short or abort thesis.
* **Judge Agent:** Evaluates arguments, assigns a Confidence Score, and outputs a strictly typed `OrderIntent` JSON (`direction`, `limit_price`, `stop_loss`, `max_slippage_bps`, `rationale`).

---

## 6. Security & Execution Guarantees (Kill Switch)

### Deterministic Go Risk Engine
AI cannot directly send orders. Every `OrderIntent` passes through a non-bypassable safety layer:
* Max 2% risk per trade 
* Mandatory stop-loss & LIMIT orders only 
* Reject malformed or hallucinated values

### Micro-Circuit Breakers (Global Halt)
If 3 active tickers hit their Stop-Loss within a 2-minute window, the Go engine triggers a 15-minute system-wide halt to protect capital from sudden flash crashes or news events.

### Slippage Guard
Live bid/ask is compared against AI's `limit_price`. If slippage exceeds `max_slippage_bps` → trade aborted.

### API Key Isolation & Account Partitioning
* Keys injected through `.env` at runtime; never stored in DB or sent to AI. 
* All historical tables are strictly partitioned by `account_id` and `env_mode` (Paper vs Live).

### Notifications
Real-time pings via Telegram/Discord Webhook for trades, DNA upgrades, kill-switch events, and debate summaries.

---

## 7. User Interface (Next.js Dashboard)
Key features:
* Live Bull vs Bear debate feed 
* MTFA regime visualization & OrderIntent display with Confidence Scores 
* Strategy DNA diffs (Old vs New) for morning approvals
* Multi-account execution monitor & System health stats 

---

## 8. Infrastructure & Deployment
Docker Compose orchestration for:
* `api-gateway`
* `executor`
* `state-machine-monitor`
* `strategy-engine`
* `timescaledb`
* `nextjs-dashboard`
* `opencode-adapter`

---

## 9. Implementation Priority List
1. **TimescaleDB schema** (multi-account, ThoughtLogs, Hypertable compression).
2. **Alpaca ingestion adapter** (Rate Limit Governor + Z-Score sanitization).
3. **Deterministic State Machine Monitor** (Phase 1).
4. **Adversarial AI debate integration** (Phase 2).
5. **Execution engine** (Kill-switch & Micro-Circuit Breakers).
6. **Strategy DNA engine** (Nightly evolution & Corporate Action checks).
7. **Full Next.js dashboard** (Live debate stream & approval workflows).
8. **Yaegi-based hot-swappable user strategies**.
