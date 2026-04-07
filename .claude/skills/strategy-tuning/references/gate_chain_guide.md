# Gate Chain Reference

Configurable filter pipeline for trade signals and order intents. Each strategy TOML defines its own `[gate_chain]` section with ordered gate lists. Gates run top-to-bottom; first rejection stops the chain.

## TOML Syntax

```toml
[gate_chain]
monitor = [
    { name = "gate_name", params = { key = value } },
]
execution = [
    { name = "gate_name" },
]
```

If `[gate_chain]` is absent, legacy hardcoded behavior runs unchanged.

## Monitor Gates (pre-signal, before AI debate)

| Gate | What it does | Params | Example |
|------|-------------|--------|---------|
| `dna_approval` | Blocks if DNA version not approved | none | `{ name = "dna_approval" }` |
| `vix` | Blocks all setups when VIX exceeds threshold | `skip_above` (float, 0=disabled) | `{ name = "vix", params = { skip_above = 35.0 } }` |
| `regime` | Blocks if market regime not in allowed list | `allowed` (string array) | `{ name = "regime", params = { allowed = ["TREND", "TREND_UP"] } }` |
| `htf_bias` | Blocks longs in BEARISH daily bias, shorts in BULLISH | none | `{ name = "htf_bias" }` |
| `min_atr_pct` | Blocks low-volatility symbols | `min_pct` (float, 0=disabled) | `{ name = "min_atr_pct", params = { min_pct = 0.4 } }` |
| `market_tide` | Blocks longs when ref index < VWAP, shorts when > VWAP | `neutral_band_bps` (int, default 10) | `{ name = "market_tide", params = { neutral_band_bps = 10 } }` |

## Execution Gates (pre-order, before broker submission)

| Gate | What it does | Params |
|------|-------------|--------|
| `short_direction` | Blocks SHORT on non-shortable assets (e.g., crypto) | none |
| `exposure_guard` | Blocks if symbol/portfolio exposure limits exceeded | none |
| `portfolio_guard` | Blocks if portfolio constraints violated | none |
| `risk_engine` | Validates position size vs equity risk (auto-dispatches equity vs options) | none |
| `slippage_guard` | Blocks if limit price deviates too far from last quote | none |
| `trading_window` | Blocks outside allowed trading hours | none |
| `spread_guard` | Blocks if bid-ask spread too wide | none |
| `buying_power_guard` | Blocks if insufficient buying power/margin | none |

## Safety Tier (always runs, NOT configurable)

- **KillSwitch** — halts trading on repeated stop-outs
- **PositionGate** — prevents duplicate/conflicting entries
- **DailyLossBreaker** — halts trading when daily loss limit hit

## Market Tide — Sector Routing

The `market_tide` gate maps symbols to their reference index:

| Sectors | Ref Index |
|---------|-----------|
| TECH, SEMIS, SOFTWARE, FINTECH, CRYPTO_PROXY, LEV_ETF | **QQQ** |
| FINANCIAL, ENERGY, HEALTH, CONSUMER, INDUSTRIAL | **SPY** |
| SPY, QQQ, IWM, DIA | skipped (no self-filtering) |

`neutral_band_bps` = dead zone around VWAP where both directions pass. At `10` bps, if QQQ is within +/-0.10% of VWAP, no filtering is applied. Tracker needs 30 bars to warm up.

## Tuning Gate Chain Parameters

### Tunable via TOML (no code change)

| Parameter | Gate | Range | Step | Impact |
|-----------|------|-------|------|--------|
| `skip_above` | vix | 25-40 | 5 | Lower = skip more volatile sessions |
| `allowed` | regime | subset of TREND/TREND_UP/TREND_DOWN/SQUEEZE/RANGE/NEUTRAL/BALANCE | -- | Drop losing regimes; check quant breakdown first |
| `min_pct` | min_atr_pct | 0.3-0.8 | 0.1 | Higher = only volatile symbols |
| `neutral_band_bps` | market_tide | 5-30 | 5 | Lower = stricter VWAP alignment, more rejections |

### Tuning approach for gates

1. **Get baseline** with current gate chain
2. **Consult quant** for regime/direction/time breakdown
3. **Drop losing regimes** from `regime.allowed` (highest impact, same as AVWAP tuning)
4. **Activate VIX gate** — set `skip_above = 35` (or 30 for conservative)
5. **Tighten market_tide** — try `neutral_band_bps = 5` (stricter) vs `15` (looser)
6. **Add/remove htf_bias** — backtest with and without; counter-trend blocking helps ORB but may hurt mean-reversion
7. **Reorder gates** — put the highest-rejection gate first for efficiency (minor impact)

### Adding/removing gates per strategy

Different strategies need different gates. A mean-reversion strategy would NOT want `htf_bias` or `market_tide`:
```toml
[gate_chain]
monitor = [
    { name = "dna_approval" },
    { name = "vix", params = { skip_above = 35.0 } },
    { name = "regime", params = { allowed = ["BALANCE", "RANGE", "NEUTRAL"] } },
]
```

An aggressive ORB strategy might skip most gates:
```toml
[gate_chain]
monitor = [
    { name = "dna_approval" },
    { name = "vix", params = { skip_above = 40.0 } },
    { name = "market_tide", params = { neutral_band_bps = 20 } },
]
execution = [
    { name = "risk_engine" },
    { name = "buying_power_guard" },
]
```

## Implementation Details

- Source: `internal/app/gate/` package
- Interface: `MonitorGate` and `ExecutionGate` with `Name() string` + `Check() *GateResult`
- Registry: `NewDefaultRegistry()` and `NewDefaultExecutionRegistry()`
- Tracker: `IndexTideTracker` in `internal/app/gate/index_tide_tracker.go`
- Wiring: `bootstrap.WireGateChain()` and `bootstrap.WireExecutionGateChain()`
