# Plan: HTTP surface for Claude-driven trading prototype (v2)

Status: DRAFT v2 — incorporates go-architect, risk-manager, quant-analyst review. Awaiting user sign-off before TDD step 1.
Scope: add the two missing HTTP endpoints so a `/loop`-driven Claude prototype can read risk budget and dry-run order proposals against omo-core.

## Goal

Make the existing internal validation reachable from HTTP so Claude in CLI can:
1. GET `/api/risk/budget` — see remaining daily loss, equity, open-position count vs cap, and kill-switch state before proposing trades.
2. POST `/api/proposals/order` — submit a slim proposal DTO and get `would_pass` + which gate would block + a TTL, without enqueuing or executing.

Read-side endpoints we already have (`/state`, `/bars`, `/api/portfolio/positions`, `/orders`) cover the rest. No MCP yet — Claude calls these via curl. /loop prototype lands in a follow-up PR under `_workspace/`.

## Non-goals

- No real order submission. `/api/proposals/order` is dry-run only; existing `process()` enqueue path is untouched.
- No refactor of `execution.Service.process()` to call the new validator in this PR. Code paths stay parallel; a single test pins them in lockstep so they cannot drift silently. Consolidation tracked as follow-up tech debt (see Follow-ups).
- No new auth — endpoints are localhost-only on `:8080` like the rest. Kill-switch state guard short-circuits when HALTED.
- No microstructure enrichment of `/state` (rvol/vwap_stretch/bid_ask_bps/current_strategy_signal). Punted to /loop prompt-design PR. The current `IndicatorSnapshot` is what Claude gets in v1.
- No shadow-mode; the loop will be keystroke-confirm paper from day one. The quant's IC/replay scoring is a desirable instrumentation but not a blocker for these endpoints.

## SOLID / KISS / DRY

- **SOLID — interface segregation at the consumer.** Each handler declares a tiny interface for what it needs (mirrors `KillSwitchController` in `kill_switch_handler.go:17`). One validator interface, not seven gate interfaces — handler must not know the gate sequence.
- **DRY — single validation method.** Add `func (s *Service) ValidateIntent(ctx, intent) *GateError` in `execution`. Same gate sequence as `process()` (`service.go:828-949` + `:986-987`), but uses **read-only probes** for any gate whose `Check()` mutates state (see Show-stopper fix below). A lockstep test pins the gate ordering in both lists.
- **KISS — minimum surface.**
  - `/api/risk/budget` v1 returns 9 fields. No sector/PDT/correlation.
  - `/api/proposals/order` v1 returns `{ would_pass, gate, reason, evaluated_at, valid_until, request_id, snapshot }`. No per-gate breakdown until requested.

## Show-stopper fix: read-only probe path

`DailyLossBreaker.Check()` (`circuit_breaker.go:211-297`) is **not** side-effect-free. On a trip it sets `haltDate`, increments Prom counters tagged `tripped`, and calls `transitionOnTrip()` which flips the live kill-switch to REDUCING and emits a sink event. Calling it from a dry-run handler would actually halt production. Required fix:

- Add `func (d *DailyLossBreaker) Inspect(tenantID string, envMode domain.EnvMode, accountEquity float64) (lossUSD, lossPct float64, tripped bool, err error)` — read-only: takes `d.mu`, computes today's loss vs both limits, **does not** write `haltDate`, **does not** call `transitionOnTrip`, **does not** increment trip counters. May increment a separate `daily_loss_inspect_total` counter.
- `ValidateIntent` calls `Inspect`; `process()` keeps calling `Check`.
- Test (regression guard): a `ValidateIntent` call with a Probe-tripped breaker must NOT mutate `haltDate`, must NOT change kill-switch state, must NOT emit a sink event.

Side-effect audit for the other gates (per go-architect, before TDD step 2 — written checklist in PR description):
- `positionGate.Check` (`position_gate.go:54`): mostly read-only; only mutates inflight on stale-TTL eviction (benign cache GC). Calls `broker.GetPositions` — note RPC cost, see Broker amplification below.
- `exposureGuard.Check`, `portfolioGuard.Check`, `slippageGuard.Check`, `tradingWindowGuard.Check`: read-only — confirmed by code reading.
- `riskEngine.Validate`, `killSwitch.IsHalted`: pure reads.

## Broker RPC amplification

Each `/api/proposals/order` call triggers `broker.GetPositions` three times (positionGate, exposureGuard, portfolioGuard). On a 5-min /loop with 1-3 symbols this is fine; document and revisit only if Claude scales out. Optional optimization: pass a single `positions []domain.Trade` snapshot into `ValidateIntent` so the three gates share one fetch. **Deferred unless the PR reviewer flags it.**

## Files

NEW (handler, ~80-120 LOC each + tests):
- `backend/internal/adapters/http/risk_budget_handler.go`
- `backend/internal/adapters/http/risk_budget_handler_test.go`
- `backend/internal/adapters/http/proposal_handler.go`
- `backend/internal/adapters/http/proposal_handler_test.go`

