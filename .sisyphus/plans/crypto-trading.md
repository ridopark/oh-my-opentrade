# Phase 15: Cryptocurrency Trading via Alpaca

## TL;DR

> **Quick Summary**: Add cryptocurrency trading capability to oh-my-opentrade via Alpaca's crypto APIs, enabling 24/7 crypto trading alongside existing equity operations. Domain-first approach: introduce `AssetClass` concept, extend adapters for crypto market data (separate WS/REST endpoints), make app services asset-class-aware (calendar, screener, execution), and update dashboard for mixed portfolios.
> 
> **Deliverables**:
> - `AssetClass` domain type (Equity, Crypto) threading through all layers
> - Crypto market data streaming (WebSocket) and REST historical bars via Alpaca `CryptoClient`
> - Crypto order execution through existing `/v2/orders` endpoint with long-only enforcement
> - Symbol normalization layer (`BTC/USD` ↔ `BTCUSD`)
> - 24/7 trading calendar for crypto (bypass NYSE hours)
> - Asset-class-aware config, screener, risk engine, and strategy routing
> - Crypto-adapted AVWAP strategy DNA
> - Dashboard crypto symbol support with 24/7 charts
> 
> **Estimated Effort**: Large (20+ tasks across 5 waves)
> **Parallel Execution**: YES — 5 waves, up to 8 tasks per wave
> **Critical Path**: Task 73 → 75 → 79 → 81 → 86 → 90 → 93 → F1-F4

---

## Context

### Original Request
Add cryptocurrency trading capability via Alpaca to oh-my-opentrade, continuing from Phase 14 (task #73+). Must follow hexagonal architecture, not break existing equity pipeline, and support mixed portfolios.

### Interview Summary
**Key Discussions**:
- Alpaca uses the same brokerage account for equity + crypto — no separate account needed
- Crypto market data uses different WS endpoint (`v1beta3/crypto/us`) and REST base (`/v1beta3/crypto/us/`)
- Order submission uses the same `/v2/orders` endpoint — crypto orders are just equity orders with crypto symbols
- Crypto is 24/7 — NYSE exchange calendar must not apply
- No short selling for crypto on Alpaca — long-only + sell
- Go SDK v3.9.1 already in go.mod with `CryptoClient` support

**Research Findings**:
- **Options extension pattern**: Already used in codebase — separate files (`options_rest.go`, `options_order.go`) on same `Adapter` struct with type-dispatch in `SubmitOrder`. Follow same pattern for crypto.
- **Symbol normalization needed**: Alpaca returns `BTCUSD` in positions but expects `BTC/USD` for orders/data. `position_gate.go` will fail without normalization.
- **Crypto WS client**: `stream.NewCryptoClient(marketdata.US, ...)` — different from `NewStocksClient`, uses `WithCryptoBars()` not `WithBars()`
- **Crypto feed**: Uses `"us"` not `"iex"` — config `Feed` field needs per-asset-class separation
- **CryptoBar.Volume is float64** (not uint64 like stocks) — domain `MarketBar.Volume` is already `float64`, so compatible
- **`sessiontime.go`**: Hardcoded 9:30-16:00 ET — needs asset-class-conditional bypass
- **`orb_tracker.go`**: 100% equity-specific (opening range breakout) — irrelevant for crypto, no changes needed
- **Execution short rejection**: Hardcoded in `service.go:131-140` — needs to be asset-class-aware (block for both crypto AND paper equities)
- **`SymbolsConfig`**: Flat `[]string` — needs restructuring for asset-class tagging

### Metis Review
**Identified Gaps** (addressed):
- **Symbol normalization**: `BTC/USD` vs `BTCUSD` mismatch in positions — added Task 75 (SymbolNormalizer)
- **Crypto feed config**: `Feed` field is global `"iex"` — added `CryptoFeed` field in Task 78
- **Crypto WS base URL**: Cannot reuse `deriveStreamURL()` — Task 79 creates separate crypto WS
- **Perpetual futures**: Exist in SDK (`BTC-PERP`, `v1beta1/crypto-perps`) — explicitly excluded from scope
- **CryptoBar.Volume float64**: Compatible with existing domain `MarketBar.Volume float64` — no changes needed
- **Position gate matching**: Will fail without normalization — Task 75 ensures all symbol comparisons use normalized form

---

## Work Objectives

### Core Objective
Enable cryptocurrency spot trading via Alpaca alongside existing equity operations, with full data pipeline (streaming + historical), risk management, strategy routing, and dashboard visibility.

### Concrete Deliverables
- `backend/internal/domain/value.go`: `AssetClass` type with `AssetClassEquity`, `AssetClassCrypto`
- `backend/internal/domain/value.go`: `SymbolNormalizer` functions (slash ↔ no-slash)
- `backend/internal/domain/exchange_calendar.go`: `TradingCalendar` interface with NYSE and Crypto24x7 implementations
- `backend/internal/adapters/alpaca/crypto_ws.go`: Crypto WebSocket streaming via `stream.CryptoClient`
- `backend/internal/adapters/alpaca/crypto_rest.go`: Crypto REST (historical bars, snapshots)
- `backend/internal/config/config.go`: Extended `SymbolsConfig` with asset-class-tagged symbols
- `backend/internal/app/monitor/sessiontime.go`: Asset-class-aware session time
- `backend/internal/app/execution/service.go`: Asset-class-aware direction enforcement
- `backend/internal/app/screener/service.go`: Crypto screening mode (no pre-market)
- `configs/strategies/crypto_avwap.toml`: Crypto-adapted strategy DNA
- `apps/dashboard/`: Crypto symbol display, 24/7 charts
- Tests for all new code + regression tests for equity pipeline

### Definition of Done
- [ ] `cd backend && go test ./...` passes (all existing + new tests)
- [ ] `cd backend && go build -o bin/omo-core ./cmd/omo-core` compiles clean
- [ ] Crypto symbols (`BTC/USD`, `ETH/USD`) stream bars via WebSocket in paper mode
- [ ] Crypto order submission works in paper mode (long-only)
- [ ] Equity pipeline unchanged — existing strategies continue to trade
- [ ] Dashboard shows crypto symbols alongside equities
- [ ] Mixed portfolio config (equities + crypto) loads and validates

### Must Have
- `AssetClass` type in domain with zero external dependencies
- Symbol normalization (slash ↔ no-slash) for position matching
- Separate crypto WebSocket stream (different URL/client from equities)
- 24/7 trading calendar for crypto (no market-hours gating)
- Long-only enforcement for crypto (no short selling)
- Config supporting mixed equity + crypto symbol lists
- At least one crypto-compatible strategy DNA (adapted AVWAP)
- All existing equity tests pass unchanged

### Must NOT Have (Guardrails)
- **No perpetual futures** (`BTC-PERP`, `v1beta1/crypto-perps`) — out of scope
- **No margin/leverage** for crypto — spot trading only
- **No DeFi, on-chain, or non-Alpaca crypto** — Alpaca brokerage only
- **No new custom strategy engine** for crypto — adapt existing AVWAP/AI Scalping via DNA config
- **No breaking changes to `BrokerPort` or `MarketDataPort` interfaces** — extend, don't modify
- **No changes to ORB strategy** — it's equity-specific and stays that way
- **No over-abstraction** — don't create a generic "exchange" abstraction; just add crypto alongside equity
- **No changes to the rate limiter** — shared global limiter is sufficient for MVP
- **No migration of existing equity data** — crypto data is new, no schema migration conflicts
- **No AI slop**: No excessive comments, no premature abstractions, no `as any` type assertions

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES — `go test ./...` with 320+ existing tests
- **Automated tests**: YES (TDD) — Each task follows RED (failing test) → GREEN (minimal impl) → REFACTOR
- **Framework**: `go test` (standard Go testing + testify for assertions)
- **If TDD**: Each task writes tests first, then implementation

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Domain/Library**: Use Bash (`go test ./...`) — run tests, verify pass counts
- **Adapter/Integration**: Use Bash (`curl`) — verify API calls work in paper mode
- **Config**: Use Bash (`go run ./cmd/omo-core/`) — verify startup with crypto config
- **Dashboard**: Use Playwright — navigate, verify crypto symbols render
- **Full Pipeline**: Use tmux — start backend, verify crypto bars stream + orders execute

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — domain foundation + config):
├── Task 73: AssetClass domain type + helpers [quick]
├── Task 74: TradingCalendar interface + implementations [quick]
├── Task 75: Symbol normalizer (BTC/USD ↔ BTCUSD) [quick]
├── Task 76: Extend InstrumentType with CRYPTO [quick]
├── Task 77: Config restructure — asset-class-tagged symbols [quick]
├── Task 78: AlpacaConfig — add CryptoDataURL + CryptoFeed [quick]

