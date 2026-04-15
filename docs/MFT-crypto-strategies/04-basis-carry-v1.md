# basis_carry_v1 — Spot-Perp Basis / Term Structure

**Status:** proposed, conditional on #3 proving out
**Effort:** M (3 days strategy-only post-#3)
**Fit score:** 3/10 today → 7/10 once #3 ships
**Honest Sharpe (with factor stack):** 1.0-1.4

## Edge thesis

Perp mark price trades at persistent premium or discount to spot (annualized basis 5-30%). **Long spot + short perp** captures the basis minus funding and fees. Unwinds when basis normalizes. Different from funding_timer_v1: this trades the *basis level*, not funding timing — a related but distinct signal.

## Crowding

**Very High on majors.** This is the cash-and-carry trade every institutional crypto desk, CME basis fund, and Ethena runs. Fully saturated on BTC/ETH. **Medium on new HL listings** (first 2-4 weeks post-listing, basis often 30-50% APR before arbs compress). **Low on mid-cap alts** where capital is too fragmented for institutional arb.

**Honest read:** on majors, retail cash-and-carry is competing against entities with 10bps borrow costs and lower fees. Retail loses unless venue-specific inefficiencies exist (new listing, high funding regime with structural short squeeze risk scaring arbs away).

## Why it's still worth doing

1. 90% of the infra is free once #3 ships (HL adapter, funding data, FundingEvent, dual-venue simbroker all reused)
2. New-listing basis capture is an uncrowded sub-strategy worth a research arm
3. It completes the "crypto market-neutral book" narrative alongside #3

## Cadence

- Decision: 15m
- Holding: days (until basis normalizes)
- Symbols: BTC, ETH (phase 1); new HL listings (phase 2 research)
- Venues: spot on Alpaca + perp on Hyperliquid

## Signal

```
spot_mid_t       = Alpaca BTC/USD mid (close of 15m bar)
perp_mark_t      = Hyperliquid BTC-PERP mark
basis_bps        = (perp_mark_t - spot_mid_t) / spot_mid_t * 10000
basis_apr_pct    = basis_bps annualized via current funding

f_mean_48h       = funding regime check
skew_regime      = Deribit skew classifier (reused from #3)
whale_flow       = on-chain ETF custodian net flow (reused from #2)
```

**Entry (long spot + short perp):**
- `basis_apr_pct > entry_threshold_pct` (e.g. 8%)
- `basis_mean_24h` confirms (no single-bar spike)
- `f_mean_48h > 0` (funding pays you to be short)
- `skew_regime != "stress"` (avoid crash carry traps)
- Optional: `whale_flow > 0` (accumulation supports premium holding)

**Exit:**
- `basis_apr_pct < exit_threshold` (e.g. 2%)
- Either leg unavailable
- Funding regime breakdown (f_mean_48h turns negative)
- Skew regime flips to stress
- Max hold 14 days (rolling into next funding cycle gets expensive)

Paired order: legs fill atomically or both roll back.

## Factor stack

| Factor | Uplift |
|---|---|
| Base basis capture | Sharpe 0.8-1.2 naive |
| + Skew regime gate | +0.2-0.3, drawdown -40% |
| + Whale-flow gate | +0.1-0.2 |
| + New-listing arm | separate edge, Sharpe 1.5+ on listings only |
| **Stacked** | **1.0-1.4 majors; 1.5+ listings** |

## Data requirements

All **inherited from #3**:
- Perp mark prices (Hyperliquid adapter)
- Funding rates (FundingRatesPort)
- Paired-order semantics (new addition for this strategy)
- Deribit IV (regime gate)
- On-chain custodian flows

Spot bars: have (Alpaca).

## Code changes

Reuses #3 fully. New additions:
- `backend/internal/domain/entity.go` — `LegGroupID` field on `OrderIntent`
- `backend/internal/app/execution/paired.go` — atomic paired submission + rollback-on-partial
- `backend/internal/app/strategy/builtin/basis_carry_v1.go`
- `configs/strategies/basis_carry_v1.toml`
- Optional: `backend/internal/app/ingestion/new_listings_watcher.go` for phase 2

## DNA

```toml
schema_version = 2
strategy_id = "basis_carry_v1"
version = "0.1.0"

[routing]
asset_classes = ["CRYPTO", "CRYPTO_PERP"]
venues = ["alpaca", "hyperliquid"]
timeframes = ["15m"]
symbols = ["BTC/USD", "BTC-PERP"]

[lifecycle]
paper_only = true

[params]
spot_symbol           = "BTC/USD"
spot_venue            = "alpaca"
perp_symbol           = "BTC-PERP"
perp_venue            = "hyperliquid"
entry_basis_apr_pct   = 8.0
exit_basis_apr_pct    = 2.0
min_basis_window_hrs  = 24
max_position_usd      = 20000
max_hold_days         = 14
funding_kill_bps_hr   = -0.5

[params.gates]
require_regime_ok   = true
require_whale_align = false
```

## Backtest feasibility

Medium (~65% fidelity first pass). Needs:
- Dual-venue simbroker (from #3)
- Paired-fill atomicity (new)
- Cross-venue 15m bar alignment (Alpaca vs HL timestamps)

## Expected edge

- Majors: net 4-10% APR; Sharpe 0.8-1.2 majors (crowded)
- With gates: Sharpe 1.0-1.4, drawdown 2-4%
- New-listing arm (phase 2): 15-40% APR annualized on captured windows; Sharpe 1.5+
- Capacity: $200k-$2M majors; $50-200k per listing window

## Key failure modes

1. **Perp short squeeze on majors** — forces margin call on hedged position (HL liquidates short). Mitigation: max position cap, paired-order atomicity prevents being unhedged.
2. **Venue outage during stress** — leaves one leg alone exactly when basis is blowing out. Mitigation: WS watchdog flattens both sides if either venue stale > 60s.
3. **Ethena saturation** — basis compresses below entry threshold for months. Strategy sits flat. Correct behavior.
4. **Funding flips negative while position held** — carry inverts, P&L bleeds. Mitigation: funding_kill_bps_hr auto-close.

## Risk controls

- Max position USD
- Paired-order atomicity: never end up with one leg
- Funding-kill: hard negative funding auto-closes
- Per-venue WS watchdog; stale > 60s → flatten and alert
- Daily mark-to-market check on basis PnL vs expected
- Skew-regime kill: no new entries in stress

## Milestones

**Assumes #3 complete (week 5+).**
- **W5:** `LegGroupID` on OrderIntent + paired execution path + tests
- **W6:** basis_carry_v1 strategy + backtest on reconstructed HL perp data + Alpaca spot
- **W7:** Paper-trade on majors; monitor basis capture vs expected
- **W8+:** (phase 2) new-listing watcher + research arm

## Success criteria (4-week paper)

- Basis capture at least 60% of theoretical (gross minus all fees)
- No single-leg exposure incident (both legs always hedged or both flat)
- Auto-close triggers fire correctly in funding-flip regime
- Majors phase Sharpe > 0.8; if below, skip to new-listing research only

## Strategic decision

**Ship #4 only if #3 achieves paper Sharpe > 1.2.** If #3 underperforms, the infra investment was still worth it (reused by #2 phase 2), but a second cash-and-carry strategy on the same data adds little. Reallocate effort to:
- HL market-making sleeve
- DEX-CEX XEMM
- Option-selling around funding

## Reusable artifacts

- `LegGroupID` + paired execution — unlocks any multi-leg strategy (XEMM, spread trades)
- New-listing watcher (phase 2) — foundation for listing-event strategies