NEW (execution method + types, ~50 LOC + tests):
- `backend/internal/app/execution/validate_intent.go` — `ValidateIntent` + `GateError` type
- `backend/internal/app/execution/validate_intent_test.go`

NEW accessors (~5 LOC each):
- `risk.DailyLossBreaker.Inspect(...)` — read-only loss probe
- `risk.DailyLossBreaker.MaxLossUSD()` / `MaxLossPct()` — config readers for the budget endpoint
- `execution.RiskEngine.MaxRiskPct()` — config reader

MODIFY:
- `backend/cmd/omo-core/http.go` — wire two routes inside `registerRoutes`.

## Type definitions (proposed)

```go
// in execution package
type GateError struct {
    Gate   string  // "kill_switch", "position_gate", "exposure_guard",
                  // "portfolio_guard", "risk_engine", "slippage", "trading_window", "daily_loss"
    Reason string  // err.Error() of the gate's returned error
}

func (e *GateError) Error() string { return e.Gate + ": " + e.Reason }
```

```go
// in http package — slim proposal request DTO, NOT full domain.OrderIntent
type ProposalRequest struct {
    Symbol         string  `json:"symbol"`
    Direction      string  `json:"direction"`       // "long" / "short" / "exit_long" / "exit_short"
    LimitPrice     float64 `json:"limit_price"`
    StopLoss       float64 `json:"stop_loss"`
    Quantity       float64 `json:"quantity"`
    MaxSlippageBPS int     `json:"max_slippage_bps,omitempty"` // default 10
    Rationale      string  `json:"rationale,omitempty"`        // free-form, logged for audit
}

// Handler internally builds domain.OrderIntent with TenantID="default",
// EnvMode=Paper, Strategy="claude_proposal", IdempotencyKey=request_id.
// Claude cannot spoof tenant, env, or strategy.
```

```go
// proposal response
type ProposalResponse struct {
    WouldPass    bool                   `json:"would_pass"`
    Gate         string                 `json:"gate,omitempty"`     // "" when would_pass=true
    Reason       string                 `json:"reason,omitempty"`
    EvaluatedAt  string                 `json:"evaluated_at"`        // RFC3339
    ValidUntil   string                 `json:"valid_until"`         // RFC3339, evaluated_at + 30s
    RequestID    string                 `json:"request_id"`
    Snapshot     ProposalSnapshot       `json:"snapshot"`
}
type ProposalSnapshot struct {
    Equity            float64 `json:"equity"`
    DailyLossUsedUSD  float64 `json:"daily_loss_used_usd"`
    OpenPositions     int     `json:"open_positions"`
    InflightIntents   int     `json:"inflight_intents"`
}
```

```go
// budget response
type BudgetResponse struct {
    KillSwitchState      string  `json:"kill_switch_state"`        // ACTIVE/REDUCING/HALTED
    AccountEquity        float64 `json:"account_equity"`
    DailyLossUsedUSD     float64 `json:"daily_loss_used_usd"`
    MaxLossUSD           float64 `json:"max_loss_usd"`
    MaxLossPct           float64 `json:"max_loss_pct"`             // fraction, 0.05 = 5%
    MaxRiskPctPerIntent  float64 `json:"max_risk_pct_per_intent"`  // fraction
    OpenPositionsCount   int     `json:"open_positions_count"`
    OpenPositionsCap     int     `json:"open_positions_cap"`
    InflightIntents      int     `json:"inflight_intents"`
}
```

## Behavior rules

- Both endpoints are GET-or-POST as appropriate; CORS + OPTIONS handling matches `kill_switch_handler.go`.
- Both endpoints accept and echo `X-Request-ID`. If absent, server generates a UUID. Logged on every handler entry/exit.
- Both endpoints short-circuit when kill-switch is HALTED:
  - `/api/risk/budget` always returns the full budget (visibility is fine when halted).
  - `/api/proposals/order` returns 200 with `would_pass:false, gate:"kill_switch", reason:"halted"` and skips downstream gates entirely.
- Validation rejection is a domain answer, not an HTTP error → return 200 with `would_pass:false`. HTTP 4xx is reserved for malformed requests; 5xx for handler bugs.
- `valid_until = evaluated_at + 30s` (constant; no config knob in v1). Clients should treat the response as advisory, not a submission token.

## TDD sequence

Every step: red → green → refactor.

### Step 1 — `DailyLossBreaker.Inspect` (the show-stopper fix)

1. RED: `circuit_breaker_test.go` — call `Inspect` on a breaker with a tripping daily PnL. Assert: returns `tripped=true`, but `IsHalted()` stays false, `State()` stays ACTIVE, no sink event emitted.
2. GREEN: implement `Inspect` reusing the loss-computation block of `Check` but skipping all writes.
3. RED: a second test — `Check` then `Inspect` then `Check` — confirm `Inspect` doesn't reset `haltDate` already set by `Check`.
4. GREEN: confirm.