Wave 2 (After Wave 1 — adapter layer, MAX PARALLEL):
├── Task 79: Crypto WebSocket streaming (crypto_ws.go) [deep]
├── Task 80: Crypto REST — historical bars + snapshots (crypto_rest.go) [deep]
├── Task 81: Adapter.StreamBars dispatch — equity vs crypto [unspecified-high]
├── Task 82: Adapter.GetHistoricalBars dispatch — equity vs crypto [unspecified-high]
├── Task 83: Adapter.SubmitOrder — crypto dispatch + long-only guard [quick]
├── Task 84: Adapter.GetPositions — symbol normalization [quick]

Wave 3 (After Wave 2 — app services layer):
├── Task 85: Session time — asset-class-aware (24/7 bypass) [unspecified-high]
├── Task 86: Execution service — asset-class-aware direction enforcement [unspecified-high]
├── Task 87: Screener — crypto mode (skip pre-market, 24/7 ranking) [unspecified-high]
├── Task 88: Strategy routing — crypto symbol → crypto-compatible strategies [unspecified-high]
├── Task 89: Crypto AVWAP strategy DNA + builtin engine gate [quick]

Wave 4 (After Wave 3 — wiring + dashboard):
├── Task 90: main.go — wire crypto streams, split symbol lists [deep]
├── Task 91: Dashboard — crypto symbols, 24/7 charts, mixed portfolio [visual-engineering]
├── Task 92: TimescaleDB — verify MarketBar schema handles crypto symbols [quick]
├── Task 93: Integration test — full crypto pipeline paper-mode E2E [deep]

Wave FINAL (After ALL tasks — independent review, 4 parallel):
├── Task F1: Plan compliance audit [oracle]
├── Task F2: Code quality review [unspecified-high]
├── Task F3: Real QA — Playwright + tmux verification [unspecified-high]
├── Task F4: Scope fidelity check [deep]

Critical Path: T73 → T75 → T79 → T81 → T86 → T90 → T93 → F1-F4
Parallel Speedup: ~65% faster than sequential
Max Concurrent: 6 (Waves 1 & 2)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 73 | — | 76, 77, 81-89 | 1 |
| 74 | — | 85, 87 | 1 |
| 75 | — | 81, 82, 84, 86 | 1 |
| 76 | — | 83 | 1 |
| 77 | 73 | 88, 89, 90 | 1 |
| 78 | — | 79, 80 | 1 |
| 79 | 78 | 81, 90 | 2 |
| 80 | 78 | 82, 90 | 2 |
| 81 | 73, 75, 79 | 90 | 2 |
| 82 | 73, 75, 80 | 90 | 2 |
| 83 | 76 | 90 | 2 |
| 84 | 75 | 90 | 2 |
| 85 | 74, 73 | 88, 90 | 3 |
| 86 | 73, 75 | 90 | 3 |
| 87 | 74, 73 | 90 | 3 |
| 88 | 77, 85 | 90 | 3 |
| 89 | 77 | 90 | 3 |
| 90 | 79-89 | 93 | 4 |
| 91 | 73, 77 | F3 | 4 |
| 92 | 73 | 93 | 4 |
| 93 | 90, 92 | F1-F4 | 4 |
| F1-F4 | 93 | — | FINAL |

### Agent Dispatch Summary

- **Wave 1**: **6 tasks** — T73-T76 → `quick`, T77 → `quick`, T78 → `quick`
- **Wave 2**: **6 tasks** — T79 → `deep`, T80 → `deep`, T81-T82 → `unspecified-high`, T83-T84 → `quick`
- **Wave 3**: **5 tasks** — T85-T88 → `unspecified-high`, T89 → `quick`
- **Wave 4**: **4 tasks** — T90 → `deep`, T91 → `visual-engineering`, T92 → `quick`, T93 → `deep`
- **FINAL**: **4 tasks** — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

> Implementation + Test = ONE Task. Never separate.
> EVERY task MUST have: Recommended Agent Profile + Parallelization info + QA Scenarios.
> **A task WITHOUT QA Scenarios is INCOMPLETE. No exceptions.**

### Wave 1 — Domain Foundation + Config (Tasks 73-78)

---

- [ ] 73. Add `AssetClass` domain type + helpers — Complexity: S

  **What to do**:
  - In `backend/internal/domain/value.go`, after the `RegimeType` block (line 99), add:
    - `type AssetClass string` with constants `AssetClassEquity AssetClass = "EQUITY"` and `AssetClassCrypto AssetClass = "CRYPTO"`
    - `func (a AssetClass) String() string` — returns string representation
    - `func NewAssetClass(s string) (AssetClass, error)` — validates input, returns error for unknown values
    - `func (a AssetClass) Is24x7() bool` — returns `true` for Crypto, `false` for Equity
    - `func (a AssetClass) SupportsShort() bool` — returns `true` for Equity, `false` for Crypto
  - In `backend/internal/domain/entity.go`, add `AssetClass AssetClass` field to the `OrderIntent` struct
  - In `backend/internal/domain/entity.go`, add `AssetClass AssetClass` field to the `Trade` struct
  - Write tests in `backend/internal/domain/value_test.go`:
    - `TestNewAssetClass_Valid` — "EQUITY" and "CRYPTO" return correct values
    - `TestNewAssetClass_Invalid` — "FOREX", "" return errors
    - `TestAssetClass_Is24x7` — Crypto=true, Equity=false
    - `TestAssetClass_SupportsShort` — Equity=true, Crypto=false

  **Must NOT do**: No `AssetClassOption`. No external imports. No existing function signature changes.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 1 (parallel with 74-78) | **Blocks**: 76, 77, 81-89 | **Blocked By**: None

  **References**:
  - `backend/internal/domain/value.go:81-99` — `RegimeType` pattern: type alias, constants, String(), constructor. Copy this exact pattern.
  - `backend/internal/domain/value.go:27-50` — `Direction` type with same canonical structure.
  - `backend/internal/domain/entity.go` — `OrderIntent` and `Trade` structs where `AssetClass` field must be added.
  - `backend/internal/domain/value_test.go` — Existing table-driven test style for `NewDirection`, `NewTimeframe`.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/domain/... -run TestAssetClass -v` → PASS (4 tests)
  - [ ] `cd backend && go vet ./internal/domain/...` → no issues
  - [ ] `AssetClassCrypto.Is24x7()` = true; `AssetClassEquity.SupportsShort()` = true

  **QA Scenarios:**
  ```
  Scenario: AssetClass validates correctly + helpers return correct booleans
    Tool: Bash
    Steps: cd backend && go test ./internal/domain/... -run TestAssetClass -v
    Expected: PASS — all 4 tests pass
    Evidence: .sisyphus/evidence/task-73-assetclass.txt

  Scenario: Regression — existing domain tests unaffected
    Tool: Bash
    Steps: cd backend && go test ./internal/domain/... -v
    Expected: 0 failures, all pre-existing tests still pass
    Evidence: .sisyphus/evidence/task-73-regression.txt
  ```

  **Commit**: Wave 1 group | `feat(domain): add AssetClass type with Equity/Crypto constants and helpers`
  Files: `domain/value.go`, `domain/entity.go`, `domain/value_test.go` | Pre-commit: `go test ./internal/domain/...`

---

- [ ] 74. Add `TradingCalendar` interface + NYSE/Crypto implementations — Complexity: M

  **What to do**:
  - In `backend/internal/domain/exchange_calendar.go`, add a `TradingCalendar` interface:
    ```go
    type TradingCalendar interface {
        IsOpen(t time.Time) bool
        SessionOpen(t time.Time) time.Time
        SessionClose(t time.Time) time.Time
        PreviousSession(now time.Time) (start, end time.Time)
    }
    ```
  - Add `NYSECalendar` struct implementing `TradingCalendar` — wraps existing `isNYSETradingDay`, `NYSECloseTime`, `PreviousRTHSession`. `IsOpen` checks weekday + not holiday + between 9:30-16:00 ET.
  - Add `Crypto24x7Calendar` struct — `IsOpen` always returns `true`. `SessionOpen/Close` return midnight-to-midnight UTC. `PreviousSession` returns yesterday midnight-to-midnight.
  - Add factory: `func CalendarFor(ac AssetClass) TradingCalendar`
  - Write tests in `backend/internal/domain/exchange_calendar_test.go`:
    - `TestNYSECalendar_IsOpen` — weekday 10AM ET = true, Saturday = false, holiday = false
    - `TestCrypto24x7Calendar_IsOpen` — always true (weekday, weekend, holiday, 3AM)
    - `TestCalendarFor` — returns correct implementation per AssetClass

  **Must NOT do**: Do NOT delete or modify existing `PreviousRTHSession`, `IsNYSEHoliday`, `NYSECloseTime` — `NYSECalendar` wraps them. No external imports.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 1 (parallel with 73, 75-78) | **Blocks**: 85, 87 | **Blocked By**: None

  **References**:
  - `backend/internal/domain/exchange_calendar.go:1-140` — ENTIRE FILE. `NYSECalendar` wraps `isNYSETradingDay()` (line 102), `NYSECloseTime()` (line 91), `PreviousRTHSession()` (line 113).
  - `backend/internal/domain/value.go` — `AssetClass` type (Task 73) for `CalendarFor()`. If not landed, use local placeholder.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/domain/... -run TestCalendar -v` → PASS (3+ tests)
  - [ ] `Crypto24x7Calendar{}.IsOpen(saturdayTime)` = true
  - [ ] `NYSECalendar{}.IsOpen(saturdayTime)` = false
  - [ ] Existing `PreviousRTHSession` still works unchanged

  **QA Scenarios:**
  ```
  Scenario: Crypto calendar always open
    Tool: Bash
    Steps: cd backend && go test ./internal/domain/... -run TestCrypto24x7 -v
    Expected: IsOpen=true for Saturday 3AM, Sunday noon, NYSE holiday, regular Wednesday
    Evidence: .sisyphus/evidence/task-74-crypto-calendar.txt

  Scenario: NYSE calendar respects market hours
    Tool: Bash
    Steps: cd backend && go test ./internal/domain/... -run TestNYSECalendar -v
    Expected: weekday 10AM ET = true, weekday 5PM ET = false, Saturday = false
    Evidence: .sisyphus/evidence/task-74-nyse-calendar.txt
  ```

  **Commit**: Wave 1 group | `feat(domain): add TradingCalendar interface with NYSE and Crypto24x7 implementations`
  Files: `domain/exchange_calendar.go`, `domain/exchange_calendar_test.go` | Pre-commit: `go test ./internal/domain/...`

