# xsec_momo_v1 — Cross-Sectional 15m Altcoin Momentum

**Status:** proposed, **research only** — do not ship as production bet
**Effort:** L (6+ days infra + ongoing research)
**Fit score:** 2/10
**Honest Sharpe (with factor stack):** 0.6-1.0

## Edge thesis (contested)

On 15m bars, relative-return ranking on a basket of 10-20 liquid alt perps captures short-horizon momentum continuation driven by narrative-momentum flows. Long top-3 / short bottom-3 dollar-balanced, rebalance each 15m with turnover cap.

## Crowding

**High and toxic.** Alt momentum ranking is the most "backfit-able" strategy in crypto. Every quant fund with a ML pipeline has run this. Walk-forward results look beautiful, live trading halves or negates.

The crowding is nasty because everyone holds the same top-3 and shorts the same bottom-3 — unwinds happen together. Positive P&L during regime continuation; sharp cross-strategy crowded-trade drawdowns when regime flips.

**Remaining edge:** very low. Sharpe 0.4-0.8 live with fat-tail drawdown character.

## Why it's in the plan at all

1. The cross-sectional dispatch infrastructure is **reusable for equities ranking strategies** (a much less crowded space where omo's factor moat matters more).
2. If it ever works, it works in the uncrowded regimes (new listings, narrative euphoria, post-halving dispersion).
3. Good place to deploy aggressive factor stacking to see if confluence can beat generic retail.

## Cadence

- Decision: 15m
- Holding: 2-6 hours (turnover cap limits churn)
- Symbols: 15-symbol alt universe
- Venue: Hyperliquid perps (spot shorting unavailable)

## Signal

Per symbol, recomputed each 15m:
```
f1  = 1h return
f2  = 4h return
f3  = 24h return
f4  = volume z-score (vs 24h mean)
f5  = funding z-score (vs 48h mean)
f6  = OI change pct (1h)
f7  = 5m realized vol
f8  = whale-flow z (on-chain custodian, if symbol tagged)
f9  = cross-venue taker flow z (bonus signal)

score = w1*f1 + w2*f2 + w3*f3 + w4*f4 + w5*f5 + w6*f6 - w_vol*f7 + w_whale*f8 + w_xv*f9
```

Weights from walk-forward. Rank all symbols; long top 3, short bottom 3, dollar-balanced; turnover cap 50% per rebalance.

**Crowded-trade escape:** if portfolio avg pairwise correlation > 0.9 (meaning everyone's crowded into same basket direction), flatten.

## Factor stack

| Factor | Uplift |
|---|---|
| Base momentum ranking | Sharpe 0.4-0.8 live |
| + Funding z-score feature | +0.1 |
| + OI change feature | +0.1 |
| + Whale-flow feature | +0.1-0.2 |
| + Cross-venue taker flow | +0.1-0.2 |
| + Skew-regime gate | drawdown -30% |
| + Correlation-crowded-trade guard | drawdown -20% |
| **Stacked** | **Sharpe 0.6-1.0, sharply reduced DD** |

Honest note: no amount of factor stacking can overcome crypto alt momentum's structural crowding for long. This is directionally positive EV in favorable regimes and sharply negative during rotations. Don't size large.

## Data requirements

All **missing**:
- 15m bars across 15-symbol alt perp universe (Hyperliquid)
- Funding per symbol (from #3)
- **Open Interest per symbol** — new ingestion, not covered by #3
- Whale-flow (from #2)
- Cross-venue taker flow (from #1 phase 2)

## Code changes

Requires SHARED-INFRA gap 5 (cross-sectional dispatch):
```go
// backend/internal/domain/strategy/contract.go
type CrossSectionalStrategy interface {
    Strategy
    OnCrossSectionalBar(
        ctx Context,
        ts time.Time,
        bars map[string]Bar,
        st State,
    ) (State, []Signal, error)
}
```

New runner path: `backend/internal/app/strategy/runner_xsec.go` — buffers bars per symbol until all universe symbols reported timestamp `ts`, then dispatches.

Backtest pipeline: `backend/internal/app/backtest/pipeline_shard.go` must emit synchronized batch events. Shard-barrier design is the hardest call in the roadmap.

OI ingestion:
- `backend/internal/ports/open_interest.go` — new port
- `backend/internal/adapters/hyperliquid/open_interest.go`
- `migrations/NNN_open_interest.sql` — hypertable

Strategy: `backend/internal/app/strategy/builtin/xsec_momo_v1.go`

## DNA

```toml
schema_version = 2
strategy_id = "xsec_momo_v1"
version = "0.1.0"

[routing]
asset_classes = ["CRYPTO_PERP"]
venues = ["hyperliquid"]
timeframes = ["15m"]
symbols = [
  "BTC-PERP","ETH-PERP","SOL-PERP","AVAX-PERP",
  "ARB-PERP","OP-PERP","SUI-PERP","APT-PERP",
  "DOGE-PERP","LINK-PERP","ADA-PERP","TIA-PERP",
  "SEI-PERP","INJ-PERP","MATIC-PERP",
]

[lifecycle]
paper_only = true

[params]
top_n                = 3
bottom_n             = 3
rebalance_bars       = 1
turnover_cap_pct     = 50
max_gross_usd        = 30000

[params.weights]
r_1h      = 1.0
r_4h      = 0.5
r_24h     = 0.2
vol_pen   = -0.3
funding_z = 0.4
oi_change = 0.3
whale_z   = 0.3
xv_flow   = 0.3

[params.gates]
require_regime_ok           = true   # skip stress regimes
avg_pair_corr_flatten       = 0.9    # crowded-trade escape
min_24h_volume_usd          = 10000000
```

## Backtest feasibility

Low today. Requires:
- Synchronized batch-bar dispatch (architectural change)
- Full Hyperliquid historical data across 15-symbol universe
- OI backfill per symbol
- Delisting-aware universe reconstruction (symbols come and go)

Fidelity ~60% initial; turnover modeling and delisting gaps are hard.

## Expected edge

- Walk-forward Sharpe 0.8-1.3 (looks great on paper)
- **Live realistically 0.4-0.8**, 0.6-1.0 with full factor stack
- Fat-tail drawdowns during regime flips
- Capacity: $30-100k before slippage dominates

## Key failure modes

1. **Regime break** (momentum → reversal): everyone crowds same top-3 / bottom-3, unwinds in single 15m bar. Crowded-trade correlation guard catches most.
2. **Delisting:** basket member goes illiquid, position stuck. Min-volume filter drops it next rebalance.
3. **BTC-led risk-off:** all alts dump correlated, cross-sectional ranking degenerates. Skew-regime gate catches stress regime.
4. **Walk-forward overfit:** weights tuned on 2023 bull data fail in 2024 sideways. Mitigation: rolling re-calibration every 90 days; compare to equal-weight baseline.

## Risk controls

- Max gross USD
- Per-symbol position cap
- Turnover cap per rebalance
- Universe-liquidity filter: drop < $10M 24h volume
- Correlation monitor: basket avg pairwise corr > 0.9 → flatten
- Skew-regime kill
- Weekly walk-forward re-check: if out-of-sample Sharpe drops > 50%, pause

## Milestones

Assumes #3 complete.
- **W1:** `CrossSectionalStrategy` interface + batch runner + backtest pipeline change
- **W2:** OI ingestion + feature pipeline + walk-forward study
- **W3-4:** Paper-trade with conservative sizing; factor uplift A/B vs unweighted baseline

## Success criteria

- Walk-forward Sharpe > 1.0 out-of-sample
- No single-rebalance drawdown > 2%
- Factor-stacked version beats equal-weight baseline by > 30% Sharpe
- If live paper Sharpe < 0.5 after 4 weeks, shelve

## Why it's ranked last

- Worst infra debt (cross-sectional dispatch is cross-cutting change)
- Worst live-decay (academic Sharpe halves more than any other family)
- Highest regime fragility (alt momentum is the most crowded retail trade on HL)

**Recommendation:** build only after #1 ships (validates MFT path), #3 ships (provides HL adapter + funding + regime infra), and #4 ships (validates paired/dual-venue execution). Treat the cross-sectional runner as a **reusable artifact** — it unlocks equities ranking strategies too — and let xsec_momo_v1 itself live-or-die on its own (likely die) without capacity concern.

## Reusable artifacts (the real value)

- `CrossSectionalStrategy` interface + batch-bar runner — foundational for any ranking strategy, including **equities ranking where omo's factor moat is much stronger**
- `OpenInterestPort` + OI ingestion — useful for every perp strategy
- Universe-liquidity filter — reusable guard for any multi-symbol basket
- Crowded-trade correlation guard — applies to any multi-position strategy
