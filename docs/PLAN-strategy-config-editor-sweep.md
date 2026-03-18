---
goal: "Add web-based strategy configuration editor and automated parameter sweep feature"
version: 1.0
date_created: 2026-03-17
last_updated: 2026-03-17
owner: oh-my-opentrade team
status: 'Planned'
tags:
  - feature
  - frontend
  - backend
  - backtest
  - strategy
  - parameter-sweep
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

This plan adds two major features to the oh-my-opentrade platform:

1. **Strategy Configuration Editor** — A web-based form UI that reads and writes strategy TOML files via new REST API endpoints, enabling real-time parameter editing with hot-reload.
2. **Automated Parameter Sweep** — A grid-search optimization engine that runs N parallel backtests across parameter ranges, streams progress via SSE, ranks results by a target metric, and allows one-click application of winning parameters.

Both features follow the existing hexagonal architecture (ports/adapters), reuse the existing `backtest.Runner` and `store_fs.Store`, and integrate with the Next.js 15 dashboard using established UI patterns (shadcn components, Tailwind, lightweight-charts).

## 1. Requirements & Constraints

### Functional Requirements

- **REQ-001**: `GET /api/strategies/{id}/config` returns the full strategy config as JSON (strategy metadata, lifecycle, routing, params, exit rules, symbol overrides, dynamic_risk, regime_filter, hooks)
- **REQ-002**: `PUT /api/strategies/{id}/config` accepts a JSON body, validates all fields, writes back to the TOML file at `configs/strategies/{id}.toml`, and triggers hot-reload via the existing `store_fs.Watch` mechanism (file mtime change)
- **REQ-003**: The config GET response must include a `param_schema` field with typed metadata (type, default, min, max, step, description, group) for each parameter — enabling dynamic form generation without frontend hardcoding
- **REQ-004**: A new Next.js page at `/strategies/{id}/config` renders a dynamic form grouped into sections: Strategy Params, Exit Rules (one panel per rule type), Symbol Overrides (tabbed per symbol), Routing, Lifecycle, Dynamic Risk, Regime Filter
- **REQ-005**: The config editor page includes a "Run Backtest with These Params" button that navigates to `/backtest` with the current params pre-filled (strategy ID, symbols from routing, timeframe from routing)
- **REQ-006**: `POST /api/strategies/{id}/sweep` accepts a sweep config specifying: parameter ranges (param name → {min, max, step}), fixed backtest params (symbols, date range, timeframe, equity, slippage), target metric for ranking (sharpe_ratio, profit_factor, total_pnl, win_rate_pct, max_drawdown_pct), and concurrency limit
- **REQ-007**: The sweep endpoint returns a `sweep_id` immediately and streams progress via SSE at `GET /api/strategies/{id}/sweep/{sweep_id}/events` with events: `sweep:progress` (completed N of M), `sweep:run_complete` (individual run result), `sweep:done` (final ranked results)
- **REQ-008**: `POST /api/strategies/{id}/sweep/{sweep_id}/apply/{run_index}` writes the winning parameter set back to the TOML file (reusing the config PUT logic)
- **REQ-009**: The sweep UI page at `/strategies/{id}/sweep` shows: parameter range configuration form, progress bar during execution, results table with sortable columns (all sweep params + trade_count + win_rate + P&L + Sharpe + PF + max_drawdown), and an "Apply" button per row
- **REQ-010**: For 2-parameter sweeps, the UI renders a heatmap visualization where X/Y axes are the two swept parameters and cell color intensity represents the target metric value

### Security & Safety Requirements

- **SEC-001**: All config writes must validate the Spec through the existing `LoadSpecFile` pipeline before persisting — reject invalid TOML/param combinations before overwriting files
- **SEC-002**: Config PUT must create a `.bak` backup of the current TOML file before overwriting, enabling rollback
- **SEC-003**: Sweep concurrency must be capped (default: 4 parallel runners) to prevent resource exhaustion on a single machine
- **SEC-004**: Sweep must be cancellable — `DELETE /api/strategies/{id}/sweep/{sweep_id}` cancels all in-flight backtest runners

### Architecture Constraints

- **CON-001**: Follow existing hexagonal architecture — new domain logic in `internal/domain/`, new port interfaces in `internal/ports/`, new adapters in `internal/adapters/`, application orchestration in `internal/app/`
- **CON-002**: TOML files at `configs/strategies/` remain the single source of truth — no database storage for strategy configs
- **CON-003**: TOML serialization must use `github.com/BurntSushi/toml` (already in go.mod) — the existing `encodeV2` function in `store_fs/store.go` provides the write pattern but must be extended to handle exit_rules, symbol_overrides, dynamic_risk, screening, options, and risk_revaluation sections
- **CON-004**: Frontend must use existing design patterns: shadcn/ui components (`Card`, `Table`, `Badge`, `Button`, `Select`), Tailwind CSS, `font-mono` for data, emerald/red color coding for positive/negative values, dark theme
- **CON-005**: All new HTTP endpoints must set CORS headers (`Access-Control-Allow-Origin: *`) matching existing handler patterns
- **CON-006**: SSE streaming must follow the existing Emitter pattern from `backtest/emitter.go` — fan-out to multiple clients, history buffer for late joiners
- **CON-007**: The sweep runner must reuse `backtest.NewRunner` with `RunConfig` — not fork/duplicate backtest logic
- **CON-008**: New Next.js rewrites in `next.config.ts` must proxy new API paths to the Go backend on port 8080