---

- [ ] 75. Add Symbol normalizer (`BTC/USD` ↔ `BTCUSD`) — Complexity: S

  **What to do**:
  - In `backend/internal/domain/value.go`, after the `Symbol` block (after line 62), add:
    - `func (s Symbol) ToSlashFormat() Symbol` — "BTCUSD" → "BTC/USD". If already has slash, return as-is. Logic: if len >= 6 and last 3 chars are "USD", insert slash before "USD".
    - `func (s Symbol) ToNoSlashFormat() Symbol` — "BTC/USD" → "BTCUSD". Removes all `/` characters.
    - `func (s Symbol) IsCryptoSymbol() bool` — true if symbol contains `/` and ends with `/USD`.
  - Write tests in `backend/internal/domain/value_test.go`:
    - `TestSymbol_ToSlashFormat` — "BTCUSD" → "BTC/USD", "ETHUSD" → "ETH/USD", "BTC/USD" → "BTC/USD" (idempotent), "AAPL" → "AAPL" (unchanged)
    - `TestSymbol_ToNoSlashFormat` — "BTC/USD" → "BTCUSD", "BTCUSD" → "BTCUSD" (idempotent)
    - `TestSymbol_IsCryptoSymbol` — "BTC/USD" = true, "AAPL" = false, "BTCUSD" = false

  **Must NOT do**: No changes to `NewSymbol`. No external dependencies. No perpetual futures symbols.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 1 (parallel with 73, 74, 76-78) | **Blocks**: 81, 82, 84, 86 | **Blocked By**: None

  **References**:
  - `backend/internal/domain/value.go:52-62` — Existing `Symbol` type. New methods are receiver methods on this type.
  - Alpaca API: Positions return `BTCUSD`, orders/data use `BTC/USD` — this normalization bridges both.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/domain/... -run TestSymbol_ -v` → PASS (3 test functions)
  - [ ] `Symbol("BTCUSD").ToSlashFormat()` = `"BTC/USD"`
  - [ ] `Symbol("AAPL").ToSlashFormat()` = `"AAPL"` (unchanged)

  **QA Scenarios:**
  ```
  Scenario: Symbol normalization round-trips correctly
    Tool: Bash
    Steps: cd backend && go test ./internal/domain/... -run TestSymbol_ -v
    Expected: All conversions correct, idempotent calls safe, equity symbols untouched
    Evidence: .sisyphus/evidence/task-75-symbol-normalize.txt
  ```

  **Commit**: Wave 1 group | `feat(domain): add Symbol normalizer for crypto slash/no-slash conversion`
  Files: `domain/value.go`, `domain/value_test.go` | Pre-commit: `go test ./internal/domain/...`

---

- [ ] 76. Extend `InstrumentType` with `CRYPTO` + update `NewInstrument` — Complexity: S

  **What to do**:
  - In `backend/internal/domain/options.go`, add `InstrumentTypeCrypto InstrumentType = "CRYPTO"` (line 19, after `InstrumentTypeOption`)
  - Update `NewInstrument` validation (line 80) to also accept `InstrumentTypeCrypto`
  - For crypto instruments: `UnderlyingSymbol` is empty. `Symbol` is crypto pair e.g. `BTC/USD`.
  - Write tests in `backend/internal/domain/options_test.go`:
    - `TestNewInstrument_Crypto` — `NewInstrument(InstrumentTypeCrypto, "BTC/USD", "")` succeeds
    - Verify existing EQUITY and OPTION tests still pass

  **Must NOT do**: No changes to option-specific types. No changes to Instrument struct fields.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 1 (parallel with 73-75, 77-78) | **Blocks**: 83 | **Blocked By**: None

  **References**:
  - `backend/internal/domain/options.go:14-20` — `InstrumentType` definition. Add CRYPTO here.
  - `backend/internal/domain/options.go:79-91` — `NewInstrument` with validation check on line 80. Modify this condition.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/domain/... -run TestNewInstrument -v` → PASS (existing + new)
  - [ ] `domain.InstrumentTypeCrypto` constant equals `"CRYPTO"`

  **QA Scenarios:**
  ```
  Scenario: Crypto InstrumentType accepted
    Tool: Bash
    Steps: cd backend && go test ./internal/domain/... -run TestNewInstrument -v
    Expected: PASS — crypto instruments create successfully, existing tests unchanged
    Evidence: .sisyphus/evidence/task-76-crypto-instrument.txt
  ```

  **Commit**: Wave 1 group | `feat(domain): add InstrumentTypeCrypto constant`
  Files: `domain/options.go`, `domain/options_test.go` | Pre-commit: `go test ./internal/domain/...`

---

