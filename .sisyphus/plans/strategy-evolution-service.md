# Strategy Evolution Service — Phase 14 (Nightly Evolution)

## TL;DR

> **Quick Summary**: Build an AI-driven nightly strategy evolution service as a standalone `cmd/omo-evolve` CLI binary. It fetches live trade P&L + ThoughtLog reasoning from TimescaleDB, asks an LLM to propose parameter mutations for `ai_scalping.toml`, runs each mutation through the existing `omo-replay --backtest` engine via in-process invocation, then asks the LLM to evaluate all backtest results and crown a winner — persisting the full audit trail to `strategy_dna_history` and `thought_logs`.
>
> **Deliverables**:
> - `cmd/omo-evolve/` — standalone binary, invocable via cron or manual at 17:00 ET
> - `internal/adapters/llm/evolution_advisor.go` — new LLM methods: `ProposeEvolution`, `EvaluateEvolution`
> - `internal/ports/evolution.go` — `EvolutionAdvisorPort` interface + `EvolutionRepoPort`
> - `internal/app/evolution/service.go` — orchestrates 5-step pipeline
> - `internal/app/evolution/backtest_runner.go` — in-process backtest execution wrapper
> - `internal/app/evolution/toml_patcher.go` — temp TOML mutator
> - `internal/adapters/timescaledb/evolution_repo.go` — `GetThoughtLogsSince`, `GetTradesSince`
> - Migration `020_add_evolution_run_log.up.sql` — optional run-level audit table
>
> **Estimated Effort**: Large (8–10 days, parallelizable to ~5 days)
> **Parallel Execution**: YES — 5 waves
> **Critical Path**: Task 1 (ports/types) → Task 4 (evolution_advisor) → Task 6 (service) → Task 8 (integration) → Task F1-F4

---

## Context

### Original Request
Build Phase 14 ("Nightly Evolution") of the oh-my-opentrade project: an AI-driven strategy evolution service where an LLM proposes parameter mutations AND analyzes backtest results. NOT a grid search. Must follow hexagonal architecture, wire all existing building blocks (LLM client, DNAManager, backtest engine, repository), and persist audit trail.

### Architecture Decisions (All 9 Design Questions)

**1. Binary location → `cmd/omo-evolve/` standalone binary**
- Rationale: omo-core runs 24/7 in paper trading; injecting a heavy backtest loop into it risks blocking the event loop. A standalone CLI binary invoked by cron at 17:00 ET is safe, testable in isolation, and follows the `omo-replay`/`omo-backfill` precedent.
- NOT a subcommand of omo-replay (different concerns), NOT integrated into omo-core (risk to live trading).

**2. Backtest execution → In-process library call (NOT `os/exec`)**
- Rationale: The backtest engine is already structured as a library (`internal/app/backtest`, `internal/app/bootstrap`). omo-replay's `main.go` is just a thin CLI wrapper. Calling the bootstrap functions directly in-process avoids subprocess overhead, temp binary paths, and shell escaping bugs. The backtest result is returned as a Go struct, not parsed JSON. We write a `BacktestRunner` struct in `internal/app/evolution/` that invokes `bootstrap.BuildIngestion`, `bootstrap.BuildStrategyPipeline`, etc. directly — exactly as omo-replay does.

**3. Generations per run → 1 generation, 5 mutations (configurable)**
- Rationale: LLM proposes 5 candidate mutations in a single call. One generation per nightly run is sufficient to make safe, incremental progress. Running multiple generations per night risks over-fitting to a single day's data. Config: `evolution.num_mutations: 5`, `evolution.num_generations: 1`.

**4. Backtest window → Last 30 calendar days (configurable)**
- Rationale: 30 days captures multiple regime cycles without over-weighting recent volatility. Matches the `strategy_daily_pnl` and `GetTrades` lookback pattern used elsewhere. Config: `evolution.lookback_days: 30`.

**5. Promotion policy → Persist to strategy_dna_history; NO auto-promote to live TOML**
- Rationale: The `dnaapproval` service already provides a human-approval workflow for DNA changes. The evolution service saves the winner's parameters as a new `strategy_dna_history` row with `performance` JSONB. Promotion to the live TOML file is opt-in via the existing DNA approval workflow. This is the safest approach for a paper-trading system.

**6. LLM model → Same `AIConfig` from `configs/config.yaml`, but add `evolution.model` override**
- Rationale: Reuse the existing `AIConfig` struct and `NewAdvisor` constructor. Evolution prompts are longer and richer than debate prompts, so allow an optional `evolution.model` config key that overrides the debate model (e.g., upgrade to `anthropic/claude-sonnet-4` for evolution, keep `gpt-4o-mini` for real-time debate). Default: use `cfg.AI.Model`.

**7. Symbol selection → From strategy TOML `[routing].symbols` list**
- Rationale: The evolution backtest should cover the exact symbols the strategy trades. `dna_manager.Load()` parses the TOML and returns `StrategyDNA.Parameters` which includes the routing symbols. Backtest runner uses this list. No screener dependency needed.

**8. Concurrency → Sequential backtests**
- Rationale: Each in-process backtest spins up a full in-memory event bus + strategy pipeline, which is CPU-intensive. Running 5 backtests sequentially on a commodity server takes ~30s–2min total — acceptable for a nightly job. Parallel execution risks port conflicts, memory pressure, and race conditions in TimescaleDB reads. Sequential is simpler and safer.

**9. How to pass mutated params → Temp TOML file written to `/tmp/omo-evolve-{uuid}.toml`**
- Rationale: The bootstrap pipeline reads strategy specs from a `store_fs.Store` pointed at a directory. The cleanest approach is to write a complete mutated TOML to a temp directory, create a `store_fs.Store` pointing there, and pass it to `BuildStrategyPipeline`. The temp file is deleted after the backtest. This reuses ALL existing parsing/validation logic and doesn't require any changes to the bootstrap or strategy packages.

### Research Findings
- `Advisor` struct is NOT interface-bound (it's a concrete type). We'll create a new `EvolutionAdvisor` struct that embeds the same HTTP client pattern.
- `RepositoryPort` already has `GetTrades` and `SaveStrategyDNA`/`SaveThoughtLog`. We need to add `GetThoughtLogsSince` (query by time range) to the port.
- `strategy_dna_history` UNIQUE constraint is `(strategy_id, version, time)` — we use `time.Now()` as time and must increment version.
- `backtest.Result` has all needed metrics: `SharpeRatio`, `MaxDrawdown`, `WinRate`, `ProfitFactor`, `TotalReturn`, `TradeCount`.
- `tomlFile` struct in `dna_manager.go` is package-private. The patcher must write raw TOML using `BurntSushi/toml` encoder, not depend on `strategy.tomlFile`.
- `store_fs.Store` accepts a directory path + a loader function — confirmed via `omo-replay/main.go` wiring.

### Metis Review Findings (Addressed)
- Gap: No `GetThoughtLogsSince(ctx, tenantID, envMode, since)` query exists — must add to port + repo adapter.
- Gap: `thought_logs` payload JSONB currently only stores `intent_id`. Evolution thought logs need richer payload (mutation_id, backtest_result summary). Must extend payload schema via new `event_type` without migration (JSONB is flexible).
- Gap: `strategy.LoadSpecFile` is the loader function signature used by `store_fs.Store` — must verify it's exported. If not, use the same inline TOML load logic.
- Gap: DNA version tracking — `GetLatestStrategyDNA` returns the latest; evolution service must read current version and increment.
- Gap: `configs/config.yaml` needs `evolution:` stanza (new config struct `EvolutionConfig`).
- Guardrail: Evolution must NOT touch omo-core's loaded `DNAManager` cache — it runs as a separate process.
- Guardrail: Must use `domain.EnvModePaper` only — never `Live`.
- Guardrail: `ThoughtLog.Symbol` is required (NOT NULL in DB). Use `"*"` as the symbol for evolution-level logs.

---

## Work Objectives

### Core Objective
Build a standalone `omo-evolve` binary that, when invoked, runs one full AI-driven evolution cycle: gather data → LLM proposes mutations → backtest each → LLM evaluates results → persist winner to DB audit trail.

### Concrete Deliverables
- `backend/cmd/omo-evolve/main.go` — binary entry point with CLI flags
- `backend/internal/ports/evolution.go` — `EvolutionAdvisorPort` interface
- `backend/internal/adapters/llm/evolution_advisor.go` — concrete LLM implementation
- `backend/internal/app/evolution/service.go` — 5-step orchestration service
- `backend/internal/app/evolution/backtest_runner.go` — in-process backtest wrapper
- `backend/internal/app/evolution/toml_patcher.go` — temp TOML file generator
- `backend/internal/adapters/timescaledb/evolution_repo.go` — `GetThoughtLogsSince`
- `backend/migrations/020_add_evolution_run_log.up.sql` — `evolution_runs` audit table
- `configs/config.yaml` update — add `evolution:` stanza
- `backend/internal/config/config.go` update — add `EvolutionConfig` struct

### Definition of Done
- [ ] `go build ./cmd/omo-evolve` succeeds with no errors
- [ ] `go test ./internal/app/evolution/...` passes (unit tests with mocks)
- [ ] `go test ./internal/adapters/llm/...` passes including new evolution_advisor tests
- [ ] Running `./bin/omo-evolve --dry-run` loads config, queries DB, prints proposed mutations, does NOT persist
- [ ] Running `./bin/omo-evolve` completes one full cycle and inserts a row into `strategy_dna_history`
- [ ] `thought_logs` contains evolution event_type rows with full AI reasoning

### Must Have
- Hexagonal architecture: ports defined, adapters implement ports, app layer depends only on ports
- LLM proposes mutations (not hardcoded grid), LLM evaluates results (not hardcoded scoring)
- Full audit trail in `strategy_dna_history` + `thought_logs`
- `--dry-run` flag: print proposed mutations + backtest results without persisting
- `--config`, `--env-file` flags matching omo-replay pattern
- `--strategy` flag to specify which TOML file to evolve
- `--from`, `--to` flags for backtest window (default: last 30 days)
- Graceful shutdown via context cancellation on SIGINT/SIGTERM

### Must NOT Have (Guardrails)
- NO auto-promotion to live TOML file (human approval required)
- NO modifications to omo-core packages that could break live trading
- NO `os/exec` subprocess for backtests (in-process only)
- NO real broker API calls during evolution (SimBroker only, EnvModePaper only)
- NO changes to `internal/app/backtest/`, `internal/app/bootstrap/`, or `internal/app/strategy/` packages (extend only, never modify existing logic)
- NO storing strategy DNA/params in LLM prompts sent to third-party APIs (privacy boundary from existing `buildPrompt` pattern)
- NO evolution runs during market hours (enforced by cron schedule, not code)
- NO more than 5 mutations per generation (LLM cost control)

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: YES (`go test ./...`)
- **Automated tests**: Tests-after (unit tests for each new package)
- **Framework**: `go test` with standard `testing` package

### QA Policy
Every task includes agent-executed QA scenarios. Evidence saved to `.sisyphus/evidence/`.

- **Backend units**: `go test ./internal/...` — verify pass/fail
- **Integration**: `./bin/omo-evolve --dry-run --config configs/config.yaml` — verify output
- **DB audit**: `docker exec omo-timescaledb psql -c "SELECT * FROM strategy_dna_history ORDER BY time DESC LIMIT 1"` — verify row inserted

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — types + interfaces, NO deps):
├── Task 1: Domain types + ports/evolution.go (EvolutionAdvisorPort) [quick]
├── Task 2: Config extension (EvolutionConfig in config.go + config.yaml) [quick]
└── Task 3: DB migration + evolution_repo.go (GetThoughtLogsSince) [quick]