### Coding Guidelines

- **GUD-001**: Go code must pass `go vet ./...` and existing linting rules
- **GUD-002**: All new Go packages must have `_test.go` files with table-driven tests
- **GUD-003**: Frontend components must be TypeScript with explicit prop interfaces
- **GUD-004**: TDD approach: write test first (RED), implement (GREEN), refactor (REFACTOR) — each task specifies tests before implementation
- **GUD-005**: Each phase produces atomic commits that pass `go test ./...` and `npm run build` independently

### Patterns to Follow

- **PAT-001**: HTTP handler pattern — `ServeHTTP` method on a struct with dependencies injected via constructor, path parsing via `strings.TrimPrefix/SplitN`, CORS preflight handling, `jsonError` helper for error responses (see `backtest_handler.go`)
- **PAT-002**: SSE emitter pattern — `emitterClient` with buffered channel, fan-out broadcast, `ServeHTTP` with `text/event-stream` content type, `event:` and `data:` lines (see `emitter.go`)
- **PAT-003**: Store pattern — `SpecStore` interface with `List/Get/GetLatest/Save/Watch` methods, filesystem adapter with mtime-based cache invalidation (see `store_fs/store.go`)
- **PAT-004**: Frontend data hook pattern — custom hook returning state + actions, `useCallback` for stable references, `EventSource` for SSE, `requestAnimationFrame` batching (see `use-backtest.ts`)
- **PAT-005**: Frontend page layout — `"use client"` directive, imported shadcn components, responsive grid, stat cards with icon + label + value pattern (see `strategies/[strategyID]/page.tsx`)

## 2. Implementation Steps

### Phase 1: Strategy Config Read API + Param Schema

- GOAL-001: Create a backend API endpoint that returns a strategy's full configuration as JSON with typed parameter metadata for frontend form generation.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | **TEST**: Create `backend/internal/domain/strategy/param_schema_test.go` with table-driven tests for `InferParamSchema()` — given a `map[string]any` of params, returns `[]ParamMeta` with correct type inference (int64→"integer", float64→"number", bool→"boolean", string→"string", []string→"string_array"), default values, and group assignment based on key prefix ("regime_filter."→"Regime Filter", "dynamic_risk."→"Dynamic Risk", others→"Strategy Params") | | |
| TASK-002 | **IMPLEMENT**: Create `backend/internal/domain/strategy/param_schema.go` — define `ParamMeta` struct `{Key, Type, Default any, Description, Group string, Min, Max, Step *float64}` and `InferParamSchema(params map[string]any, descriptions map[string]string) []ParamMeta` function. Descriptions come from a static map initialized from the README table content. Known numeric params get hardcoded min/max/step ranges (e.g., `hold_bars`: min=1, max=50, step=1; `atr_multiplier`: min=0.5, max=10.0, step=0.5; `stop_bps`: min=10, max=500, step=5). Return sorted by group then key. | | |
| TASK-003 | **TEST**: Create `backend/internal/adapters/http/config_handler_test.go` — test `GET /strategies/{id}/config` returns 200 with JSON containing `strategy`, `lifecycle`, `routing`, `params`, `param_schema`, `exit_rules`, `symbol_overrides` fields. Test 404 for unknown strategy ID. Test CORS headers present. | | |
| TASK-004 | **IMPLEMENT**: Create `backend/internal/adapters/http/config_handler.go` — `ConfigHandler` struct with `specStore portstrategy.SpecStore` and `strategyDir string` dependencies. `ServeHTTP` routes `GET /strategies/{id}/config` and `PUT /strategies/{id}/config`. GET handler calls `specStore.GetLatest(ctx, strategyID)`, builds JSON response with all Spec fields plus `InferParamSchema(spec.Params, descriptions)`. Use existing JSON encoding patterns. | | |
| TASK-005 | **WIRE**: Register `ConfigHandler` in `backend/cmd/omo-core/http.go` — `imux.Handle("/strategies/config/", configHandler)` with the `store_fs.Store` instance and `strategyBasePath`. Add `/api/strategies/config/:path*` rewrite in `apps/dashboard/next.config.ts`. | | |

**Commit**: `feat(api): add GET /strategies/{id}/config endpoint with param schema metadata`

### Phase 2: Strategy Config Write API + TOML Serialization