- [ ] 77. Restructure `SymbolsConfig` for asset-class-tagged symbols — Complexity: M

  **What to do**:
  - In `backend/internal/config/config.go`, extend `SymbolsConfig` (lines 76-79):
    ```go
    type SymbolGroupConfig struct {
        AssetClass string   `yaml:"asset_class"` // "EQUITY" or "CRYPTO"
        Symbols    []string `yaml:"symbols"`
        Timeframe  string   `yaml:"timeframe"`
    }
    type SymbolsConfig struct {
        Groups    []SymbolGroupConfig `yaml:"groups"`
        Symbols   []string            `yaml:"symbols,omitempty"`  // backward compat
        Timeframe string              `yaml:"timeframe,omitempty"` // backward compat
    }
    ```
  - Add `func (sc *SymbolsConfig) Normalize()` — if Groups is empty and Symbols is populated, create single EQUITY group (backward compat)
  - Add `func (sc *SymbolsConfig) SymbolsByAssetClass(ac string) []string`
  - Update `validate()` (line 313) for new structure: at least one group, valid asset classes, valid timeframes
  - Call `Normalize()` in `Load()` after YAML unmarshal
  - Update `configs/config.yaml` to grouped format keeping existing equity symbols + adding `BTC/USD`, `ETH/USD`
  - Write tests: `TestSymbolsConfig_Normalize_BackwardCompat`, `TestSymbolsConfig_SymbolsByAssetClass`, `TestValidate_EmptyGroups`

  **Must NOT do**: Do NOT break existing config loading (old format must work). Do NOT remove flat fields. No env overlay changes.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 1 (parallel with 73-76, 78) | **Blocks**: 88, 89, 90 | **Blocked By**: 73 (needs AssetClass constants for validation — can use string fallback)

  **References**:
  - `backend/internal/config/config.go:75-79` — Current `SymbolsConfig` to extend
  - `backend/internal/config/config.go:312-342` — `validate()` to update
  - `backend/internal/config/config.go:128-212` — `Load()` where `Normalize()` must be called
  - `configs/config.yaml` — Current YAML to update

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/config/... -run TestSymbolsConfig -v` → PASS (3 tests)
  - [ ] Existing `configs/config.yaml` loads successfully (backward compat)
  - [ ] `SymbolsByAssetClass("CRYPTO")` returns `["BTC/USD", "ETH/USD"]`

  **QA Scenarios:**
  ```
  Scenario: Backward-compatible config loading
    Tool: Bash
    Steps: cd backend && go test ./internal/config/... -run TestSymbolsConfig_Normalize -v
    Expected: Flat symbols auto-migrate to EQUITY group
    Evidence: .sisyphus/evidence/task-77-backward-compat.txt

  Scenario: Mixed portfolio config
    Tool: Bash
    Steps: cd backend && go test ./internal/config/... -run TestSymbolsConfig_SymbolsByAssetClass -v
    Expected: EQUITY returns equity tickers, CRYPTO returns BTC/USD + ETH/USD
    Evidence: .sisyphus/evidence/task-77-mixed-portfolio.txt
  ```

  **Commit**: Wave 1 group | `feat(config): restructure SymbolsConfig for asset-class-tagged symbol groups`
  Files: `config/config.go`, `config/config_test.go`, `configs/config.yaml` | Pre-commit: `go test ./internal/config/...`

---

- [ ] 78. Extend `AlpacaConfig` with `CryptoDataURL` + `CryptoFeed` — Complexity: S

  **What to do**:
  - In `backend/internal/config/config.go`, add to `AlpacaConfig` struct (after line 34):
    - `CryptoDataURL string \`yaml:"crypto_data_url"\`` — default `"wss://stream.data.alpaca.markets"`
    - `CryptoFeed string \`yaml:"crypto_feed"\`` — default `"us"` (NOT "iex")
  - Add defaults in `Load()` (around line 143-148)
  - Add env overlays: `APCA_CRYPTO_DATA_URL`, `APCA_CRYPTO_FEED`
  - Update `configs/config.yaml` alpaca section with crypto fields
  - Write tests: `TestAlpacaConfig_CryptoDefaults`, `TestAlpacaConfig_CryptoEnvOverlay`

  **Must NOT do**: No changes to existing `DataURL` or `Feed` fields. No existing env overlay changes.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 1 (parallel with 73-77) | **Blocks**: 79, 80 | **Blocked By**: None

  **References**:
  - `backend/internal/config/config.go:28-36` — `AlpacaConfig` struct to extend
  - `backend/internal/config/config.go:143-148` — Default initialization in `Load()`
  - `backend/internal/config/config.go:215-229` — Env overlay pattern for Alpaca
  - Alpaca crypto WS: `wss://stream.data.alpaca.markets`, feed: `"us"`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/config/... -run TestAlpacaConfig -v` → PASS
  - [ ] Default `CryptoFeed` = `"us"`, Default `CryptoDataURL` = `"wss://stream.data.alpaca.markets"`

  **QA Scenarios:**
  ```
  Scenario: Crypto config defaults applied
    Tool: Bash
    Steps: cd backend && go test ./internal/config/... -run TestAlpacaConfig_Crypto -v
    Expected: CryptoDataURL and CryptoFeed have correct defaults, env vars override
    Evidence: .sisyphus/evidence/task-78-crypto-config.txt
  ```

  **Commit**: Wave 1 group | `feat(config): add CryptoDataURL and CryptoFeed to AlpacaConfig`
  Files: `config/config.go`, `config/config_test.go`, `configs/config.yaml` | Pre-commit: `go test ./internal/config/...`

---

### Wave 2 — Adapter Layer (Tasks 79-84)

---

- [ ] 79. Crypto WebSocket streaming (`crypto_ws.go`) — Complexity: L

  **What to do**:
  - Create `backend/internal/adapters/alpaca/crypto_ws.go` — new file:
    - `type CryptoWSClient struct` with `stream.CryptoClient` from Alpaca SDK
    - Constructor: `func NewCryptoWSClient(cryptoDataURL, apiKey, apiSecret string)` — creates `stream.NewCryptoClient(marketdata.US, stream.WithCredentials(apiKey, apiSecret))`
    - `func (c *CryptoWSClient) StreamBars(ctx context.Context, symbols []domain.Symbol, handler ports.BarHandler) error` — subscribes using `WithCryptoBars(handler)` (NOT `WithBars`)
    - Convert `stream.CryptoBar` → `domain.MarketBar` in handler callback. Map fields: Open, High, Low, Close, Volume (already float64), Timestamp, Symbol.
    - `func (c *CryptoWSClient) Close() error`
  - **CRITICAL**: Do NOT reuse `deriveStreamURL()` from equity WS. Crypto WS uses a completely different URL and client.
  - Write tests in `backend/internal/adapters/alpaca/crypto_ws_test.go`:
    - `TestCryptoWSClient_ConvertBar` — verify `stream.CryptoBar` → `domain.MarketBar` conversion
    - `TestNewCryptoWSClient_RequiresCredentials` — empty key/secret returns error

  **Must NOT do**: Do NOT modify `websocket.go`. Do NOT reuse `WSClient` struct. Do NOT handle perpetual futures subscriptions.

  **Recommended Agent Profile**: `deep` | **Skills**: `[]`
  **Parallelization**: Wave 2 (parallel with 80-84) | **Blocks**: 81, 90 | **Blocked By**: 78

  **References**:
  - `backend/internal/adapters/alpaca/websocket.go` — Equity WSClient pattern. Follow same structure but with `stream.CryptoClient`. Key differences: `NewCryptoClient` instead of `NewStocksClient`, `WithCryptoBars` instead of `WithBars`.
  - `backend/internal/adapters/alpaca/options_rest.go` — Shows how to add new files to same package following extension pattern.
  - SDK: `stream.NewCryptoClient(marketdata.US, ...)` — the US feed is the only valid crypto feed.
  - SDK: `stream.CryptoBar` has `Volume float64` (not uint64 like StockBar).

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/adapters/alpaca/... -run TestCryptoWS -v` → PASS
  - [ ] `CryptoBar` → `MarketBar` conversion maps all fields correctly
  - [ ] File compiles without modifying any existing adapter files

  **QA Scenarios:**
  ```
  Scenario: CryptoBar conversion is correct
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -run TestCryptoWSClient_ConvertBar -v
    Expected: All fields (OHLCV, Timestamp, Symbol) map correctly from CryptoBar to MarketBar
    Evidence: .sisyphus/evidence/task-79-crypto-ws.txt

  Scenario: Adapter package still compiles and all existing tests pass
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -v
    Expected: 0 failures including existing adapter tests
    Evidence: .sisyphus/evidence/task-79-regression.txt
  ```

  **Commit**: Wave 2 group | `feat(alpaca): add crypto WebSocket streaming client`
  Files: `adapters/alpaca/crypto_ws.go`, `adapters/alpaca/crypto_ws_test.go` | Pre-commit: `go test ./internal/adapters/alpaca/...`

---

