# Options Trading Wiring Plan — Phase 14 (Options Integration)

> **Status**: Plan ready for implementation
> **Source**: Session research + codebase audit + Oracle review questions submitted
> **PRD Reference**: PRD Section 4 (Options Trading)
> **Implementation Plan Items**: #72 (infrastructure done), wiring gaps identified below

---

## TL;DR

~70% of the options trading infrastructure already exists: domain types, port interfaces, Alpaca adapter (chain fetch + order submission), contract selection service with regime awareness, and an options risk engine. However, **none of it is wired into the live trading pipeline**. The `[options]` TOML block is silently dropped by `spec_loader.go`, `RiskSizer` always creates equity `OrderIntent`s, `OptionsRiskEngine` is orphaned, and the guard chain breaks for options. This plan closes 7 specific gaps to make the first options trade flow end-to-end.

### User Scoping Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Order direction | **Buy-to-open only** | No naked short risk; simplifies guard logic |
| Price data | **REST polling every 60s** | No OPRA WebSocket needed; Alpaca snapshots sufficient for 35-45 DTE contracts |
| Leg structure | **Single-leg only** | Multi-leg (spreads, straddles) deferred to future plan |
| Bearish signal mapping | Buy puts (not sell calls) | Aligns with buy-to-open constraint |

### Deliverables

- `spec_loader.go` — parse `[options]` TOML block into `Spec.Options`
- `ports/strategy/store.go` — add `Options *domain.OptionsConfig` field to `Spec`
- `domain/options_config.go` — move `OptionsConfig` from `app/options/` to domain (break circular dep)
- `strategy/risk_sizer.go` — options branch in `handleSignal()` to create option `OrderIntent`
- `execution/service.go` — wire `OptionsRiskEngine` into execution pipeline
- `execution/exposure_guard.go` — options-aware exposure calculation
- `migrations/011_add_options_columns.up.sql` — add options columns to `trades` and `orders` tables
- `positionmonitor/service.go` — DTE_FLOOR and EXPIRY_WATCH exit rules for options

### Estimated Effort

- Medium (~1.5 days, 7 sequential steps)
- Steps 1-3 are foundational; steps 4-7 can partially overlap

---

## What Already Exists (Codebase Audit)

### Domain Layer (`internal/domain/`)

| File | Contents | Status |
|------|----------|--------|
| `options.go` | `InstrumentType` (EQUITY/OPTION/CRYPTO), `OptionRight` (CALL/PUT), `OptionStyle` (AMERICAN), `Instrument`, `OptionQuote`, `Greeks`, `OptionContract`, `OptionContractSnapshot`, `FormatOCCSymbol()` | Complete |
| `entity.go` | `OrderIntent.Instrument *Instrument`, `OrderIntent.MaxLossUSD float64`, `NewOptionOrderIntent()` constructor | Complete |

### Port Layer (`internal/ports/`)

| File | Contents | Status |
|------|----------|--------|
| `options_market_data.go` | `OptionsMarketDataPort` interface with `GetOptionChain(ctx, underlying, expiry, right)` | Complete |

### Adapter Layer (`internal/adapters/`)

| File | Contents | Status |
|------|----------|--------|
| `alpaca/options_rest.go` | `GetOptionChain()` — fetches snapshots from Alpaca REST API, maps to `domain.OptionContractSnapshot` | Complete (178 lines) |
| `alpaca/options_order.go` | `SubmitOptionOrder()` — submits option orders, maps direction to buy/sell side | Complete (66 lines) |
| `alpaca/adapter.go:206` | `SubmitOrder()` dispatches to `SubmitOptionOrder()` when `intent.Instrument.Type == OPTION` | Complete |

### Application Layer (`internal/app/`)

| File | Contents | Status |
|------|----------|--------|
| `options/contract_selection.go` | `ContractSelectionService` — filters chain by DTE, delta, OI, spread, IV + regime-aware constraint overrides | Complete (122 lines) |
| `options/config.go` | `OptionsConfig` struct (Enabled, Defaults, RegimeOverrides), `ContractSelectionConstraints`, `ToRegimeConstraintsMap()` | Complete but in wrong package |
| `execution/options_risk.go` | `OptionsRiskEngine` — 4 validation methods: `ValidateOptionIntent`, `ValidateOptionLiquidity`, `ValidateOptionVolatility`, `ValidateOptionExpiry` | Complete but **orphaned** (not wired) |

