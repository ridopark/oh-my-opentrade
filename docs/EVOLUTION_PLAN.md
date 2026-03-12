# Strategy Evolution Service — Phase 14 (Nightly Evolution)

> **Status**: Plan approved with changes (Oracle review 2026-03-11)
> **Source**: Prometheus plan + Oracle architectural review
> **PRD Reference**: PRD Section 3 ("17:00 - Nightly: AI analyzes trades, updates DNA, runs backtests")
> **Implementation Plan Items**: #67, #68, #70

---

## TL;DR

Build an AI-driven nightly strategy evolution service as a standalone `cmd/omo-evolve` CLI binary. It fetches live trade P&L + ThoughtLog reasoning from TimescaleDB, asks an LLM to propose parameter mutations for strategy TOML configs, runs each mutation through the existing backtest engine via in-process invocation, then asks the LLM to evaluate all backtest results and crown a winner — persisting the full audit trail to `strategy_dna_history` and `thought_logs`.

### Deliverables
- `cmd/omo-evolve/` — standalone binary, invocable via cron or manual at 17:00 ET
- `internal/adapters/llm/evolution_advisor.go` — new LLM methods: `ProposeEvolution`, `EvaluateEvolution`
- `internal/ports/evolution.go` — `EvolutionAdvisorPort` + `EvolutionRepoPort` interfaces
- `internal/app/evolution/service.go` — orchestrates 5-step pipeline
- `internal/app/evolution/backtest_runner.go` — in-process backtest execution wrapper
- `internal/app/evolution/toml_patcher.go` — temp TOML mutator
- `internal/adapters/timescaledb/evolution_repo.go` — `GetThoughtLogsSince`, `GetTradesSince`, `SaveEvolutionRun`
- Migration `011_add_evolution_run_log.up.sql` — evolution_runs audit table
- Config updates to `config.go` and `config.yaml`

### Estimated Effort
- Large (8-10 days, parallelizable to ~5 days)
- 5 execution waves, 11 implementation tasks + 4 verification tasks

---

## Architecture Decisions

All 9 design questions resolved:

| # | Decision | Resolution | Rationale |
|---|----------|-----------|-----------|
| 1 | Binary location | `cmd/omo-evolve/` standalone | Keeps omo-core isolated; follows omo-replay/omo-backfill precedent |
| 2 | Backtest execution | In-process library call | No subprocess overhead; bootstrap functions are already a library |
| 3 | Mutations per run | 5 mutations, 1 generation (configurable) | Incremental, no overfitting; LLM token cost balanced with signal quality |
| 4 | Backtest window | Last 30 days (configurable) | Covers multiple regime cycles; matches existing GetTrades lookback |
| 5 | Promotion policy | Persist to DB only; NO auto-TOML-write | Uses existing DNA approval workflow for human-in-the-loop |
| 6 | LLM model | Inherit `cfg.AI.Model` with optional `evolution.model` override | Flexible upgrade (e.g., Claude Sonnet for richer reasoning) |
| 7 | Symbol selection | From strategy TOML `[routing].symbols` | Exact match to live strategy scope |
| 8 | Concurrency | Sequential backtests | Simpler, safer for commodity hardware; ~30s-2min for 5 runs |
| 9 | Param passing | Temp TOML file per run in `/tmp/omo-evolve/{runID}/` | Reuses all existing TOML parsing/validation; no new code paths |

