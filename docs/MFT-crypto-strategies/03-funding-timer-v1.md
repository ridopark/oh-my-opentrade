# funding_timer_v1 — Perp Funding-Rate Timing with Skew-Regime Gate

**Status:** proposed, the real strategic bet
**Effort:** L (3-4 weeks including Hyperliquid adapter)
**Fit score:** 1/10 today → 7/10 once adapter + funding infra ships
**Honest Sharpe (with factor stack):** 1.3-2.3

## Why this is the strategic bet

Crypto MFT Sharpe lives in perp venues and funding, not in spot reversion. This plan's real deliverable isn't funding_timer_v1 — it's the **Hyperliquid adapter + funding data layer + Deribit skew feed**, all of which unlock:

- basis_carry_v1 (#4)
- crypto_pairs_v1 phase 2 (market-neutral)
- xsec_momo_v1 (perp universe + OI)
- Future: HL market-making sleeve, DEX-CEX arb, option-selling around funding

Treat #3 as "buy the infrastructure by shipping a strategy that pays for it."

## Edge thesis

On perpetual futures, when funding is persistently positive, longs pay shorts at every funding reset. Retail crowd-positioning during bull regimes creates this premium systematically. Short-perp (optionally hedged with spot) captures the carry minus fees and funding-flip risk.

**The key differentiator vs. textbook funding arb:** skip entries during stress regimes. Funding arb during negative-skew (options market pricing crash risk) is the dominant drawdown path — a crash drains funding and pins basis simultaneously. Deribit skew + RR as a regime filter is cheap to add and cuts max drawdown 30-50%.

## Crowding

**Medium-High in bull regimes.** Ethena and institutional basis funds are the apex predators; funding compresses to 3-8% APR when they dominate. **Low in uncrowded sub-states:**
- New-listing perp launches on HL: first 2-4 weeks often 30-50% APR funding
- Narrative-driven short squeezes: funding 20-40% APR for days
- Post-halving / post-ETF euphoria: months of 15-30% APR

Strategy performs by **regime selection**, not continuous participation. Plan trades 30-50% of days only.

## Cadence

- Decision: every 1h (Hyperliquid funding is hourly; Binance/Bybit 8h)
- Holding: 8h-5d (multiple funding windows)
- Symbols: ETH-PERP, BTC-PERP, SOL-PERP (phase 1); add mid-caps (ARB, SUI, TIA) phase 2
- Venue: Hyperliquid primary, Bybit secondary (for funding history backfill pre-HL-adapter)

## Signal

```
f_current     = current funding rate (per hour)
f_mean_48h    = rolling 48h mean funding
sigma_1d      = 1-day realized mark-price vol (pct)

skew_25d      = Deribit 25-delta risk reversal (30d tenor)
iv_term_slope = (IV_30d - IV_7d) / IV_7d
regime        = classify("bull", "neutral", "stress") from skew + slope

whale_oi      = ETF custodian net flow (on-chain signal)
```

**Entry (short perp):**
- `f_mean_48h > min_threshold` (e.g. 0.01%/h ~ 87% APR gross)
- `f_current > 0`
- `sigma_1d < vol_cap`
- `regime != "stress"` (SKEW GATE — the differentiator)
- Optional: `whale_oi > 0` (accumulation aligns with persistent-long crowd)

**Sizing:** `notional = min(risk_budget / sigma_1d, max_gross)`

**Exit:**
- `f_current <= 0` (funding flip — auto-close within same funding window)
- `f_mean_48h < exit_threshold`
- Mark-price move > 2*sigma_1d against position
- `regime` flips to "stress" mid-hold
- Hedge leg unavailable

**Optional spot hedge (delta-neutral):** long BTC/USD on Alpaca to offset short perp notional. Skip for phase 1.

## Factor stack

| Factor | Uplift |
|---|---|
| Base short-perp funding carry | Sharpe 1.0-1.8 naive |
| + Skew regime gate | drawdown -30-50%, Sharpe +0.3-0.5 |
| + Whale on-chain gate | Sharpe +0.1-0.2 |
| + Session / funding-window timing | Sharpe +0.1 |
| **Stacked live** | **1.3-2.3** |

## Data requirements

All **missing** today:
- Funding rate history + stream per venue per symbol
- Perp mark-price 1h bars — needs Hyperliquid adapter
- Deribit options IV surface + 25d RR + term structure — new read-only adapter
- On-chain ETF custodian flows — shared with #2 phase 1

Spot bars for hedge leg: **have** (Alpaca).

## Code changes

**New ports:**
```go
// backend/internal/ports/funding.go
type FundingRate struct {
    Venue, Symbol string
    Timestamp     time.Time
    Rate          float64
    IntervalHours int
    MarkPrice     float64
}
type FundingRatesPort interface {
    Latest(ctx, venue, symbol) (FundingRate, error)
    History(ctx, venue, symbol, from, to) ([]FundingRate, error)
    Stream(ctx, venue, symbol) (<-chan FundingRate, error)
}

// backend/internal/ports/options_iv.go
type OptionsIVPort interface {
    Surface(ctx, asset) (IVSurface, error)
    SkewRR(ctx, asset, tenor string) (float64, error)
    TermSlope(ctx, asset) (float64, error)
}
```

**New adapters:**
- `backend/internal/adapters/hyperliquid/` — rest.go, ws.go, broker.go, funding.go
- `backend/internal/adapters/bybit/` — read-only funding (backfill + pre-HL research)
- `backend/internal/adapters/deribit/` — read-only options chain + IV surface
- `backend/internal/adapters/onchain/custodian_flows.go` — shared with #2

**Storage:**
```sql
-- migrations/NNN_funding_rates.sql
CREATE TABLE funding_rates (
    venue TEXT, symbol TEXT, timestamp TIMESTAMPTZ,
    rate DOUBLE PRECISION, interval_hours INT, mark_price DOUBLE PRECISION,
    PRIMARY KEY (venue, symbol, timestamp)
);
SELECT create_hypertable('funding_rates', 'timestamp');

-- migrations/NNN_iv_snapshots.sql
CREATE TABLE iv_snapshots (
    venue TEXT, asset TEXT, timestamp TIMESTAMPTZ,
    atm_iv_7d DOUBLE PRECISION, atm_iv_30d DOUBLE PRECISION,
    rr_25d_30d DOUBLE PRECISION, bf_25d_30d DOUBLE PRECISION
);
SELECT create_hypertable('iv_snapshots', 'timestamp');
```

**Ingestion:**
- `backend/internal/app/ingestion/funding_backfill.go`
- `backend/internal/app/ingestion/funding_live.go`
- `backend/internal/app/ingestion/iv_snapshot_live.go`

**Simbroker:**
- `backend/internal/adapters/simbroker/funding_event.go` — accrue PnL at each reset
- Dual-venue position tracking (perp + spot hedge)

**Strategy:**
- `backend/internal/app/strategy/builtin/funding_timer_v1.go`
- `backend/internal/domain/strategy/skew_regime.go` — classifier
- `configs/strategies/funding_timer_v1.toml`

## DNA

```toml
schema_version = 2
strategy_id = "funding_timer_v1"
version = "0.1.0"

[routing]
asset_classes = ["CRYPTO_PERP"]
venues = ["hyperliquid"]
timeframes = ["1h"]
symbols = ["ETH-PERP"]

[lifecycle]
paper_only = true

[params]
funding_window_hours          = 48
min_avg_funding_bps_per_hour  = 1.0      # ~87% APR gross
exit_avg_funding_bps_per_hour = 0.2
max_realized_vol_pct          = 120
vol_stop_mult                 = 2.0
max_position_usd              = 10000
delta_hedge                   = false

[params.gates]
require_regime_ok       = true
stress_skew_threshold   = -0.05    # 25d RR more negative than -5% => stress
stress_iv_slope_max     = -0.1     # inverted term structure
require_whale_align     = false    # optional phase 2

[params.hedge]
hedge_symbol  = "ETH/USD"
hedge_venue   = "alpaca"
hedge_ratio   = 1.0
```

## Backtest feasibility

Medium once `FundingEvent` ships (~30 LOC simbroker addition). Fidelity 80%: funding accrual per reset, mark-price slippage, vol-stop logic, skew-regime replay. Missing: exchange outage modeling (rare but catastrophic).

## Expected edge

- In-regime: net 8-25% APR
- All-regime (with skew gate filtering out ~40% of days): 6-18% APR
- Sharpe 1.3-2.3 net over full cycle
- Max drawdown: 3-6% with skew gate (vs 10-15% without)
- Capacity: $100k-$1M on HL majors without moving funding; compete with Ethena above that

## Key failure modes

1. **Regime flip (2022 LUNA/FTX style)** — funding stays positive while price crashes, then flips hard negative exactly when you've accumulated maximum short. **Skew gate catches most of these but not all.** Mitigation: vol-stop, max position cap, auto-close on funding flip.
2. **Exchange outage during hedge** — HL goes down while short perp is open, Alpaca spot still trading, unhedged exposure. Mitigation: watchdog closes all positions if WS stale > 60s.
3. **Ethena compression** — when $5B+ of Ethena capital enters, funding compresses below threshold for months. Strategy sits flat. This is correct behavior; not a failure.

## Risk controls

- Max position USD per symbol
- Vol-stop at 2-sigma adverse mark move
- Funding-flip auto-close (exit on first negative print)
- Skew-regime kill: no new entries during stress
- Exchange-outage watchdog: if WS stale > 60s, close all and alert
- Per-venue net inventory cap
- Paper-only until 4 weeks of green

## Milestones

- **W1:** `FundingRatesPort` + Timescale schema + Bybit funding backfill (REST-only, no orders). Run in-sample research showing Sharpe with and without skew gate.
- **W2:** Hyperliquid REST+WS adapter (read-only). Mark-price 1h bars flowing. Deribit IV ingestion. Simbroker FundingEvent accrual.
- **W3:** Hyperliquid order submission + paper-trade delta-tolerant short-perp-only. Skew-regime classifier live.
- **W4:** Paper-trade with full factor stack. A/B vs vanilla (no gates) to prove factor uplift.

## Success criteria (4-week paper)

- Net return > 2% over paper period (~30% APR)
- No single drawdown > 2% of notional
- Skew gate filters 30-50% of candidate entries with per-trade edge up
- Funding-flip response: auto-close within 1 funding window
- Zero silent failures (all venue errors alerted)

## Reusable artifacts (massive value)

- **Hyperliquid adapter** — unlocks #2 phase 2, #4, #5, future MM and arb
- `FundingRatesPort` + hypertable + backfill — all perp strategies
- Simbroker FundingEvent accrual — all perp backtests
- Dual-venue position tracking — #4 basis, any cross-venue work
- Deribit IV adapter + skew-regime classifier — gate for #4, regime overlay for #1 phase 2
- Bybit read-only adapter — research-phase funding backfill