Wave 2 (After Wave 1 — core adapters + utilities):
├── Task 4: LLM evolution_advisor.go (ProposeEvolution + EvaluateEvolution) [deep]
├── Task 5: toml_patcher.go (temp TOML file generation + cleanup) [quick]
└── Task 6: backtest_runner.go (in-process backtest invocation wrapper) [deep]

Wave 3 (After Wave 2 — orchestration service):
└── Task 7: evolution/service.go (5-step pipeline: gather→propose→backtest→evaluate→persist) [deep]

Wave 4 (After Wave 3 — binary + wiring):
└── Task 8: cmd/omo-evolve/main.go (CLI binary, flags, config loading, service wiring) [unspecified-high]

Wave 5 (After Wave 4 — verification):
├── Task 9: Unit tests for evolution package [deep]
├── Task 10: Unit tests for evolution_advisor.go [deep]
└── Task 11: Integration smoke test + dry-run verification [unspecified-high]

Wave FINAL (After ALL tasks — independent review):
├── Task F1: Plan compliance audit [oracle]
├── Task F2: Code quality review (go vet, staticcheck) [unspecified-high]
├── Task F3: Real dry-run QA [unspecified-high]
└── Task F4: Scope fidelity check [deep]
```

### Dependency Matrix

| Task | Depends On | Reason |
|------|-----------|--------|
| 1 | None | Foundation types, no prerequisites |
| 2 | None | Config extension, independent |
| 3 | 1 | evolution_repo needs EvolutionRepoPort from ports |
| 4 | 1 | evolution_advisor implements EvolutionAdvisorPort |
| 5 | 1, 2 | toml_patcher needs StrategyDNA types + config path |
| 6 | 1, 2 | backtest_runner needs types + config struct |
| 7 | 3, 4, 5, 6 | service orchestrates all adapters via ports |
| 8 | 2, 7 | binary wires config + service |
| 9 | 7 | tests for service |
| 10 | 4 | tests for evolution_advisor |
| 11 | 8 | integration requires binary |
| F1-F4 | 9, 10, 11 | verification after all implementation |

### Agent Dispatch Summary
- **Wave 1**: 3 parallel `quick` agents
- **Wave 2**: 2 `deep` + 1 `quick` parallel agents
- **Wave 3**: 1 `deep` agent (service is complex, sequential required)
- **Wave 4**: 1 `unspecified-high` agent
- **Wave 5**: 2 `deep` + 1 `unspecified-high` parallel agents
- **FINAL**: 4 parallel review agents

---

## TODOs

- [ ] 1. Define domain types and ports (`internal/ports/evolution.go`, update `RepositoryPort`)

  **What to do**:
  - Create `backend/internal/ports/evolution.go` with the following types and interfaces:
    ```go
    package ports

    import (
        "context"
        "time"
        "github.com/oh-my-opentrade/backend/internal/domain"
    )

    // EvolutionMutation represents a single LLM-proposed parameter change set.
    type EvolutionMutation struct {
        ID         string            // UUID string, e.g. "mut-001"
        Parameters map[string]any    // full parameter map for the strategy
        Reasoning  string            // LLM rationale for this mutation
    }

    // EvolutionBacktestResult pairs a mutation with its backtest outcome.
    type EvolutionBacktestResult struct {
        Mutation      EvolutionMutation
        BacktestFrom  time.Time
        BacktestTo    time.Time
        SharpeRatio   float64
        MaxDrawdown   float64
        WinRate       float64
        ProfitFactor  float64
        TotalReturn   float64
        TradeCount    int
        Error         error // non-nil if backtest failed
    }

    // EvolutionWinner is the LLM-selected best mutation with evaluation reasoning.
    type EvolutionWinner struct {
        Mutation      EvolutionMutation
        BacktestResult EvolutionBacktestResult
        Reasoning     string  // LLM evaluation rationale
        Confidence    float64 // LLM confidence in this selection [0,1]
        Rank          int     // 1 = best
    }

    // EvolutionAdvisorPort defines the LLM contract for the evolution service.
    type EvolutionAdvisorPort interface {
        // ProposeEvolution receives current strategy context and returns N mutation candidates.
        ProposeEvolution(ctx context.Context, req EvolutionProposalRequest) ([]EvolutionMutation, error)

        // EvaluateEvolution receives all backtest results and returns ranked winners.
        EvaluateEvolution(ctx context.Context, req EvolutionEvaluationRequest) ([]EvolutionWinner, error)
    }

    // EvolutionProposalRequest is the input to ProposeEvolution.
    type EvolutionProposalRequest struct {
        StrategyID      string
        CurrentParams   map[string]any    // current live parameters from TOML
        BaselineMetrics map[string]float64 // current performance from strategy_dna_history
        RecentTrades    []EvolutionTradeSummary
        ThoughtLogSample []EvolutionThoughtSample
        NumMutations    int
    }

    // EvolutionEvaluationRequest is the input to EvaluateEvolution.
    type EvolutionEvaluationRequest struct {
        StrategyID      string
        CurrentParams   map[string]any
        BaselineMetrics map[string]float64
        Candidates      []EvolutionBacktestResult
    }

    // EvolutionTradeSummary is a compact trade record for the LLM prompt.
    type EvolutionTradeSummary struct {
        Symbol  string
        Side    string
        PnL     float64
        Regime  string
    }

    // EvolutionThoughtSample is a compact AI debate log for the LLM prompt.
    type EvolutionThoughtSample struct {
        Symbol         string
        Direction      string
        Confidence     float64
        JudgeReasoning string
    }

    // EvolutionRepoPort extends RepositoryPort with evolution-specific queries.
    // Implemented by timescaledb.Repository.
    type EvolutionRepoPort interface {
        // GetThoughtLogsSince returns all thought_logs for the given tenant/env since `since`.
        GetThoughtLogsSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.ThoughtLog, error)
        // GetTradesSince returns all trades since `since` (wraps existing GetTrades with to=Now).
        GetTradesSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.Trade, error)
        // SaveEvolutionRun persists a full evolution run record for audit.
        SaveEvolutionRun(ctx context.Context, run EvolutionRun) error
    }

    // EvolutionRun is the top-level audit record for one full evolution cycle.
    type EvolutionRun struct {
        ID             string
        Time           time.Time
        StrategyID     string
        TenantID       string
        EnvMode        domain.EnvMode
        NumMutations   int
        WinnerID       string            // EvolutionMutation.ID of winner
        WinnerParams   map[string]any
        WinnerSharpe   float64
        WinnerDrawdown float64
        ProposalLog    string            // full LLM proposal response (JSON)
        EvaluationLog  string            // full LLM evaluation response (JSON)
        Promoted       bool              // whether winner was written to live TOML
    }
    ```
  - Add `GetThoughtLogsSince` and `GetTradesSince` to `RepositoryPort` in `backend/internal/ports/repository.go`:
    ```go
    // GetThoughtLogsSince returns thought logs from `since` to now (for evolution analysis).
    GetThoughtLogsSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.ThoughtLog, error)
    // GetTradesSince returns all trades from `since` to now.
    GetTradesSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.Trade, error)
    // SaveEvolutionRun persists a full evolution cycle audit record.
    SaveEvolutionRun(ctx context.Context, run ports.EvolutionRun) error
    ```
  - Add stub implementations to `backend/internal/adapters/noop/repo.go` to satisfy the interface:
    ```go
    func (n *NoopRepo) GetThoughtLogsSince(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) ([]domain.ThoughtLog, error) { return nil, nil }
    func (n *NoopRepo) GetTradesSince(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) ([]domain.Trade, error) { return nil, nil }
    func (n *NoopRepo) SaveEvolutionRun(_ context.Context, _ ports.EvolutionRun) error { return nil }
    ```

  **Must NOT do**:
  - Do NOT modify `domain/entity.go` — EvolutionMutation lives in `ports/evolution.go`
  - Do NOT add to RepositoryPort in a way that requires all existing test mocks to break — add default stub methods if needed

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Pure type/interface definitions, no business logic, single-pass file creation
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go interface design patterns, hexagonal port definitions
  - **Skills Evaluated but Omitted**:
    - `senior-architect`: Overkill for type definitions

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Tasks 3, 4, 5, 6 (all depend on port types)
  - **Blocked By**: None (can start immediately)

  **References**:
  - `backend/internal/ports/repository.go:1-60` — RepositoryPort pattern to extend
  - `backend/internal/ports/ai_advisor.go` — DebateOption functional option pattern
  - `backend/internal/adapters/noop/repo.go` — All NoopRepo stubs (must add new methods)
  - `backend/internal/domain/entity.go` — ThoughtLog and Trade struct definitions

  **Acceptance Criteria**:
  - [ ] `backend/internal/ports/evolution.go` created with all types above
  - [ ] `RepositoryPort` in `repository.go` has 3 new methods
  - [ ] `noop/repo.go` has stub implementations for all 3 new methods
  - [ ] `go build ./...` passes (no interface satisfaction errors)

  **QA Scenarios**:
  ```
  Scenario: Go build passes after port additions
    Tool: Bash
    Preconditions: Files created as specified
    Steps:
      1. cd backend && go build ./...
      2. Assert exit code 0
    Expected Result: No compilation errors
    Evidence: .sisyphus/evidence/task-1-build.txt

  Scenario: Noop repo satisfies RepositoryPort
    Tool: Bash
    Preconditions: noop/repo.go updated
    Steps:
      1. cd backend && go vet ./internal/adapters/noop/...
      2. Assert exit code 0, no "does not implement" errors
    Expected Result: Clean vet output
    Evidence: .sisyphus/evidence/task-1-vet.txt
  ```

  **Commit**: YES (groups with Task 2 and 3)
  - Message: `feat(evolution): add domain types, ports, and EvolutionRepoPort interface`

- [ ] 2. Extend config: `EvolutionConfig` struct + `evolution:` YAML stanza

  **What to do**:
  - Add `EvolutionConfig` struct to `backend/internal/config/config.go`:
    ```go
    // EvolutionConfig holds configuration for the nightly strategy evolution service.
    type EvolutionConfig struct {
        Enabled          bool    `yaml:"enabled"`           // default: false
        StrategyPath     string  `yaml:"strategy_path"`     // e.g. "configs/strategies/ai_scalping.toml"
        LookbackDays     int     `yaml:"lookback_days"`     // default: 30
        NumMutations     int     `yaml:"num_mutations"`     // default: 5
        NumGenerations   int     `yaml:"num_generations"`   // default: 1
        SlippageBPS      int64   `yaml:"slippage_bps"`      // default: 5
        InitialEquity    float64 `yaml:"initial_equity"`    // default: 100000
        Model            string  `yaml:"model"`             // override cfg.AI.Model for evolution (e.g. "anthropic/claude-sonnet-4")
        TenantID         string  `yaml:"tenant_id"`         // default: "default"
        OutputDir        string  `yaml:"output_dir"`        // where to write backtest JSON files (default: "/tmp/omo-evolve")
    }
    ```
  - Add `Evolution EvolutionConfig` field to the `Config` struct:
    ```go
    type Config struct {
        // ... existing fields ...
        Evolution EvolutionConfig `yaml:"evolution"`
    }
    ```
  - Add defaults in the config loading logic (look for how `AIScreenerConfig` defaults are set, follow same pattern).
  - Add `evolution:` stanza to `configs/config.yaml`:
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

  **Must NOT do**:
  - Do NOT change any existing config fields or their YAML keys
  - Do NOT add evolution config to omo-core services.go (omo-evolve loads it independently)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Pure config struct + YAML file edit, no business logic
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go struct tags, YAML configuration patterns

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 5, 6, 8
  - **Blocked By**: None

  **References**:
  - `backend/internal/config/config.go:1-100` — Config struct, AIScreenerConfig as pattern for defaults
  - `configs/config.yaml` — Full current config to append to

  **Acceptance Criteria**:
  - [ ] `EvolutionConfig` struct added to `config.go` with all fields and yaml tags
  - [ ] `Config.Evolution` field present
  - [ ] `configs/config.yaml` has `evolution:` stanza with all keys
  - [ ] `go build ./internal/config/...` passes
  - [ ] `config.Load(".env", "configs/config.yaml")` successfully unmarshals the new stanza

  **QA Scenarios**:
  ```
  Scenario: Config loads evolution stanza correctly
    Tool: Bash
    Preconditions: Both files updated as specified
    Steps:
      1. cd backend && go build ./internal/config/...
      2. Assert exit code 0
    Expected Result: No compilation errors
    Evidence: .sisyphus/evidence/task-2-build.txt
  ```

  **Commit**: YES (groups with Tasks 1, 3)

- [ ] 3. DB migration + `timescaledb/evolution_repo.go` (GetThoughtLogsSince, GetTradesSince, SaveEvolutionRun)

  **What to do**:
  - Create migration `backend/migrations/020_add_evolution_run_log.up.sql`:
    ```sql
    -- 020_add_evolution_run_log.up.sql
    -- Audit table for each nightly evolution cycle run.
    CREATE TABLE IF NOT EXISTS evolution_runs (
        time            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        id              TEXT NOT NULL PRIMARY KEY,
        account_id      TEXT NOT NULL,
        env_mode        TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
        strategy_id     TEXT NOT NULL,
        num_mutations   INTEGER NOT NULL,
        winner_id       TEXT,
        winner_params   JSONB,
        winner_sharpe   DOUBLE PRECISION,
        winner_drawdown DOUBLE PRECISION,
        proposal_log    TEXT,
        evaluation_log  TEXT,
        promoted        BOOLEAN NOT NULL DEFAULT FALSE
    );
    CREATE INDEX IF NOT EXISTS idx_evolution_runs_strategy ON evolution_runs (account_id, env_mode, strategy_id, time DESC);
    ```
  - Create `backend/migrations/020_add_evolution_run_log.down.sql`:
    ```sql
    DROP TABLE IF EXISTS evolution_runs;
    ```
  - Create `backend/internal/adapters/timescaledb/evolution_repo.go`:
    ```go
    package timescaledb

    import (
        "context"
        "encoding/json"
        "fmt"
        "time"

        "github.com/oh-my-opentrade/backend/internal/domain"
        "github.com/oh-my-opentrade/backend/internal/ports"
    )

    // GetThoughtLogsSince returns all thought_logs for the tenant/env from `since` to now.
    // Used by the evolution service to sample recent AI debate reasoning.
    func (r *Repository) GetThoughtLogsSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.ThoughtLog, error) {
        rows, err := r.db.QueryContext(ctx,
            `SELECT time, account_id, env_mode, symbol, event_type,
                    COALESCE(direction, ''), COALESCE(confidence, 0),
                    COALESCE(bull_argument, ''), COALESCE(bear_argument, ''),
                    COALESCE(judge_reasoning, ''), COALESCE(rationale, ''), payload
             FROM thought_logs
             WHERE account_id = $1 AND env_mode = $2 AND time >= $3
             ORDER BY time DESC LIMIT 200`,
            tenantID, string(envMode), since,
        )
        if err != nil {
            return nil, fmt.Errorf("timescaledb: get thought logs since: %w", err)
        }
        defer rows.Close()

        var logs []domain.ThoughtLog
        for rows.Next() {
            var tl domain.ThoughtLog
            var sym, envStr string
            var payload json.RawMessage
            if err := rows.Scan(&tl.Time, &tl.TenantID, &envStr, &sym, &tl.EventType,
                &tl.Direction, &tl.Confidence, &tl.BullArgument, &tl.BearArgument,
                &tl.JudgeReasoning, &tl.Rationale, &payload); err != nil {
                return nil, fmt.Errorf("timescaledb: scan thought log: %w", err)
            }
            tl.Symbol = domain.Symbol(sym)
            tl.EnvMode = domain.EnvMode(envStr)
            var p map[string]string
            if json.Unmarshal(payload, &p) == nil {
                tl.IntentID = p["intent_id"]
            }
            logs = append(logs, tl)
        }
        return logs, rows.Err()
    }

    // GetTradesSince returns all trades for the tenant/env from `since` to now.
    // Wraps the existing GetTrades with to=time.Now().
    func (r *Repository) GetTradesSince(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) ([]domain.Trade, error) {
        return r.GetTrades(ctx, tenantID, envMode, since, time.Now().UTC())
    }

    // SaveEvolutionRun persists an evolution cycle audit record to evolution_runs.
    func (r *Repository) SaveEvolutionRun(ctx context.Context, run ports.EvolutionRun) error {
        winnerParamsJSON, _ := json.Marshal(run.WinnerParams)
        _, err := r.db.ExecContext(ctx,
            `INSERT INTO evolution_runs
             (time, id, account_id, env_mode, strategy_id, num_mutations, winner_id,
              winner_params, winner_sharpe, winner_drawdown, proposal_log, evaluation_log, promoted)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
             ON CONFLICT (id) DO NOTHING`,
            run.Time, run.ID, run.TenantID, string(run.EnvMode), run.StrategyID,
            run.NumMutations, run.WinnerID, winnerParamsJSON,
            run.WinnerSharpe, run.WinnerDrawdown, run.ProposalLog, run.EvaluationLog, run.Promoted,
        )
        if err != nil {
            return fmt.Errorf("timescaledb: save evolution run: %w", err)
        }
        return nil
    }
    ```

  **Must NOT do**:
  - Do NOT modify existing repository.go methods
  - Do NOT use hypertable for evolution_runs (it's not time-series append-only enough to justify)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: SQL migration + adapter methods following established repo pattern
  - **Skills**: [`senior-backend`]
    - `senior-backend`: SQL, Go database patterns, TimescaleDB

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Task 7 (service uses EvolutionRepoPort)
  - **Blocked By**: Task 1 (needs EvolutionRun type from ports)

  **References**:
  - `backend/internal/adapters/timescaledb/repository.go:490-560` — SaveThoughtLog + GetThoughtLogsByIntentID pattern
  - `backend/internal/adapters/timescaledb/repository.go:176-210` — GetTrades pattern
  - `backend/migrations/004_create_thought_logs.up.sql` — migration format to follow
  - `backend/internal/ports/evolution.go` — EvolutionRun struct (from Task 1)

  **Acceptance Criteria**:
  - [ ] `backend/migrations/020_add_evolution_run_log.up.sql` created
  - [ ] `backend/internal/adapters/timescaledb/evolution_repo.go` created with 3 methods
  - [ ] `Repository` satisfies `EvolutionRepoPort` interface
  - [ ] `go build ./internal/adapters/timescaledb/...` passes

  **QA Scenarios**:
  ```
  Scenario: Repository satisfies EvolutionRepoPort
    Tool: Bash
    Preconditions: evolution_repo.go created
    Steps:
      1. cd backend && go build ./internal/adapters/timescaledb/...
    Expected Result: exit 0, no interface errors
    Evidence: .sisyphus/evidence/task-3-build.txt

  Scenario: Migration SQL is valid
    Tool: Bash
    Preconditions: migration file created
    Steps:
      1. docker exec omo-timescaledb psql -U opentrade -d opentrade -f /path/to/020_add_evolution_run_log.up.sql
    Expected Result: exit 0, table created
    Evidence: .sisyphus/evidence/task-3-migration.txt
  ```

  **Commit**: YES (groups with Tasks 1, 2)

- [ ] 4. LLM evolution adapter: `internal/adapters/llm/evolution_advisor.go`

  **What to do**:
  - Create `backend/internal/adapters/llm/evolution_advisor.go`. This is a NEW struct `EvolutionAdvisor` that follows the exact same HTTP client pattern as `Advisor` (no SDK, pure `net/http`). It implements `ports.EvolutionAdvisorPort`.
  - Struct definition:
    ```go
    type EvolutionAdvisor struct {
        baseURL    string
        model      string
        apiKey     string
        httpClient *http.Client
        provider   *providerRouting  // reuse providerRouting from advisor.go
    }

    func NewEvolutionAdvisor(baseURL, model, apiKey string, httpClient *http.Client) *EvolutionAdvisor { ... }
    ```
  - **Method 1: `ProposeEvolution`** — sends proposal prompt, parses N mutations
    - System prompt:
      ```
      You are a quantitative trading strategy optimizer. Your task is to propose parameter mutations
      for an algorithmic trading strategy based on recent performance data and AI debate logs.
      Each mutation must be a COMPLETE set of all strategy parameters — not just the changed ones.
      Respond ONLY with valid JSON — no markdown, no extra text.
      ```
    - User prompt structure (build via `buildProposalPrompt`):
      ```
      Strategy: {StrategyID}
      
      Current Parameters:
      {JSON of CurrentParams}
      
      Current Performance (last {LookbackDays} days):
      {JSON of BaselineMetrics}
      
      Recent Trade Sample (last {N} trades):
      {compact trade list: symbol, side, pnl}
      
      Recent AI Debate Reasoning Sample (last {N} debates):
      {compact thought log: symbol, direction, confidence, judge_reasoning}
      
      Instructions:
      Propose exactly {NumMutations} parameter mutations. For each mutation:
      1. Identify one or two parameters to adjust based on the evidence above
      2. Provide the COMPLETE parameter set (not just the changed ones)
      3. Explain your reasoning
      
      You MUST NOT change: strategy ID, version, routing symbols, asset_class, timeframes.
      You MAY adjust: rsi_long, rsi_short, stoch_long, stoch_short, rsi_exit_mid,
        cooldown_seconds, max_trades_per_day, ai_min_confidence, size_mult_min/base/max,
        stop_bps, limit_offset_bps, risk_per_trade_bps, max_position_bps,
        ai_veto_on_strong_opposite, allowed_hours_start, allowed_hours_end.
      
      Response schema (array of N objects):
      [
        {
          "id": "mut-001",
          "parameters": { ... complete param map ... },
          "reasoning": "why these changes address observed issues"
        },
        ...
      ]
      ```
    - Parse response: `json.Unmarshal` into `[]proposalEntry`, map to `[]ports.EvolutionMutation`
    - Internal type: `type proposalEntry struct { ID string; Parameters map[string]any; Reasoning string }`

  - **Method 2: `EvaluateEvolution`** — sends evaluation prompt, parses ranked winners
    - System prompt:
      ```
      You are a quantitative trading strategy evaluator. You have run backtests on multiple
      parameter mutations. Your task is to analyze the results and select the best mutation.
      Respond ONLY with valid JSON — no markdown, no extra text.
      ```
    - User prompt structure (build via `buildEvaluationPrompt`):
      ```
      Strategy: {StrategyID}
      
      Baseline (current live parameters):
      Sharpe: {X}, MaxDrawdown: {X}%, WinRate: {X}%, ProfitFactor: {X}, TotalReturn: {X}%
      
      Mutation Backtest Results:
      {For each candidate:}
      Mutation {ID} ({reasoning}):
        Sharpe: {X}, MaxDrawdown: {X}%, WinRate: {X}%, ProfitFactor: {X},
        TotalReturn: {X}%, Trades: {N}
      
      Instructions:
      1. Rank all mutations from best to worst. Consider: Sharpe ratio (primary), max drawdown (risk),
         win rate, profit factor. Prefer mutations that IMPROVE over baseline.
      2. Select a winner. If NO mutation improves over baseline, still select the least-bad option
         but set confidence < 0.5.
      3. Return ALL candidates ranked.
      
      Response schema:
      {
        "ranked": [
          {
            "id": "mut-001",
            "rank": 1,
            "reasoning": "why this is the best choice",
            "confidence": 0.85
          },
          ...
        ]
      }
      ```
    - Parse response: map ranked entries back to `[]ports.EvolutionWinner` by joining with input candidates

  - **Privacy guardrail comment** (same pattern as `buildPrompt` in advisor.go):
    ```go
    // PRIVACY BOUNDARY: This prompt sends strategy parameter names and values to the LLM endpoint.
    // This is intentional for the evolution service — unlike the real-time debate which must
    // protect parameters, the evolution service requires parameter-level reasoning.
    // Do NOT use this advisor for real-time trading decisions.
    // Recommendation: use a private/local LLM endpoint (Ollama, LM Studio) for evolution.
    ```

  **Must NOT do**:
  - Do NOT add evolution methods to the existing `Advisor` struct — keep them separate
  - Do NOT import `internal/app/...` from the adapter layer (hexagonal boundary)
  - Do NOT send more than 20 trade samples or 20 thought log samples in the prompt (token cost)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Complex prompt engineering, JSON schema design, response parsing with edge cases
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go HTTP client patterns, JSON marshaling/unmarshaling, LLM prompt design
  - **Skills Evaluated but Omitted**:
    - `senior-architect`: Not needed for a single adapter file

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 5, 6)
  - **Blocks**: Task 7 (service uses EvolutionAdvisorPort)
  - **Blocked By**: Task 1 (needs EvolutionAdvisorPort interface)

  **References**:
  - `backend/internal/adapters/llm/advisor.go` — Full file: HTTP client pattern, providerRouting, JSON parsing, rate limiting
  - `backend/internal/adapters/llm/risk_assessor.go` — Second LLM struct following same pattern
  - `backend/internal/ports/evolution.go` — EvolutionAdvisorPort, EvolutionMutation, EvolutionWinner types (Task 1)
  - `configs/strategies/ai_scalping.toml:39-62` — The 23 tunable parameters (params section) to include in prompts

  **Acceptance Criteria**:
  - [ ] `evolution_advisor.go` created, `EvolutionAdvisor` struct implements `EvolutionAdvisorPort`
  - [ ] `ProposeEvolution` sends correct JSON body and parses `[]EvolutionMutation` response
  - [ ] `EvaluateEvolution` sends correct JSON body and parses `[]EvolutionWinner` response
  - [ ] Both methods respect context cancellation (timeout)
  - [ ] Privacy boundary comment present in buildProposalPrompt
  - [ ] `go build ./internal/adapters/llm/...` passes

  **QA Scenarios**:
  ```
  Scenario: EvolutionAdvisor satisfies EvolutionAdvisorPort interface
    Tool: Bash
    Preconditions: evolution_advisor.go created
    Steps:
      1. cd backend && go build ./internal/adapters/llm/...
    Expected Result: exit 0
    Evidence: .sisyphus/evidence/task-4-build.txt

  Scenario: ProposeEvolution returns error on non-2xx HTTP response
    Tool: Bash (go test)
    Preconditions: Mock HTTP server returning 429
    Steps:
      1. cd backend && go test ./internal/adapters/llm/... -run TestEvolutionAdvisor -v
    Expected Result: PASS — error returned, not panic
    Evidence: .sisyphus/evidence/task-4-test.txt
  ```

  **Commit**: YES (groups with Task 5, 6)

- [ ] 5. TOML patcher: `internal/app/evolution/toml_patcher.go`

  **What to do**:
  - Create `backend/internal/app/evolution/toml_patcher.go`
  - Package: `evolution`
  - Purpose: Given current TOML content and a `ports.EvolutionMutation`, write a complete mutated TOML file to a temp path and return the path. Also provide cleanup function.
  - Key types and functions:
    ```go
    package evolution

    import (
        "fmt"
        "os"
        "path/filepath"
        "github.com/BurntSushi/toml"
        "github.com/oh-my-opentrade/backend/internal/ports"
    )

    // TOMLPatcher creates temporary mutated TOML files for backtesting.
    type TOMLPatcher struct {
        outputDir string // e.g. "/tmp/omo-evolve"
    }

    func NewTOMLPatcher(outputDir string) *TOMLPatcher {
        return &TOMLPatcher{outputDir: outputDir}
    }

    // WriteMutatedTOML reads the original TOML file at sourcePath, applies
    // mutation.Parameters (overwriting the [params] section), and writes
    // a complete new TOML to outputDir/{mutationID}.toml.
    // Returns the path of the written file.
    func (p *TOMLPatcher) WriteMutatedTOML(sourcePath string, mutation ports.EvolutionMutation) (string, error) {
        // 1. Read source TOML as raw map[string]any using BurntSushi/toml
        // 2. Replace top-level "params" key with mutation.Parameters
        // 3. Encode back to TOML using toml.NewEncoder
        // 4. Write to outputDir/{mutationID}.toml
        // Returns path string
    }

    // Cleanup deletes the temp file at path. Call defer cleanup() after use.
    func (p *TOMLPatcher) Cleanup(path string) {
        _ = os.Remove(path)
    }

    // EnsureOutputDir creates the output directory if it doesn't exist.
    func (p *TOMLPatcher) EnsureOutputDir() error {
        return os.MkdirAll(p.outputDir, 0o755)
    }
    ```
  - Implementation notes for `WriteMutatedTOML`:
    - Use `toml.DecodeReader` to parse the source into `map[string]any`
    - Replace `rawMap["params"]` with `mutation.Parameters`
    - Use `toml.NewEncoder(f).Encode(rawMap)` to write
    - File name: `filepath.Join(p.outputDir, mutation.ID+".toml")`
    - Preserve ALL other TOML sections unchanged (strategy, lifecycle, routing, exit_rules, etc.)

  **Must NOT do**:
  - Do NOT hard-code field names — the patcher only replaces the `params` key; all other keys are preserved verbatim
  - Do NOT modify `strategy/dna_manager.go` — patcher is standalone

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Simple TOML read-modify-write utility, ~80 lines
  - **Skills**: [`senior-backend`]
    - `senior-backend`: BurntSushi/toml API, Go file I/O patterns

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 6)
  - **Blocks**: Task 7 (service uses patcher)
  - **Blocked By**: Tasks 1, 2 (ports types + config path)

  **References**:
  - `backend/internal/app/strategy/dna_manager.go:Load()` — TOML parsing using BurntSushi/toml (exact same import)
  - `backend/internal/app/strategy/dna_manager.go:UpdateScript()` — file read/write pattern
  - `configs/strategies/ai_scalping.toml` — source TOML structure to preserve
  - `backend/go.mod` — confirm BurntSushi/toml is already a dependency

  **Acceptance Criteria**:
  - [ ] `toml_patcher.go` created in `internal/app/evolution/`
  - [ ] `WriteMutatedTOML` reads source TOML, replaces params section, writes new file
  - [ ] Written file is valid TOML parseable by `strategy.dna_manager.Load()`
  - [ ] `Cleanup` deletes the temp file
  - [ ] `go build ./internal/app/evolution/...` passes

  **QA Scenarios**:
  ```
  Scenario: WriteMutatedTOML produces valid TOML with mutated params
    Tool: Bash (go test)
    Preconditions: ai_scalping.toml exists at configs/strategies/
    Steps:
      1. cd backend && go test ./internal/app/evolution/... -run TestTOMLPatcher -v
      2. Assert output file exists and contains mutated param value
      3. Assert original file is unmodified
    Expected Result: PASS
    Evidence: .sisyphus/evidence/task-5-test.txt
  ```

  **Commit**: YES (groups with Task 4, 6)

- [ ] 6. Backtest runner: `internal/app/evolution/backtest_runner.go`

  **What to do**:
  - Create `backend/internal/app/evolution/backtest_runner.go`
  - Package: `evolution`
  - Purpose: Runs one in-process backtest for a given TOML file and returns `ports.EvolutionBacktestResult`. Mirrors what `omo-replay/main.go` does in backtest mode, but called as a library function.
  - Key type and function:
    ```go
    package evolution

    import (
        "context"
        "fmt"
        "os"
        "time"

        "github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
        "github.com/oh-my-opentrade/backend/internal/adapters/llm"
        "github.com/oh-my-opentrade/backend/internal/adapters/noop"
        "github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
        "github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
        "github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
        "github.com/oh-my-opentrade/backend/internal/app/backtest"
        "github.com/oh-my-opentrade/backend/internal/app/bootstrap"
        "github.com/oh-my-opentrade/backend/internal/app/monitor"
        "github.com/oh-my-opentrade/backend/internal/app/perf"
        "github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
        "github.com/oh-my-opentrade/backend/internal/app/strategy"
        "github.com/oh-my-opentrade/backend/internal/config"
        "github.com/oh-my-opentrade/backend/internal/domain"
        start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
        "github.com/oh-my-opentrade/backend/internal/ports"
        "github.com/rs/zerolog"
    )

    // BacktestConfig holds parameters for a single backtest run.
    type BacktestConfig struct {
        TOMLPath      string          // path to the mutated TOML file
        Symbols       []domain.Symbol // symbols to backtest (from strategy TOML routing)
        From          time.Time
        To            time.Time
        SlippageBPS   int64
        InitialEquity float64
        Timeframe     domain.Timeframe // from cfg.Symbols
        MutationID    string          // for logging
    }

    // BacktestRunner runs in-process backtests using the existing bootstrap pipeline.
    // repo is used ONLY for GetMarketBars (historical bar reads).
    // All trade/order persistence inside the backtest uses noop.NoopRepo.
    type BacktestRunner struct {
        cfg    *config.Config
        repo   *timescaledb.Repository // for market bar reads only (GetMarketBars)
        log    zerolog.Logger
    }

    func NewBacktestRunner(cfg *config.Config, repo *timescaledb.Repository, log zerolog.Logger) *BacktestRunner {
        return &BacktestRunner{cfg: cfg, repo: repo, log: log}
    }

    // Run executes a single in-process backtest and returns the result.
    // It DOES NOT write to the database (uses NoopRepo for trades/orders).
    func (r *BacktestRunner) Run(ctx context.Context, btCfg BacktestConfig) (ports.EvolutionBacktestResult, error) {
        // 1. Create isolated event bus (memory.NewBus())
        // 2. Create store_fs.Store pointing at the DIRECTORY of btCfg.TOMLPath
        //    store_fs.NewStore(filepath.Dir(btCfg.TOMLPath), strategy.LoadSpecFile)
        // 3. Bootstrap: BuildIngestion, BuildMonitor, BuildExecutionService (with noop.NoopRepo),
        //    BuildPositionMonitor, BuildStrategyPipeline (with llm.NewNoOpAdvisor(), DisableEnricher=true)
        // 4. Subscribe backtest.Collector to event bus
        // 5. Replay bars: load from r.repo.GetMarketBars, publish as omo-replay does
        //    - IMPORTANT: use the same warmup pattern from omo-replay (120 bars before fromTime)
        //    - IMPORTANT: call posMonSvc.EvalExitRules after each bar group (like omo-replay)
        // 6. Cancel context, collect result from collector.Result()
        // 7. Map backtest.Result → ports.EvolutionBacktestResult
        // 8. Return result
    }
    ```
  - **CRITICAL implementation detail**: The backtest runner must exactly mirror the omo-replay pipeline wiring sequence. Study `omo-replay/main.go:backtestFlag` branch carefully:
    - `BuildIngestion` → `BuildMonitor` → `BuildExecutionService` → `BuildPositionMonitor` → `BuildStrategyPipeline`
    - `pipeline.RiskSizer.SetExitCooldown(3 * time.Minute)` (backtest mode)
    - `pipeline.RiskSizer.SetNowFn(clockFn)` (use injected clock)
    - `eventBus.WaitPending()` after each bar group
    - `posMonSvc.EvalExitRules(minTime)` after WaitPending
  - **Context isolation**: Each call to `Run()` creates fresh in-process state. No shared state between calls.
  - **Error handling**: If backtest panics (e.g., no bars loaded), recover and return error in result.

  **Must NOT do**:
  - Do NOT use `os/exec` to call the omo-replay binary
  - Do NOT write any trade data to the real TimescaleDB repo (use `&noop.NoopRepo{}` for repo in bootstrap calls)
  - Do NOT share event bus instances between calls to `Run()`
  - Do NOT call `sqlDB.PingContext` (repo already connected, pass it through)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Complex bootstrap wiring, must exactly mirror omo-replay's pipeline setup, multiple sub-services, clock injection, replay loop
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go concurrency, in-process library patterns, event bus patterns

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5)
  - **Blocks**: Task 7
  - **Blocked By**: Tasks 1, 2 (ports + config)

  **References**:
  - `backend/cmd/omo-replay/main.go` — FULL FILE — the entire backtestFlag branch is the template
  - `backend/internal/app/bootstrap/` — all BuildXxx functions used in the wiring
  - `backend/internal/app/backtest/collector.go` — Result struct and Collector API
  - `backend/internal/adapters/simbroker/` — SimBroker constructor and UpdatePrice
  - `backend/internal/adapters/strategy/store_fs/` — Store constructor (directory + loader fn)
  - `backend/internal/adapters/llm/noop_advisor.go` — NewNoOpAdvisor() constructor

  **Acceptance Criteria**:
  - [ ] `backtest_runner.go` created with `BacktestRunner` struct and `Run()` method
  - [ ] `Run()` creates fully isolated event bus per invocation
  - [ ] `Run()` uses `noop.NoopRepo{}` for all trade/order persistence
  - [ ] `Run()` returns `ports.EvolutionBacktestResult` with all 7 metrics populated
  - [ ] `go build ./internal/app/evolution/...` passes

  **QA Scenarios**:
  ```
  Scenario: BacktestRunner builds without import cycle
    Tool: Bash
    Preconditions: backtest_runner.go created
    Steps:
      1. cd backend && go build ./internal/app/evolution/...
    Expected Result: exit 0, no import cycle errors
    Evidence: .sisyphus/evidence/task-6-build.txt
  ```

  **Commit**: YES (groups with Task 4, 5)

- [ ] 7. Evolution orchestration service: `internal/app/evolution/service.go`

  **What to do**:
  - Create `backend/internal/app/evolution/service.go`
  - Package: `evolution`
  - This is the heart of the system — a 5-step orchestration service.
  - **Service struct**:
    ```go
    package evolution

    import (
        "context"
        "fmt"
        "time"

        "github.com/google/uuid"
        "github.com/oh-my-opentrade/backend/internal/config"
        "github.com/oh-my-opentrade/backend/internal/domain"
        "github.com/oh-my-opentrade/backend/internal/ports"
        "github.com/rs/zerolog"
    )

    // Service orchestrates one complete evolution cycle.
    type Service struct {
        cfg            *config.Config
        evolutionCfg   config.EvolutionConfig
        advisor        ports.EvolutionAdvisorPort
        repo           ports.EvolutionRepoPort
        baseRepo       ports.RepositoryPort       // for SaveStrategyDNA, SaveThoughtLog
        runner         *BacktestRunner
        patcher        *TOMLPatcher
        log            zerolog.Logger
    }

    func NewService(
        cfg *config.Config,
        advisor ports.EvolutionAdvisorPort,
        repo ports.EvolutionRepoPort,
        baseRepo ports.RepositoryPort,
        runner *BacktestRunner,
        patcher *TOMLPatcher,
        log zerolog.Logger,
    ) *Service { ... }
    ```
  - **5-step `Run(ctx context.Context) error` method**:

    **Step 1: GATHER DATA**
    ```go
    func (s *Service) gatherData(ctx context.Context) (*evolutionContext, error) {
        lookback := time.Duration(s.evolutionCfg.LookbackDays) * 24 * time.Hour
        since := time.Now().UTC().Add(-lookback)
        tenantID := s.evolutionCfg.TenantID
        envMode := domain.EnvModePaper

        trades, err := s.repo.GetTradesSince(ctx, tenantID, envMode, since)
        thoughtLogs, err := s.repo.GetThoughtLogsSince(ctx, tenantID, envMode, since)

        // Load current DNA from TOML file (not DB — live config is in TOML)
        dnaManager := strategy_pkg.NewDNAManager() // import strategy package
        currentDNA, err := dnaManager.Load(s.evolutionCfg.StrategyPath)

        // Get baseline metrics from strategy_dna_history (latest version)
        latestDBDNA, err := s.baseRepo.GetLatestStrategyDNA(ctx, tenantID, envMode)
        // If no DB record, use empty metrics as baseline

        return &evolutionContext{
            currentParams:   currentDNA.Parameters,
            baselineMetrics: latestDBDNA.PerformanceMetrics, // may be empty
            trades:          trades,
            thoughtLogs:     thoughtLogs,
            since:           since,
        }, nil
    }
    ```

    **Step 2: LLM PROPOSES MUTATIONS**
    ```go
    func (s *Service) proposeMutations(ctx context.Context, ec *evolutionContext) ([]ports.EvolutionMutation, error) {
        // Build compact trade summary (max 20)
        tradeSummaries := buildTradeSummaries(ec.trades, 20)
        // Build compact thought log sample (max 20)
        thoughtSamples := buildThoughtSamples(ec.thoughtLogs, 20)

        req := ports.EvolutionProposalRequest{
            StrategyID:       strategyIDFromPath(s.evolutionCfg.StrategyPath),
            CurrentParams:    ec.currentParams,
            BaselineMetrics:  ec.baselineMetrics,
            RecentTrades:     tradeSummaries,
            ThoughtLogSample: thoughtSamples,
            NumMutations:     s.evolutionCfg.NumMutations,
        }
        mutations, err := s.advisor.ProposeEvolution(ctx, req)
        
        // Save proposal to thought_logs with event_type = "evolution_propose"
        s.saveThoughtLog(ctx, "evolution_propose", fmt.Sprintf("%d mutations proposed", len(mutations)), ...)
        
        return mutations, err
    }
    ```

    **Step 3: BACKTEST EACH MUTATION (SEQUENTIAL)**
    ```go
    func (s *Service) backtestMutations(ctx context.Context, mutations []ports.EvolutionMutation, btFrom, btTo time.Time) ([]ports.EvolutionBacktestResult, error) {
        // Also run baseline (current TOML params) for comparison
        var results []ports.EvolutionBacktestResult

        for _, mut := range mutations {
            // Write temp TOML
            tmpPath, err := s.patcher.WriteMutatedTOML(s.evolutionCfg.StrategyPath, mut)
            defer s.patcher.Cleanup(tmpPath)

            // Extract symbols from TOML (parse routing.symbols)
            symbols := extractSymbolsFromTOML(s.evolutionCfg.StrategyPath)

            // Run backtest
            btCfg := BacktestConfig{
                TOMLPath:      tmpPath,
                Symbols:       symbols,
                From:          btFrom,
                To:            btTo,
                SlippageBPS:   s.evolutionCfg.SlippageBPS,
                InitialEquity: s.evolutionCfg.InitialEquity,
                Timeframe:     "1m",
                MutationID:    mut.ID,
            }
            result, err := s.runner.Run(ctx, btCfg)
            result.Mutation = mut
            results = append(results, result)
            
            s.log.Info().Str("mutation_id", mut.ID).
                Float64("sharpe", result.SharpeRatio).
                Float64("drawdown", result.MaxDrawdown).
                Msg("backtest complete")
        }
        return results, nil
    }
    ```

    **Step 4: LLM EVALUATES RESULTS**
    ```go
    func (s *Service) evaluateResults(ctx context.Context, ec *evolutionContext, results []ports.EvolutionBacktestResult) ([]ports.EvolutionWinner, error) {
        req := ports.EvolutionEvaluationRequest{
            StrategyID:      strategyIDFromPath(s.evolutionCfg.StrategyPath),
            CurrentParams:   ec.currentParams,
            BaselineMetrics: ec.baselineMetrics,
            Candidates:      results,
        }
        winners, err := s.advisor.EvaluateEvolution(ctx, req)
        
        // Save evaluation to thought_logs with event_type = "evolution_evaluate"
        s.saveThoughtLog(ctx, "evolution_evaluate", winners[0].Reasoning, ...)
        
        return winners, err
    }
    ```

    **Step 5: PERSIST WINNER**
    ```go
    func (s *Service) persistWinner(ctx context.Context, winner ports.EvolutionWinner, runID string) error {
        // Get next version from DB
        tenantID := s.evolutionCfg.TenantID
        envMode := domain.EnvModePaper
        
        latestDNA, _ := s.baseRepo.GetLatestStrategyDNA(ctx, tenantID, envMode)
        nextVersion := 1
        if latestDNA != nil {
            nextVersion = latestDNA.Version + 1
        }
        
        // Build domain.StrategyDNA with winner params + backtest metrics
        dnaID := uuid.MustParse("...") // fixed UUID derived from strategy ID
        metrics := map[string]float64{
            "sharpe_ratio":   winner.BacktestResult.SharpeRatio,
            "max_drawdown":   winner.BacktestResult.MaxDrawdown,
            "win_rate":       winner.BacktestResult.WinRate,
            "profit_factor":  winner.BacktestResult.ProfitFactor,
            "total_return":   winner.BacktestResult.TotalReturn,
            "trade_count":    float64(winner.BacktestResult.TradeCount),
            "llm_confidence": winner.Confidence,
        }
        dna := domain.StrategyDNA{
            ID:                 dnaID,
            TenantID:           tenantID,
            EnvMode:            envMode,
            Version:            nextVersion,
            Parameters:         winner.Mutation.Parameters,
            PerformanceMetrics: metrics,
        }
        if err := s.baseRepo.SaveStrategyDNA(ctx, dna); err != nil {
            return fmt.Errorf("evolution: save winner DNA: %w", err)
        }
        
        // Save winner thought log
        s.saveThoughtLog(ctx, "evolution_winner", winner.Reasoning, winner.Confidence)
        
        // Save full evolution run audit record
        return s.repo.SaveEvolutionRun(ctx, ports.EvolutionRun{
            ID:             runID,
            Time:           time.Now().UTC(),
            StrategyID:     strategyIDFromPath(s.evolutionCfg.StrategyPath),
            TenantID:       tenantID,
            EnvMode:        envMode,
            NumMutations:   len(winners),
            WinnerID:       winner.Mutation.ID,
            WinnerParams:   winner.Mutation.Parameters,
            WinnerSharpe:   winner.BacktestResult.SharpeRatio,
            WinnerDrawdown: winner.BacktestResult.MaxDrawdown,
            // ProposalLog + EvaluationLog: JSON encode from advisor responses
            Promoted:       false, // always false — human approval required
        })
    }
    ```

    - **`saveThoughtLog` helper**:
      ```go
      func (s *Service) saveThoughtLog(ctx context.Context, eventType, rationale string, confidence float64) {
          tl := domain.ThoughtLog{
              Time:           time.Now().UTC(),
              TenantID:       s.evolutionCfg.TenantID,
              EnvMode:        domain.EnvModePaper,
              Symbol:         domain.Symbol("*"), // evolution is not symbol-specific
              EventType:      eventType,
              Direction:      "",
              Confidence:     confidence,
              Rationale:      rationale,
              JudgeReasoning: "", // populated for evaluate + winner events
          }
          if err := s.baseRepo.SaveThoughtLog(ctx, tl); err != nil {
              s.log.Error().Err(err).Str("event_type", eventType).Msg("failed to save evolution thought log")
          }
      }
      ```

    - **Helper `strategyIDFromPath`**: parse TOML file header to get strategy.id (use DNAManager.Load().ID)
    - **Helper `extractSymbolsFromTOML`**: parse routing.symbols from TOML using BurntSushi/toml into a raw map

  **Must NOT do**:
  - Do NOT auto-promote winner to live TOML (`Promoted` must always be `false`)
  - Do NOT run `Step 5` when `--dry-run` flag is set (the service itself doesn't know about dry-run; the binary decides whether to call `Run` or `DryRun`)
  - Do NOT share mutable state between concurrent calls (design for single-goroutine use)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Complex multi-step orchestration, requires careful error handling at each step, domain logic
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go service patterns, error wrapping, hexagonal architecture

  **Parallelization**:
  - **Can Run In Parallel**: NO (sequential, depends on all Wave 2 outputs)
  - **Parallel Group**: Wave 3 (solo)
  - **Blocks**: Task 8
  - **Blocked By**: Tasks 3, 4, 5, 6

  **References**:
  - `backend/internal/app/debate/service.go` — AI-calling service pattern with thought log persistence
  - `backend/internal/ports/evolution.go` — All evolution port types (Task 1)
  - `backend/internal/adapters/timescaledb/evolution_repo.go` — EvolutionRepoPort methods (Task 3)
  - `backend/internal/adapters/timescaledb/repository.go:211-260` — SaveStrategyDNA implementation
  - `backend/internal/domain/entity.go` — domain.StrategyDNA, domain.ThoughtLog constructors
  - `backend/internal/app/evolution/backtest_runner.go` — BacktestRunner.Run() API (Task 6)
  - `backend/internal/app/evolution/toml_patcher.go` — TOMLPatcher.WriteMutatedTOML() API (Task 5)

  **Acceptance Criteria**:
  - [ ] `service.go` created with `Service` struct, `Run()` and all 5 step methods
  - [ ] `Run()` returns nil error on success, wrapped error on failure
  - [ ] `saveThoughtLog` uses `domain.Symbol("*")` as symbol for evolution logs
  - [ ] `persistWinner` sets `Promoted: false` always
  - [ ] `go build ./internal/app/evolution/...` passes with all 4 files present

  **QA Scenarios**:
  ```
  Scenario: Service builds without import cycle
    Tool: Bash
    Preconditions: All Wave 2 tasks complete, service.go created
    Steps:
      1. cd backend && go build ./internal/app/evolution/...
    Expected Result: exit 0
    Evidence: .sisyphus/evidence/task-7-build.txt
  ```

  **Commit**: YES
  - Message: `feat(evolution): implement evolution orchestration service`

- [ ] 8. CLI binary: `cmd/omo-evolve/main.go`

  **What to do**:
  - Create `backend/cmd/omo-evolve/main.go`
  - Follow exact same pattern as `cmd/omo-replay/main.go`: flag parsing, config loading, zerolog setup, signal handling
  - **CLI flags**:
    ```go
    flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
    flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
    flag.StringVar(&strategyPath, "strategy", "", "Override strategy TOML path (default: cfg.Evolution.StrategyPath)")
    flag.StringVar(&fromFlag, "from", "", "Backtest start date YYYY-MM-DD (default: lookback_days ago)")
    flag.StringVar(&toFlag, "to", "", "Backtest end date YYYY-MM-DD (default: today)")
    flag.BoolVar(&dryRun, "dry-run", false, "Propose mutations and run backtests but do NOT persist to DB")
    flag.IntVar(&numMutations, "mutations", 0, "Override num_mutations from config (0 = use config)")
    ```
  - **Wiring sequence**:
    ```go
    // 1. Load config + env
    cfg, err := config.Load(envPath, configPath)

    // 2. Setup zerolog (same as omo-replay)
    log := logger.New(...)

    // 3. Connect to TimescaleDB (same DSN construction as omo-replay)
    // 4. Create repo := timescaledb.NewRepositoryWithLogger(...)

    // 5. Create EvolutionAdvisor
    model := cfg.Evolution.Model
    if model == "" { model = cfg.AI.Model }
    advisor := llm.NewEvolutionAdvisor(cfg.AI.BaseURL, model, cfg.AI.APIKey, http.DefaultClient)

    // 6. Create BacktestRunner
    runner := evolution.NewBacktestRunner(cfg, repoForBars, log)

    // 7. Create TOMLPatcher
    patcher := evolution.NewTOMLPatcher(cfg.Evolution.OutputDir)
    if err := patcher.EnsureOutputDir(); err != nil { ... }

    // 8. Create Service
    svc := evolution.NewService(cfg, advisor, repo, repo, runner, patcher, log)

    // 9. Run or DryRun based on --dry-run flag
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    // Signal handling (same as omo-replay)

    if dryRun {
        // Call service step methods individually, print results, do NOT call persistWinner
        // Print proposed mutations in formatted table
        // Print backtest results per mutation
        // Print recommended winner with reasoning
        // Exit 0 without writing to DB
    } else {
        if err := svc.Run(ctx); err != nil {
            log.Fatal().Err(err).Msg("evolution run failed")
        }
        log.Info().Msg("evolution cycle complete")
    }
    ```
  - **Dry-run output format** (print to stdout):
    ```
    === EVOLUTION DRY RUN ===
    Strategy: ai_scalping_v1 (configs/strategies/ai_scalping.toml)
    Lookback: 2026-01-01 to 2026-01-31

    Proposed Mutations (5):
    mut-001: rsi_long 30→25, stop_bps 50→60 — "RSI too conservative, stops too tight"
    mut-002: cooldown_seconds 60→30, max_trades_per_day 10→15 — "Undertrading in balance regime"
    ...

    Backtest Results:
    BASELINE:   Sharpe=0.82  Drawdown=3.1%  WinRate=54%  ProfitFactor=1.3  Return=+2.1%
    mut-001:    Sharpe=1.14  Drawdown=2.7%  WinRate=58%  ProfitFactor=1.6  Return=+3.8%  ✓ WINNER
    mut-002:    Sharpe=0.71  Drawdown=4.2%  WinRate=51%  ProfitFactor=1.1  Return=+1.2%
    ...

    Winner: mut-001 (confidence: 0.87)
    Reasoning: <LLM evaluation text>

    *** DRY RUN — nothing was written to the database ***
    ```

  **Must NOT do**:
  - Do NOT import omo-core packages (no circular dependency)
  - Do NOT call `evolution.Service.Run()` when `--dry-run` is set — call the step methods individually
  - Do NOT have `enabled` config guard in the binary (caller is responsible for whether to run)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: CLI binary wiring, multiple dependencies, flag parsing, service instantiation, dry-run output formatting
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go CLI patterns, service wiring, graceful shutdown

  **Parallelization**:
  - **Can Run In Parallel**: NO (sequential, depends on Task 7)
  - **Parallel Group**: Wave 4 (solo)
  - **Blocks**: Tasks 9, 10, 11
  - **Blocked By**: Tasks 2, 7

  **References**:
  - `backend/cmd/omo-replay/main.go` — FULL FILE: flag parsing, config loading, signal handling, DB connection, logger setup — copy the boilerplate pattern exactly
  - `backend/internal/app/evolution/service.go` — Service API (Task 7)
  - `backend/internal/adapters/llm/evolution_advisor.go` — EvolutionAdvisor constructor (Task 4)
  - `backend/internal/config/config.go` — Config.Evolution field (Task 2)

  **Acceptance Criteria**:
  - [ ] `backend/cmd/omo-evolve/main.go` created
  - [ ] `go build -o bin/omo-evolve ./cmd/omo-evolve` succeeds
  - [ ] `./bin/omo-evolve --help` prints all flags
  - [ ] `./bin/omo-evolve --dry-run` prints evolution output without DB writes
  - [ ] SIGINT gracefully cancels the running evolution

  **QA Scenarios**:
  ```
  Scenario: Binary builds successfully
    Tool: Bash
    Preconditions: All Wave 1-3 tasks complete
    Steps:
      1. cd backend && go build -o bin/omo-evolve ./cmd/omo-evolve
    Expected Result: exit 0, bin/omo-evolve exists
    Evidence: .sisyphus/evidence/task-8-build.txt

  Scenario: --help flag prints usage
    Tool: Bash
    Preconditions: Binary built
    Steps:
      1. ./bin/omo-evolve --help
    Expected Result: Lists all flags: --config, --env-file, --strategy, --from, --to, --dry-run, --mutations
    Evidence: .sisyphus/evidence/task-8-help.txt
  ```

  **Commit**: YES
  - Message: `feat(evolution): add omo-evolve CLI binary`

- [ ] 9. Unit tests: `internal/app/evolution/`

  **What to do**:
  - Create `backend/internal/app/evolution/service_test.go` — unit tests for Service using mock ports
  - Create `backend/internal/app/evolution/toml_patcher_test.go` — unit tests for TOMLPatcher
  - **Service tests** (use table-driven pattern):
    - Mock `EvolutionAdvisorPort` and `EvolutionRepoPort` using `testing/mock` or hand-written mocks
    - Test: `ProposeEvolution` failure → `Run()` returns wrapped error
    - Test: empty mutations list → `Run()` returns specific error "no mutations proposed"
    - Test: all backtests fail → `EvaluateEvolution` still called with error results, service handles gracefully
    - Test: `persistWinner` always sets `Promoted: false`
    - Test: `saveThoughtLog` called 3 times per run (propose, evaluate, winner)
  - **TOMLPatcher tests**:
    - Test: `WriteMutatedTOML` with `ai_scalping.toml` produces a valid TOML with mutated `rsi_long`
    - Test: original file is NOT modified
    - Test: `Cleanup` deletes the temp file
    - Test: second write with same mutation ID overwrites (not appends)
  - Mock patterns: follow `backend/internal/adapters/noop/repo.go` pattern for mock struct creation

  **Must NOT do**:
  - Do NOT write integration tests here (no real DB, no real LLM)
  - Do NOT test BacktestRunner in unit tests (too complex to mock; covered by integration test in Task 11)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Multiple test scenarios, mock creation, table-driven tests, error path coverage
  - **Skills**: [`testing-patterns`, `senior-backend`]
    - `testing-patterns`: Go test factory patterns, mock strategies, table-driven tests
    - `senior-backend`: Go testing idioms, error handling verification

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 5 (with Tasks 10, 11)
  - **Blocks**: Final wave
  - **Blocked By**: Task 7 (service must exist before testing)

  **References**:
  - `backend/internal/app/evolution/service.go` — Service API under test (Task 7)
  - `backend/internal/app/evolution/toml_patcher.go` — TOMLPatcher under test (Task 5)
  - `backend/internal/adapters/noop/repo.go` — Mock pattern to follow
  - `configs/strategies/ai_scalping.toml` — Source TOML for patcher tests

  **Acceptance Criteria**:
  - [ ] `service_test.go` and `toml_patcher_test.go` created
  - [ ] `go test ./internal/app/evolution/...` passes (all tests green)
  - [ ] Coverage includes error paths (propose failure, persist failure)
  - [ ] TOMLPatcher test verifies param mutation is in output file

  **QA Scenarios**:
  ```
  Scenario: All evolution package tests pass
    Tool: Bash
    Preconditions: Task 7 complete, test files created
    Steps:
      1. cd backend && go test ./internal/app/evolution/... -v -count=1
    Expected Result: PASS, no failures, exit 0
    Evidence: .sisyphus/evidence/task-9-tests.txt
  ```

  **Commit**: YES (groups with Task 10)
  - Message: `test(evolution): add unit tests for evolution package`

- [ ] 10. Unit tests: `internal/adapters/llm/evolution_advisor_test.go`

  **What to do**:
  - Create `backend/internal/adapters/llm/evolution_advisor_test.go`
  - Follow the existing `advisor_test.go` pattern (look at how it mocks the HTTP server)
  - **Tests**:
    - `TestProposeEvolution_Success`: mock HTTP server returns valid JSON with 3 mutations → parsed correctly into `[]ports.EvolutionMutation`
    - `TestProposeEvolution_InvalidJSON`: mock returns garbled response → error returned
    - `TestProposeEvolution_Non2xx`: mock returns 429 → error with status code
    - `TestEvaluateEvolution_Success`: mock HTTP server returns ranked winners → parsed correctly
    - `TestEvaluateEvolution_EmptyRanked`: mock returns `{"ranked": []}` → error "no winners returned"
    - `TestEvaluateEvolution_ContextTimeout`: mock delays 5s, context timeout 1s → context error returned
    - Test prompt content: verify `buildProposalPrompt` includes "rsi_long" (param name) in the request body
    - Test HTTP headers: Authorization Bearer header sent when apiKey non-empty

  **Must NOT do**:
  - Do NOT make real HTTP calls in tests — use `httptest.NewServer`

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: HTTP server mocking, JSON response crafting, multiple error paths
  - **Skills**: [`testing-patterns`, `senior-backend`]
    - `testing-patterns`: Mock HTTP server patterns in Go
    - `senior-backend`: `net/http/httptest`, response crafting

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 5 (with Tasks 9, 11)
  - **Blocks**: Final wave
  - **Blocked By**: Task 4 (evolution_advisor must exist)

  **References**:
  - `backend/internal/adapters/llm/advisor_test.go` — FULL FILE: httptest.Server pattern, JSON mock responses
  - `backend/internal/adapters/llm/evolution_advisor.go` — Implementation under test (Task 4)
  - `backend/internal/ports/evolution.go` — Expected return types

  **Acceptance Criteria**:
  - [ ] `evolution_advisor_test.go` created with 6+ test cases
  - [ ] `go test ./internal/adapters/llm/... -run TestEvolution` passes
  - [ ] Context timeout test verifies function returns within 2s of context cancellation

  **QA Scenarios**:
  ```
  Scenario: All evolution advisor tests pass
    Tool: Bash
    Preconditions: evolution_advisor.go and test file created
    Steps:
      1. cd backend && go test ./internal/adapters/llm/... -run TestEvolution -v -count=1
    Expected Result: PASS, 6+ tests, exit 0
    Evidence: .sisyphus/evidence/task-10-tests.txt
  ```

  **Commit**: YES (groups with Task 9)

- [ ] 11. Integration smoke test + dry-run verification

  **What to do**:
  - Run the complete integration test against real infrastructure (TimescaleDB + LLM endpoint)
  - Step 1: Ensure DB migration `020_add_evolution_run_log.up.sql` is applied
  - Step 2: Build the binary: `cd backend && go build -o bin/omo-evolve ./cmd/omo-evolve`
  - Step 3: Run dry-run smoke test:
    ```bash
    ./bin/omo-evolve \
      --dry-run \
      --config configs/config.yaml \
      --env-file .env \
      --from 2026-01-01 \
      --to 2026-01-31
    ```
    Verify:
    - Exit code 0
    - Stdout contains "Proposed Mutations"
    - Stdout contains "Backtest Results"
    - Stdout contains "*** DRY RUN"
    - `thought_logs` table NOT modified (confirm with psql query)
    - `strategy_dna_history` table NOT modified
  - Step 4: Run full evolution (without --dry-run):
    ```bash
    ./bin/omo-evolve \
      --config configs/config.yaml \
      --env-file .env \
      --from 2026-01-01 \
      --to 2026-01-31
    ```
    Verify:
    - Exit code 0
    - `strategy_dna_history` has new row: `SELECT * FROM strategy_dna_history ORDER BY time DESC LIMIT 1`
    - `thought_logs` has 3 evolution rows: `SELECT event_type FROM thought_logs WHERE event_type LIKE 'evolution%' ORDER BY time DESC LIMIT 5`
    - `evolution_runs` table has 1 new row

  **Must NOT do**:
  - Do NOT run during market hours
  - Do NOT use `--from` dates before available market bar data

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Requires running real binary, checking real DB, interpreting output
  - **Skills**: [`monitor-omo-services`]
    - `monitor-omo-services`: DB verification, log checking patterns for this project

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 5 (with Tasks 9, 10)
  - **Blocks**: Final wave
  - **Blocked By**: Task 8 (binary must exist)

  **References**:
  - `AGENTS.md` — DB verification queries (psql patterns)
  - `backend/cmd/omo-evolve/main.go` — Binary flags to use (Task 8)

  **Acceptance Criteria**:
  - [ ] `go build -o bin/omo-evolve ./cmd/omo-evolve` exits 0
  - [ ] Dry-run exits 0, prints mutations, prints backtest results, does NOT write to DB
  - [ ] Full run inserts row to `strategy_dna_history` with `performance` JSONB containing `sharpe_ratio`
  - [ ] `thought_logs` has rows with event_type in `('evolution_propose', 'evolution_evaluate', 'evolution_winner')`

  **QA Scenarios**:
  ```
  Scenario: Dry-run completes without DB writes
    Tool: Bash
    Preconditions: Binary built, DB accessible, LLM endpoint reachable
    Steps:
      1. ./bin/omo-evolve --dry-run --config configs/config.yaml --env-file .env --from 2026-01-01 --to 2026-01-31
      2. Assert exit code 0
      3. Assert stdout contains "*** DRY RUN"
      4. docker exec omo-timescaledb psql -U opentrade -d opentrade -c "SELECT COUNT(*) FROM evolution_runs WHERE time > NOW() - INTERVAL '1 minute'"
      5. Assert count = 0
    Expected Result: Dry run passes, no DB writes
    Evidence: .sisyphus/evidence/task-11-dryrun.txt

  Scenario: Full run persists audit trail
    Tool: Bash
    Preconditions: Dry-run passed
    Steps:
      1. ./bin/omo-evolve --config configs/config.yaml --env-file .env --from 2026-01-01 --to 2026-01-31
      2. Assert exit code 0
      3. docker exec omo-timescaledb psql -U opentrade -d opentrade -c "SELECT version, performance->>'sharpe_ratio' FROM strategy_dna_history ORDER BY time DESC LIMIT 1"
      4. Assert row exists with sharpe_ratio populated
      5. docker exec omo-timescaledb psql -U opentrade -d opentrade -c "SELECT event_type FROM thought_logs WHERE event_type LIKE 'evolution%' ORDER BY time DESC LIMIT 5"
      6. Assert 3 rows: evolution_propose, evolution_evaluate, evolution_winner
    Expected Result: Full audit trail written to DB
    Evidence: .sisyphus/evidence/task-11-fullrun.txt
  ```

  **Commit**: YES (groups with Tasks 9, 10)

---

## Final Verification Wave

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [ ] F1. **Plan Compliance Audit** — `oracle`
  Read plan end-to-end. For each "Must Have": verify file exists (`ls backend/cmd/omo-evolve`, `ls backend/internal/app/evolution/`, `ls backend/internal/adapters/llm/evolution_advisor.go`). For each "Must NOT Have": grep for forbidden patterns (`os/exec` in evolution package, `EnvModeLive`). Check `strategy_dna_history` row exists after integration test. Verify `thought_logs` has evolution event_type rows.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] F2. **Code Quality Review** — `unspecified-high`
  Run `go build ./...` + `go vet ./...` from backend/. Check for `as any`/unchecked errors, empty error returns, missing context propagation, unused imports. Check AI slop: no excessive comments, no over-abstraction, no generic names.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | VERDICT`