### Strategy Config

| File | Contents | Status |
|------|----------|--------|
| `configs/strategies/orb_break_retest.toml` | `[options]` block with `enabled=false`, `[options.defaults]`, `[options.regime_overrides.BALANCE]` | Complete (config exists, not parsed) |

---

## The 7 Gaps

### Gap 1: `[options]` TOML Not Parsed in `spec_loader.go`

**Problem**: `loadV2()` in `spec_loader.go` (line 176) does not include an `Options` field in the raw struct. The `[options]` TOML block is silently dropped by BurntSushi/toml — no error, no warning.

**Fix**:
1. Add `Options *rawOptionsSection` to the raw struct in `loadV2()`
2. Define `rawOptionsSection` matching the TOML structure (enabled, defaults, regime_overrides)
3. Parse into `domain.OptionsConfig` (see Gap 2) and set on `Spec.Options`

**Files**: `backend/internal/app/strategy/spec_loader.go`

---

### Gap 2: `Options` Field Missing from `Spec` Struct + Circular Dependency

**Problem**: `portstrategy.Spec` (in `ports/strategy/store.go`, line 41) has no `Options` field. And `OptionsConfig` lives in `app/options/config.go` — the ports layer can't import the app layer.

**Fix**:
1. Create `domain/options_config.go` with `OptionsConfig`, `ContractSelectionConstraints` (move from `app/options/config.go`)
2. Add `Options *domain.OptionsConfig` to `portstrategy.Spec`
3. Update `app/options/config.go` to re-export or alias from domain (or remove and use domain directly)
4. Update all import sites

**Files**: 
- `backend/internal/domain/options_config.go` (new)
- `backend/internal/ports/strategy/store.go`
- `backend/internal/app/options/config.go` (refactor)
- `backend/internal/app/options/contract_selection.go` (update imports)

---

### Gap 3: No Options Branch in `RiskSizer.handleSignal()`

**Problem**: `RiskSizer.handleSignal()` (in `strategy/risk_sizer.go`, line 242) always creates an equity `OrderIntent` with `domain.NewOrderIntent()`. When a strategy has `options.enabled = true`, it should instead:
1. Determine option right: LONG signal → buy calls, SHORT signal → buy puts (buy-to-open only)
2. Call `ContractSelectionService.SelectBestContract()` to pick the option contract
3. Call `OptionsMarketDataPort.GetOptionChain()` to get live chain data
4. Create option `OrderIntent` with `domain.NewOptionOrderIntent()`

**Fix**:
1. `RiskSizer` needs access to `OptionsMarketDataPort` and `ContractSelectionService`
2. Add an options branch in `handleSignal()`: if strategy spec has `Options != nil && Options.Enabled`, take the options path
3. Map signal direction to option right: `DirectionLong → OptionRightCall`, `DirectionShort → OptionRightPut`
4. Compute `MaxLossUSD` = premium × multiplier × quantity (max loss for buy-to-open = premium paid)

**Files**: `backend/internal/app/strategy/risk_sizer.go`

**Dependencies**: Gaps 1, 2 must be done first (Spec.Options must be populated)

---

### Gap 4: `OptionsRiskEngine` Not Wired into Execution

**Problem**: `OptionsRiskEngine` in `execution/options_risk.go` has 4 validation methods but is never instantiated or called. The `execution.Service` only runs equity risk checks.

**Fix**:
1. Add `optionsRisk *OptionsRiskEngine` field to `execution.Service`
2. Add `WithOptionsRiskEngine()` option to service constructor
3. In the `handleOrderIntent()` method, check `intent.Instrument.Type`:
   - If `OPTION`: run `ValidateOptionIntent()`, `ValidateOptionLiquidity()`, `ValidateOptionVolatility()`, `ValidateOptionExpiry()` (in that order)
   - If `EQUITY`/`CRYPTO`: run existing equity risk checks
4. Wire in `bootstrap/execution.go`

**Files**:
- `backend/internal/app/execution/service.go`
- `backend/internal/app/bootstrap/execution.go`
- `backend/cmd/omo-core/services.go`

