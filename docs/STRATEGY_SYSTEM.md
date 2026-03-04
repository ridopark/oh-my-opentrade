# Strategy System (V2)

The `oh-my-opentrade` strategy system is a multi-strategy, multi-instance architecture designed for professional-grade algorithmic trading. It follows hexagonal architecture principles, where strategies are pure decision engines decoupled from infrastructure, broker APIs, and persistence.

## Overview

High-level architecture:

```text
Market Data (Alpaca/WS)
       |
       v
[ Event Bus ] --(MarketBarSanitized)--> [ Strategy Runner ]
                                               |
                                        [ Router ] ----> [ Instance (AAPL) ]
                                               |             |
                                               |      [ Strategy Implementation ]
                                               |             |
[ Risk Sizer ] <--(SignalCreated)--------------+------ [ Signal ]
       |
       v
[ Order Intent ] --(Event)--> [ Execution Service ]
```

### Core Concepts
- **Strategy**: Pure logic implementing the `Strategy` interface. Stateless in implementation, but manages an opaque `State` object.
- **Instance**: A specific deployment of a strategy for a set of symbols and timeframes. Manages its own lifecycle and state.
- **Spec (TOML)**: Declarative configuration for strategy instances, parameters, and risk settings.
- **Blue/Green Swap**: Mechanism for updating strategy logic or parameters without missing bars or losing state.

## Quick Start

The strategy system V2 is currently feature-flagged. To enable it:

1. Set the environment variable: `STRATEGY_V2=true`.
2. Ensure strategy specs are located in `configs/strategies/*.toml`.
3. On startup, `omo-core` will:
   - Load all TOML specs from the configured directory.
   - Register builtin strategies (e.g., `orb_break_retest`).
   - Create and initialize instances for each symbol defined in the specs.
   - Subscribe the `Runner` and `RiskSizer` to the event bus.

## TOML Spec Format

Strategy instances are defined using TOML files with `schema_version = 2`.

### Example: `orb_break_retest.toml`

```toml
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.0.0"
name = "ORB Break & Retest"
description = "Opening Range Breakout — Break & Retest with volume confirmation"
author = "system"
created_at = "2026-03-04T00:00:00Z"

[lifecycle]
state = "LiveActive"
paper_only = false

[routing]
symbols = ["AAPL", "MSFT", "GOOGL", "AMZN", "TSLA", "SOXL", "U", "PLTR", "SPY", "META"]
timeframes = ["1m"]
priority = 100
conflict_policy = "priority_wins"
exclusive_per_symbol = false

[params]
orb_window_minutes = 30
min_rvol = 1.5
min_confidence = 0.65
breakout_confirm_bps = 2
touch_tolerance_bps = 2
hold_confirm_bps = 0
max_retest_bars = 15
allow_missing_bars = 1
max_signals_per_session = 1
stop_bps = 25
limit_offset_bps = 5
risk_per_trade_bps = 10

[regime_filter]
enabled = true
allowed_regimes = ["TREND"]
min_atr_pct = 0.8

[hooks]
signals = { engine = "builtin", name = "orb_v1" }
```

### Section Reference
- `[strategy]`: Immutable metadata identifying the logic.
- `[lifecycle]`: Initial state and environment restrictions.
- `[routing]`: Defines which symbols and timeframes this instance handles.
- `[params]`: Arbitrary key-value pairs passed to the strategy's `Init` and `OnBar` methods.
- `[regime_filter]`: (Optional) Parameters for market regime validation.
- `[hooks]`: Pluggable logic providers (Builtin or Yaegi).

## Strategy Pipeline

1. **Bar Ingestion**: `MarketBarSanitized` event is published to the bus.
2. **Routing**: The `Runner` receives the bar and queries the `Router` for active instances assigned to the bar's symbol.
3. **Execution**: The `Runner` calls `Instance.OnBar()`.
   - The instance injects indicators (RSI, VWAP, etc.) into the state.
   - The strategy implementation logic is executed.
4. **Signal Generation**: If the strategy produces a `Signal` and is not in warmup, the `Runner` publishes a `SignalCreated` event.
5. **Risk Sizing**: The `RiskSizer` consumes the `SignalCreated` event.
   - It calculates `limit_price` and `stop_loss` using `limit_offset_bps` and `stop_bps`.
   - It calculates quantity using `risk_per_trade_bps` and current account equity.
6. **Execution Intent**: An `OrderIntentCreated` event is published for the execution service to handle.

