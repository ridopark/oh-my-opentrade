# Sprint 4 Implementation Plan: Risk Management Gates

> Date: 2026-04-11
> Goal: Add four risk management capabilities that no other robustness sprint covers: portfolio heat, sector/industry exposure limits, directional bias cap, and a 3-state kill switch. All four close specific classes of outsized loss.
> Estimated effort: 3–4 days of focused work
> Branch: `feat/robustness-sprint-4` (fresh branch off main post-Sprint-3.5)

---

## Why these four, in this order

Each item came out of `COMPARISON.md` Tier 3 and addresses a class of failure the existing 7-gate `ExecutionGateChain` cannot catch today.

| # | Capability | What it prevents | Source |
|---|-----------|------------------|--------|
| 1 | Portfolio heat metric | Death by 1000 cuts — small individual trades accumulating to outsized aggregate risk | IBKR-trader |
| 2 | Sector/industry exposure limits | Correlated blowups — 6 tech positions during a rally, then a single tech crash wipes all 6 | IBKR-trader |
| 3 | Directional bias cap | Accidentally going 100% long or short — unhedged directional exposure | IBKR-trader |
| 4 | 3-state kill switch (ACTIVE/HALTED/REDUCING) | Binary halt is too blunt — we want "close positions but block new entries" for graceful degradation | NautilusTrader |

**Execution order**: 1 → 2 → 3 → 4. Each of the first three adds a new gate to `ExecutionGateChain` following the same pattern. The 4th is a state-machine change that's best done after the gates are in place so it can drive them.

Commit each item independently: four commits, each shippable on its own.

---

## Existing gate architecture (confirmed)

From `backend/internal/app/gate/registry.go`:

```go
type ExecutionGateDeps struct {
    ExposureGuard      ExposureChecker
    PortfolioGuard     PortfolioChecker
    RiskEngine         RiskValidator
    OptionsRiskEngine  OptionsRiskValidator
    SlippageGuard      SlippageChecker
    TradingWindowGuard TradingWindowChecker
    SpreadGuard        SpreadChecker
    BuyingPowerGuard   BuyingPowerChecker
}

type ExecutionGateFactory func(params map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error)
```

Every execution gate follows this pattern (from `exec_portfolio.go`):

```go
type portfolioGate struct {
    checker PortfolioChecker
}

func newPortfolioGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
    return &portfolioGate{checker: deps.PortfolioGuard}, nil
}

func (g *portfolioGate) Name() string { return "portfolio_guard" }

func (g *portfolioGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
    if gctx.Intent.Direction.IsExit() {
        return nil  // exits always bypass
    }
    if g.checker == nil {
        return nil  // nil guard = disabled
    }
    if err := g.checker.Check(ctx, gctx.Intent); err != nil {
        return &GateResult{GateName: "portfolio_guard", Reason: err.Error()}
    }
    return nil
}
```

Registration in `NewDefaultExecutionRegistry()`:

```go
r.Register("short_direction", newShortDirectionGate)
r.Register("exposure_guard", newExposureGate)
r.Register("portfolio_guard", newPortfolioGate)
r.Register("risk_engine", newRiskGate)
r.Register("slippage_guard", newSlippageGate)
r.Register("trading_window", newTradingWindowGate)
r.Register("spread_guard", newSpreadGate)
r.Register("buying_power_guard", newBuyingPowerGate)
```

**Sprint 4 adds three new lines** to this registry + one state machine change.

---

## Phase 1: Portfolio Heat Metric

### Problem
No aggregate view of total risk across open positions. Each trade individually passes the per-trade risk check, but 5 trades at 2% risk each = 10% aggregate, which could be 3× the account's drawdown tolerance. When the correlated news hits, all 5 positions move together and cumulative drawdown is brutal.

### Concept
**Portfolio heat** = sum of risk-per-trade across all open positions, as a percentage of NLV. Capped at a configured threshold (default 10%).