- GOAL-002: Enable saving edited strategy configurations back to TOML files with validation, backup, and hot-reload.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-006 | **TEST**: Create `backend/internal/adapters/strategy/store_fs/encode_full_test.go` — test `EncodeFullV2(spec)` produces valid TOML that round-trips through `LoadSpecFile`. Test cases: (a) spec with exit_rules produces `[[exit_rules]]` array-of-tables syntax, (b) spec with symbol_overrides produces `[symbol_overrides.NVDA]` sections, (c) spec with dynamic_risk map produces `[dynamic_risk]` table, (d) spec with screening produces `[screening]` table. Verify round-trip: encode→decode→compare all fields. | | |
| TASK-007 | **IMPLEMENT**: Extend `backend/internal/adapters/strategy/store_fs/store.go` — add `EncodeFullV2(spec portstrategy.Spec) ([]byte, error)` that encodes ALL spec sections (the existing `encodeV2` omits exit_rules, symbol_overrides, dynamic_risk, screening, options, risk_revaluation). Use `toml.NewEncoder` with ordered struct fields. Handle `[[exit_rules]]` by encoding as `[]rawExitRule` struct. Handle `[symbol_overrides.X]` by encoding as `map[string]map[string]any`. | | |
| TASK-008 | **TEST**: Add test cases to `config_handler_test.go` — test `PUT /strategies/{id}/config` with valid JSON body returns 200, verify TOML file was written with correct content. Test 400 for invalid params (negative hold_bars). Test 422 for contradictory exit rules (trailing_stop >= max_loss). Test backup file `.bak` was created. | | |
| TASK-009 | **IMPLEMENT**: Add PUT handler to `ConfigHandler` — (1) decode JSON request body into a `ConfigUpdateRequest` struct, (2) build `portstrategy.Spec` from request, (3) validate by encoding to TOML bytes then calling `LoadSpecFile` on a temp file, (4) create `.bak` backup of existing TOML, (5) write new TOML via `EncodeFullV2`, (6) return 200 with updated config. The `store_fs.Watch` polling detects the mtime change automatically for hot-reload. | | |
| TASK-010 | **DEFINE**: Create `ConfigUpdateRequest` struct in `config_handler.go` — mirrors the JSON shape returned by GET but without read-only fields (schema_version is always 2, param_schema is output-only). Fields: `Strategy{ID,Version,Name,Description,Author}`, `Lifecycle{State,PaperOnly}`, `Routing{Symbols,Timeframes,AssetClasses,AllowedDirections,Priority,ConflictPolicy,ExclusivePerSymbol,WatchlistMode}`, `Params map[string]any`, `ExitRules[]ExitRuleJSON{Type string, Params map[string]float64}`, `SymbolOverrides map[string]map[string]any`, `DynamicRisk map[string]any`, `RegimeFilter map[string]any`, `Screening{Description}`. | | |

**Commit**: `feat(api): add PUT /strategies/{id}/config with TOML write, validation, and backup`

### Phase 3: Strategy Config Editor Frontend

- GOAL-003: Build a dynamic form-based config editor page that renders controls from the param_schema API response.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-011 | **IMPLEMENT**: Create `apps/dashboard/lib/use-strategy-config.ts` — custom hook that (1) fetches `GET /api/strategies/config/{id}/config` on mount, (2) holds form state as a mutable copy of the config, (3) exposes `save()` that PUTs the modified config, (4) exposes `isDirty` computed by deep comparison, (5) exposes `error` and `saving` state. Return type: `{config, paramSchema, updateParam, updateExitRule, updateSymbolOverride, updateRouting, updateLifecycle, save, isDirty, saving, error, loading}`. | | |
| TASK-012 | **IMPLEMENT**: Create `apps/dashboard/components/strategy-config/param-field.tsx` — a single form field component that renders the correct input based on `ParamMeta.type`: `"integer"` → `<input type="number" step={meta.step ?? 1} min={meta.min} max={meta.max}>`, `"number"` → `<input type="number" step={meta.step ?? 0.1}>`, `"boolean"` → checkbox, `"string"` → text input, `"string_array"` → comma-separated tag input. Each field shows label (key), description tooltip, current value, and default value badge. Uses existing `inputCls` Tailwind pattern from backtest page. | | |
| TASK-013 | **IMPLEMENT**: Create `apps/dashboard/components/strategy-config/params-section.tsx` — renders a group of `ParamField` components for a given group name. Receives `params: Record<string, any>`, `schema: ParamMeta[]`, `group: string`, `onChange: (key, value) => void`. Filters schema by group, renders as 2-column grid on desktop, 1-column on mobile. | | |
| TASK-014 | **IMPLEMENT**: Create `apps/dashboard/components/strategy-config/exit-rules-editor.tsx` — renders exit rules as an array of collapsible cards. Each card shows the rule type as a badge header, with `ParamField` inputs for each param in the rule's params map. "Add Rule" button with a dropdown of available rule types (from `ExitRuleType` enum). "Remove" button per rule. Known param ranges are hardcoded per rule type (e.g., VOLATILITY_STOP.atr_multiplier: min=0.5, max=10, step=0.5). | | |
| TASK-015 | **IMPLEMENT**: Create `apps/dashboard/components/strategy-config/symbol-overrides-editor.tsx` — renders symbol overrides as horizontal tabs (one tab per symbol from `routing.symbols`). Each tab shows override params as `ParamField` inputs. "Add Override" for symbols not yet overridden. Each param shows the base value (from main params) as a muted reference. | | |
| TASK-016 | **IMPLEMENT**: Create `apps/dashboard/app/strategies/[strategyID]/config/page.tsx` — the main config editor page. Uses `useStrategyConfig` hook. Layout: breadcrumb (`Strategies > {name} > Config`), sticky top bar with strategy name + version + save button + dirty indicator. Body sections in order: (1) Strategy Metadata card (name, description, author — text inputs), (2) Lifecycle card (state select, paper_only checkbox), (3) Routing card (symbols multi-select, timeframes pills, asset_classes, allowed_directions), (4) Strategy Params section (grouped by `param_schema` groups), (5) Exit Rules editor, (6) Symbol Overrides editor, (7) Dynamic Risk card, (8) Regime Filter card. Footer: "Run Backtest with These Params" button → navigates to `/backtest?strategy={id}&symbols={routing.symbols.join(',')}&timeframe={routing.timeframes[0]}`. | | |
| TASK-017 | **WIRE**: Add link from `apps/dashboard/app/strategies/[strategyID]/page.tsx` detail page to the new config editor — add a `<Button>` labeled "Edit Config" with a `<Link href={/strategies/${strategyID}/config}>` in the strategy detail header section, next to the existing "Back" button. | | |