Verify: `go test ./internal/app/risk/...` green.

### Step 2 — `execution.Service.ValidateIntent` + `GateError`

1. RED: `validate_intent_test.go` — given a Service wired with stub gates (one returning err), `ValidateIntent` returns a `*GateError` with the right `Gate` name; on all-pass returns nil.
2. GREEN: implement `ValidateIntent`. Order: `kill_switch` (state read) → `position_gate` → `exposure_guard` → `portfolio_guard` → `risk_engine` → `slippage` → `trading_window` → `daily_loss` (via `Inspect`). No side effects beyond what each gate's read-only path does.
3. RED: lockstep test — assert the gate-name slice matches the order `process()` calls them at `service.go:828-949` + `:986-987`. Drift in either side fails the test with a message naming both files.
4. RED: regression — `ValidateIntent` with a tripping daily loss must return `*GateError{Gate:"daily_loss"}` AND must not mutate breaker state (`IsHalted()` still false after).
5. GREEN: pin order; one-line comment in the function naming the lockstep test.

Verify: `go test ./internal/app/execution/...` green.

### Step 3 — `/api/risk/budget` handler

1. RED: handler test — GET returns 200 with all 9 fields populated from stubs. Method allowlist: POST/DELETE → 405.
2. RED: kill-switch HALTED → still returns the budget (not blocked).
3. RED: `X-Request-ID` echoed in response header.
4. GREEN: implement handler with consumer-side interfaces. Add the 3 small accessors (`MaxLossUSD`, `MaxLossPct`, `MaxRiskPct`) with their own unit tests.

Verify: `go test ./internal/adapters/http/...` green.

### Step 4 — `/api/proposals/order` handler

1. RED: POST valid `ProposalRequest`, validator returns nil → 200, `would_pass:true`, `gate:""`, `valid_until` is `evaluated_at + 30s`.
2. RED: validator returns `*GateError{Gate:"position_gate"}` → 200, `would_pass:false`, `gate:"position_gate"`.
3. RED: kill-switch HALTED → 200, `would_pass:false, gate:"kill_switch"`, validator NOT called.
4. RED: malformed JSON → 400. Non-POST → 405. Missing required fields (symbol, direction, limit_price, stop_loss, quantity) → 400 with field names.
5. RED: `X-Request-ID` round-trip — header in, same value in body's `request_id` and response header.
6. RED: snapshot fields populated from stubs (equity, daily loss used, open positions, inflight).
7. GREEN: implement. Slim DTO → `domain.OrderIntent` conversion lives in the handler with hard-coded TenantID="default", EnvMode=Paper, Strategy="claude_proposal".

Verify: `go test ./internal/adapters/http/...` green.

### Step 5 — Wire routes

1. GREEN: register both handlers in `registerRoutes` (`cmd/omo-core/http.go`). Pattern matches `imux.Handle("/api/v1/admin/kill-switch", ksHandler)`.
2. Manual smoke: `go build ./cmd/omo-core && ./omo-core` (paper config), then curl both endpoints. Document the curl commands in the PR description.

## Blast radius

- Risk path: ZERO. `process()` is untouched; new code paths are parallel and cannot mutate breaker state.
- Live trading: ZERO. Endpoints are read or dry-run only; kill-switch HALTED short-circuits the proposal endpoint before any gate runs.
- Existing tests: lockstep ordering test will fail if anyone reorders `process()` gates without also reordering `ValidateIntent`. That is the desired behavior.
- Concurrency: gates' Check methods are concurrent-safe (verified by go-architect). Broker RPC amplification noted but acceptable at /loop scale.

## Follow-ups (not in this PR — track separately)

1. Consolidate `process()` to call `ValidateIntent` + side-effect callbacks. Removes the parallel-paths-pinned-by-test debt.
2. `GET /api/portfolio/reconcile` — broker-vs-internal position diff for the headless heartbeat. Cheap, mergeable independently. (risk-manager recommendation)
3. Decision-log table + `/api/proposals/decisions` write endpoint joining `request_id` → response → realized outcome at +1/+3/+6 bars. Enables post-mortem scoring of Claude vs AVWAP. (quant-analyst recommendation)
4. `/state` enrichment: pre-computed `rvol_5m`, `vwap_stretch_sigma`, `bid_ask_bps`, `current_strategy_signal`. Address in /loop prompt-design PR. (quant-analyst recommendation)
5. Single-fetch position snapshot threaded through `ValidateIntent` to dedupe broker RPC calls. Optional perf optimization.

## Open question for user

`/loop` prototype itself lives where — under `_workspace/scripts/` as a small Bash/Python harness, or a separate repo? (Suggest `_workspace/` for now since it's a prototype and you have other workspace files.)