## Lifecycle Management

Strategies transition through a defined state machine:

| State | Meaning |
|-------|---------|
| `Draft` | Initial state. Receives bars (warmup) but signals are suppressed. |
| `BacktestReady` | Validated configuration, ready for historical testing. |
| `PaperActive` | Trading on paper accounts. Signals are actionable. |
| `LiveActive` | Trading on live accounts. Signals are actionable. |
| `Deactivated` | Paused. State is preserved but `OnBar` is not called. |
| `Archived` | Terminal state. Instance is removed from the active router. |

Valid transitions:
- `Draft` → `BacktestReady`
- `BacktestReady` → `PaperActive` \| `Deactivated`
- `PaperActive` → `LiveActive` \| `Deactivated`
- `LiveActive` → `Deactivated`
- `Deactivated` → `PaperActive` \| `Archived`

## Blue/Green Swap

Hot-swapping allows updating a strategy instance without downtime:

1. **RequestSwap**: A new instance is created (Green) while the old one (Blue) continues trading.
2. **State Handoff**: The Green instance is initialized with the current `State` of the Blue instance.
3. **Shadow Warmup**: The Green instance starts in `Draft` state. It receives real-time bars via `WarmupOnBar` to synchronize its internal indicators and logic.
4. **Atomic Swap**: Once the Green instance's warmup period (defined by `WarmupBars()`) is complete for all symbols, the `SwapManager` atomically replaces Blue with Green in the `Router`.
5. **Archival**: The Blue instance is set to `Archived`.

## Risk Sizing

The `RiskSizer` converts strategy intent into executable orders.

**Position Sizing Formula:**
```text
max_risk_usd = (risk_per_trade_bps / 10000) * account_equity
risk_per_share = abs(limit_price - stop_loss)
quantity = floor(max_risk_usd / risk_per_share)
```

**Price Calculation:**
- `ref_price`: The price provided by the strategy in signal tags.
- `limit_price`: `ref_price * (1 + limit_offset_bps/10000)` (for Buy).
- `stop_loss`: `ref_price * (1 - stop_bps/10000)` (for Buy).

## HTTP API Reference (V2)

All V2 endpoints are prefixed with `/strategies/v2/`.

### GET `/strategies/v2/instances`
Returns a list of all managed strategy instances and their current status.

### POST `/strategies/v2/instances/:id/promote`
Transition an instance to a new lifecycle state.
- **Body**: `{"target": "PaperActive"}`

### POST `/strategies/v2/instances/:id/deactivate`
Move an instance to the `Deactivated` state.

### POST `/strategies/v2/instances/:id/archive`
Move an instance to the `Archived` state.

## Creating a New Strategy

1. **Implement Interface**: Define a struct that implements `strategy.Strategy` in `backend/internal/domain/strategy/`.
2. **Define State**: Create a struct implementing `strategy.State` to hold per-symbol internal logic.
3. **Register**: Add the strategy to the `MemRegistry` in `backend/cmd/omo-core/main.go`.
4. **Configure**: Create a TOML spec in `configs/strategies/`.

### Strategy Interface (`contract.go`)

```go
type Strategy interface {
	Meta() Meta
	WarmupBars() int
	Init(ctx Context, symbol string, params map[string]any, prior State) (State, error)
	OnBar(ctx Context, symbol string, bar Bar, st State) (next State, signals []Signal, err error)
	OnEvent(ctx Context, symbol string, evt any, st State) (next State, signals []Signal, err error)
}
```

## Architecture Decisions

- **Hexagonal Boundaries**: Strategies have no knowledge of databases or brokers. They interact with the world via the `Context` interface and the `Signal` type.
- **Signals, Not Orders**: Strategies express intent. Positioning, risk checking, and execution nuances (slippage, market hours) are handled by specialized application services.
- **TOML Specs**: Declarative configuration allows for version-controlled strategy deployments and easy multi-instance management.
- **Blue/Green vs. Rolling**: Atomic swaps at bar boundaries ensure that no bar is processed by two different versions of the same instance simultaneously, preventing double-fills.

## Future: Yaegi Scripting

*Status: Not yet implemented (Planned for Phase H)*

The system is designed to support dynamic strategy loading via the Yaegi Go interpreter. This will allow:
- Hot-loading strategies without recompiling the core binary.
- Multi-tenant sandboxing of strategy execution.
- Runtime timeout and circuit breaking for script execution.
