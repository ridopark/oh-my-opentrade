# MFT Crypto Strategies — Planning Docs

Mid-Frequency Trading (MFT) crypto strategy roadmap for oh-my-opentrade. Decisions seconds-to-minutes; holdings seconds-to-hours. No HFT (no colo, no FPGA). Retail-accessible on Hyperliquid / Binance / Bybit / Alpaca.

## Candidates

| Rank | Plan | Fit | Days | Honest Sharpe* | Ship order |
|---|---|---|---|---|---|
| 1 | [crypto_revert_v1](01-crypto-revert-v1.md) | 6/10 | 4-5 | 1.0-1.7 | **first** |
| 2 | [crypto_pairs_v1](02-crypto-pairs-v1.md) | 4/10 | 8+ | 1.0-1.8 | after #1 (phase 1 only) |
| 3 | [funding_timer_v1](03-funding-timer-v1.md) | 1/10 | 7+ | 1.3-2.3 | strategic bet; build HL adapter |
| 4 | [basis_carry_v1](04-basis-carry-v1.md) | 3/10 | 8+ | 1.0-1.4 | skip unless #3 proves out |
| 5 | [xsec_momo_v1](05-xsec-momo-v1.md) | 2/10 | 6+ | 0.6-1.0 | research only |

*Naive paper Sharpe halves live. Honest = live-decay applied + omo factors (inducement, TFI, cross-venue flow, whale net-flow, skew regime) stacked on top.

## Shared infrastructure

Gaps blocking multiple proposals: see [SHARED-INFRA.md](SHARED-INFRA.md). Engine-change detail: [ENGINE-CHANGES.md](ENGINE-CHANGES.md). Alpha / crowding framing: [ALPHA-AND-CROWDING.md](ALPHA-AND-CROWDING.md).

## Core thesis

**Don't compete on speed — compete on confluence.** omo will lose every microsecond race on Binance perps to Jump/Wintermute/GSR. It will not lose the multi-factor judgment race. The real moat is trade-by-trade confluence of microstructure + on-chain + options-implied + cross-venue signals, which siloed HFT shops don't do.

**Don't compete on majors — compete on mid-caps.** Top-20 HL alts (ex top-5) are where narrative divergences last hours, not milliseconds.

**Don't compete on frequency — compete on regime selection.** A strategy running 40% of days at Sharpe 2.0 beats one running 100% at Sharpe 0.8.

## Decision framing

- **Crypto MFT Sharpe lives in perp venues and funding**, not spot reversion. Funding (#3) is the strategic bet.
- **Ship #1 first** — the strategy is commoditized, but it's the vehicle to operationalize omo's differentiating factors (inducement, TFI, cross-venue flow) in crypto before committing to the HL adapter build.
- **Published Sharpes halve live.** Budget +5bps slippage tax on every backtest number.
- **Skip #4 and #5 unless #3 proves out.** They stack infra cost on top of already-crowded strategies.

## Recommended 6-week plan

```
Weeks 1-2: Ship #1 + inducement + TFI gates (Sharpe 1.0-1.7 target)
           Engine: MarketTrade.TakerSide, Alpaca WS wiring, SessionVWAP, TFI
Weeks 3-4: Hyperliquid adapter (read-only) + funding data layer + cross-venue
           taker-flow aggregator (bonus to #1, foundation for #3)
Weeks 5-6: Ship #3 with Deribit skew-regime gate (Sharpe 1.3-2.3 target)
           Simbroker FundingEvent + HL order submission + paper trade
```

Result: **two-strategy crypto MFT book with genuine differentiation** in 6 weeks.

## Factors omo has that generic retail quants don't

| Factor | Status | Alpha uplift | Applies to |
|---|---|---|---|
| Inducement detector | in equities, ports to crypto | +0.3-0.6 Sharpe on #1 | #1, future MM |
| Trade-flow imbalance (TFI) | to build | +0.2-0.3 Sharpe | all |
| Cross-venue taker flow | to build post-HL | +0.3-0.5 Sharpe | #1, #3 |
| Whale 13F / on-chain ETF flows | 13F for equities, port pattern | +0.2-0.4 Sharpe | #2, #4 |
| Deribit options skew / RR | new adapter | drawdown -30-50% | #3, #4 |
| Session-time weighting | implemented | +0.1-0.2 Sharpe | all |
| **Cross-strategy confluence scoring** | infra exists | **+0.5-1.0 Sharpe** when stacked | all |

The architecture to combine these at trade-entry level is the actual moat.
