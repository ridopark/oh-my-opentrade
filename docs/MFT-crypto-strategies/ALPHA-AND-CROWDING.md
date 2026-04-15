# Alpha Differentiation and Crowding Analysis

## Core thesis

**Don't compete on speed — compete on confluence.** omo loses every microsecond race on Binance perps to Jump, Wintermute, GSR. omo does not lose the multi-factor judgment race — those firms run siloed signal teams and firm-wide risk limits prevent bespoke trade-by-trade confluence stacking.

**Don't compete on majors — compete on mid-caps.** BTC/ETH perps are HFT-dominated. Top-20 HL alts (ex top-5) have narrative divergences that last hours, not milliseconds.

**Don't compete on frequency — compete on regime selection.** A strategy running 40% of days at Sharpe 2.0 beats one running 100% at Sharpe 0.8.

## Crowding per strategy

| Strategy | Base crowding | With factor stack | Remaining edge |
|---|---|---|---|
| #1 crypto_revert | Very High | Low | Confluence is the moat |
| #2 pairs (BTC/ETH) | High (textbook) | Medium | Kalman + whale gate differentiates |
| #3 funding_timer | Medium-High in bull regimes | Low with skew gate | Regime selection is the edge |
| #4 basis_carry majors | Very High | Very High | Saturated by Ethena + funds |
| #4 basis_carry listings | Low | Low | Uncrowded sub-strategy |
| #5 xsec_momo | High and toxic | Medium | Walk-forward overfit problem |

## omo's factor moat

### Factor A — Cross-venue taker flow (crypto-native trade-tape)

**Source:** omo equity side has dark-pool trade-level backfill with 5m aggregation. Crypto equivalent: coordinated large taker prints across Binance + HL + Coinbase within 10-second windows.

**Alpha mechanism:** when > $5M aggressor-buy prints cluster across 3+ venues simultaneously, institutional accumulation is underway. Retail bots don't aggregate cross-venue; this is a real edge.

**Applies to:** #1 phase 2 (gate), #3 (entry confirmation).

**Sharpe uplift:** +0.3-0.5.

**Crowding:** Low. Requires multi-venue WS infra most retail quants don't build.

### Factor B — Whale on-chain flow (ETF custodian accumulation)

**Source:** 13F plan for equities adapts to tagged on-chain wallets — ETF custodians (BlackRock, Fidelity, Grayscale), known MM cold wallets, exchange hot/cold.

**Alpha mechanism:** accumulation days have structurally different intraday reversion patterns than distribution days. Net-inflow days favor long-side bias; net-outflow days favor short-side.

**Applies to:** #2 (pair direction bias), #4 (basis holding period), #3 (optional confirmation), #5 (ranking feature).

**Sharpe uplift:** +0.2-0.4.

**Crowding:** Medium among funds (Arkham/Nansen users), low among retail quants.

### Factor C — Deribit options skew + RR regime classifier

**Source:** DoltHub options plan for equities adapts directly — Deribit is the equivalent for crypto.

**Alpha mechanism:** 25-delta risk reversal signals directional sentiment; ATM IV term structure slope signals regime (inverted = stress, steep = complacency). Funding arb during negative-skew regimes is the dominant drawdown path — skip those days.

**Applies to:** #3 (primary gate), #4 (primary gate), #1 phase 2 (optional regime overlay).

**Sharpe uplift:** drawdown -30-50%, Sharpe +0.2-0.5 (via drawdown reduction improving Sortino).

**Crowding:** Medium. Professional basis funds all use it; retail doesn't bother.

### Factor D — Inducement detector (liquidity sweep confluence)

**Source:** already built for AVWAP higher-conviction entries in equities. Concept ports directly to crypto and arguably works better — crypto books are thinner, sweeps are cleaner, stop-hunting is well-documented on HL where book is public.

**Alpha mechanism:** fade 2-sigma overshoots only when a liquidity sweep (stops cleared) just occurred. Separates reversions from trend-continuations.

