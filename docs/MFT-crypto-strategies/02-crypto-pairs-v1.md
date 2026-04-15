# crypto_pairs_v1 — BTC/ETH Cointegration Pairs

**Status:** proposed, phase 1 only (phase 2 deferred unless #3 ships)
**Effort:** M (8 days phase 1; +5 days phase 2)
**Fit score:** 4/10
**Honest Sharpe (with factor stack):** 1.0-1.8

## Edge thesis

BTC and ETH share a volatile but persistent cointegrating relationship driven by correlated macro flows (ETF rotation, risk-on/risk-off). When the log-spread `log(ETH) - beta*log(BTC) - alpha` diverges by >2-sigma, it historically mean-reverts within hours. Kalman-drifted beta + OU half-life filter separates fresh cointegration from broken regimes.

## Crowding

**High** for the textbook static-beta version — every quant interview question starts here. **Medium** for Kalman-drifted beta + half-life gate — rarely done right by retail. **Low** for Kalman + whale-flow gate + session weighting — genuinely differentiated.

## Cadence

- Decision: every 5m bar close
- Holding: 2-12 hours
- Symbols: BTC/USD, ETH/USD
- Venue: Alpaca spot (phase 1), Hyperliquid perps (phase 2)

## Signal

Kalman-filtered cointegration:
```
observation:  log(ETH_t) = alpha_t + beta_t * log(BTC_t) + epsilon_t
state drift:  beta_t = beta_{t-1} + w_t      (w ~ N(0, Q))
residual:     spread_t = log(ETH_t) - beta_t * log(BTC_t) - alpha_t
z_t           = spread_t / sigma(spread)
half_life     = OU fit on spread residuals
```

**Entry:**
- `|z_t| > 2.0` AND `half_life < 24h` AND `beta_t in [0.5, 2.0]`
- Phase 1 long-only ETH: `z_t < -2.0` only (Alpaca cannot short spot)
- Phase 2 bidirectional: long undervalued, short overvalued, dollar-balanced
- **Optional factor gate:** whale net-flow on relevant asset aligns with entry direction

**Exit:**
- `|z_t| < 0.3` OR half-life blows up OR 24h time stop OR beta jumps > 30% in one bar (regime-break detector)

## Factor stack

| Factor | Phase | Uplift |
|---|---|---|
| Base cointegration | 1 | Sharpe 0.8-1.4 naive |
| + Kalman drifting beta | 1 | +0.2-0.3 (regime survival) |
| + OU half-life gate | 1 | +0.1-0.2 |
| + Whale net-flow gate | 1 | +0.2-0.4 |
| + Session-time weight | 1 | +0.1 |
| + Phase 2 real shorts | 2 | +0.3-0.5 (true market-neutral) |
| **Stacked** | 2 | **1.0-1.8 live** |

## Phase plan

**Phase 1 (spot-only, long-only, Alpaca):**
- Only enter when ETH is cheap vs BTC (z_t < -2.0)
- Long ETH, no short leg (Alpaca cannot short)
- Implicit hedge: the absence of a BTC position is the "short"
- Runs on existing infra + new pair runner

**Phase 2 (market-neutral, Hyperliquid perps):**
- Real long ETH-PERP / short BTC-PERP
- Requires Hyperliquid adapter + paired-order semantics
- Gated on SHARED-INFRA gaps 2 and 4
- **Decision point:** only proceed if phase 1 achieves paper Sharpe > 0.6 AND #3 ships

## Data requirements

Phase 1:
- 5m bars BTC/USD, ETH/USD — **have**
- Whale net-flow signal (port concept from 13F project to on-chain ETF custodian wallets) — **build**

Phase 2:
- Perp mark-price from Hyperliquid — **needs HL adapter**
- Paired-order execution — **needs LegGroupID work**

## Code changes

Phase 1:
- `backend/internal/domain/strategy/kalman_beta.go` — new
- `backend/internal/domain/strategy/ou_half_life.go` — new
- `backend/internal/domain/strategy/pair_spread.go` — new
- `backend/internal/domain/strategy/contract.go` — add `PairStrategy` interface
- `backend/internal/app/strategy/runner_pairs.go` — new synchronized-bar dispatch
- `backend/internal/app/strategy/builtin/crypto_pairs_v1.go` — new
- `backend/internal/domain/strategy/whale_flow.go` — new crypto adaptation
- `backend/internal/adapters/onchain/` — new read-only adapter for ETF custodian wallet flows (Arkham/Nansen alternative via on-chain indexers)
- `configs/strategies/crypto_pairs_v1.toml` — new

Phase 2 additions:
- `backend/internal/domain/entity.go` — `LegGroupID` on OrderIntent
- `backend/internal/app/execution/paired.go` — atomic submission + rollback
- Hyperliquid adapter (shared with #3)

## DNA

```toml
schema_version = 2
strategy_id = "crypto_pairs_v1"
version = "0.1.0"

[routing]
asset_classes = ["CRYPTO"]
venues = ["alpaca"]            # phase 2: ["hyperliquid"]
timeframes = ["5m"]
symbols = ["BTC/USD", "ETH/USD"]

[lifecycle]
paper_only = true

[params]
leg_a = "ETH/USD"
leg_b = "BTC/USD"
spread_window_bars   = 1440    # ~5 days of 5m
kalman_q             = 0.0001
kalman_r             = 0.01
entry_z              = 2.0
exit_z               = 0.3
max_half_life_bars   = 288     # 24h
hold_timeout_bars    = 288
beta_min             = 0.5
beta_max             = 2.0
beta_jump_kill_pct   = 30

[params.gates]
require_whale_flow_align = true
whale_flow_window_hrs    = 24
whale_flow_min_score     = 0.3

[params.phase]
value = 1   # 1 = long-only spot, 2 = market-neutral perp
```

## Backtest feasibility

- **Phase 1:** High. Simbroker handles; synchronized dispatch is the only new plumbing. Whale-flow historical requires on-chain backfill (day of work).
- **Phase 2:** Medium (~70%) without funding event modeling; improves to 85% once #3's FundingEvent ships.

## Expected edge

Phase 1 naive: Sharpe 0.8-1.4, net 5-15 bps/trade after 25bps Alpaca fees.
Phase 1 with whale-flow + session: Sharpe 1.0-1.5.
Phase 2 with real shorts + HL fees: Sharpe 1.3-1.8. Capacity $200k-$1M.

## Key failure modes

1. **Cointegration break** (ETH/BTC narrative divergence — post-Shanghai, post-ETF, L2 wars). Kalman adapts slowly; time stop closes at loss. Mitigation: half-life cap and beta-jump kill.
2. **Asymmetric payoffs phase 1** — long-only captures only half the opportunities; positive-beta upside drift leaves edge on table.
3. **Whale-flow signal noise** — on-chain labels are imperfect; ETF custodian inflows can be rehypothecation rather than accumulation.

## Risk controls

- Beta-range clamp [0.5, 2.0]
- 24h time stop
- Beta jump kill (> 30% in one bar)
- Max 1 concurrent pair position
- 5-consecutive-loss kill-switch (regime-break detector)

## Milestones

**Phase 1 (Week 2 after #1 ships):**
- W1: Kalman + OU primitives + `PairStrategy` interface + pair runner
- W2: Backtest harness; BTC/ETH 2023-2025 replay on 5m; param sweep
- W3: Whale-flow adapter (on-chain ETF custodian wallets) + gate integration
- W4: Paper-trade phase 1 long-only alongside VWTSM and #1

**Phase 2 (conditional; Week 7+ post-#3):**
- LegGroupID + paired execution + tests
- HL perp integration
- Paper market-neutral

## Success criteria

**Phase 1 (4-week paper):**
- Net Sharpe > 0.6
- At least 15 completed pair trades
- Beta stability: no day where Kalman beta jumps > 30%
- Whale-flow gate reduces trades by 20-40% with per-trade edge up

**If Sharpe < 0.6 after 4 weeks → kill, do not build phase 2.**

## Reusable artifacts

- Kalman beta + OU half-life primitives — any cointegration work
- `PairStrategy` + synchronized-bar runner — foundation for #4 and #5
- Whale-flow adapter + signal — pure tailwind for any directional crypto strategy
