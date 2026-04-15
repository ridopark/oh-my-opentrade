# crypto_revert_v1 — 5m Mean Reversion with TFI + Inducement Gates

**Status:** proposed, ship first
**Effort:** S (5 days strategy-only); M (10-12 days with full factor stack)
**Fit score:** 6/10
**Honest Sharpe (with factor stack):** 1.0-1.7

## Why this strategy first

The base strategy is commoditized retail fare (every TradingView bot runs VWAP fade). **The reason to ship it first is not the P&L of the vanilla strategy** (which is Sharpe 0.4-0.8 live) — it's the vehicle to:

1. Pressure-test the 5m crypto code path before committing to the HL adapter build
2. Operationalize omo's differentiating factors (TFI, inducement, cross-venue flow) in crypto
3. Produce reusable artifacts (SessionVWAP, TFI, taker-side pipeline) for every future crypto strategy

## Edge thesis

On 5m bars, BTC/ETH spot exhibits transient overshoots versus session VWAP that revert when the trade-flow-imbalance neutralizes. Fade only when multiple confluence gates align:
- Price extended (dev_z > threshold)
- Trade-flow imbalance pulling against the move (TFI gate)
- A liquidity sweep / stop-hunt just occurred (inducement gate)
- Cross-venue taker-flow not aligned with the move (optional, post-HL adapter)

Each gate individually is weak. Stacked they discriminate reversions from trend-continuations materially better than any single signal.

## Crowding

**Very high** for the base pattern (VWAP fade); **low** for the stacked confluence version. The edge is not "nobody does this" — it's "nobody combines microstructure + inducement + cross-venue flow at trade-entry level."

## Cadence

- Decision: every 5m bar close
- Holding: 15-90 minutes
- Symbols: BTC/USD, ETH/USD (phase 1); add SOL/USD if paper proves out
- Venue: Alpaca spot (phase 1); Hyperliquid spot-or-perp (phase 2)

## Signal

```
vwap_t         = session VWAP (UTC-midnight reset)
sigma_vwap_t   = rolling std dev of (price - vwap) over session
dev_z          = (close - vwap_t) / sigma_vwap_t

buy_vol_15m    = sum of taker-buy volume over last 15m
sell_vol_15m   = sum of taker-sell volume over last 15m
tfi            = (buy_vol_15m - sell_vol_15m) / (buy_vol_15m + sell_vol_15m)

inducement_t   = sweep_detected && stops_cleared (port from equities detector)

xv_flow_t      = cross-venue aggregated taker-flow score (phase 2, post-HL)
skew_t         = Deribit ATM skew regime (phase 2, optional gate)
```

**Long entry** (all must be true):
- `dev_z < -2.0`
- `tfi > 0` (taker buying despite price drop = exhaustion signal)
- `inducement_t` fired in last 3 bars (stops cleared below)
- Phase 2: `xv_flow_t > 0` (aggregated cross-venue buying)
- Phase 2: `skew_t != "stress"` (skip crash regimes)

**Short entry** (phase 2 only, perp):
- Mirror conditions

**Exit:**
- `|dev_z| < 0.3` (reversion complete)
- 90m time stop
- Hard stop at `|dev_z| > 3.5`
- Inducement re-fires in opposite direction (regime flip)

## Factor stack (the actual alpha)

| Factor | Phase | Expected Sharpe uplift |
|---|---|---|
| Base VWAP fade | 1 | 0.4-0.8 naive |
| + TFI gate | 1 | +0.2-0.3 |
| + Inducement detector | 1 | +0.3-0.6 |
| + Cross-venue taker flow | 2 | +0.3-0.5 |
| + Skew regime gate | 2 | drawdown -30-50% |
| + Session-time weighting | 1 | +0.1-0.2 |
| **Stacked total** | 2 | **1.0-1.7 live** |

## Data requirements

Phase 1:
- 5m bars for BTC/USD, ETH/USD — **have** (`adapters/alpaca/crypto_rest.go:52-100`)
- Trade ticks with taker side — **needs** (domain `MarketTrade.TakerSide` + Alpaca WS wiring)
- Inducement signal — **needs port from equities** (`backend/internal/app/strategy/.../inducement*` — adapt to crypto tick data)
- Fallback if taker side unreliable: `sign(close-open) * volume` per bar (ships same-day)

Phase 2:
- Cross-venue trades from HL + Coinbase + Binance — needs HL adapter + Binance/CB read-only feeds
- Deribit options skew — new read-only adapter

## Code changes

**Phase 1 (5 days):**
- `backend/internal/domain/entity.go` — add `TakerSide string` to `MarketTrade` (~line 511)
- `backend/internal/adapters/alpaca/crypto_ws.go` — populate taker side from Alpaca trade channel
- `backend/internal/domain/strategy/session_vwap.go` — new
- `backend/internal/domain/strategy/tfi.go` — new
- `backend/internal/domain/strategy/crypto_inducement.go` — port from equities
- `backend/internal/app/strategy/builtin/crypto_revert_v1.go` — new
- `backend/internal/app/strategy/builtin/crypto_revert_v1_test.go` — new
- `backend/internal/app/strategy/spec_loader.go` — register
- `configs/strategies/crypto_revert_v1.toml` — new