### Scope
- **IN**: `cmd/omo-evolve`, `ports/evolution.go`, `adapters/llm/evolution_advisor.go`, `app/evolution/service.go`, `app/evolution/backtest_runner.go`, `app/evolution/toml_patcher.go`, `adapters/timescaledb/evolution_repo.go`, migration, config extension
- **OUT**: Auto-promotion to live TOML, corporate action check (#69), omo-core integration, Live mode, grid search

---

## Oracle Review — Changes Incorporated

Oracle verdict: **APPROVE WITH CHANGES**. The following critical issues and improvements from the review have been incorporated into this plan:

### Critical Fixes Applied

**C1. BacktestRunner uses port, not concrete adapter** (hexagonal compliance)
- `BacktestRunner.repo` field changed from `*timescaledb.Repository` to `ports.RepositoryPort`
- Only calls `repo.GetMarketBars()` — clean port boundary
- Concrete `*timescaledb.Repository` injected at wiring time in `cmd/omo-evolve/main.go`

**C2. EvolutionRepoPort is standalone — RepositoryPort NOT modified**
- `GetThoughtLogsSince`, `GetTradesSince`, `SaveEvolutionRun` live only on `EvolutionRepoPort`
- `RepositoryPort` remains untouched — zero blast radius to existing 10+ test mocks
- `timescaledb.Repository` satisfies both interfaces; passed as both at wiring time

**C3. Baseline backtest added to pipeline**
- Step 3 (`backtestMutations`) now runs the **current unmodified TOML** first as "baseline"
- Baseline result included in `EvaluationRequest.Candidates` with ID `"baseline"`
- LLM compares mutations against a true same-window baseline, not stale DB metrics

### Important Improvements Applied

**I1. LLM JSON retry/recovery**
- `cleanLLMJSON()` helper strips markdown fences, trims whitespace
- One retry on parse failure before returning error

**I2. Parameter constraints in LLM prompt**
- Proposal prompt includes min/max ranges for all tunable parameters
- Prevents nonsensical values (e.g., `rsi_long=0`, `stop_bps=10000`)

**I3. Per-run temp directory scoping**
- Each evolution run creates `/tmp/omo-evolve/{runID}/` subdirectory
- `store_fs.Store` points at per-run directory (prevents stale TOML accumulation)
- Cleanup removes entire subdirectory after all backtests complete

**I4. DryRun as exported method**
- Added `RunDryRun(ctx) (*DryRunResult, error)` to Service
- Returns all intermediate results (mutations, backtest results, evaluation) without persisting
- Internal step methods remain private

**I5. StrategyID as uuid.UUID**
- `EvolutionRun.StrategyID` changed to `uuid.UUID`
- `strategyIDFromPath` loads TOML to extract actual strategy UUID

### Minor Improvements Applied

- `runtime.GC()` between sequential backtests to reduce peak memory
- `defer cleanup()` in backtest loop fixed to use immediate cleanup (not deferred to function return)
- `extractSymbolsFromTOML` reuses `dnaManager.Load()` instead of re-parsing
- TOML patcher documents that BurntSushi/toml encoder is lossy (no comments/ordering) but functionally correct
- `--model` CLI flag added for quick experiments

### Risks Confirmed Acceptable

- **In-process isolation**: All mutable state (event bus, strategy instances, indicators) created fresh per `Run()`. No shared mutable globals. ✅
- **Race with omo-core**: omo-evolve only reads bars, writes to history tables. omo-core reads history at bootstrap only. Postgres MVCC handles concurrent access. ✅
- **Memory**: Sequential backtests + `runtime.GC()` between runs keeps peak memory manageable on 24GB server. ✅
- **Clock injection**: Each `BacktestRunner.Run()` creates new `atomic.Value` clock per invocation. ✅

---

## Data Flow

```
                     omo-evolve binary (cmd/omo-evolve/)
                     ========================================
                     |
  Step 1: GATHER     |  TimescaleDB ──→ trades (last 30 days)
                     |  TimescaleDB ──→ thought_logs (last 30 days)
                     |  Filesystem  ──→ current TOML params
                     |  TimescaleDB ──→ latest strategy_dna_history (baseline version)
                     |
  Step 2: PROPOSE    |  ──→ LLM (ProposeEvolution)
                     |      Input: current params + trade P&L + AI debate logs
                     |      Output: 5 mutation candidates with reasoning
                     |
  Step 3: BACKTEST   |  For each mutation (+ baseline):
                     |    ──→ TOMLPatcher writes temp TOML to /tmp/omo-evolve/{runID}/
                     |    ──→ BacktestRunner.Run() (in-process)
                     |        Creates: fresh EventBus + SimBroker + StrategyPipeline
                     |        Reads: market bars from TimescaleDB (GetMarketBars)
                     |        Produces: backtest.Result (Sharpe, drawdown, win rate, etc.)
                     |    ──→ runtime.GC() + cleanup temp file
                     |
  Step 4: EVALUATE   |  ──→ LLM (EvaluateEvolution)
                     |      Input: baseline + all mutation backtest results
                     |      Output: ranked winners with reasoning + confidence
                     |
  Step 5: PERSIST    |  ──→ strategy_dna_history (winner params + performance metrics)
                     |  ──→ thought_logs (3 rows: propose, evaluate, winner)
                     |  ──→ evolution_runs (full audit record)
```

---

## Port Interfaces

### `backend/internal/ports/evolution.go`

```go
package ports

import (
    "context"
    "time"
    "github.com/google/uuid"
    "github.com/oh-my-opentrade/backend/internal/domain"
)

// --- Domain Types ---

// EvolutionMutation represents a single LLM-proposed parameter change set.
type EvolutionMutation struct {
    ID         string         // e.g. "mut-001" or "baseline"
    Parameters map[string]any // full parameter map for the strategy
    Reasoning  string         // LLM rationale for this mutation
}

// EvolutionBacktestResult pairs a mutation with its backtest outcome.
type EvolutionBacktestResult struct {
    Mutation     EvolutionMutation
    BacktestFrom time.Time
    BacktestTo   time.Time
    SharpeRatio  float64
    MaxDrawdown  float64
    WinRate      float64
    ProfitFactor float64
    TotalReturn  float64
    TradeCount   int
    Error        error // non-nil if backtest failed
}

// EvolutionWinner is the LLM-selected best mutation with evaluation reasoning.
type EvolutionWinner struct {
    Mutation       EvolutionMutation
    BacktestResult EvolutionBacktestResult
    Reasoning      string  // LLM evaluation rationale
    Confidence     float64 // [0,1]
    Rank           int     // 1 = best
}

// EvolutionRun is the top-level audit record for one full evolution cycle.
type EvolutionRun struct {
    ID             string
    Time           time.Time
    StrategyID     uuid.UUID
    TenantID       string
    EnvMode        domain.EnvMode
    NumMutations   int
    WinnerID       string         // EvolutionMutation.ID of winner
    WinnerParams   map[string]any
    WinnerSharpe   float64
    WinnerDrawdown float64
    ProposalLog    string // full LLM proposal response (JSON)
    EvaluationLog  string // full LLM evaluation response (JSON)
    Promoted       bool   // always false — human approval required
}

// --- Port Interfaces ---

// EvolutionAdvisorPort defines the LLM contract for the evolution service.
type EvolutionAdvisorPort interface {
    ProposeEvolution(ctx context.Context, req EvolutionProposalRequest) ([]EvolutionMutation, error)
    EvaluateEvolution(ctx context.Context, req EvolutionEvaluationRequest) ([]EvolutionWinner, error)
}

// EvolutionProposalRequest is the input to ProposeEvolution.
type EvolutionProposalRequest struct {
    StrategyID       string
    CurrentParams    map[string]any
    BaselineMetrics  map[string]float64
    RecentTrades     []EvolutionTradeSummary
    ThoughtLogSample []EvolutionThoughtSample
    NumMutations     int
}

// EvolutionEvaluationRequest is the input to EvaluateEvolution.
type EvolutionEvaluationRequest struct {
    StrategyID      string
    CurrentParams   map[string]any
    BaselineMetrics map[string]float64
    Candidates      []EvolutionBacktestResult // includes "baseline" entry
}

// EvolutionTradeSummary is a compact trade record for the LLM prompt.
type EvolutionTradeSummary struct {
    Symbol string
    Side   string
    PnL    float64
    Regime string
}

// EvolutionThoughtSample is a compact AI debate log for the LLM prompt.
type EvolutionThoughtSample struct {
    Symbol         string
    Direction      string
    Confidence     float64
    JudgeReasoning string
}

// EvolutionRepoPort — narrow interface for evolution-specific DB queries.
// Does NOT extend RepositoryPort — avoids breaking existing test mocks.
// timescaledb.Repository satisfies both RepositoryPort AND EvolutionRepoPort.
type EvolutionRepoPort interface {
    GetThoughtLogsSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.ThoughtLog, error)
    GetTradesSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.Trade, error)
    SaveEvolutionRun(ctx context.Context, run EvolutionRun) error
}
```

---

## Config Schema

### `backend/internal/config/config.go` — new struct

```go
type EvolutionConfig struct {
    Enabled        bool    `yaml:"enabled"`          // default: false
    StrategyPath   string  `yaml:"strategy_path"`    // e.g. "configs/strategies/ai_scalping.toml"
    LookbackDays   int     `yaml:"lookback_days"`    // default: 30
    NumMutations   int     `yaml:"num_mutations"`    // default: 5
    NumGenerations int     `yaml:"num_generations"`  // default: 1
    SlippageBPS    int64   `yaml:"slippage_bps"`     // default: 5
    InitialEquity  float64 `yaml:"initial_equity"`   // default: 100000
    Model          string  `yaml:"model"`            // override cfg.AI.Model (e.g. "anthropic/claude-sonnet-4")
    TenantID       string  `yaml:"tenant_id"`        // default: "default"
    OutputDir      string  `yaml:"output_dir"`       // default: "/tmp/omo-evolve"
}
```

### `configs/config.yaml` — new stanza

```yaml
evolution:
  enabled: false
  strategy_path: "configs/strategies/ai_scalping.toml"
  lookback_days: 30
  num_mutations: 5
  num_generations: 1
  slippage_bps: 5
  initial_equity: 100000.0
  model: ""  # empty = use cfg.AI.Model
  tenant_id: "default"
  output_dir: "/tmp/omo-evolve"
```

---

## CLI Binary: `cmd/omo-evolve/main.go`

### Flags

```go
flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config")
flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
flag.StringVar(&strategyPath, "strategy", "", "Override strategy TOML path")
flag.StringVar(&fromFlag, "from", "", "Backtest start YYYY-MM-DD (default: lookback_days ago)")
flag.StringVar(&toFlag, "to", "", "Backtest end YYYY-MM-DD (default: today)")
flag.StringVar(&modelFlag, "model", "", "Override LLM model (e.g. anthropic/claude-sonnet-4)")
flag.BoolVar(&dryRun, "dry-run", false, "Propose + backtest but do NOT persist to DB")
flag.IntVar(&numMutations, "mutations", 0, "Override num_mutations (0 = use config)")
```

### Wiring Sequence

```
1. Load config + env
2. Setup zerolog
3. Connect to TimescaleDB
4. Create EvolutionAdvisor (LLM client)
5. Create BacktestRunner (takes RepositoryPort for bar reads)
6. Create TOMLPatcher (output dir from config)
7. Create Service (wire all dependencies)
8. Signal handling (SIGINT/SIGTERM → cancel context)
9. If --dry-run: call svc.RunDryRun(ctx), print results, exit
   Else: call svc.Run(ctx), log completion
```

### Dry-Run Output Format

```
=== EVOLUTION DRY RUN ===
Strategy: ai_scalping_v1 (configs/strategies/ai_scalping.toml)
Lookback: 2026-02-09 to 2026-03-11

Proposed Mutations (5):
  mut-001: rsi_long 30->25, stop_bps 50->60 — "RSI too conservative, stops too tight"
  mut-002: cooldown_seconds 60->30, max_trades 10->15 — "Undertrading in balance regime"
  ...

Backtest Results:
  BASELINE:  Sharpe=0.82  Drawdown=3.1%  WinRate=54%  PF=1.3  Return=+2.1%
  mut-001:   Sharpe=1.14  Drawdown=2.7%  WinRate=58%  PF=1.6  Return=+3.8%  << WINNER
  mut-002:   Sharpe=0.71  Drawdown=4.2%  WinRate=51%  PF=1.1  Return=+1.2%
  ...

Winner: mut-001 (confidence: 0.87)
Reasoning: <LLM evaluation text>

*** DRY RUN — nothing was written to the database ***
```

---

## LLM Prompt Design

### ProposeEvolution — System Prompt

```
You are a quantitative trading strategy optimizer. Your task is to propose parameter
mutations for an algorithmic trading strategy based on recent performance data and
AI debate logs. Each mutation must be a COMPLETE set of all strategy parameters —
not just the changed ones. Respond ONLY with valid JSON — no markdown, no extra text.
```

### ProposeEvolution — User Prompt Structure

```
Strategy: {StrategyID}

Current Parameters:
{JSON of CurrentParams}

Current Performance (last {LookbackDays} days):
{JSON of BaselineMetrics}

Recent Trade Sample (last {N} trades):
{compact trade list: symbol, side, pnl, regime}

Recent AI Debate Reasoning Sample (last {N} debates):
{compact thought log: symbol, direction, confidence, judge_reasoning}

Parameter constraints (must respect):
  rsi_long: [10, 45], rsi_short: [55, 90], stoch_long: [5, 40], stoch_short: [60, 95]
  rsi_exit_mid: [40, 60], cooldown_seconds: [10, 300], max_trades_per_day: [1, 30]
  ai_min_confidence: [0.3, 0.9], size_mult_min: [0.1, 1.0], size_mult_base: [0.5, 2.0]
  size_mult_max: [1.0, 3.0], stop_bps: [20, 200], limit_offset_bps: [1, 20]
  risk_per_trade_bps: [25, 200], max_position_bps: [200, 1500]

Instructions:
Propose exactly {NumMutations} parameter mutations. For each mutation:
1. Identify one or two parameters to adjust based on the evidence above
2. Provide the COMPLETE parameter set (not just the changed ones)
3. Explain your reasoning

You MUST NOT change: strategy ID, version, routing symbols, asset_class, timeframes.
You MAY adjust any parameter listed in the constraints above.

Response schema (array of N objects):
[
  {
    "id": "mut-001",
    "parameters": { ... complete param map ... },
    "reasoning": "why these changes address observed issues"
  }
]
```

### EvaluateEvolution — System Prompt

```
You are a quantitative trading strategy evaluator. You have run backtests on multiple
parameter mutations. Your task is to analyze the results and select the best mutation.
Respond ONLY with valid JSON — no markdown, no extra text.
```

### EvaluateEvolution — User Prompt Structure

```
Strategy: {StrategyID}

Baseline (current live parameters, same backtest window):
  Sharpe: {X}, MaxDrawdown: {X}%, WinRate: {X}%, ProfitFactor: {X}, TotalReturn: {X}%, Trades: {N}

Mutation Backtest Results:
  Mutation {ID} ({reasoning}):
    Sharpe: {X}, MaxDrawdown: {X}%, WinRate: {X}%, ProfitFactor: {X}, TotalReturn: {X}%, Trades: {N}
  ...

Instructions:
1. Rank all mutations from best to worst. Consider: Sharpe ratio (primary), max drawdown
   (risk), win rate, profit factor. Prefer mutations that IMPROVE over baseline.
2. Select a winner. If NO mutation improves over baseline, still select the least-bad
   option but set confidence < 0.5.
3. Return ALL candidates ranked.

Response schema:
{
  "ranked": [
    {
      "id": "mut-001",
      "rank": 1,
      "reasoning": "why this is the best choice",
      "confidence": 0.85
    }
  ]
}
```

---

## DB Migration: `011_add_evolution_run_log`

### `011_add_evolution_run_log.up.sql`

```sql
CREATE TABLE IF NOT EXISTS evolution_runs (
    time            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    id              TEXT NOT NULL PRIMARY KEY,
    account_id      TEXT NOT NULL,
    env_mode        TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    strategy_id     UUID NOT NULL,
    num_mutations   INTEGER NOT NULL,
    winner_id       TEXT,
    winner_params   JSONB,
    winner_sharpe   DOUBLE PRECISION,
    winner_drawdown DOUBLE PRECISION,
    proposal_log    TEXT,
    evaluation_log  TEXT,
    promoted        BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_evolution_runs_strategy
    ON evolution_runs (account_id, env_mode, strategy_id, time DESC);
```

### `011_add_evolution_run_log.down.sql`

```sql
DROP TABLE IF EXISTS evolution_runs;
```

---

## Execution Waves

```
Wave 1 (Start Immediately — types + interfaces, NO deps):
  Task 1: Domain types + ports/evolution.go                    [quick]
  Task 2: Config extension (EvolutionConfig)                   [quick]
  Task 3: DB migration + evolution_repo.go                     [quick]

Wave 2 (After Wave 1 — core adapters + utilities):
  Task 4: LLM evolution_advisor.go (ProposeEvolution + Evaluate) [deep]
  Task 5: toml_patcher.go (temp TOML generation + cleanup)      [quick]
  Task 6: backtest_runner.go (in-process backtest wrapper)       [deep]

Wave 3 (After Wave 2 — orchestration service):
  Task 7: evolution/service.go (5-step pipeline)                 [deep]

Wave 4 (After Wave 3 — binary + wiring):
  Task 8: cmd/omo-evolve/main.go (CLI binary)                   [unspecified-high]

Wave 5 (After Wave 4 — verification):
  Task 9:  Unit tests for evolution package                      [deep]
  Task 10: Unit tests for evolution_advisor.go                   [deep]
  Task 11: Integration smoke test + dry-run verification         [unspecified-high]

Wave FINAL (After ALL tasks):
  Task F1: Plan compliance audit
  Task F2: Code quality review (go vet, staticcheck)
  Task F3: Real dry-run QA
  Task F4: Scope fidelity check
```

### Dependency Matrix

| Task | Depends On | Reason |
|------|-----------|--------|
| 1 | None | Foundation types |
| 2 | None | Config extension, independent |
| 3 | 1 | evolution_repo needs EvolutionRepoPort |
| 4 | 1 | evolution_advisor implements EvolutionAdvisorPort |
| 5 | 1, 2 | patcher needs types + config path |
| 6 | 1, 2 | runner needs types + config struct |
| 7 | 3, 4, 5, 6 | service orchestrates all |
| 8 | 2, 7 | binary wires config + service |
| 9 | 7 | tests for service |
| 10 | 4 | tests for advisor |
| 11 | 8 | integration requires binary |

---

## Guardrails

### Must Have
- Hexagonal architecture: ports defined, adapters implement ports, app depends only on ports
- LLM proposes mutations (not hardcoded grid), LLM evaluates results (not hardcoded scoring)
- Baseline backtest run alongside mutations for fair comparison
- Full audit trail in `strategy_dna_history` + `thought_logs` + `evolution_runs`
- `--dry-run` flag: print mutations + results without persisting
- Parameter constraints in LLM prompt to prevent nonsensical mutations
- LLM JSON response cleanup + 1 retry on parse failure
- `runtime.GC()` between sequential backtests

### Must NOT Have
- NO auto-promotion to live TOML (human approval required)
- NO modifications to omo-core packages that could break live trading
- NO `os/exec` subprocess for backtests (in-process only)
- NO real broker API calls during evolution (SimBroker only, EnvModePaper only)
- NO changes to `RepositoryPort` (use `EvolutionRepoPort` instead)
- NO `EnvModeLive` in evolution code
- NO more than 5 mutations per generation (LLM cost control)
- NO storing API keys in LLM prompts

---

## Testing Strategy

### Unit Tests
- `service_test.go` — Mock EvolutionAdvisorPort + EvolutionRepoPort, test all 5 steps
- `toml_patcher_test.go` — Verify TOML roundtrip, param mutation, cleanup
- `evolution_advisor_test.go` — httptest.Server mocking, JSON parsing, error paths

### Integration Test
- Build binary, run `--dry-run` against real TimescaleDB + LLM endpoint
- Verify DB writes after full run (strategy_dna_history, thought_logs, evolution_runs)
- Tagged `//go:build integration` — skip in CI if LLM endpoint unavailable

### Verification Commands
```bash
cd backend && go build ./cmd/omo-evolve                           # build
cd backend && go test ./internal/app/evolution/... ./internal/adapters/llm/...  # unit tests
./bin/omo-evolve --dry-run --config configs/config.yaml --env-file .env         # dry-run

# Check DB audit trail after full run
docker exec omo-timescaledb psql -U opentrade -d opentrade -c \
  "SELECT strategy_id, version, performance FROM strategy_dna_history ORDER BY time DESC LIMIT 3;"

docker exec omo-timescaledb psql -U opentrade -d opentrade -c \
  "SELECT event_type, rationale FROM thought_logs WHERE event_type LIKE 'evolution%' ORDER BY time DESC LIMIT 5;"
```

---

## Key References

| File | Purpose |
|------|---------|
| `backend/cmd/omo-replay/main.go` | Template for CLI binary + backtest wiring |
| `backend/internal/adapters/llm/advisor.go` | LLM HTTP client pattern to follow |
| `backend/internal/app/debate/service.go` | AI-calling service pattern with ThoughtLog persistence |
| `backend/internal/app/strategy/dna_manager.go` | TOML parsing + DNAManager API |
| `backend/internal/app/backtest/collector.go` | backtest.Result struct |
| `backend/internal/adapters/timescaledb/repository.go` | SaveStrategyDNA, GetTrades patterns |
| `backend/internal/ports/repository.go` | RepositoryPort interface |
| `backend/internal/ports/ai_advisor.go` | AIAdvisorPort pattern |
| `configs/strategies/ai_scalping.toml` | Source TOML with 23 tunable parameters |
| `docs/PRD.md` (Section 3) | Nightly evolution requirements |
| `docs/IMPLEMENTATION_PLAN.md` (Phase 14) | Items #67-70 |