---

### Gap 5: Guard Chain Not Options-Aware

**Problem**: The execution guard chain (`SlippageGuard`, `ExposureGuard`, `BuyingPowerGuard`, `SpreadGuard`) assumes equity pricing:
- `SlippageGuard` compares limit price vs current market price — options have different pricing semantics
- `ExposureGuard` sums `price × qty` — options exposure = `premium × multiplier × qty`
- `BuyingPowerGuard` checks equity buying power — option buying power is premium-based for buy-to-open

**Fix**:
1. `ExposureGuard`: for options, exposure = `intent.MaxLossUSD` (already set in Gap 3)
2. `SlippageGuard`: skip for options (limit orders use mid-price from chain snapshot)
3. `BuyingPowerGuard`: for buy-to-open, required buying power = premium × 100 × qty
4. `SpreadGuard`: skip for options (bid-ask spread already validated by `OptionsRiskEngine.ValidateOptionLiquidity`)

**Files**:
- `backend/internal/app/execution/exposure_guard.go`
- `backend/internal/app/execution/slippage.go`
- `backend/internal/app/execution/buying_power_guard.go`
- `backend/internal/app/execution/spread_guard.go`

---

### Gap 6: DB Schema Missing Options Columns

**Problem**: The `trades` and `orders` tables have no columns for options-specific data (contract symbol, strike, expiry, right, underlying, premium, greeks at entry).

**Fix**: Create migration `011_add_options_columns`:

```sql
-- trades table
ALTER TABLE trades
  ADD COLUMN IF NOT EXISTS instrument_type TEXT DEFAULT 'EQUITY',
  ADD COLUMN IF NOT EXISTS option_symbol TEXT,
  ADD COLUMN IF NOT EXISTS underlying TEXT,
  ADD COLUMN IF NOT EXISTS strike DOUBLE PRECISION,
  ADD COLUMN IF NOT EXISTS expiry TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS option_right TEXT,
  ADD COLUMN IF NOT EXISTS premium DOUBLE PRECISION,
  ADD COLUMN IF NOT EXISTS delta_at_entry DOUBLE PRECISION,
  ADD COLUMN IF NOT EXISTS iv_at_entry DOUBLE PRECISION;

-- orders table
ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS instrument_type TEXT DEFAULT 'EQUITY',
  ADD COLUMN IF NOT EXISTS option_symbol TEXT,
  ADD COLUMN IF NOT EXISTS underlying TEXT,
  ADD COLUMN IF NOT EXISTS strike DOUBLE PRECISION,
  ADD COLUMN IF NOT EXISTS expiry TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS option_right TEXT;
```

Update `timescaledb.Repository` to populate these columns when persisting option trades/orders.

**Files**:
- `migrations/011_add_options_columns.up.sql` (new)
- `migrations/011_add_options_columns.down.sql` (new)
- `backend/internal/adapters/timescaledb/repository.go` (update INSERT queries)

---

### Gap 7: Position Monitor Has No Options Exit Rules

**Problem**: `PositionMonitor.EvalExitRules()` only evaluates equity exit rules (trailing stop, profit target, EOD flatten, etc.). Options need:
- **DTE_FLOOR**: Force exit when days-to-expiry drops below threshold (e.g., 7 days) — avoid gamma risk near expiry
- **EXPIRY_WATCH**: Alert/exit at configurable DTE milestones (e.g., 50% of original DTE elapsed)

**Fix**:
1. Add `DTE_FLOOR` and `EXPIRY_WATCH` to `domain.ExitRuleType`
2. Implement option exit evaluation in `PositionMonitor`:
   - Look up contract expiry from the position's option metadata
   - Call `OptionsMarketDataPort.GetOptionChain()` to get current greeks
   - Trigger exit if DTE < floor
3. Wire `OptionsMarketDataPort` into `PositionMonitor` deps

**Files**:
- `backend/internal/domain/exit_rules.go` (add new types)
- `backend/internal/app/positionmonitor/service.go` (add option exit logic)
- `backend/internal/app/bootstrap/posmon.go` (wire deps)

---

## Implementation Order