**Commit**: `feat(ui): add strategy config editor page with dynamic form generation`

### Phase 4: Parameter Sweep Backend — Domain + Ports

- GOAL-004: Define the domain model and port interfaces for parameter sweep orchestration.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-018 | **TEST**: Create `backend/internal/domain/sweep/sweep_test.go` — test `GenerateGrid(ranges []ParamRange) []map[string]any` produces correct cartesian product. Cases: (a) single param `hold_bars` range 4–8 step 2 → [{hold_bars:4}, {hold_bars:6}, {hold_bars:8}], (b) two params → 3×3=9 combinations, (c) empty ranges → [{} single empty run], (d) float param `atr_multiplier` 2.0–3.0 step 0.5 → [2.0, 2.5, 3.0]. Test `TotalRuns(ranges)` returns correct count. | | |
| TASK-019 | **IMPLEMENT**: Create `backend/internal/domain/sweep/sweep.go` — define types: `ParamRange{Key string, Min, Max, Step float64}`, `SweepConfig{StrategyID string, Ranges []ParamRange, TargetMetric string, BacktestConfig backtest.RunConfig, MaxConcurrency int}`, `SweepRunResult{Index int, Params map[string]any, Metrics backtest.Result, Duration time.Duration}`, `SweepResult{Config SweepConfig, Runs []SweepRunResult, BestIndex int, TotalDuration time.Duration}`. Implement `GenerateGrid(ranges []ParamRange) []map[string]any` using iterative cartesian product. Implement `TotalRuns(ranges) int`. Implement `RankRuns(runs []SweepRunResult, metric string, ascending bool) []SweepRunResult` — sorts by the named metric field. | | |
| TASK-020 | **IMPLEMENT**: Create `backend/internal/ports/sweep.go` — define `SweepPort` interface: `Start(ctx, config SweepConfig) (sweepID string, err error)`, `Events(ctx, sweepID string) (<-chan SweepEvent, error)`, `Cancel(sweepID string) error`, `GetResult(sweepID string) (*SweepResult, error)`, `ApplyBest(ctx, sweepID string, runIndex int) error`. Define `SweepEvent{Type string, Data any}` with types: `"sweep:progress"`, `"sweep:run_complete"`, `"sweep:done"`, `"sweep:error"`. | | |

**Commit**: `feat(domain): add sweep grid generation, ranking, and port interface`

### Phase 5: Parameter Sweep Backend — Application Service + Adapter

- GOAL-005: Implement the sweep orchestrator that runs parallel backtests and streams progress.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-021 | **TEST**: Create `backend/internal/app/sweep/orchestrator_test.go` — test with a mock backtest runner: (a) 4 param combos with concurrency=2 completes all 4 runs, (b) cancel mid-sweep stops remaining runs, (c) results are ranked by target metric, (d) progress events emit correct counts (completed 1 of 4, 2 of 4, ...). Use an in-memory mock runner that returns predefined results per param set. | | |
| TASK-022 | **IMPLEMENT**: Create `backend/internal/app/sweep/orchestrator.go` — `Orchestrator` struct with fields: `db *sql.DB`, `appCfg *config.Config`, `marketData ports.MarketDataPort`, `specStore portstrategy.SpecStore`, `strategyDir string`, `log zerolog.Logger`, `mu sync.RWMutex`, `sessions map[string]*sweepSession`. A `sweepSession` holds: `id`, `config SweepConfig`, `cancelFn context.CancelFunc`, `events chan SweepEvent`, `clients []chan SweepEvent` (for SSE fan-out), `result *SweepResult`, `status string`. `Start()` method: (1) generate grid via `GenerateGrid`, (2) load base spec via `specStore.GetLatest`, (3) spawn worker pool of `min(maxConcurrency, len(grid))` goroutines reading from a `paramCombos` channel, (4) each worker: clone base spec, merge sweep params into `spec.Params`, write to a temp TOML file, create `backtest.NewRunner` with `RunConfig{StrategyDir: tempDir}`, call `runner.Run(ctx)`, collect `runner.GetResult()`, send `SweepEvent{Type:"sweep:run_complete", Data: runResult}`, (5) main goroutine sends `sweep:progress` after each completion, (6) on all done: rank runs, send `sweep:done`, store result. | | |
| TASK-023 | **IMPLEMENT**: Add `ApplyBest(ctx, sweepID, runIndex)` method to `Orchestrator` — retrieves the sweep result, gets the params at `runIndex`, builds a full Spec by merging those params into the base spec, calls `EncodeFullV2`, writes to `configs/strategies/{id}.toml` with `.bak` backup. | | |
| TASK-024 | **IMPLEMENT**: Add `Events(ctx, sweepID) (<-chan SweepEvent, error)` method — creates a new client channel, adds to session's clients list, returns channel. Ensure cleanup on context cancellation. Buffer last N events for late joiners (like backtest emitter history pattern). | | |