```
heat = Σ (position.risk_usd for each open position) / account.nlv
```

Where `position.risk_usd` is the dollar amount between entry price and stop-loss for a given position. For options: the max loss (debit paid for long options, undefined for short — Sprint 4 punts on short options for now).

### Target files

**New file**: `backend/internal/app/gate/exec_portfolio_heat.go`

```go
package gate

import (
    "context"
    "fmt"

    "github.com/oh-my-opentrade/backend/internal/domain"
)

type portfolioHeatGate struct {
    checker PortfolioHeatChecker
}

func newPortfolioHeatGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
    return &portfolioHeatGate{checker: deps.PortfolioHeatGuard}, nil
}

func (g *portfolioHeatGate) Name() string { return "portfolio_heat_guard" }

func (g *portfolioHeatGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
    if gctx.Intent.Direction.IsExit() {
        return nil
    }
    if g.checker == nil {
        return nil
    }
    if err := g.checker.Check(ctx, gctx.Intent); err != nil {
        return &GateResult{GateName: "portfolio_heat_guard", Reason: err.Error()}
    }
    return nil
}
```

**Modify**: `backend/internal/app/gate/registry.go`

Add to `ExecutionGateDeps`:
```go
PortfolioHeatGuard PortfolioHeatChecker
```

Add interface:
```go
type PortfolioHeatChecker interface {
    Check(ctx context.Context, intent domain.OrderIntent) error
}
```

Add to `NewDefaultExecutionRegistry`:
```go
r.Register("portfolio_heat_guard", newPortfolioHeatGate)
```

**New file**: `backend/internal/app/risk/portfolio_heat.go`

```go
package risk

// PortfolioHeat tracks aggregate risk across all open positions and
// refuses new entries that would push total heat above the configured
// maximum.
//
// Rationale: the per-trade risk check already caps each individual
// position at ~2% of NLV, but nothing today prevents ten such positions
// from accumulating to 20% aggregate. A correlated event (sector news,
// macro shock, flash crash) moves them all together and the aggregate
// drawdown is what actually wipes the account.
//
// Heat = Σ (entry_price - stop_loss) * qty for each open position, as
// a fraction of account NLV. Capped at MaxHeatPct (default 10%).
type PortfolioHeat struct {
    maxHeatPct  float64
    posSource   PositionSource
    equitySource EquitySource
    log         zerolog.Logger
}

type PositionSource interface {
    OpenPositions() []domain.MonitoredPosition
}

type EquitySource interface {
    AccountEquity() float64
}

func NewPortfolioHeat(maxHeatPct float64, posSource PositionSource, equitySource EquitySource, log zerolog.Logger) *PortfolioHeat {
    return &PortfolioHeat{
        maxHeatPct:   maxHeatPct,
        posSource:    posSource,
        equitySource: equitySource,
        log:          log,
    }
}

// Check implements gate.PortfolioHeatChecker.
func (p *PortfolioHeat) Check(ctx context.Context, intent domain.OrderIntent) error {
    equity := p.equitySource.AccountEquity()
    if equity <= 0 {
        return fmt.Errorf("portfolio_heat: invalid equity %.2f", equity)
    }

    currentHeat := p.currentHeat()
    newRisk := intentRisk(intent)
    projectedHeat := (currentHeat + newRisk) / equity

    if projectedHeat > p.maxHeatPct {
        return fmt.Errorf(
            "portfolio_heat: %.2f%% projected exceeds %.2f%% max (current=%.2f, new=%.2f, equity=%.2f)",
            projectedHeat*100, p.maxHeatPct*100, currentHeat, newRisk, equity,
        )
    }
    return nil
}

func (p *PortfolioHeat) currentHeat() float64 {
    var total float64
    for _, pos := range p.posSource.OpenPositions() {
        total += positionRisk(pos)
    }
    return total
}

func positionRisk(pos domain.MonitoredPosition) float64 {
    if pos.StopLoss <= 0 {
        return 0 // no stop set — can't compute heat contribution
    }
    return math.Abs(pos.EntryPrice-pos.StopLoss) * pos.Quantity
}

func intentRisk(intent domain.OrderIntent) float64 {
    if intent.StopLoss <= 0 || intent.LimitPrice <= 0 {
        return 0
    }
    return math.Abs(intent.LimitPrice-intent.StopLoss) * intent.Quantity
}
```