**Applies to:** #1 (primary gate), future MM sleeve.

**Sharpe uplift:** +0.3-0.6 on #1. **Probably the highest-leverage factor omo already has.**

**Crowding:** Low. Most retail bots don't implement sweep detection; most professional bots don't use it at this cadence.

### Factor E — Session-time weighting

**Source:** already built. Graduated time-of-day multiplier on entry strength.

**Alpha mechanism:** crypto regimes differ by session (Asia thin/range-bound; US trendy/ETF-driven; weekend manipulation-prone).

**Applies to:** all.

**Sharpe uplift:** +0.1-0.2.

**Crowding:** Low. Retail mostly ignores; funds use but rarely expose in decision logic.

### Factor F — Cross-strategy confluence scoring

**Source:** the infrastructure to combine factors at trade-entry level.

**Alpha mechanism:** a trade scoring high on (TFI + cross-venue flow + whale net-inflow + skew OK + inducement) is not the same trade as one with only (TFI). Most retail runs single-factor; most funds combine at portfolio level (Barra-style) not trade-entry level.

**Applies to:** all.

**Sharpe uplift:** +0.5-1.0 when stacked correctly.

**Crowding:** **This is the actual moat.** Very low — requires the infrastructure omo has been building for 18 months.

## Reality-adjusted Sharpe projections

| Strategy | Naive paper | Live-decay | + omo factors | Honest projection |
|---|---|---|---|---|
| #1 crypto_revert (stacked) | 1.0-1.5 | 0.4-0.8 | +0.6-0.9 | **1.0-1.7** |
| #2 pairs phase 1 | 1.2-1.8 | 0.8-1.4 | +0.2-0.4 | **1.0-1.8** |
| #3 funding_timer + skew gate | 1.5-2.5 | 1.0-1.8 | +0.3-0.5 | **1.3-2.3** |
| #4 basis_carry majors | 1.8-2.5 | 0.8-1.2 | +0.2 | **1.0-1.4** |
| #4 basis_carry listings | 2.0-3.0 | 1.2-1.8 | +0.2 | **1.4-2.0** |
| #5 xsec_momo stacked | 1.0-1.5 | 0.4-0.8 | +0.2 | **0.6-1.0** |

## Clear winners by Sharpe-to-effort

1. **#1 with full factor stack** — Sharpe 1.0-1.7 in 6-8 days of work. Ship first.
2. **#3 with skew-regime gate** — Sharpe 1.3-2.3 in 3 weeks. Real strategic bet; infra reused everywhere.
3. **#2 phase 1 with whale gate** — Sharpe 1.0-1.5 in 6 days post-infra. Bonus.
4. **#4 new-listing arm** — Sharpe 1.4-2.0 on captured windows; niche but uncrowded.

Everything else is infrastructure work paying forward to strategies above, not production bets in itself.

## Where omo can actually win

### Uncrowded niches
- Mid-cap HL perps (top-20 ex top-5)
- New-listing basis capture (first 2-4 weeks)
- Post-halving / post-ETF euphoria windows
- Narrative-driven short squeezes

### Competitive advantages to lean on
- Multi-venue aggregation (cross-venue taker flow)
- Cross-class factor combination (options skew gating crypto trades)
- On-chain signal discipline (whale flow with proper lag handling)
- Inducement detection at trade-entry level

### Avoid
- BTC/ETH majors HFT-adjacent (Jump wins)
- Simple funding arb at size (Ethena wins)
- Triangular arb (dead)
- Generic momentum at sizes where slippage dominates

## Decision heuristics

1. **If a strategy has > 3 factor gates, it's honest.** Single-factor backtests overfit. Stack 4+ before trusting live.
2. **If a strategy trades > 70% of days, it's not selective enough.** Regime selection is where crypto MFT alpha lives.
3. **If expected Sharpe relies on paper fidelity, halve it.** Budget +5bps slippage tax always.
4. **If a factor works on all five strategies, prioritize building it.** Shared-factor uplift compounds.
