# Engine Changes Summary

Grouped by subsystem. Each change lists dependent strategies and effort.

## Summary by strategy

| Strategy | Engine change cost |
|---|---|
| #1 crypto_revert_v1 phase 1 | 1a (5h) + 3a (0.5d) + inducement port (1d). **~2 days.** |
| #1 crypto_revert_v1 phase 2 | + 3b, 3c (Deribit), 2c (cross-venue), 7a. **+4-5 days.** |
| #2 phase 1 | + 4a (pair runner, 2d) + whale adapter (3-4d). **~6 days.** |
| #2 phase 2 | + 1b, 2c, 3b, 4d, 5b, 7a. **+5 days on top of phase 1 + #3.** |
| #3 funding_timer | 1c, 1d, 2a, 2c, 3b, 3c, 3d (FundingEvent), 5b, 6a, 7a, 8a + Deribit. **~10 days on top of prior work.** |
| #4 basis_carry | + 1b, 3d (dual-venue + paired), 4d, 5c, 6c. **~5 days on top of #3.** |
| #5 xsec_momo | + 2b, 4b, 5a, 6b, 7a, 8b. **~8-10 days.** |

## 1. Domain layer

- **1a.** `MarketTrade.TakerSide` — #1 — 5h
- **1b.** `OrderIntent.LegGroupID` + paired semantics — #2 phase 2, #4 — 1-2d
- **1c.** New `FundingRate` + `FundingEvent` types — #3, #4 — 4h
- **1d.** Venue-qualified symbols (`QualifiedSymbol{Venue, Symbol}`) — #3, #4, #5 — 2-3d

## 2. Ports layer

- **2a.** `FundingRatesPort` — #3, #4 — 2h
- **2b.** `OpenInterestPort` — #5 — 2h
- **2c.** Broker extensions: `FeeTier()`, `SubmitPairedOrder()` — #3, #4 — 1d
- **2d.** `OptionsIVPort` (Deribit) — #3, #4, #1 phase 2 — 2h
- **2e.** `WhaleFlowPort` (on-chain) — #2, #3, #5 — 2h

## 3. Adapter layer

- **3a.** Alpaca crypto WS taker-side plumbing — #1 — 0.5d
- **3b.** Hyperliquid adapter (new package) — #3, #4, #2p2, #5 — 4-5d
- **3c.** Bybit funding read-only — #3 research — 1d
- **3d.** Simbroker upgrades (FundingEvent, dual-venue, fee tiers, bid/ask fills, paired atomic) — all — 3-4d phased
- **3e.** Deribit adapter — #3, #4 — 2d
- **3f.** On-chain custodian flow adapter — #2, #3, #5 — 3-4d

## 4. Strategy framework

- **4a.** `PairStrategy` interface + synchronized pair runner — #2 — 2d
- **4b.** `CrossSectionalStrategy` interface + universe runner — #5 — 4-5d
- **4c.** Spec loader registrations — all — trivial
- **4d.** Risk sizer paired-group awareness — #2p2, #4 — 1d

## 5. Backtest engine

- **5a.** Batch bar events in pipeline shard — #2, #5 — 3-4d
- **5b.** Funding replay — #3, #4 — 2d
- **5c.** Dual-venue bar synchronization — #4 — 1-2d
- **5d.** FreezeHandler ordering audit — #2, #5 — 1d

## 6. Storage

- **6a.** `funding_rates` hypertable — #3, #4 — 2h
- **6b.** `open_interest` hypertable — #5 — 2h
- **6c.** Position table venue column — #4 — 0.5d
- **6d.** `iv_snapshots` hypertable — #3, #4 — 2h
- **6e.** `whale_flows` hypertable — #2, #3, #5 — 4h

## 7. Event bus

- **7a.** New event types (FundingEvent, PairedFillEvent, CrossSectionalBarEvent) — 1d total

## 8. Ingestion

- **8a.** Funding backfill + live jobs — #3, #4 — 1-2d
- **8b.** OI ingestion — #5 — 1d
- **8c.** IV snapshot live poller — #3, #4 — 1d
- **8d.** On-chain whale flow poller — #2, #3, #5 — 2d

## 9. Dashboard

- Funding rate panel per venue/symbol
- Paired-position rendering (group by LegGroupID)
- Venue attribute on positions and fills
- IV surface + skew regime display
- Whale-flow signal panel
- Effort: ~4-5 days UI work, can lag backend

## Biggest engine risks

1. **Venue-qualified symbols (1d)** — touches every layer. Decide early whether to retrofit now or carry parallel venue field forward.
2. **Batch bar dispatch (5a)** — shard-barrier design is the hardest call. Get it right once; everything ranking-based rides on it.
3. **Paired-fill atomicity (3d + 4d)** — rollback semantics when one leg rejects. Production-grade requires compensating-order logic, not just "cancel the other side."
4. **FreezeHandlers ordering (5d)** — the dual-pipeline bug pattern recurs anywhere new subscribe-after-freeze code lands. Audit every new runner path.

## Do-this-first PR sequence (maximizes unlocked surface)

1. **Gap 1 + 3a + register #1 phase 1** — 2 days, ships revert strategy with TFI
2. **Inducement port to crypto** — 1 day, upgrades #1 to differentiated
3. **Venue-qualified symbols (1d)** — 2-3 days, unlocks cross-venue cleanly before HL lands
4. **Funding data layer (1c, 2a, 6a, 8a) + Bybit read-only (3c)** — 3 days, research phase
5. **Hyperliquid adapter (3b)** — 4-5 days, unlocks everything else
6. **Deribit adapter + skew classifier (3e, 2d, 6d, 8c)** — 3 days, the differentiator gate
7. **Cross-venue flow aggregator (Gap 9)** — 2 days, upgrades #1 to phase 2
8. **Simbroker FundingEvent (3d.1)** — 2 days, enables credible #3 backtest
9. **Ship #3 with full gates** — 3 days

Sequence total: ~20-22 days from zero to paper-trading two differentiated strategies (#1 stacked + #3 with skew gate).