**Commit**: `feat(sweep): implement parallel sweep orchestrator with worker pool and SSE events`

### Phase 6: Parameter Sweep HTTP Handler

- GOAL-006: Expose sweep orchestration through HTTP endpoints with SSE streaming.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-025 | **TEST**: Create `backend/internal/adapters/http/sweep_handler_test.go` — test `POST /strategies/{id}/sweep` returns 202 with `sweep_id`. Test `GET /strategies/{id}/sweep/{sweep_id}/events` returns SSE stream. Test `DELETE /strategies/{id}/sweep/{sweep_id}` returns 200. Test `POST /strategies/{id}/sweep/{sweep_id}/apply/0` returns 200. Test 404 for unknown sweep_id. Test 400 for invalid sweep config (empty ranges, step=0). | | |
| TASK-026 | **IMPLEMENT**: Create `backend/internal/adapters/http/sweep_handler.go` — `SweepHandler` struct with `orchestrator *sweep.Orchestrator` dependency. `ServeHTTP` routes: `POST /strategies/{id}/sweep` (start), `GET /strategies/{id}/sweep/{sweepID}/events` (SSE), `GET /strategies/{id}/sweep/{sweepID}/results` (final results), `DELETE /strategies/{id}/sweep/{sweepID}` (cancel), `POST /strategies/{id}/sweep/{sweepID}/apply/{runIndex}` (apply). POST start handler: decode `SweepStartRequest{Ranges []ParamRangeJSON, TargetMetric string, Symbols []string, From string, To string, Timeframe string, InitialEquity float64, SlippageBPS int64, NoAI bool, MaxConcurrency int}`, build `SweepConfig`, call `orchestrator.Start()`, return 202 with sweep_id. SSE handler: call `orchestrator.Events()`, write SSE events with `event:` and `data:` lines, flush after each event, close on context done. | | |
| TASK-027 | **WIRE**: Register `SweepHandler` in `backend/cmd/omo-core/http.go` — `imux.Handle("/strategies/sweep/", sweepHandler)`. Add Next.js rewrite `/api/strategies/sweep/:path*` → `http://localhost:8080/strategies/sweep/:path*` in `next.config.ts`. | | |

**Commit**: `feat(api): add sweep HTTP handler with SSE streaming and apply endpoint`

### Phase 7: Parameter Sweep Frontend

- GOAL-007: Build the sweep configuration, progress, and results UI.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-028 | **IMPLEMENT**: Create `apps/dashboard/lib/use-sweep.ts` — custom hook managing sweep lifecycle. State: `status` (idle/configuring/running/completed/error), `sweepId`, `progress` ({completed, total, pct}), `runs` (array of individual run results as they stream in), `finalResult` (ranked result array), `error`. Actions: `start(config)` → POST to sweep API + connect SSE, `cancel()` → DELETE, `apply(runIndex)` → POST apply. SSE handling: parse `sweep:progress`, `sweep:run_complete` (append to runs array), `sweep:done` (set finalResult). | | |
| TASK-029 | **IMPLEMENT**: Create `apps/dashboard/components/sweep/range-config-form.tsx` — form for configuring sweep ranges. Receives the `param_schema` from the config API. Shows a checkbox per numeric param to "Include in sweep". For each included param: shows `min`, `max`, `step` number inputs pre-populated from param_schema defaults. Shows total combinations count (`TotalRuns = product of (max-min)/step+1 per param`). Also includes: target metric dropdown (Sharpe Ratio, Profit Factor, Total P&L, Win Rate, Max Drawdown), concurrency slider (1–8), and fixed backtest params (symbols, date range, timeframe — reuse from backtest page patterns). | | |
| TASK-030 | **IMPLEMENT**: Create `apps/dashboard/components/sweep/sweep-progress.tsx` — progress indicator during sweep. Shows: progress bar with `{completed}/{total} runs ({pct}%)`, elapsed time, estimated remaining time, live-updating mini results table showing completed runs sorted by arrival order. Cancel button. | | |
| TASK-031 | **IMPLEMENT**: Create `apps/dashboard/components/sweep/sweep-results-table.tsx` — sortable results table. Columns: Rank, each swept param value, Trade Count, Win Rate %, Total P&L, Sharpe Ratio, Profit Factor, Max Drawdown %, Duration. Rows highlighted: #1 in emerald, negative P&L in red. Click column header to sort. "Apply" button on each row → calls `useSweep.apply(index)`. Applied row shows green checkmark. | | |
| TASK-032 | **IMPLEMENT**: Create `apps/dashboard/components/sweep/sweep-heatmap.tsx` — heatmap for 2-parameter sweeps. Uses `@nivo/heatmap` (install as dependency) or a custom canvas-based renderer. X-axis: first swept param values, Y-axis: second swept param values, cell color: target metric value (green=good, red=bad for Sharpe/PF/PnL; inverted for drawdown). Hover shows tooltip with full metrics. Click cell highlights the corresponding row in the results table. Only renders when exactly 2 params are swept. | | |
| TASK-033 | **IMPLEMENT**: Create `apps/dashboard/app/strategies/[strategyID]/sweep/page.tsx` — the sweep page. Layout: breadcrumb (`Strategies > {name} > Sweep`), three states: (1) `configuring` — shows `RangeConfigForm`, "Start Sweep" button, (2) `running` — shows `SweepProgress`, (3) `completed` — shows `SweepResultsTable` + `SweepHeatmap` (if applicable), "New Sweep" button to reset. Uses `useStrategyConfig` to load param_schema for the range form. | | |
| TASK-034 | **WIRE**: Add link from strategy detail page and config editor to sweep page — add "Parameter Sweep" button in both `strategies/[strategyID]/page.tsx` header and `strategies/[strategyID]/config/page.tsx` footer. | | |