**Phase 2 (+5-7 days, gated on HL adapter):**
- `backend/internal/ports/cross_venue_flow.go` — aggregator interface
- `backend/internal/app/strategy/flow/cross_venue_aggregator.go` — sums taker volume across venues in rolling windows
- `backend/internal/adapters/deribit/` — read-only options chain + skew
- `backend/internal/domain/strategy/skew_regime.go` — classifier
- Update `crypto_revert_v1.go` to consume new gates

No new ports needed for phase 1. No adapter work beyond Alpaca WS wiring. No schema migration.

## DNA (schema_version = 2)

```toml
schema_version = 2
strategy_id = "crypto_revert_v1"
version = "0.1.0"

[routing]
asset_classes = ["CRYPTO"]
venues = ["alpaca"]
timeframes = ["5m"]
symbols = ["BTC/USD", "ETH/USD"]

[lifecycle]
paper_only = true

[params]
entry_dev_z        = -2.0
exit_dev_z         = -0.3
hard_stop_dev_z    = -3.5
tfi_lookback_min   = 15
tfi_min            = 0.0
time_stop_min      = 90
session_reset_utc  = 0
max_concurrent     = 2

[params.gates]
require_tfi         = true
require_inducement  = true
inducement_lookback_bars = 3
require_xv_flow     = false   # phase 2 flip
require_skew_ok     = false   # phase 2 flip

[params.fallback]
use_bar_sign_tfi = true   # if taker side missing

[params.session]
# session-time weighting (already in codebase)
weight_us_hours     = 1.0
weight_asia_hours   = 0.7
weight_weekend      = 0.5
```

## Backtest feasibility

**High** for phase 1. Simbroker slippage-bps is honest for 5m spot majors. Two years of BTC/ETH 5m is cheap to replay. Tax +5bps on every reported edge.

**Medium** for phase 2 — cross-venue historical alignment is messy; need synchronized bars from HL + Binance + Coinbase on same timestamp grid. Deribit historical skew available via their public API.

## Expected edge (reality-adjusted)

Phase 1:
- Per-trade net: 8-20 bps after 25bps Alpaca taker fees
- Sharpe: 0.4-0.8 naive → 0.7-1.3 with TFI + inducement
- Drawdown: shallow/frequent; worst-case 3-5% on trending regime

Phase 2 (full stack):
- Sharpe 1.0-1.7
- Drawdown capped at 2-3% via skew-regime gate
- Lower trade frequency (40-60% of days filtered out) but cleaner P&L

Capacity: ~$100k on Alpaca; grows to $500k+ if migrated to HL spot.

## Key failure modes

1. **Trending days** — price runs from VWAP, TFI stays positive all the way up. Mitigation: hard stop at 3.5-sigma, max 2 concurrent, inducement gate refuses entries absent a sweep.
2. **Alpaca fee drag** — 25bps is brutal for 8-20bps edge. Migration to HL spot (~5-10bps fees) is the path to scale.
3. **Inducement false positives** — port from equities may not calibrate to crypto tick volatility. Needs threshold tuning on crypto data.

## Risk controls

- Max 2 concurrent positions
- Hard stop at 3.5-sigma
- 90m time stop
- Session kill-switch via `execution/risk.go`
- `paper_only = true` until 2 weeks of positive paper
- Skew-regime kill (phase 2): no new entries during stress regime

## Milestones

**Phase 1 (Week 1):**
- Day 1: Extend `MarketTrade` + wire Alpaca WS taker-side. Unit tests.
- Days 2-3: `SessionVWAP` + `TFI` + crypto inducement port + strategy + tests.
- Day 4: Backtest on 2 years BTC/ETH 5m; param sweep; honest Sharpe read.
- Day 5: TOML + register + paper-trade kickoff alongside VWTSM.

**Phase 2 (Weeks 3-4, overlaps HL adapter work):**
- Cross-venue taker-flow aggregator (reuses HL + adds Binance/CB read-only)
- Deribit skew adapter + regime classifier
- A/B: phase-1 vanilla vs phase-2 stacked on parallel paper accounts

## Success criteria

**Phase 1 (2-week paper):**
- Net Sharpe > 0.5 after fees
- Max drawdown < 3% of notional
- At least 40 trades
- No single-day loss > 2%

**Phase 2 (2-week paper):**
- Net Sharpe > 1.0
- Drawdown < 2%
- Confluence gates reduce trade count by 30-50% with P&L/trade up 50%+

Fail either phase → do not proceed to next phase.

## Reusable artifacts (pay forward regardless)

- `TFI` indicator — gate for every future crypto strategy
- `SessionVWAP` — used by pairs and basis
- `MarketTrade.TakerSide` + Alpaca WS taker pipeline
- Crypto-adapted inducement detector
- Cross-venue taker-flow aggregator (phase 2) — foundation for HL MM and XEMM
- Deribit skew-regime classifier (phase 2) — gate for #3 and #4