**Wire it**: `backend/internal/app/bootstrap/execution.go`

```go
portfolioHeat := risk.NewPortfolioHeat(
    cfg.Trading.MaxPortfolioHeat, // new config field, default 0.10
    posMonBundle.Service,
    accountEquityProvider,
    log,
)
execDeps.PortfolioHeatGuard = portfolioHeat
```

**New config field** in `backend/internal/config/config.go`:
```go
type TradingConfig struct {
    // ... existing fields
    MaxPortfolioHeat float64 `yaml:"max_portfolio_heat"` // e.g., 0.10 for 10%
}
```

Default if unset: 0 = disabled (matches the pattern used by existing gates). A config value of 0.10 activates the gate.

### Tests

`backend/internal/app/risk/portfolio_heat_test.go`:
- Empty portfolio + new intent → allowed
- Existing positions but new intent would keep total heat < 10% → allowed
- New intent pushes heat > 10% → rejected with the exact heat percentages in the error
- Intent has no stop loss → intent risk = 0, should pass (edge case — `risk_sizer` normally sets the stop, but defensive)
- Existing position has no stop loss → contributes 0 to heat, not an error
- Zero or negative equity → returns error (don't divide by zero)

`backend/internal/app/gate/exec_portfolio_heat_test.go`:
- Nil checker → gate returns nil (disabled)
- Exit intent → gate returns nil (exits always bypass)
- Passing checker → gate returns nil
- Failing checker → gate returns `GateResult{GateName: "portfolio_heat_guard", Reason: <err>}`

### Commit message template

```
feat(risk): portfolio heat metric — aggregate risk cap across open positions

Today the per-trade risk check caps each position at ~2% of NLV but
nothing prevents ten such positions from accumulating to 20% aggregate
portfolio risk. A correlated event (sector rotation, macro shock, flash
crash) moves them all together and the aggregate drawdown is what
actually wipes the account. The per-trade gate is the wrong place to
catch this — it needs a portfolio-level view.

PortfolioHeat sums (entry - stop) * qty for every open position plus
the proposed new intent, divides by current NLV, and refuses the
intent when the projected aggregate exceeds cfg.Trading.MaxPortfolioHeat
(default 0, disabled; recommended 0.10). The check is wired as a new
execution gate (portfolio_heat_guard) that every non-exit intent
traverses before broker submission.

Source: IBKR-trader risk architecture — one of the four risk-layer
items from COMPARISON.md Tier 3. Sprint 4 item 1 of 4.
```

---

## Phase 2: Sector/Industry Exposure Limits

### Problem
Portfolio heat catches total risk, but not concentration. 6 positions in semiconductors during an AI rally all individually pass both per-trade and heat checks, but collectively represent 80% sector exposure. When the sector rolls over — as it always does — those 6 positions move together and the concentrated hit is far worse than the sum of independent hits.

### Concept
Cap the **notional exposure** (position value as a fraction of NLV) within any single sector (max 30%) and any single industry (max 20%).

```
sector_exposure[S] = Σ (pos.notional for pos in open_positions if pos.sector == S) / account.nlv
```

### Symbol → sector/industry mapping

**This is the hard part.** We need a reliable mapping from ticker symbol to sector and industry. Options:

1. **Alpaca REST API** — returns sector/industry in the asset metadata endpoint. Free, covered by existing Alpaca subscription.
2. **DoltHub / Financial Modeling Prep** — cached historical snapshots, loaded once at startup into memory.
3. **Hardcoded TOML mapping** — `configs/symbol_metadata.toml` with entries for the 34 active symbols. Fastest, simplest, but requires manual updates when the universe changes.

**Recommendation**: start with **option 3** (hardcoded TOML). It's 34 entries, takes 10 minutes, and we can replace it with a proper lookup later. The TOML file lives in `configs/symbol_metadata.toml` and loads at startup into a `map[string]SymbolMeta`.

**New file**: `configs/symbol_metadata.toml`

```toml
# Symbol sector/industry metadata for Sprint 4 exposure limits.
# Classification follows GICS (Global Industry Classification Standard).
# Update when the trading universe changes.

[AAPL]
sector = "Technology"
industry = "Consumer Electronics"

[MSFT]
sector = "Technology"
industry = "Software"

[NVDA]
sector = "Technology"
industry = "Semiconductors"

[AMD]
sector = "Technology"
industry = "Semiconductors"

# ... etc for all 34 symbols
```

**New file**: `backend/internal/config/symbol_metadata.go`

```go
package config

type SymbolMeta struct {
    Sector   string `toml:"sector"`
    Industry string `toml:"industry"`
}

type SymbolMetadata map[string]SymbolMeta

func LoadSymbolMetadata(path string) (SymbolMetadata, error) {
    var m SymbolMetadata
    if _, err := toml.DecodeFile(path, &m); err != nil {
        return nil, fmt.Errorf("load symbol metadata: %w", err)
    }
    return m, nil
}
```

### Target files

**New file**: `backend/internal/app/gate/exec_sector_exposure.go` — same boilerplate pattern as `exec_portfolio_heat.go`

**Modify**: `backend/internal/app/gate/registry.go`

```go
type ExecutionGateDeps struct {
    // ...
    SectorExposureGuard SectorExposureChecker
}

type SectorExposureChecker interface {
    Check(ctx context.Context, intent domain.OrderIntent) error
}
```

Register: `r.Register("sector_exposure_guard", newSectorExposureGate)`

**New file**: `backend/internal/app/risk/sector_exposure.go`

```go
package risk

type SectorExposure struct {
    maxSectorPct    float64 // e.g., 0.30
    maxIndustryPct  float64 // e.g., 0.20
    metadata        config.SymbolMetadata
    posSource       PositionSource
    equitySource    EquitySource
    log             zerolog.Logger
}

func (s *SectorExposure) Check(ctx context.Context, intent domain.OrderIntent) error {
    meta, ok := s.metadata[string(intent.Symbol)]
    if !ok {
        // Symbol not in metadata table — fail open (log warning, allow).
        // Alternative: fail closed (reject). Start with open for
        // backward compatibility; tighten later if operators want.
        s.log.Warn().Str("symbol", string(intent.Symbol)).Msg("sector_exposure: symbol not in metadata table, allowing")
        return nil
    }

    equity := s.equitySource.AccountEquity()
    if equity <= 0 {
        return fmt.Errorf("sector_exposure: invalid equity %.2f", equity)
    }

    sectorNotional := intent.LimitPrice * intent.Quantity // new position
    industryNotional := sectorNotional

    for _, pos := range s.posSource.OpenPositions() {
        posMeta, ok := s.metadata[string(pos.Symbol)]
        if !ok {
            continue
        }
        if posMeta.Sector == meta.Sector {
            sectorNotional += pos.Notional()
        }
        if posMeta.Industry == meta.Industry {
            industryNotional += pos.Notional()
        }
    }

    if sectorPct := sectorNotional / equity; sectorPct > s.maxSectorPct {
        return fmt.Errorf(
            "sector_exposure: sector %q projected %.2f%% exceeds %.2f%% max",
            meta.Sector, sectorPct*100, s.maxSectorPct*100,
        )
    }
    if industryPct := industryNotional / equity; industryPct > s.maxIndustryPct {
        return fmt.Errorf(
            "sector_exposure: industry %q projected %.2f%% exceeds %.2f%% max",
            meta.Industry, industryPct*100, s.maxIndustryPct*100,
        )
    }
    return nil
}
```

**Domain addition**: `domain.MonitoredPosition` needs a `Notional() float64` method if it doesn't already exist — returns `CurrentPrice * Quantity` (absolute value for shorts).

**Config additions**:
```go
type TradingConfig struct {
    // ...
    MaxSectorExposure   float64 `yaml:"max_sector_exposure"`   // 0.30
    MaxIndustryExposure float64 `yaml:"max_industry_exposure"` // 0.20
    SymbolMetadataPath  string  `yaml:"symbol_metadata_path"`  // configs/symbol_metadata.toml
}
```

### Tests

- Empty portfolio + AAPL intent → allowed (new sector, under limit)
- 1 AAPL position + new MSFT intent → allowed (both Technology/Software/Consumer Electronics, under 30% sector + 20% industry)
- 5 semiconductor positions + new NVDA intent → rejected with exact industry percentage in error
- Symbol not in metadata → warning logged, intent allowed (fail-open)
- Exit intent → gate returns nil regardless

### Commit message template

```
feat(risk): sector and industry exposure caps

Portfolio heat catches total risk but not concentration. 6 semiconductor
positions during an AI rally all individually pass the heat check at
~2% each but collectively represent 80% sector exposure — when the
sector rolls over every one of them moves together and the concentrated
drawdown is far worse than the sum of independent drawdowns.

SectorExposure reads a GICS-style sector/industry classification from
configs/symbol_metadata.toml, sums current notional per sector and
per industry, and refuses any new intent that would push either above
cfg.Trading.MaxSectorExposure (default 0.30) or MaxIndustryExposure
(default 0.20). Symbols not in the metadata table fail open with a
warning log — operators can tighten to fail-closed later by removing
the fallback path.

Source: IBKR-trader risk architecture. Sprint 4 item 2 of 4.
```

---

## Phase 3: Directional Bias Cap

### Problem
Nothing limits net directional exposure. We can accidentally (or stubbornly) go 100% long at a market top. The existing exposure gate caps per-symbol concentration but not aggregate direction.

### Concept
Cap **net long** and **net short** exposure separately at 70% of NLV. Net long = Σ long notional - Σ short notional. If positive, cap at +70% NLV. If negative, cap at -70% NLV.

### Target files

Same pattern as Phase 1 and 2:
- `backend/internal/app/gate/exec_directional_bias.go` — boilerplate
- `backend/internal/app/risk/directional_bias.go` — logic
- Interface + registry + config wiring

**Config**:
```go
MaxDirectionalBias float64 `yaml:"max_directional_bias"` // 0.70
```

**Logic**:
```go
func (d *DirectionalBias) Check(ctx context.Context, intent domain.OrderIntent) error {
    equity := d.equitySource.AccountEquity()
    if equity <= 0 {
        return fmt.Errorf("directional_bias: invalid equity")
    }

    var netNotional float64
    for _, pos := range d.posSource.OpenPositions() {
        if pos.IsLong() {
            netNotional += pos.Notional()
        } else {
            netNotional -= pos.Notional()
        }
    }

    // Add the new intent
    delta := intent.LimitPrice * intent.Quantity
    if intent.Direction.IsLong() {
        netNotional += delta
    } else {
        netNotional -= delta
    }

    biasPct := math.Abs(netNotional) / equity
    if biasPct > d.maxBiasPct {
        side := "long"
        if netNotional < 0 {
            side = "short"
        }
        return fmt.Errorf(
            "directional_bias: net %s %.2f%% exceeds %.2f%% max",
            side, biasPct*100, d.maxBiasPct*100,
        )
    }
    return nil
}
```

### Tests

- Empty portfolio → any new intent allowed
- Balanced (50/50) → either direction allowed
- 60% net long + new 20% long intent → rejected at 80% > 70% max
- 60% net long + new 20% short intent → allowed (brings to 40% net long)
- Net short past threshold + any new long → allowed (reduces bias)

### Commit message template

```
feat(risk): cap net directional exposure

Nothing today stops us from accidentally (or stubbornly) going 100%
long at a market top or 100% short into a squeeze. The existing
exposure gate caps per-symbol concentration but is silent on the
aggregate long-short net.

DirectionalBias sums long notional minus short notional across open
positions plus the proposed intent, takes the absolute value, and
refuses when that exceeds cfg.Trading.MaxDirectionalBias * NLV
(default 0.70). Intents that reduce the existing bias always pass
— the gate only blocks intents that push the account further from
neutral.

Source: IBKR-trader risk architecture. Sprint 4 item 3 of 4.
```

---

## Phase 4: 3-State Kill Switch (ACTIVE / HALTED / REDUCING)

### Problem
Today's `DailyLossBreaker.SetGlobalHalt()` is binary — on or off. When it trips, existing positions can't be closed through the normal strategy path because the halt blocks ALL orders, including exit orders. Operators have to disable the halt, which also unblocks new entries. There's no "quiet shutdown" mode.

### Concept
Three states:
- **ACTIVE** — normal operation
- **REDUCING** — exits allowed, new entries blocked
- **HALTED** — everything blocked

Transitions:
- `ACTIVE → REDUCING` on first trigger (daily loss threshold crossed, reconnect exhausted, etc.)
- `REDUCING → HALTED` on second trigger or manual operator command
- `HALTED → ACTIVE` manual operator command only (never automatic)
- `REDUCING → ACTIVE` manual operator command only (never automatic — once we've decided to reduce, stay reduced for the session)

### Target files

**Modify**: `backend/internal/app/risk/circuit_breaker.go`

```go
type KillSwitchState int32

const (
    KillSwitchActive KillSwitchState = iota
    KillSwitchReducing
    KillSwitchHalted
)

func (s KillSwitchState) String() string {
    switch s {
    case KillSwitchActive:
        return "ACTIVE"
    case KillSwitchReducing:
        return "REDUCING"
    case KillSwitchHalted:
        return "HALTED"
    }
    return "UNKNOWN"
}

// Add to DailyLossBreaker:
type DailyLossBreaker struct {
    // ... existing fields
    state atomic.Int32 // KillSwitchState
}

func (d *DailyLossBreaker) State() KillSwitchState {
    return KillSwitchState(d.state.Load())
}

func (d *DailyLossBreaker) SetState(s KillSwitchState, reason string) {
    old := KillSwitchState(d.state.Swap(int32(s)))
    if old != s {
        d.log.Warn().
            Str("old_state", old.String()).
            Str("new_state", s.String()).
            Str("reason", reason).
            Msg("kill switch state transition")
        // Emit a domain event for the notifier to pick up.
        // ...
    }
}
```

**Modify**: the existing `SetGlobalHalt` callback (wired to the kill switch via `infra.ibkrBroker.SetReconnectFatalHalt`) should now call `SetState(KillSwitchHalted, "ibkr reconnect exhausted")` instead of the old binary halt.

**New gate**: `backend/internal/app/gate/exec_kill_switch.go`

```go
func (g *killSwitchGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
    state := g.checker.State()
    switch state {
    case risk.KillSwitchActive:
        return nil // everything allowed
    case risk.KillSwitchReducing:
        if gctx.Intent.Direction.IsExit() {
            return nil // exits allowed
        }
        return &GateResult{
            GateName: "kill_switch",
            Reason:   "kill switch REDUCING: new entries blocked, exits allowed",
        }
    case risk.KillSwitchHalted:
        return &GateResult{
            GateName: "kill_switch",
            Reason:   "kill switch HALTED: all orders blocked",
        }
    }
    return nil
}
```

**Dashboard/HTTP endpoint**: a new admin endpoint to manually transition state:

```
POST /api/v1/admin/kill-switch
Body: {"state": "REDUCING", "reason": "operator requested"}
```

Returns 200 with the new state. Auth required (existing admin middleware). Persists the state change to a `kill_switch_events` table for audit.

### Tests

- ACTIVE state: new long intent → allowed
- REDUCING state: new long intent → rejected with "new entries blocked"
- REDUCING state: exit intent → allowed
- HALTED state: any intent → rejected with "all orders blocked"
- State transition is atomic (multiple goroutines calling SetState concurrently see consistent results)
- State transition emits exactly one domain event per transition (dedup check)
- ACTIVE → REDUCING on daily loss trip
- REDUCING → HALTED on second trip (if implemented — might defer to operator action only)

### Commit message template

```
feat(risk): 3-state kill switch (ACTIVE/REDUCING/HALTED)

Today DailyLossBreaker.SetGlobalHalt() is binary — on or off. When it
trips, operators lose the ability to close existing positions through
the normal strategy path because the halt blocks every order including
exits. The only workaround is to disable the halt, which also unblocks
new entries, creating a window where the condition that triggered the
halt in the first place can reassert itself.

Add a middle state REDUCING where exit orders pass through but new
entries are rejected. The circuit breaker (daily loss threshold, IBKR
reconnect exhaustion, operator command) now transitions ACTIVE ->
REDUCING on first trip. A second trip or manual operator command
transitions REDUCING -> HALTED. Both recoveries (-> ACTIVE) require
manual operator intervention via a new POST /api/v1/admin/kill-switch
endpoint — never automatic, because the conditions that trip the
switch don't fix themselves.

The kill switch check is wired as a new execution gate (kill_switch)
that every intent — entry or exit — traverses before broker submission.
The gate is cheap: one atomic Int32 load per call.

Source: NautilusTrader 3-state kill switch pattern. Sprint 4 item 4
of 4, closing the last risk-management gap from COMPARISON.md Tier 3.
```

---

## Config surface summary

New `configs/config.yaml` section:

```yaml
trading:
  # Sprint 4 risk gates
  max_portfolio_heat: 0.10      # 10% — aggregate risk across open positions
  max_sector_exposure: 0.30     # 30% — per GICS sector
  max_industry_exposure: 0.20   # 20% — per GICS industry
  max_directional_bias: 0.70    # 70% — net long or net short
  symbol_metadata_path: "configs/symbol_metadata.toml"
```

All defaults are 0 (disabled) if unset, so adding this sprint's code without updating config leaves behavior unchanged.

New `configs/symbol_metadata.toml` with 34 entries (one per active symbol), sector + industry per GICS.

---

## Testing strategy

### Unit tests
- Each risk module has its own test file covering normal paths, edge cases (nil position source, zero equity, missing metadata), and exact error message formatting
- Each gate has a boilerplate test confirming nil guard + exit bypass + error propagation

### Integration tests
- End-to-end: construct a real `ExecutionGateChain` with all four new gates wired, feed it synthetic intents + positions, assert the expected gate trips
- The chain test already exists in `exec_gates_test.go` — extend it with the new gates

### Property tests (optional, nice-to-have)
- For portfolio heat: total heat is monotonic in the number of positions (adding a position can never decrease heat)
- For sector exposure: if A and B are in the same sector, max(exposure[A], exposure[B]) <= exposure[A] + exposure[B]
- For directional bias: bias is symmetric around zero (swapping all long and short gives the same absolute value)

### Manual validation
- Deploy to staging with all four gates enabled at conservative thresholds
- Run for 24h under real signals
- Verify no false rejections in normal operation (`SELECT gate_name, COUNT(*) FROM gate_rejections GROUP BY gate_name`)
- Deliberately cross each threshold in a test account (if possible) to verify the gate fires
- Verify kill switch state transitions propagate to the dashboard

---

## Risks and open questions

1. **Sector metadata staleness** — TOML file has to be updated when the universe changes. Easy to forget. Mitigation: emit a startup warning if any active symbol is missing from the metadata table. Aggressive: refuse to start until all symbols have metadata. Start with warning.

2. **Position source accuracy** — `OpenPositions()` must return the CURRENT state, not a stale snapshot. The existing position monitor already maintains this — confirm it's the right source to wire in.

3. **Notional computation for options** — an options position's notional is not `price * qty` the same way equities are. For long options: `price * qty * 100` (per-contract). For short options: infinite / undefined. Skip options for Sprint 4; add option-aware notional in Sprint 5 when we ship combo orders.

4. **Intent notional before sizing** — risk_sizer might have already computed the intent's notional. Check if `intent.Quantity * intent.LimitPrice` matches what risk_sizer uses. If not, use whatever risk_sizer writes into the intent.

5. **Kill switch state persistence** — does state survive restart? If it was HALTED when the process crashed, coming up ACTIVE is wrong. Store state in DB, read on startup. Starts ACTIVE only if no persisted state exists or last persisted state was ACTIVE.

6. **Race between exit bypass and entry check** — an intent marked as exit by the strategy but mislabeled could slip through a HALTED kill switch. Confirm `Direction.IsExit()` is authoritative. Defensive: kill switch gate fires FIRST in the chain, exit detection comes from the intent's direction field which is set by risk_sizer at creation time.

---

## Acceptance criteria

Sprint 4 is done when:

- [ ] All four new gates registered in `NewDefaultExecutionRegistry()`
- [ ] All four wired into `ExecutionGateDeps` with matching interfaces
- [ ] `configs/symbol_metadata.toml` created with all 34 active symbols
- [ ] `configs/config.yaml` updated with the four new threshold fields
- [ ] Unit tests green for each risk module
- [ ] Unit tests green for each gate
- [ ] Integration test confirms chain ordering + gate interaction
- [ ] Kill switch state endpoint returns 200 on valid transitions, 403 on unauth, 400 on invalid state name
- [ ] Kill switch state persists across restart
- [ ] `go build ./...` + `go vet ./...` clean
- [ ] Deployed to staging, ran 24h with all gates on, no false rejections, kill switch endpoint tested once
- [ ] `ROADMAP.md` updated: Sprint 4 status → ✅ shipped

---

## Commit sequence

Four commits total, shippable independently:

1. `feat(risk): portfolio heat metric — aggregate risk cap` (~300 lines)
2. `feat(risk): sector and industry exposure caps` (~400 lines, including symbol metadata loader)
3. `feat(risk): cap net directional exposure` (~200 lines)
4. `feat(risk): 3-state kill switch (ACTIVE/REDUCING/HALTED)` (~500 lines, biggest)

Commits 1-3 can be squashed if you prefer a single "risk gates" commit, but keeping them separate makes the history cleaner and each commit is independently revertible.

---

## What Sprint 4 does NOT add

- **Margin usage cap** (ThetaGang pattern) — deferred until we need it; today's risk per trade is a reasonable proxy
- **Greeks-based position limits** (IBKR-trader pattern) — deferred until Sprint 5 ships options combos, because that's when Greeks start to matter
- **Reputation-weighted gate bypass** — no trading system needs this

---

## References

- [`../../tmp/others/COMPARISON.md`](../../tmp/others/COMPARISON.md) §3 — Risk management comparison table, Tier 3 action items
- [`../../tmp/others/ROBUSTNESS.md`](../../tmp/others/ROBUSTNESS.md) — portfolio heat + sector limits + directional bias gaps
- [`../../tmp/others/ibkr_trader.md`](../../tmp/others/ibkr_trader.md) §5 — the IBKR-trader risk architecture source
- [`../../tmp/others/nautilus_trader.md`](../../tmp/others/nautilus_trader.md) §3 — 3-state kill switch pattern source
- `backend/internal/app/gate/registry.go` — existing gate chain architecture
- `backend/internal/app/gate/exec_portfolio.go` — gate boilerplate template
- `backend/internal/app/risk/circuit_breaker.go` — DailyLossBreaker, to be extended
