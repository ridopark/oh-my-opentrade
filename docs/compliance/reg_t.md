# Reg-T initial margin enforcement

## What it is

Federal Reserve Regulation T requires a 50% initial margin deposit on
marginable-equity purchases (and on short sales — short proceeds plus
50% margin, which nets to the same 50% buying-power consumption).
Options and crypto are governed by separate rules; Sprint 4.5 defers
options margin to a later sprint and skips crypto (non-marginable).

## How we check it

`backend/internal/app/risk/reg_t_check.go` — `RegTCheck.Check` reads
`BuyingPower.EffectiveBuyingPower` via `ports.AccountPort` (IBKR adapter
sources this from the `BuyingPower` tag on the IBKR account summary),
computes `required = limit_price * quantity * 0.50`, and returns an
error if `required > effective_buying_power`.

Skipped cases:

- `intent.Direction.IsExit()` — exits reduce, not add to, margin usage.
- `intent.AssetClass == AssetClassCrypto`.
- `intent.Instrument.Type == InstrumentTypeOption` (deferred to a
  dedicated options-margin gate).
- `intent.LimitPrice <= 0 || intent.Quantity <= 0` — the risk engine
  will reject these separately.

## What the gate blocks

`reg_t_guard` (`backend/internal/app/gate/exec_reg_t.go`) rejects the
intent whenever `RegTCheck` returns an error. A nil checker = disabled
(the gate degrades to pass-through), which is how we keep simbroker
and Alpaca paper runs free of Reg-T overhead.

## Config

```yaml
trading:
  reg_t_enforcement: true  # default false; enable on IBKR paper/live
```