**Commit**: `feat(ui): add parameter sweep page with range config, progress, results table, and heatmap`

### Phase 8: Integration Testing + Polish

- GOAL-008: End-to-end validation, error handling edge cases, and UX polish.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-035 | **TEST**: Create `backend/internal/adapters/http/config_handler_integration_test.go` — integration test that creates a temp strategy dir, writes a test TOML, starts an HTTP test server with ConfigHandler wired up, exercises GET → edit → PUT → GET round-trip verifying all fields persist correctly. Test with the actual `avwap_v2.toml` content as fixture. | | |
| TASK-036 | **TEST**: Create `backend/internal/app/sweep/orchestrator_integration_test.go` — integration test that runs a minimal 2×2 sweep (2 params, 2 values each = 4 runs) using the real backtest runner with `backtest_test.toml` strategy and a small date range (1 day). Verify: all 4 runs complete, results are ranked, apply writes correct TOML. This test requires DB access (use test tag or skip if DB unavailable). | | |
| TASK-037 | **IMPLEMENT**: Add error boundary and loading states to all new frontend pages — config editor shows skeleton loaders during fetch, toast notification on save success/failure (use a simple div-based toast, no new dependency), sweep page shows error state with retry button if SSE disconnects. | | |
| TASK-038 | **IMPLEMENT**: Add keyboard shortcuts to config editor — `Ctrl+S` / `Cmd+S` to save, `Ctrl+Z` / `Cmd+Z` to undo last param change (simple single-level undo via stored previous state). | | |
| TASK-039 | **IMPLEMENT**: Add "Export as TOML" button to config editor — downloads the current config as a `.toml` file to the user's machine (client-side generation via the GET response, formatted as TOML). Useful for backup before manual editing. | | |
| TASK-040 | **TEST**: Run `cd backend && go test ./...` and `cd apps/dashboard && npm run build` to verify all phases compile and pass. Fix any issues. | | |

**Commit**: `test: add integration tests and UX polish for config editor and sweep`

## 3. Alternatives

- **ALT-001**: **Database-backed config storage** — Store strategy configs in TimescaleDB instead of TOML files. Rejected because TOML files are the established source of truth, the existing `store_fs.Watch` mechanism works well, and files are version-controllable in git. Database would add migration complexity with no clear benefit.

- **ALT-002**: **Bayesian optimization instead of grid search** — Use Optuna-style TPE or Bayesian optimization for smarter parameter search. Rejected for v1 because grid search is simpler to implement, understand, and debug. The heatmap visualization is also natural for grid search. Bayesian optimization can be added as ALT-002-next in a future phase.

- **ALT-003**: **JSON Schema for form generation** — Use standard JSON Schema (draft-07) to describe parameter types and render forms with a library like `@rjsf/core`. Rejected because our param types are simpler than JSON Schema warrants, and a custom `ParamMeta` struct gives us trading-specific fields (group, min/max/step for numeric ranges) without the complexity overhead.

- **ALT-004**: **WebSocket instead of SSE for sweep progress** — Use WebSocket for bidirectional communication during sweeps. Rejected because the existing backtest system uses SSE successfully, the communication is server→client only (progress updates), and SSE is simpler with automatic reconnection. The existing Emitter pattern is proven.

- **ALT-005**: **Separate sweep microservice** — Run sweep as a separate Go process to isolate resource usage. Rejected because the existing architecture is a single binary (`omo-core`), adding a second service increases operational complexity, and the concurrency cap (CON: SEC-003) provides sufficient resource protection.

- **ALT-006**: **pelletier/go-toml/v2 for TOML write** — Switch to pelletier's library which has better comment preservation. Rejected because the project already uses `BurntSushi/toml` everywhere, and comment preservation is not a requirement (the generated TOML will be clean and consistent).

## 4. Dependencies

### Backend Dependencies (existing — no new Go modules needed)

- **DEP-001**: `github.com/BurntSushi/toml v1.6.0` — TOML read/write (already in `go.mod`)
- **DEP-002**: `github.com/rs/zerolog` — Structured logging (already in `go.mod`)
- **DEP-003**: `database/sql` + TimescaleDB — Required by `backtest.Runner` (already wired)
- **DEP-004**: `backtest.Runner` / `backtest.RunConfig` — Core dependency for sweep; must not be modified, only consumed

