# Shared Infrastructure Gaps

Multi-proposal prerequisites, ranked by number of strategies they unblock. Ship top-down.

## Gap 1 — Trade-tick taker side

**Blocks:** #1 (TFI gate), any future microstructure work
**Effort:** ~5 hours

Current state:
- `domain.MarketTrade` (`backend/internal/domain/entity.go:511`) has `Time/Symbol/Price/Size` only
- Alpaca crypto WS streams trades but taker side is stripped in domain mapping

Changes:
1. Add `TakerSide string` to `MarketTrade`
2. Wire Alpaca WS trade channel to populate it (`adapters/alpaca/crypto_ws.go`)
3. Fallback: `sign(close-open) * volume` per bar when taker side missing

Reusable by every future crypto strategy.

---

## Gap 2 — Hyperliquid adapter

**Blocks:** #3, #4, phase 2 of #2, #5, phase 2 of #1 (cross-venue flow), any perp work
**Effort:** ~4-5 days

Current state: zero. `grep -ri hyperliquid backend/` returns empty.

New package `backend/internal/adapters/hyperliquid/`:
- `rest.go` — REST client (account, fees, funding history, candles)
- `ws.go` — WS subscribe (`l2Book`, `trades`, `userEvents`, `funding`)
- `broker.go` — order submission (limit, market, cancel, modify)
- `funding.go` — FundingRatesPort implementation
- `open_interest.go` — OpenInterestPort (for #5)

Go SDK is thin/community; write against the documented HTTP+WS protocol directly (see `hyperliquid-python-sdk` for reference shapes).

Shared with: every future perp strategy.

---

## Gap 3 — Funding rate data layer

**Blocks:** #3, #4, feature in #5
**Effort:** ~1-2 days once adapter exists

New port:
```go
// backend/internal/ports/funding.go
type FundingRatesPort interface {
    Latest(ctx, venue, symbol) (FundingRate, error)
    History(ctx, venue, symbol, from, to time.Time) ([]FundingRate, error)
    Stream(ctx, venue, symbol) (<-chan FundingRate, error)
}
```

New storage: Timescale hypertable `funding_rates(venue, symbol, timestamp, rate, interval_hours, mark_price)`.

New jobs:
- `backend/internal/app/ingestion/funding_backfill.go`
- `backend/internal/app/ingestion/funding_live.go`

Bybit read-only adapter for pre-HL research: `backend/internal/adapters/bybit/funding.go` (1 day).

---

## Gap 4 — Paired/grouped orders

**Blocks:** #2 phase 2, #4
**Effort:** ~3-4 days

Current state: `OrderIntent` (`entity.go:64-87`) is per-symbol only. No atomic multi-leg semantics.

Changes:
1. Add `LegGroupID string` to `OrderIntent`
2. New `backend/internal/app/execution/paired.go` — atomic submit with rollback-on-partial
3. Simbroker update: treat grouped legs as a unit for fill decisions
4. Risk sizer: group-level notional check

---

## Gap 5 — Cross-sectional dispatch

**Blocks:** #5, future equity ranking (higher-alpha application)
**Effort:** ~4-6 days

Current state: `pipeline_shard.go` dispatches per-symbol sequentially. No synchronized-timestamp batch.

Changes:
1. New interface:
   ```go
   type CrossSectionalStrategy interface {
       Strategy
       OnCrossSectionalBar(ctx Context, ts time.Time, bars map[string]Bar, st State)
         (State, []Signal, error)
   }
   ```
2. New runner path `backend/internal/app/strategy/runner_xsec.go` — buffers per-symbol bars until universe complete at `ts`, dispatches batch
3. Backtest pipeline emits batch events
4. Live pipeline uses time-window buffering (e.g. 500ms grace) to handle WS jitter
5. Shard-barrier design: either force universe into single shard or build cross-shard barrier

**Strategic note:** the reusable artifact is worth more than xsec_momo_v1 itself. Cross-sectional dispatch unlocks equity ranking strategies where omo's factor moat (dark-pool, 13F, options flow) produces real alpha.

---

## Gap 6 — Simbroker realism upgrades

**Blocks:** credible backtest for anything beyond #1
**Effort:** ~2-3 days per feature

Current state: slippage-bps fills at close; no queue model, no funding events, no dual-venue.

Upgrades (in priority order):
1. **FundingEvent accrual** (~30 LOC) — blocks #3 and #4 credibility
2. **Dual-venue positions** — blocks #4
3. **Maker/taker fee-tier model** — blocks all perp strategies' edge math
4. **Bid/ask-aware fills** (not close+bps) — blocks MFT honest-slippage
5. **Queue-position approximate** (hftbacktest-style) — for future MM work

---

## Gap 7 — Deribit options IV adapter

**Blocks:** skew-regime gate on #3 and #4; optional gate on #1 phase 2
**Effort:** ~2 days

New read-only adapter `backend/internal/adapters/deribit/`:
- REST client for options chain snapshots
- IV surface computation (ATM IV 7d/30d, 25d RR, 25d BF)

New port:
```go
type OptionsIVPort interface {
    Surface(ctx, asset) (IVSurface, error)
    SkewRR(ctx, asset, tenor string) (float64, error)
    TermSlope(ctx, asset) (float64, error)
}
```

New hypertable `iv_snapshots`. Live poll every 5 minutes is sufficient (skew regime moves slowly).

Powers `domain/strategy/skew_regime.go` classifier. **Drawdown reduction 30-50% on funding/basis strategies** — highest-leverage gate in the roadmap.

---

## Gap 8 — On-chain custodian flow adapter

**Blocks:** whale-flow gate on #2 phase 1, optional feature on #3, feature on #5
**Effort:** ~3-4 days

New read-only adapter `backend/internal/adapters/onchain/`:
- Integration with on-chain indexer (Dune, Flipside, or raw node)
- Tagged wallet set: ETF custodians (BlackRock, Fidelity, Grayscale), known MM cold wallets, exchange hot/cold
- Compute net-flow per asset per window (1h, 24h, 7d)

New port:
```go
type WhaleFlowPort interface {
    NetFlow(ctx, asset, windowHrs int) (float64, error)
    LargeTransfers(ctx, asset, windowHrs, minUSD int) ([]Transfer, error)
}
```

**Signal uplift 0.2-0.4 Sharpe** on directional strategies. Low crowding among retail; medium among funds.

---

## Gap 9 — Cross-venue taker-flow aggregator

**Blocks:** phase 2 of #1 (cross-venue confluence gate), feature in #5
**Effort:** ~2 days (post-HL adapter)

New package `backend/internal/app/strategy/flow/`:
- Aggregates taker-buy and taker-sell volume across HL + Binance + Coinbase in rolling 10s/60s windows
- Per-symbol net flow score
- Detects coordinated large prints across venues (institutional accumulation signal)

Depends on:
- Gap 2 (HL adapter) for HL trades
- New Binance read-only WS adapter (~1 day)
- Coinbase read-only WS adapter (existing Alpaca or new)

**This is omo's dark-pool/trade-tape moat ported to crypto.** Retail bots don't aggregate cross-venue; this is a real edge.

---

## Gap 10 — Cross-venue venue-qualified symbols

**Blocks:** clean execution on #3, #4, #5
**Effort:** ~2-3 days

Current state: `Symbol` is a string. Cross-venue strategies need `(Venue, Symbol)` pairs in positions, orders, fills.

Options:
- Promote to `QualifiedSymbol{Venue, Symbol}` type
- OR add parallel venue field to Position, OrderIntent, Fill

Retrofit cost grows with every perp strategy that ships on the current model. Decide early.

---

## Build order (recommended, 6 weeks to two-strategy differentiated book)

```
Phase A (enables #1 + phase-2 factor stack prep):
  Gap 1 (taker side)                    — 5 hours
  Gap 10 (venue-qualified symbols)      — 2-3 days
  Crypto inducement port from equities  — 1 day
  -> Ship #1 phase 1                     — 2-3 days

Phase B (enables #3, unlocks perp path, bonus to #1):
  Gap 2 (Hyperliquid adapter)           — 4-5 days
  Gap 3 (funding data layer)            — 1-2 days
  Gap 6.1 (FundingEvent accrual)        — 2 days
  Gap 7 (Deribit IV + skew)             — 2 days
  Gap 9 (cross-venue flow aggregator)   — 2 days (in parallel)
  -> Upgrade #1 to phase 2 stacked       — 2-3 days
  -> Ship #3 funding_timer_v1            — 3 days (most infra done)

Phase C (completes market-neutral book):
  Gap 4 (paired orders)                 — 3-4 days
  Gap 6.2 (dual-venue positions)        — 2 days
  Gap 8 (whale-flow on-chain)           — 3-4 days
  -> Ship #4 basis_carry_v1              — 3 days
  -> Ship #2 phase 1 with whale gate     — 4 days (optional)

Phase D (research, equities-reusable):
  Gap 5 (cross-sectional dispatch)      — 4-6 days
  Gap 6.3-6.5 (fee tiers, bid/ask, queue) — ongoing
  -> Ship #5 xsec_momo_v1 as research    — 4 days
```

Total Phase A-C: ~6 weeks for **two-strategy crypto MFT book with genuine differentiation** (#1 stacked + #3 with skew gate), plus #2 phase 1 and #4 as bonuses.