- [ ] 80. Crypto REST — historical bars + snapshots (`crypto_rest.go`) — Complexity: L

  **What to do**:
  - Create `backend/internal/adapters/alpaca/crypto_rest.go` — new file:
    - `func (r *RESTClient) GetCryptoHistoricalBars(ctx context.Context, dataURL string, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)` — calls Alpaca `/v1beta3/crypto/us/bars?symbols={sym}&timeframe={tf}&start={from}&end={to}`. Use symbol in slash format (`BTC/USD`).
    - `func (r *RESTClient) GetCryptoSnapshot(ctx context.Context, dataURL string, symbol domain.Symbol) (*domain.MarketBar, error)` — calls `/v1beta3/crypto/us/snapshots?symbols={sym}`.
    - Convert Alpaca crypto bar JSON response to `domain.MarketBar` — Volume is float64 (compatible).
  - **CRITICAL**: REST endpoint is `/v1beta3/crypto/us/bars` — NOT `/v2/stocks/{sym}/bars`. The crypto endpoint takes `symbols` as query param (comma-separated), not path param.
  - Write tests: `TestGetCryptoHistoricalBars_URLConstruction`, `TestGetCryptoHistoricalBars_ResponseParsing`

  **Must NOT do**: Do NOT modify `rest.go`. Do NOT change `GetHistoricalBars` in existing code. Do NOT handle pagination in MVP (single page sufficient for crypto).

  **Recommended Agent Profile**: `deep` | **Skills**: `[]`
  **Parallelization**: Wave 2 (parallel with 79, 81-84) | **Blocks**: 82, 90 | **Blocked By**: 78

  **References**:
  - `backend/internal/adapters/alpaca/rest.go:380-430` — Existing `GetHistoricalBars` for equities. Follow same pattern but with crypto URL `/v1beta3/crypto/us/bars`. Key difference: symbols as query param not path param.
  - `backend/internal/adapters/alpaca/options_rest.go` — Extension pattern: new methods on same `RESTClient` struct in separate file.
  - `backend/internal/adapters/alpaca/rest.go:1-50` — `RESTClient` struct and `doRequest` helper.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/adapters/alpaca/... -run TestGetCryptoHistorical -v` → PASS
  - [ ] URL correctly constructs to `/v1beta3/crypto/us/bars?symbols=BTC/USD&...`
  - [ ] Response parsing handles float64 Volume correctly

  **QA Scenarios:**
  ```
  Scenario: Crypto REST URL construction
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -run TestGetCryptoHistoricalBars -v
    Expected: URL matches /v1beta3/crypto/us/bars pattern, symbols as query param
    Evidence: .sisyphus/evidence/task-80-crypto-rest.txt
  ```

  **Commit**: Wave 2 group | `feat(alpaca): add crypto REST historical bars and snapshots`
  Files: `adapters/alpaca/crypto_rest.go`, `adapters/alpaca/crypto_rest_test.go` | Pre-commit: `go test ./internal/adapters/alpaca/...`

---

- [ ] 81. `Adapter.StreamBars` dispatch — equity vs crypto — Complexity: M

  **What to do**:
  - Modify `backend/internal/adapters/alpaca/adapter.go` `StreamBars` method (line 61-63):
    - Accept all symbols, split into equity symbols and crypto symbols using `Symbol.IsCryptoSymbol()`
    - Stream equity symbols via existing `a.ws.StreamBars()`
    - Stream crypto symbols via new `a.cryptoWs.StreamBars()` (added in Task 79)
    - Both run concurrently using goroutines, funneling bars to the same handler
  - Add `cryptoWs *CryptoWSClient` field to `Adapter` struct (line 17-23)
  - Update `NewAdapter` constructor (line 28-58) to create `CryptoWSClient` using `cfg.CryptoDataURL`
  - Update `Close()` (line 75-77) to close both WS clients

  **Must NOT do**: Do NOT change the `StreamBars` method signature. Do NOT modify the equity streaming path. Do NOT break existing callers.

  **Recommended Agent Profile**: `unspecified-high` | **Skills**: `[]`
  **Parallelization**: Wave 2 (parallel with 80, 82-84) | **Blocks**: 90 | **Blocked By**: 73, 75, 79

  **References**:
  - `backend/internal/adapters/alpaca/adapter.go:17-23` — `Adapter` struct to add `cryptoWs` field
  - `backend/internal/adapters/alpaca/adapter.go:28-58` — `NewAdapter` constructor to create crypto WS
  - `backend/internal/adapters/alpaca/adapter.go:60-63` — `StreamBars` method to add dispatch logic
  - `backend/internal/domain/value.go` — `Symbol.IsCryptoSymbol()` (Task 75) for dispatch
  - `backend/internal/adapters/alpaca/crypto_ws.go` — `CryptoWSClient` (Task 79)

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/adapters/alpaca/... -v` → PASS (all existing + new)
  - [ ] `StreamBars(ctx, ["AAPL", "BTC/USD"], ...)` creates both equity and crypto streams
  - [ ] Equity-only calls still work identically to before

  **QA Scenarios:**
  ```
  Scenario: Mixed symbol streaming dispatches correctly
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -run TestStreamBars -v
    Expected: Equity symbols → equity WS, crypto symbols → crypto WS, both feed same handler
    Evidence: .sisyphus/evidence/task-81-stream-dispatch.txt

  Scenario: Equity-only streaming unchanged (regression)
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -v
    Expected: All pre-existing adapter tests pass
    Evidence: .sisyphus/evidence/task-81-regression.txt
  ```

  **Commit**: Wave 2 group | `feat(alpaca): add crypto/equity dispatch to StreamBars`
  Files: `adapters/alpaca/adapter.go` | Pre-commit: `go test ./internal/adapters/alpaca/...`

---

- [ ] 82. `Adapter.GetHistoricalBars` dispatch — equity vs crypto — Complexity: M

  **What to do**:
  - Modify `backend/internal/adapters/alpaca/adapter.go` `GetHistoricalBars` (line 66-68):
    - Check `symbol.IsCryptoSymbol()` — if true, call `r.GetCryptoHistoricalBars()` (Task 80)
    - If false, call existing `r.GetHistoricalBars()` as before
  - Same dispatch pattern for `GetSnapshots` (line 70-72) if applicable
  - Also update the `fetcher` closure in `NewAdapter` (line 46-48) to dispatch crypto symbols to crypto REST

  **Must NOT do**: No signature changes. No modification to equity path.

  **Recommended Agent Profile**: `unspecified-high` | **Skills**: `[]`
  **Parallelization**: Wave 2 (parallel with 79, 81, 83-84) | **Blocks**: 90 | **Blocked By**: 73, 75, 80

  **References**:
  - `backend/internal/adapters/alpaca/adapter.go:66-68` — `GetHistoricalBars` to add dispatch
  - `backend/internal/adapters/alpaca/adapter.go:46-48` — `fetcher` closure in constructor (used by WS for ghost window polling)
  - `backend/internal/adapters/alpaca/crypto_rest.go` — `GetCryptoHistoricalBars` (Task 80)
  - `backend/internal/domain/value.go` — `Symbol.IsCryptoSymbol()` (Task 75)

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/adapters/alpaca/... -run TestGetHistoricalBars -v` → PASS
  - [ ] Crypto symbols route to `/v1beta3/crypto/us/bars`
  - [ ] Equity symbols still route to `/v2/stocks/{sym}/bars`

  **QA Scenarios:**
  ```
  Scenario: Historical bars dispatch by asset class
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -run TestGetHistoricalBars -v
    Expected: BTC/USD → crypto REST, AAPL → equity REST
    Evidence: .sisyphus/evidence/task-82-historical-dispatch.txt
  ```

  **Commit**: Wave 2 group | `feat(alpaca): add crypto/equity dispatch to GetHistoricalBars`
  Files: `adapters/alpaca/adapter.go` | Pre-commit: `go test ./internal/adapters/alpaca/...`

---

- [ ] 83. `Adapter.SubmitOrder` — crypto dispatch + long-only guard — Complexity: S

  **What to do**:
  - Modify `backend/internal/adapters/alpaca/adapter.go` `SubmitOrder` (line 81-98):
    - Add crypto instrument check: if `intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeCrypto`
    - For crypto: reject if `intent.Direction == domain.DirectionShort` with error "crypto does not support short selling"
    - For crypto: ensure symbol is in slash format (`Symbol.ToSlashFormat()`) before submitting to Alpaca
    - Otherwise: submit via existing `a.rest.SubmitOrder()` — crypto orders use the same `/v2/orders` endpoint
  - Write test: `TestSubmitOrder_CryptoLongOnly`, `TestSubmitOrder_CryptoRejectsShort`

  **Must NOT do**: Do NOT create a separate crypto order endpoint. Do NOT modify equity order path.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 2 (parallel with 79-82, 84) | **Blocks**: 90 | **Blocked By**: 76

  **References**:
  - `backend/internal/adapters/alpaca/adapter.go:81-98` — `SubmitOrder` with existing options dispatch. Add crypto check BEFORE the options check.
  - `backend/internal/adapters/alpaca/options_order.go` — Options dispatch pattern to follow.
  - `backend/internal/domain/options.go:17-20` — `InstrumentTypeCrypto` (Task 76)

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/adapters/alpaca/... -run TestSubmitOrder_Crypto -v` → PASS
  - [ ] Crypto SHORT is rejected with clear error message
  - [ ] Crypto LONG submits to same `/v2/orders` endpoint

  **QA Scenarios:**
  ```
  Scenario: Crypto order submission + short rejection
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -run TestSubmitOrder_Crypto -v
    Expected: LONG order accepted, SHORT order rejected with "crypto does not support short selling"
    Evidence: .sisyphus/evidence/task-83-crypto-order.txt
  ```

  **Commit**: Wave 2 group | `feat(alpaca): add crypto order dispatch with long-only guard`
  Files: `adapters/alpaca/adapter.go`, `adapters/alpaca/adapter_test.go` | Pre-commit: `go test ./internal/adapters/alpaca/...`

---