```
Step 1: Move OptionsConfig to domain     ─── Gap 2 (foundation)
    │
Step 2: Parse [options] in spec_loader   ─── Gap 1 (depends on Step 1)
    │
Step 3: Options branch in RiskSizer      ─── Gap 3 (depends on Steps 1-2)
    │
    ├── Step 4: Wire OptionsRiskEngine   ─── Gap 4 (depends on Step 3)
    │
    ├── Step 5: Options-aware guards     ─── Gap 5 (depends on Step 3)
    │
    ├── Step 6: DB migration             ─── Gap 6 (independent, can run anytime)
    │
    └── Step 7: Options exit rules       ─── Gap 7 (depends on Steps 1-2)
```

Steps 4, 5, 6, 7 can be parallelized after Step 3 is complete.

---

## Oracle Review Questions (Submitted)

The following architectural questions were submitted for Oracle review. Answers should be incorporated before starting implementation:

### Q1: Package Restructuring — OptionsConfig to Domain

Should `OptionsConfig` and `ContractSelectionConstraints` move to `domain/` or to `ports/strategy/`? Moving to domain means the domain package grows; moving to ports means the Spec struct can reference it directly but it's technically a config concept, not a domain entity.

**Recommendation**: Move to `domain/` — these are value objects describing option contract constraints. They have no external dependencies. The domain package is the right home.

### Q2: Regime Detection in RiskSizer

`RiskSizer.handleSignal()` currently doesn't know the market regime. `ContractSelectionService.SelectBestContract()` requires a `domain.RegimeType`. How should regime flow into the options path?

**Options**:
- (a) Enrich `domain.Signal` with regime before it reaches RiskSizer (already done for AI debate?)
- (b) RiskSizer queries regime detector directly
- (c) Pass regime through the event payload

### Q3: Bearish Signal Mapping

With buy-to-open only constraint: SHORT signal → buy puts. But the `SubmitOptionOrder()` adapter maps `DirectionShort → side="sell"`. This needs adjustment: for buy-to-open puts, the Alpaca side should still be `"buy"` with the right set to PUT.

**This is a critical correctness issue** — the adapter direction mapping must be option-right-aware, not just direction-based.

### Q4: Guard Chain Sequencing for Options

Should options skip the equity guards entirely, or should each guard have an `isOption()` early-return? The cleaner pattern may be separate guard chains per instrument type.

### Q5: REST Polling Endpoint

For live option price monitoring (position monitor), should we use `GetOptionChain()` (returns full chain) or add a new `GetOptionSnapshot()` (returns single contract)? Full chain is wasteful for monitoring one held contract.

**Recommendation**: Add `GetOptionSnapshot(ctx, contractSymbol)` to `OptionsMarketDataPort` — more efficient for position monitoring.

### Q6: Biggest Risk Assessment

What is the single biggest risk in this implementation plan? What could go wrong in production that isn't covered by the 7 gaps?

---

## Constraints

- **Buy-to-open only** — no naked short risk, no sell-to-open
- **REST polling only** — no OPRA WebSocket needed for 35-45 DTE contracts
- **Single-leg only** — multi-leg strategies deferred to future plan
- **omo-core is running live** — changes to shared code (domain, ports, execution) must not break equity trading
- **Hexagonal architecture** — domain has zero external deps; all I/O behind ports

---

## Testing Strategy

### Unit Tests
- `spec_loader_test.go`: add test for TOML with `[options]` block → verify `Spec.Options` populated
- `risk_sizer_test.go`: test options branch creates correct `OrderIntent` with `Instrument`, `MaxLossUSD`
- `options_risk_test.go`: already has 4 validation methods — ensure they're called from execution service
- `exposure_guard_test.go`: add option intent case
- Domain: test `OptionsConfig` moved to domain compiles, `ContractSelectionConstraints` accessible

### Integration Tests
- End-to-end: signal → RiskSizer (options path) → execution → Alpaca adapter → `SubmitOptionOrder()`
- Guard chain: option intent passes through modified guards without false rejection
- Position monitor: DTE_FLOOR triggers exit at configured threshold

### Manual Verification
- Paper trade: load `orb_break_retest.toml` with `options.enabled = true`
- Verify option order appears in Alpaca paper positions
- Verify position monitor tracks DTE and triggers exit