### Frontend Dependencies (one new package)

- **DEP-005**: `@nivo/heatmap` + `@nivo/core` — Heatmap visualization for 2-param sweep results. Install via `npm install @nivo/heatmap @nivo/core`. If bundle size is a concern, a custom canvas renderer (~150 LOC) is the fallback (ALT: no new dep, implement `<canvas>` heatmap manually).

### Internal Dependencies (between phases)

- **DEP-006**: Phase 2 depends on Phase 1 (config GET must exist before PUT can be tested)
- **DEP-007**: Phase 3 depends on Phases 1+2 (frontend needs both GET and PUT APIs)
- **DEP-008**: Phase 5 depends on Phase 4 (orchestrator implements the domain/port interfaces)
- **DEP-009**: Phase 6 depends on Phase 5 (HTTP handler wraps the orchestrator)
- **DEP-010**: Phase 7 depends on Phases 3+6 (sweep UI needs config editor components + sweep API)
- **DEP-011**: Phase 8 depends on all prior phases

## 5. Files

### New Backend Files

- **FILE-001**: `backend/internal/domain/strategy/param_schema.go` — ParamMeta type, InferParamSchema function, static description/range maps
- **FILE-002**: `backend/internal/domain/strategy/param_schema_test.go` — Tests for param schema inference
- **FILE-003**: `backend/internal/domain/sweep/sweep.go` — Sweep domain types (ParamRange, SweepConfig, SweepRunResult, SweepResult), GenerateGrid, TotalRuns, RankRuns
- **FILE-004**: `backend/internal/domain/sweep/sweep_test.go` — Tests for grid generation and ranking
- **FILE-005**: `backend/internal/ports/sweep.go` — SweepPort interface and SweepEvent type
- **FILE-006**: `backend/internal/app/sweep/orchestrator.go` — Sweep orchestrator with worker pool, SSE fan-out, apply logic
- **FILE-007**: `backend/internal/app/sweep/orchestrator_test.go` — Unit + integration tests for orchestrator
- **FILE-008**: `backend/internal/adapters/http/config_handler.go` — HTTP handler for GET/PUT strategy config
- **FILE-009**: `backend/internal/adapters/http/config_handler_test.go` — Tests for config handler
- **FILE-010**: `backend/internal/adapters/http/sweep_handler.go` — HTTP handler for sweep lifecycle + SSE
- **FILE-011**: `backend/internal/adapters/http/sweep_handler_test.go` — Tests for sweep handler

### Modified Backend Files

- **FILE-012**: `backend/internal/adapters/strategy/store_fs/store.go` — Add `EncodeFullV2` function (extend existing `encodeV2`)
- **FILE-013**: `backend/internal/adapters/strategy/store_fs/encode_full_test.go` — Tests for full TOML encoding
- **FILE-014**: `backend/cmd/omo-core/http.go` — Register ConfigHandler and SweepHandler routes
- **FILE-015**: `backend/cmd/omo-core/main.go` — Instantiate sweep.Orchestrator in service wiring (if needed)

### New Frontend Files

- **FILE-016**: `apps/dashboard/lib/use-strategy-config.ts` — Hook for strategy config CRUD
- **FILE-017**: `apps/dashboard/lib/use-sweep.ts` — Hook for sweep lifecycle + SSE
- **FILE-018**: `apps/dashboard/components/strategy-config/param-field.tsx` — Single parameter form field
- **FILE-019**: `apps/dashboard/components/strategy-config/params-section.tsx` — Grouped params section
- **FILE-020**: `apps/dashboard/components/strategy-config/exit-rules-editor.tsx` — Exit rules array editor
- **FILE-021**: `apps/dashboard/components/strategy-config/symbol-overrides-editor.tsx` — Symbol overrides tabbed editor
- **FILE-022**: `apps/dashboard/app/strategies/[strategyID]/config/page.tsx` — Config editor page
- **FILE-023**: `apps/dashboard/components/sweep/range-config-form.tsx` — Sweep parameter range form
- **FILE-024**: `apps/dashboard/components/sweep/sweep-progress.tsx` — Sweep progress indicator
- **FILE-025**: `apps/dashboard/components/sweep/sweep-results-table.tsx` — Ranked results table
- **FILE-026**: `apps/dashboard/components/sweep/sweep-heatmap.tsx` — 2-param heatmap
- **FILE-027**: `apps/dashboard/app/strategies/[strategyID]/sweep/page.tsx` — Sweep page

### Modified Frontend Files

- **FILE-028**: `apps/dashboard/next.config.ts` — Add rewrites for config and sweep API paths
- **FILE-029**: `apps/dashboard/app/strategies/[strategyID]/page.tsx` — Add "Edit Config" and "Parameter Sweep" links

## 6. Testing

### Unit Tests (TDD — write first)