- [ ] F3. **Real Dry-Run QA** — `unspecified-high`
  Build binary: `go build -o bin/omo-evolve ./cmd/omo-evolve`. Run `./bin/omo-evolve --dry-run --config configs/config.yaml --env-file .env`. Verify: exits 0, prints proposed mutations, prints backtest results summary, does NOT write to `strategy_dna_history`. Run without `--dry-run` and verify DB row inserted.
  Output: `Dry-run [PASS/FAIL] | DB write [PASS/FAIL] | VERDICT`

- [ ] F4. **Scope Fidelity Check** — `deep`
  For each task: verify "What to do" matches actual diff. Check no modification to `internal/app/backtest/`, `internal/app/bootstrap/`, or `internal/app/strategy/`. Verify `EnvModeLive` never appears in evolution package. Verify no auto-TOML-promotion code.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `feat(evolution): add domain types, ports, and config stanza` — ports/evolution.go, config/config.go, config.yaml, migration
- **Wave 2**: `feat(evolution): add LLM evolution advisor, TOML patcher, backtest runner` — adapters/llm/evolution_advisor.go, app/evolution/toml_patcher.go, app/evolution/backtest_runner.go
- **Wave 3**: `feat(evolution): implement evolution orchestration service` — app/evolution/service.go
- **Wave 4**: `feat(evolution): add omo-evolve CLI binary` — cmd/omo-evolve/main.go
- **Wave 5**: `test(evolution): add unit and integration tests` — all *_test.go files