- [ ] 84. `Adapter.GetPositions` — symbol normalization — Complexity: S

  **What to do**:
  - Modify `backend/internal/adapters/alpaca/adapter.go` `GetPositions` (line 111-126) or the underlying `rest.GetPositions`:
    - After fetching positions from Alpaca, normalize crypto symbols from `BTCUSD` (Alpaca format) to `BTC/USD` (domain format) using `Symbol.ToSlashFormat()`
    - Also set `AssetClass` on returned `Trade` structs: detect crypto positions by checking if symbol matches crypto pattern after normalization
  - Alternatively, normalize in `rest.go`'s `GetPositions` when parsing the Alpaca response — find where position symbols are mapped to `Trade` structs and apply `ToSlashFormat()` there
  - Write test: `TestGetPositions_CryptoSymbolNormalized` — position with symbol `BTCUSD` returns Trade with symbol `BTC/USD`

  **Must NOT do**: Do NOT change position cache logic. Do NOT modify equity position handling.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 2 (parallel with 79-83) | **Blocks**: 90 | **Blocked By**: 75

  **References**:
  - `backend/internal/adapters/alpaca/adapter.go:111-126` — `GetPositions` with cache
  - `backend/internal/adapters/alpaca/rest.go` — Find where Alpaca positions are mapped to `domain.Trade`. That's where normalization should happen.
  - `backend/internal/domain/value.go` — `Symbol.ToSlashFormat()` (Task 75)
  - `backend/internal/app/execution/position_gate.go` — Position gate compares symbols — normalization ensures crypto positions match.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/adapters/alpaca/... -run TestGetPositions_Crypto -v` → PASS
  - [ ] Position with Alpaca symbol `BTCUSD` returns `Trade.Symbol = "BTC/USD"`
  - [ ] Equity positions unchanged

  **QA Scenarios:**
  ```
  Scenario: Crypto position symbol normalized
    Tool: Bash
    Steps: cd backend && go test ./internal/adapters/alpaca/... -run TestGetPositions_Crypto -v
    Expected: BTCUSD → BTC/USD, AAPL → AAPL (unchanged)
    Evidence: .sisyphus/evidence/task-84-position-normalize.txt
  ```

  **Commit**: Wave 2 group | `feat(alpaca): normalize crypto symbols in GetPositions`
  Files: `adapters/alpaca/adapter.go` or `adapters/alpaca/rest.go` | Pre-commit: `go test ./internal/adapters/alpaca/...`

---

### Wave 3 — App Services Layer (Tasks 85-89)

---

- [ ] 85. Session time — asset-class-aware (24/7 bypass) — Complexity: M

  **What to do**:
  - Modify `backend/internal/app/monitor/sessiontime.go`:
    - Add `func RTHOpenUTCForAsset(t time.Time, ac domain.AssetClass) time.Time` — for Crypto returns `time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)`. For Equity calls existing `RTHOpenUTC(t)`.
    - Add `func RTHEndUTCForAsset(t time.Time, ac domain.AssetClass) time.Time` — for Crypto returns `time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)`. For Equity calls existing `RTHEndUTC(t)`.
    - Add `func SessionKeyForAsset(t time.Time, ac domain.AssetClass) string` — for Crypto returns UTC date key. For Equity returns existing ET date key.
    - Keep existing functions unchanged for backward compat.
  - Find all callers of `RTHOpenUTC`, `RTHEndUTC`, `SessionKeyET` and determine if they need to be updated to use the asset-class-aware versions. Update callers that handle mixed asset classes.
  - Write tests: `TestRTHOpenUTCForAsset_Crypto`, `TestRTHOpenUTCForAsset_Equity` (matches existing), `TestSessionKeyForAsset`

  **Must NOT do**: Do NOT delete existing `RTHOpenUTC`, `RTHEndUTC`, `SessionKeyET`. They are used by equity code paths.

  **Recommended Agent Profile**: `unspecified-high` | **Skills**: `[]`
  **Parallelization**: Wave 3 (parallel with 86-89) | **Blocks**: 88, 90 | **Blocked By**: 73, 74

  **References**:
  - `backend/internal/app/monitor/sessiontime.go:1-47` — ENTIRE FILE. All session time functions to extend.
  - `backend/internal/domain/exchange_calendar.go` — `TradingCalendar` (Task 74) — can use `CalendarFor()` internally.
  - `backend/internal/domain/value.go` — `AssetClass` type (Task 73)

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/app/monitor/... -run TestRTHOpenUTCForAsset -v` → PASS
  - [ ] Crypto returns midnight UTC; Equity returns 9:30 ET
  - [ ] Existing callers of original functions still work

  **QA Scenarios:**
  ```
  Scenario: Session time dispatch by asset class
    Tool: Bash
    Steps: cd backend && go test ./internal/app/monitor/... -run TestRTH -v
    Expected: Crypto → 00:00-23:59 UTC, Equity → 9:30-16:00 ET
    Evidence: .sisyphus/evidence/task-85-session-time.txt
  ```

  **Commit**: Wave 3 group | `feat(monitor): add asset-class-aware session time functions`
  Files: `app/monitor/sessiontime.go`, `app/monitor/sessiontime_test.go` | Pre-commit: `go test ./internal/app/monitor/...`

---