- **TEST-001**: `param_schema_test.go` — ParamMeta type inference from `map[string]any` values, group assignment, description lookup, range defaults for known params
- **TEST-002**: `sweep_test.go` — Grid generation cartesian product correctness, edge cases (single param, float steps, empty ranges), TotalRuns calculation, RankRuns sorting by each metric
- **TEST-003**: `encode_full_test.go` — TOML round-trip for specs with exit_rules, symbol_overrides, dynamic_risk, screening, options; verify no data loss
- **TEST-004**: `config_handler_test.go` — HTTP GET returns correct JSON shape, PUT validates and writes, 404/400/422 error cases, CORS headers
- **TEST-005**: `sweep_handler_test.go` — HTTP POST starts sweep, SSE stream delivers events, DELETE cancels, apply writes config
- **TEST-006**: `orchestrator_test.go` — Worker pool runs correct number of backtests, respects concurrency limit, cancel stops workers, results ranked correctly

### Integration Tests

- **TEST-007**: `config_handler_integration_test.go` — Full round-trip with real TOML file: write test fixture → GET → modify params → PUT → GET → verify changes persisted → verify `.bak` exists
- **TEST-008**: `orchestrator_integration_test.go` — Small sweep (2×2 grid) with real backtest runner against test data. Requires DB connection. Skip gracefully if DB unavailable.

### Frontend Tests (manual verification checklist — no Jest tests required for v1)

- **TEST-009**: Config editor loads and displays all params from `avwap_v2` strategy correctly
- **TEST-010**: Editing a numeric param, saving, and reloading shows the new value
- **TEST-011**: "Run Backtest" button navigates to backtest page with correct pre-filled values
- **TEST-012**: Sweep page shows correct combination count as params are added/removed
- **TEST-013**: Sweep progress updates in real-time during execution
- **TEST-014**: Results table sorts correctly by each column
- **TEST-015**: Heatmap renders for exactly-2-param sweeps, hidden otherwise
- **TEST-016**: "Apply" button on results row saves params and shows confirmation

## 7. Risks & Assumptions

### Risks

- **RISK-001**: **Sweep resource consumption** — Running 100+ parallel backtests could exhaust memory/CPU. Mitigation: SEC-003 caps concurrency at 4 (configurable up to 8), each backtest uses its own isolated event bus but shares the same DB connection pool. Monitor with existing Prometheus metrics. If issues arise, add memory limits per runner.
- **RISK-002**: **TOML write format divergence** — The new `EncodeFullV2` may produce TOML that's valid but formatted differently from hand-written files, causing noisy git diffs. Mitigation: Accept this trade-off — machine-generated TOML is consistent and correct. Users can reformat manually if needed.
- **RISK-003**: **Backtest runner single-active limitation** — The existing `BacktestHandler` only allows one active backtest (`h.active`). The sweep orchestrator creates its own runners independently, so sweeps and manual backtests could run simultaneously, competing for DB and market data resources. Mitigation: Document this behavior; consider adding a global runner registry in a future phase.
- **RISK-004**: **SSE connection limits** — Browsers limit SSE connections per domain (typically 6). If a user opens multiple sweep tabs, they could hit the limit. Mitigation: Sweep UI shows a warning if another sweep is already running, and the SSE endpoint reuses the same event stream for multiple viewers of the same sweep_id.
- **RISK-005**: **Param type inference accuracy** — `InferParamSchema` guesses types from Go `any` values (TOML decoder produces int64 for integers, float64 for floats). Some params like `cooldown_seconds` are int64 but could be edited as float. Mitigation: The static description map can override inferred types for known params.

### Assumptions

- **ASSUMPTION-001**: The `backtest.Runner` can be instantiated multiple times concurrently without shared mutable state (each has its own event bus, sim broker, etc.). Verified by reading `runner.go` — each runner creates isolated `memory.Bus`, `simbroker`, and `Collector`.
- **ASSUMPTION-002**: The `store_fs.Store.Watch` polling (5-second interval) is sufficient for hot-reload after config saves — no need for immediate filesystem notification. The user will see "saved" confirmation in the UI before the live system picks up the change within 5 seconds.
- **ASSUMPTION-003**: `BurntSushi/toml` encoder can produce `[[exit_rules]]` array-of-tables syntax when given a struct with `[]rawExitRule` field tagged appropriately. This needs verification in TASK-006 test.
- **ASSUMPTION-004**: The existing `backtest.RunConfig.Strategies` field filters which strategies are loaded — the sweep can use this to run only the target strategy. Verified: `RunConfig.Strategies []string` is passed through to strategy loading.
- **ASSUMPTION-005**: Nivo heatmap library works with Next.js 15 App Router and "use client" directive without SSR issues.

## 8. Related Specifications / Further Reading

- [docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md) — Hexagonal architecture overview
- [docs/PRD.md](../docs/PRD.md) — Product Requirements Document
- [configs/strategies/README.md](../configs/strategies/README.md) — Complete strategy TOML field reference
- [FreqTrade Hyperopt Documentation](https://www.freqtrade.io/en/stable/hyperopt/) — Inspiration for parameter space definition
- [Backtrader Optimization](https://www.backtrader.com/docu/optimization/) — Grid search pattern reference
- [Nivo Heatmap](https://nivo.rocks/heatmap/) — Heatmap visualization library
- [BurntSushi/toml](https://github.com/BurntSushi/toml) — Go TOML library documentation