---

## Success Criteria

### Verification Commands
```bash
# Build
cd backend && go build ./cmd/omo-evolve  # Expected: no errors

# Tests
cd backend && go test ./internal/app/evolution/... ./internal/adapters/llm/...  # Expected: PASS

# Dry run
./bin/omo-evolve --dry-run --config configs/config.yaml --env-file .env  # Expected: exit 0, prints mutations

# Check DB audit trail
docker exec omo-timescaledb psql -U opentrade -d opentrade -c \
  "SELECT strategy_id, version, performance FROM strategy_dna_history ORDER BY time DESC LIMIT 3;"
# Expected: at least 1 row after full run

# Check thought logs
docker exec omo-timescaledb psql -U opentrade -d opentrade -c \
  "SELECT event_type, rationale FROM thought_logs WHERE event_type LIKE 'evolution%' ORDER BY time DESC LIMIT 5;"
# Expected: rows with event_type in ('evolution_propose', 'evolution_evaluate', 'evolution_winner')
```

### Final Checklist
- [ ] `go build ./cmd/omo-evolve` passes
- [ ] All unit tests pass
- [ ] `--dry-run` works without DB writes
- [ ] Full run inserts row to `strategy_dna_history`
- [ ] `thought_logs` has evolution reasoning rows
- [ ] No modifications to omo-core packages
- [ ] No `os/exec` subprocess calls
- [ ] No `EnvModeLive` in evolution code
