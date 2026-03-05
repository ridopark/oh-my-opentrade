# AI Debate Integration with AVWAP Strategy

## Status: Research Complete — Awaiting Architecture Decision

## Background

All live trades use the `avwap_v1` strategy, which generates signals directly with generic rationale (`"signal: entry buy strength=0.70"`) without any AI adversarial debate. The debate strategy is a separate pipeline path that is never triggered for AVWAP trades.

**Goal**: Integrate the AI adversarial debate system with AVWAP so that:
1. AVWAP signals get validated/enriched by AI agents (bull/bear/judge) before becoming OrderIntents
2. Discord/Telegram notifications show full AI reasoning for ALL trades
3. The system produces richer rationale instead of generic signal strings

## Current Pipelines

### AVWAP Pipeline (Current)
```
MarketBar
  → Runner dispatches to AVWAP.OnBar()
    → SignalCreated (strength=0.60-0.70)
      → RiskSizer converts Signal → OrderIntent
        → OrderIntentCreated (rationale="signal: entry buy strength=0.70")
          → Execution service validates & submits to broker
```

### Debate Pipeline (Separate, Never Triggered by AVWAP)
```
SetupDetected (from monitor screener, not strategy signal)
  → DebateRequested event
    → AI Advisor called with market regime + indicators
      → DebateCompleted event (bull/bear/judge arguments, dynamic confidence)
        → OrderIntent created with AI rationale
          → Execution service validates & submits to broker
```

### Desired Pipeline
```
MarketBar
  → AVWAP.OnBar() → Signal
    → [NEW] AI Debate validates/enriches signal
      → OrderIntent with rich AI rationale + adjusted confidence
        → Execution service validates & submits to broker
```

## AVWAP Signal Generation Details

**File**: `backend/internal/app/strategy/builtin/avwap_v1.go`

AVWAP generates breakout, bounce, and exit signals:
- **Breakout**: 0.7 strength
- **Bounce**: 0.6 strength
- **Exit**: 0.8 strength

Each signal includes metadata tags:
```go
sig := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideBuy, 0.7,
    map[string]string{
        "ref_price": "150.25",
        "setup":     "avwap_breakout",
        "anchor":    "session_open",
        "avwap":     "150.15",
        "vol_ratio": "1.50",
        "mode":      "breakout",
        "regime_5m": "BALANCE",
    },
)
```

## Signal-to-OrderIntent Conversion (RiskSizer)

**File**: `backend/internal/app/strategy/risk_sizer.go`

The RiskSizer bridges signals to OrderIntents:
```go
rationale := fmt.Sprintf("signal: %s %s strength=%.2f", sig.Type, sig.Side, sig.Strength)
// Example: "signal: entry buy strength=0.70"

intent := domain.NewOrderIntent(
    intentID, event.TenantID, event.EnvMode,
    domain.Symbol(sig.Symbol), direction,
    limitPrice, stopLoss,
    10,            // maxSlippageBPS
    qty,           // position-sized from equity & risk parameters
    strategyName,  // "avwap_v1"
    rationale,     // "signal: entry buy strength=0.70"
    sig.Strength,  // 0.70
    intentID.String(),
)
```

## Strategy Interface

**File**: `backend/internal/domain/strategy/contract.go`

```go
type Strategy interface {
    Meta() Meta
    WarmupBars() int
    Init(ctx Context, symbol string, params map[string]any, prior State) (State, error)
    OnBar(ctx Context, symbol string, bar Bar, st State) (State, []Signal, error)
    OnEvent(ctx Context, symbol string, evt any, st State) (State, []Signal, error)
}

type Signal struct {
    StrategyInstanceID InstanceID
    Symbol             string
    Type               SignalType    // "entry", "exit", "adjust", "flat"
    Side               Side          // "buy", "sell"
    Strength           float64       // [0.0, 1.0]
    Tags               map[string]string
}
```

## OrderIntent Structure

**File**: `backend/internal/domain/entity.go`