- [ ] 86. Execution service — asset-class-aware direction enforcement — Complexity: M

  **What to do**:
  - Modify `backend/internal/app/execution/service.go` lines 131-140:
    - Current code: unconditionally rejects `DirectionShort` (comment says "paper account does not support short selling")
    - New code: reject SHORT if `intent.AssetClass == domain.AssetClassCrypto` (always — crypto can't short on Alpaca) OR if paper mode (existing behavior)
    - The conditional should be: `if intent.Direction == domain.DirectionShort && (!intent.AssetClass.SupportsShort() || isPaperMode)` — this preserves existing paper-mode short rejection and adds crypto-specific rejection
  - Update rejection log message to include asset class: `"SHORT rejected — %s does not support short selling"`
  - Write tests: `TestExecutionService_CryptoShortRejected`, `TestExecutionService_EquityShortRejected_Paper`, verify existing equity tests pass

  **Must NOT do**: Do NOT change the overall execution flow. Do NOT remove position gate or risk engine checks.

  **Recommended Agent Profile**: `unspecified-high` | **Skills**: `[]`
  **Parallelization**: Wave 3 (parallel with 85, 87-89) | **Blocks**: 90 | **Blocked By**: 73, 75

  **References**:
  - `backend/internal/app/execution/service.go:131-140` — EXACT lines with SHORT rejection logic to modify.
  - `backend/internal/app/execution/service.go:120-129` — Position gate check (above) — do not touch.
  - `backend/internal/domain/value.go` — `AssetClass.SupportsShort()` (Task 73) for decision.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/app/execution/... -run TestExecution -v` → PASS
  - [ ] Crypto SHORT → rejected with asset-class-specific message
  - [ ] Equity SHORT on paper → still rejected (existing behavior)
  - [ ] Equity LONG → passes through (existing behavior)

  **QA Scenarios:**
  ```
  Scenario: Crypto short rejected, equity behavior preserved
    Tool: Bash
    Steps: cd backend && go test ./internal/app/execution/... -run TestExecution -v
    Expected: Crypto SHORT rejected, paper equity SHORT rejected, equity LONG accepted
    Evidence: .sisyphus/evidence/task-86-direction-enforce.txt
  ```

  **Commit**: Wave 3 group | `feat(execution): asset-class-aware short selling enforcement`
  Files: `app/execution/service.go`, `app/execution/service_test.go` | Pre-commit: `go test ./internal/app/execution/...`

---

- [ ] 87. Screener — crypto mode (skip pre-market, 24/7 ranking) — Complexity: M

  **What to do**:
  - Modify `backend/internal/app/screener/service.go`:
    - Current screener runs pre-market screening at 8:30 AM ET and checks NYSE holidays
    - For crypto symbols: skip pre-market timing gate (crypto has no pre-market), skip holiday check, allow screening at any time
    - Add `AssetClass` parameter or detect from symbol to determine screening mode
    - Crypto screening: rank by volume and volatility metrics (same ranking logic, just no time restrictions)
  - Use `TradingCalendar.IsOpen()` (Task 74) instead of hardcoded NYSE checks
  - Write tests: `TestScreener_CryptoAlwaysEligible`, `TestScreener_EquityPreMarketGating`

  **Must NOT do**: Do NOT change the ranking algorithm. Do NOT modify equity screening behavior.

  **Recommended Agent Profile**: `unspecified-high` | **Skills**: `[]`
  **Parallelization**: Wave 3 (parallel with 85, 86, 88, 89) | **Blocks**: 90 | **Blocked By**: 73, 74

  **References**:
  - `backend/internal/app/screener/service.go` — Full file. Find pre-market timing gates and NYSE holiday checks.
  - `backend/internal/domain/exchange_calendar.go` — `TradingCalendar.IsOpen()` (Task 74) to replace hardcoded checks.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/app/screener/... -v` → PASS
  - [ ] Crypto symbols screened regardless of time of day
  - [ ] Equity symbols still gated by pre-market timing

  **QA Scenarios:**
  ```
  Scenario: Crypto screening has no time restrictions
    Tool: Bash
    Steps: cd backend && go test ./internal/app/screener/... -run TestScreener_Crypto -v
    Expected: Crypto symbols eligible at 3AM Saturday; equity symbols blocked outside market hours
    Evidence: .sisyphus/evidence/task-87-screener.txt
  ```

  **Commit**: Wave 3 group | `feat(screener): add crypto mode with 24/7 screening`
  Files: `app/screener/service.go`, `app/screener/service_test.go` | Pre-commit: `go test ./internal/app/screener/...`

---

- [ ] 88. Strategy routing — crypto symbol → crypto-compatible strategies — Complexity: M

  **What to do**:
  - In the strategy routing/dispatching code (find where symbols are matched to strategy engines):
    - Ensure crypto symbols are only routed to strategies that support crypto (check strategy DNA `asset_classes` field)
    - ORB strategy must NOT receive crypto symbols (it's equity-only)
    - AVWAP strategy with crypto DNA (Task 89) receives crypto symbols
  - Add `asset_classes` field to strategy DNA TOML schema: `asset_classes = ["EQUITY"]` or `["CRYPTO"]` or `["EQUITY", "CRYPTO"]`
  - Read this field from existing DNA TOML files and add `asset_classes = ["EQUITY"]` to existing strategies (ORB, AVWAP, AI Scalping)
  - Route symbols to strategies based on matching asset class
  - Write tests: `TestStrategyRouting_CryptoToAVWAP`, `TestStrategyRouting_CryptoNotToORB`

  **Must NOT do**: Do NOT modify ORB strategy internals. Do NOT create new strategy engines.

  **Recommended Agent Profile**: `unspecified-high` | **Skills**: `[]`
  **Parallelization**: Wave 3 (parallel with 85-87, 89) | **Blocks**: 90 | **Blocked By**: 77, 85

  **References**:
  - `backend/internal/app/strategy/` — Strategy routing code. Find where DNA configs are loaded and symbols dispatched.
  - `configs/strategies/orb_break_retest.toml` — Existing ORB DNA. Add `asset_classes = ["EQUITY"]`.
  - `configs/strategies/avwap.toml` — Existing AVWAP DNA. Add `asset_classes = ["EQUITY"]`.
  - `configs/strategies/ai_scalping.toml` — Existing AI Scalping DNA. Add `asset_classes = ["EQUITY"]`.
  - `backend/internal/config/config.go` — `SymbolsConfig.SymbolsByAssetClass()` (Task 77) for symbol grouping.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./internal/app/strategy/... -v` → PASS
  - [ ] Crypto symbols only route to strategies with `"CRYPTO"` in `asset_classes`
  - [ ] Existing equity strategies receive `asset_classes = ["EQUITY"]` and still work

  **QA Scenarios:**
  ```
  Scenario: Strategy routing respects asset classes
    Tool: Bash
    Steps: cd backend && go test ./internal/app/strategy/... -run TestStrategyRouting -v
    Expected: BTC/USD → crypto AVWAP only, AAPL → ORB + AVWAP + AI Scalping
    Evidence: .sisyphus/evidence/task-88-strategy-routing.txt
  ```

  **Commit**: Wave 3 group | `feat(strategy): asset-class-aware strategy routing`
  Files: `app/strategy/`, `configs/strategies/*.toml` | Pre-commit: `go test ./internal/app/strategy/...`

---

- [ ] 89. Crypto AVWAP strategy DNA + engine gate — Complexity: S

  **What to do**:
  - Create `configs/strategies/crypto_avwap.toml` — adapted AVWAP strategy for crypto:
    - Copy structure from `configs/strategies/avwap.toml`
    - Set `asset_classes = ["CRYPTO"]`
    - Adjust parameters for crypto market characteristics:
      - `routing.symbols` — leave empty or use `["BTC/USD", "ETH/USD"]` as defaults
      - Timeframe: keep `"1m"` or adjust to crypto volatility
      - Risk parameters: potentially tighter stops for crypto volatility
      - Remove any ORB-window references (crypto has no opening range)
    - Set `direction = "LONG"` only (no SHORT for crypto)
  - In the AVWAP strategy engine code: add a guard that skips if `direction == SHORT` and `asset_class == CRYPTO`
  - Write test: `TestCryptoAVWAP_DNALoads` — verify TOML parses correctly

  **Must NOT do**: Do NOT modify the AVWAP algorithm. Do NOT create a new strategy engine. Just adapt the DNA.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 3 (parallel with 85-88) | **Blocks**: 90 | **Blocked By**: 77

  **References**:
  - `configs/strategies/avwap.toml` — Source AVWAP DNA to copy and adapt for crypto.
  - `backend/internal/app/strategy/` — AVWAP engine code to add long-only guard.

  **Acceptance Criteria**:
  - [ ] `configs/strategies/crypto_avwap.toml` loads without parse errors
  - [ ] `asset_classes = ["CRYPTO"]` is set
  - [ ] `direction = "LONG"` enforced

  **QA Scenarios:**
  ```
  Scenario: Crypto AVWAP DNA loads and validates
    Tool: Bash
    Steps: cd backend && go test ./internal/app/strategy/... -run TestCryptoAVWAP -v
    Expected: TOML parses, asset_classes=["CRYPTO"], direction=LONG
    Evidence: .sisyphus/evidence/task-89-crypto-avwap.txt
  ```

  **Commit**: Wave 3 group | `feat(strategy): add crypto AVWAP strategy DNA`
  Files: `configs/strategies/crypto_avwap.toml`, strategy engine guard | Pre-commit: `go test ./internal/app/strategy/...`

---

### Wave 4 — Wiring + Dashboard + E2E (Tasks 90-93)

---

- [ ] 90. `main.go` — wire crypto streams, split symbol lists — Complexity: L

  **What to do**:
  - Modify `backend/cmd/omo-core/main.go` (913 lines):
    - In DI wiring: use `SymbolsConfig.SymbolsByAssetClass("EQUITY")` and `SymbolsByAssetClass("CRYPTO")` to split symbol lists
    - Create separate crypto stream subscription alongside equity stream
    - Wire `CryptoWSClient` into `Adapter` constructor (may already be done via Task 81's changes to `NewAdapter`)
    - Ensure crypto bar handler feeds into the same strategy/execution pipeline but with `AssetClass = Crypto` on events
    - Add crypto symbols to the screener initialization if applicable
    - Verify: `go build` compiles, `go run ./cmd/omo-core/` starts without error with mixed config
  - This is the integration point — all previous tasks come together here.

  **Must NOT do**: Do NOT restructure the entire main.go. Minimal changes — just wire new crypto paths alongside existing equity paths.

  **Recommended Agent Profile**: `deep` | **Skills**: `[]`
  **Parallelization**: Wave 4 (parallel with 91, 92) | **Blocks**: 93 | **Blocked By**: 79-89 (ALL Wave 2+3 tasks)

  **References**:
  - `backend/cmd/omo-core/main.go` — Full 913-line file. Find: adapter construction, stream subscription, symbol loading, strategy wiring.
  - `backend/internal/config/config.go` — `SymbolsConfig.SymbolsByAssetClass()` (Task 77)
  - `backend/internal/adapters/alpaca/adapter.go` — Updated `NewAdapter` and `StreamBars` (Tasks 81, 82)

  **Acceptance Criteria**:
  - [ ] `cd backend && go build -o bin/omo-core ./cmd/omo-core` → compiles clean
  - [ ] `cd backend && go run ./cmd/omo-core/` starts without panic with mixed config (may fail to connect to Alpaca if no keys, but should not crash on config/wiring)
  - [ ] Crypto symbols appear in startup logs

  **QA Scenarios:**
  ```
  Scenario: Binary compiles with all crypto wiring
    Tool: Bash
    Steps: cd backend && go build -o bin/omo-core ./cmd/omo-core
    Expected: Clean compile, zero errors
    Evidence: .sisyphus/evidence/task-90-build.txt

  Scenario: Startup with mixed config doesn't crash
    Tool: Bash (tmux)
    Steps: Start omo-core with mixed equity+crypto config, wait 5 seconds, verify no panic
    Expected: Process starts, logs show both equity and crypto symbol initialization
    Evidence: .sisyphus/evidence/task-90-startup.txt
  ```

  **Commit**: Wave 4 group | `feat(crypto): wire crypto streaming and routing in main.go`
  Files: `cmd/omo-core/main.go` | Pre-commit: `go build ./cmd/omo-core/`

---

- [ ] 91. Dashboard — crypto symbols, 24/7 charts, mixed portfolio — Complexity: L

  **What to do**:
  - In `apps/dashboard/` (Next.js 15 frontend):
    - Add crypto symbols (`BTC/USD`, `ETH/USD`) to the symbol list/selector
    - Ensure chart component handles 24/7 data (no market-hours gaps like equities)
    - If symbol selector has equity-only filtering, add "Crypto" filter/tab
    - Display asset class badge (EQUITY / CRYPTO) next to symbol names
    - Verify WebSocket connection receives crypto bars and renders them
    - Test: crypto charts show continuous data without NYSE-hours gaps
  - This is frontend work — explore the dashboard code to understand current symbol display, chart component, and WebSocket integration before making changes.

  **Must NOT do**: Do NOT redesign the dashboard. Minimal changes to support crypto display.

  **Recommended Agent Profile**: `visual-engineering` | **Skills**: `["senior-frontend", "react-best-practices"]`
  **Parallelization**: Wave 4 (parallel with 90, 92) | **Blocks**: F3 | **Blocked By**: 73, 77

  **References**:
  - `apps/dashboard/` — Explore entire frontend structure. Find: symbol list component, chart component, WebSocket hook, data fetching.
  - Backend WebSocket: crypto bars stream on same handler — frontend should receive them automatically if WS is wired correctly.

  **Acceptance Criteria**:
  - [ ] `cd apps/dashboard && npm run build` → compiles without errors
  - [ ] Crypto symbols appear in symbol selector
  - [ ] Charts render 24/7 data without gaps

  **QA Scenarios:**
  ```
  Scenario: Dashboard shows crypto symbols
    Tool: Playwright
    Steps:
      1. Navigate to http://localhost:3000
      2. Look for symbol selector/list
      3. Verify BTC/USD and ETH/USD appear
      4. Click on BTC/USD
      5. Verify chart renders
    Expected: Crypto symbols visible and clickable, chart shows data
    Evidence: .sisyphus/evidence/task-91-dashboard-crypto.png

  Scenario: Dashboard build succeeds
    Tool: Bash
    Steps: cd apps/dashboard && npm run build
    Expected: Build completes with 0 errors
    Evidence: .sisyphus/evidence/task-91-build.txt
  ```

  **Commit**: Wave 4 group | `feat(dashboard): add crypto symbol support with 24/7 charts`
  Files: `apps/dashboard/` components | Pre-commit: `cd apps/dashboard && npm run build`

---

- [ ] 92. TimescaleDB — verify MarketBar schema handles crypto symbols — Complexity: S

  **What to do**:
  - Review `migrations/` directory for the `market_bars` table schema
  - Verify the `symbol` column (likely `TEXT` or `VARCHAR`) can store crypto symbols with slashes (`BTC/USD`) — it should, since `Symbol` is just a string
  - Verify no constraints or indexes that would reject slash-containing symbols
  - If there's an asset-class column, ensure it accepts "CRYPTO". If not, consider adding one (optional for MVP — can query by symbol pattern)
  - Write a migration test or verification script that inserts a crypto bar and reads it back
  - Check if TimescaleDB hypertable partitioning is symbol-aware — if so, crypto symbols should work the same way

  **Must NOT do**: Do NOT modify existing equity data. Do NOT change partitioning strategy.

  **Recommended Agent Profile**: `quick` | **Skills**: `[]`
  **Parallelization**: Wave 4 (parallel with 90, 91) | **Blocks**: 93 | **Blocked By**: 73

  **References**:
  - `migrations/` — SQL migration files for MarketBar table schema
  - `backend/internal/adapters/timescaledb/` — TimescaleDB adapter code for bar storage

  **Acceptance Criteria**:
  - [ ] `BTC/USD` symbol can be inserted into MarketBar table
  - [ ] No schema changes needed OR migration added
  - [ ] Existing equity data unaffected

  **QA Scenarios:**
  ```
  Scenario: Crypto symbol compatible with DB schema
    Tool: Bash
    Steps: Review migration SQL, verify TEXT/VARCHAR column accepts slash symbols
    Expected: No schema blockers for crypto symbols
    Evidence: .sisyphus/evidence/task-92-db-schema.txt
  ```

  **Commit**: Wave 4 group | `chore(db): verify MarketBar schema supports crypto symbols`
  Files: `migrations/` (if migration needed) | Pre-commit: `go test ./internal/adapters/timescaledb/...`

---

- [ ] 93. Integration test — full crypto pipeline paper-mode E2E — Complexity: L

  **What to do**:
  - Create `backend/internal/integration/crypto_e2e_test.go` (or appropriate test location):
    - Test the full crypto pipeline end-to-end in paper mode:
      1. Load mixed config (equity + crypto)
      2. Initialize adapter with crypto WS + REST
      3. Verify crypto bars can be fetched historically (REST)
      4. Verify crypto bar streaming starts (WS mock or real paper connection)
      5. Submit a crypto LONG order in paper mode → verify acceptance
      6. Submit a crypto SHORT order → verify rejection
      7. Fetch positions → verify crypto position has normalized symbol
      8. Verify equity pipeline still works alongside crypto
    - This may need build tag `//go:build integration` if it requires live Alpaca connection
  - Also run the full test suite: `cd backend && go test ./...` to verify no regressions

  **Must NOT do**: Do NOT test against production Alpaca. Paper mode only. Do NOT skip equity regression.

  **Recommended Agent Profile**: `deep` | **Skills**: `[]`
  **Parallelization**: Wave 4 (sequential after 90, 92) | **Blocks**: F1-F4 | **Blocked By**: 90, 92

  **References**:
  - All previous tasks — this test validates they work together
  - `backend/internal/adapters/alpaca/adapter.go` — Updated adapter with crypto support
  - `backend/internal/config/config.go` — Mixed portfolio config
  - `configs/config.yaml` — Updated config with crypto symbols

  **Acceptance Criteria**:
  - [ ] `cd backend && go test ./... -v` → ALL PASS (320+ existing + ~40 new)
  - [ ] Crypto LONG order accepted in paper mode
  - [ ] Crypto SHORT order rejected
  - [ ] Crypto position symbol normalized correctly

  **QA Scenarios:**
  ```
  Scenario: Full crypto pipeline E2E
    Tool: Bash
    Steps: cd backend && go test ./internal/integration/... -run TestCryptoE2E -v -tags integration
    Expected: All E2E steps pass — config load, bar fetch, order submit, position normalize
    Evidence: .sisyphus/evidence/task-93-e2e.txt

  Scenario: Full regression suite
    Tool: Bash
    Steps: cd backend && go test ./... -v
    Expected: ALL tests pass — 320+ existing + ~40 new crypto tests
    Evidence: .sisyphus/evidence/task-93-full-regression.txt
  ```

  **Commit**: Wave 4 group | `test(crypto): add full crypto pipeline E2E integration test`
  Files: `backend/internal/integration/crypto_e2e_test.go` | Pre-commit: `go test ./...`

---

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [ ] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, run `go test`, check config). For each "Must NOT Have": search codebase for forbidden patterns (perps, margin, DeFi). Check evidence files exist in `.sisyphus/evidence/`. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] F2. **Code Quality Review** — `unspecified-high`
  Run `cd backend && go vet ./... && go test ./...`. Review all changed files for: type assertions without check, empty error handling, `fmt.Println` in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names. Verify hexagonal boundaries: domain has zero imports from adapters/app.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [ ] F3. **Real QA** — `unspecified-high` (+ `playwright` skill for dashboard)
  Start from clean state. Execute EVERY QA scenario from EVERY task. Test cross-task integration: crypto bars stream → strategy picks up → order executes → position appears in dashboard. Test edge cases: equity pipeline still works alongside crypto. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [ ] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (`git log/diff`). Verify 1:1 — everything in spec was built, nothing beyond spec was built. Check "Must NOT do" compliance: no perps code, no margin, no interface breaking changes. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Wave | Commit | Message | Files | Pre-commit |
|------|--------|---------|-------|------------|
| 1 | Group | `feat(domain): add AssetClass type, TradingCalendar, SymbolNormalizer` | domain/*.go, config/*.go | `go test ./internal/domain/... ./internal/config/...` |
| 2 | Group | `feat(alpaca): add crypto market data streaming and REST` | adapters/alpaca/crypto_*.go, adapter.go | `go test ./internal/adapters/alpaca/...` |
| 3 | Group | `feat(app): make services asset-class-aware for crypto` | app/**/*.go, configs/strategies/*.toml | `go test ./internal/app/...` |
| 4 | Group | `feat(crypto): wire crypto pipeline + dashboard + E2E test` | cmd/omo-core/main.go, apps/dashboard/**, test files | `go test ./...` |
| FINAL | Single | `test(phase15): final verification evidence` | .sisyphus/evidence/** | — |

---

## Success Criteria

### Verification Commands
```bash
cd backend && go test ./...                    # Expected: ALL PASS (320+ existing + ~40 new)
cd backend && go build -o bin/omo-core ./cmd/omo-core  # Expected: clean compile
cd backend && go vet ./...                     # Expected: no issues
```

### Final Checklist
- [ ] All "Must Have" items present and verified
- [ ] All "Must NOT Have" items absent (no perps, margin, DeFi, interface breaks)
- [ ] All existing 320+ equity tests pass unchanged
- [ ] Crypto bars stream in paper mode (`BTC/USD`, `ETH/USD`)
- [ ] Crypto order submission works in paper mode (long-only)
- [ ] Mixed portfolio config loads and validates
- [ ] Dashboard shows crypto alongside equities
- [ ] `AssetClass` threads cleanly through domain → config → adapters → app services