```go
type OrderIntent struct {
    ID             uuid.UUID
    TenantID       string
    EnvMode        EnvMode
    Symbol         Symbol
    Direction      Direction     // "long" or "short"
    LimitPrice     float64
    StopLoss       float64
    MaxSlippageBPS int
    Quantity       float64
    Strategy       string        // "avwap_v1" or "debate"
    Rationale      string
    Confidence     float64       // [0.0, 1.0]
    IdempotencyKey string
    Instrument     *Instrument
}
```

## Architecture Options

### Option A: Debate as RiskSizer Enhancement (Recommended)

Insert debate between signal and OrderIntent creation:

1. RiskSizer receives `SignalCreated`
2. **NEW**: Emit `SignalDebateRequested` with signal context
3. **NEW**: Debate service calls AI with AVWAP signal metadata (setup type, vol ratio, regime, etc.)
4. **NEW**: Emit `SignalEnriched` with AI confidence + bull/bear/judge arguments
5. RiskSizer creates OrderIntent with enriched confidence and AI rationale
6. Emit `OrderIntentCreated`

**Pros**: Preserves AVWAP's fast signal detection, adds AI validation layer, adjusts confidence dynamically.
**Cons**: +2-5s latency from LLM call before order placement.

### Option B: Parallel Debate Channel

Run debate in parallel with normal execution:

1. AVWAP → `SignalCreated` → RiskSizer → `OrderIntentCreated` (unchanged, ~100ms)
2. **NEW**: Debate service also listens to `SignalCreated` in parallel
3. **NEW**: AI debate runs concurrently
4. **NEW**: If AI disagrees, emit `OrderIntentAmended` to veto/lower confidence before execution validates

**Pros**: Non-blocking, allows fast execution path while debate validates in parallel.
**Cons**: Highest complexity, race condition management, potential for orders firing before debate finishes.

### Option C: Pre-Execution Debate Gate (Most Conservative)

Add debate as a gate in the execution service:

1. AVWAP → `SignalCreated` → RiskSizer → `OrderIntentCreated` (unchanged)
2. **NEW**: Execution service calls debate before broker submission
3. **NEW**: If AI confidence < threshold, reject OrderIntent

**Pros**: Simplest change, no new event types, maximum safety before broker exposure.
**Cons**: +2-5s blocking latency in execution path, mixes concerns (execution shouldn't know about debate).

## Target Notification Format

```
🤖 AI Debate — LONG SPY (Confidence: 85%)
🟢 Bull: AVWAP reclaim with volume surge above 20-day avg...
🔴 Bear: Approaching resistance at prior day high...
⚖️ Judge: Momentum + volume confirm entry — go long

📤 Order Submitted: LONG SPY @ $677.67 (qty: 4.00)
📊 Strategy: avwap_debate | Confidence: 85%
💡 Rationale: AVWAP reclaim validated by AI debate
```

## Key Files

| File | Role |
|------|------|
| `backend/internal/app/strategy/builtin/avwap_v1.go` | AVWAP signal generation |
| `backend/internal/app/strategy/runner.go` | Dispatches bars to strategy instances |
| `backend/internal/app/strategy/risk_sizer.go` | Converts Signal → OrderIntent |
| `backend/internal/app/debate/service.go` | AI debate pipeline |
| `backend/internal/domain/strategy/contract.go` | Strategy interface definition |
| `backend/internal/domain/entity.go` | OrderIntent domain entity |
| `backend/internal/domain/event.go` | Event type constants |
| `backend/internal/domain/advisory.go` | AdvisoryDecision struct |
| `backend/internal/app/execution/service.go` | Execution validation pipeline |
| `backend/internal/app/notify/service.go` | Notification formatting |

## Next Steps

1. Choose architecture option (A, B, or C) — consult Oracle recommended
2. Design new event types if needed
3. Implement with TDD workflow
4. Test end-to-end with AVWAP signals flowing through debate
5. Deploy and verify enriched notifications appear for AVWAP trades
