# Robustness + Capability Roadmap

> Last updated: 2026-04-11
> Source docs: [`ROBUSTNESS.md`](../../tmp/others/ROBUSTNESS.md), [`COMPARISON.md`](../../tmp/others/COMPARISON.md), [`GHOST_PRINTS_RESEARCH.md`](../../tmp/others/GHOST_PRINTS_RESEARCH.md)
> Active branch: `feat/robustness-sprint-1`

This is the single top-level index for all sprint work coming out of the 6-repo research phase (`tmp/others/*.md`). It replaces the "sprint list in my head" and gives every downstream plan a home.

---

## Quick state table

| Sprint | Name | State | Plan | Notes |
|--------|------|-------|------|-------|
| 1 | Robustness quick wins | ✅ shipped | [SPRINT_1_PLAN.md](SPRINT_1_PLAN.md) | 4 commits + 2 follow-ups |
| 2 | Persistent order journal + startup reconciliation | ✅ shipped | [SPRINT_2_PLAN.md](SPRINT_2_PLAN.md) | 2 commits, feature-flagged |
| 3 | Sprint 3 cleanup | ✅ shipped | (no dedicated plan) | Config move + notifier plumbing — done conversationally |
| — | Sprint 3 discovery: alpaca nil-guard fix | ✅ shipped | — | Found during live testing, commit `5a8f6a8` |
| — | Live validation (Saturday) | ✅ complete | — | 13 test cases validated against real IBKR + DB |
| — | Monday live validation under organic signals | ⏳ pending | — | Only gate-keeper before flag removal |
| 3.5 | Flag removal (`OMO_ORDER_JOURNAL_ENABLED` → always on) | 🔜 next | [SPRINT_3_5_PLAN.md](SPRINT_3_5_PLAN.md) | Trigger: 24h clean Monday run |
| 4 | Risk management gates | 📋 planned | [SPRINT_4_PLAN.md](SPRINT_4_PLAN.md) | 4 new gates + 3-state kill switch |
| 5 | Options execution unlock (BAG combo orders) | 📋 planned | *(draft when ready)* | Biggest capability unlock per effort |
| 6 | Signal quality (block filter, NaN fallbacks, regime weighting) | 📋 planned | *(draft when ready)* | Grounded in `GHOST_PRINTS_RESEARCH.md` |
| 7 | Backtest fidelity (fill models, bar decomposition, fee modeling) | 📋 planned | *(draft when ready)* | Improves strategy selection trust |
| 8+ | Nice-to-haves (retry queue, FSM, state persistence) | 📋 backlog | — | Only if live evidence demands |

Legend:
- ✅ shipped — merged + pushed + validated
- 🔜 next — ready to execute, gated on a single trigger
- 📋 planned — scope known, detailed plan not yet written
- ⏳ pending — waiting on a passive gate (like market hours)

---

## Dependency graph

```
Sprint 1 ─┐
          ├─> Sprint 2 ─> Monday validation ─> Sprint 3.5 ─> Sprint 4 ─> Sprint 5 ─┐
Sprint 3 ─┘                                                                        ├─> Sprint 6
                                                                                   └─> Sprint 7
```

Hard dependencies:
- **Sprint 3.5 blocks on Monday validation.** The flag exists specifically to de-risk the journal. Remove it only after 24h of real signals prove the journal path works end-to-end.
- **Sprint 4 does NOT block on 3.5** — the risk gates are orthogonal to the journal. Could be run in parallel if you wanted two active branches.
- **Sprint 5 does NOT block on Sprint 4** either — BAG combo orders are a broker-adapter change, independent of the gate chain.
- **Sprints 6 and 7 have no hard blockers** on earlier sprints. Can be parallelized after Sprint 4/5 land.

Soft dependencies (not blockers, but cleaner ordering):
- Ship Sprint 3.5 first so the journal becomes the default assumption in all future docs.
- Do Sprint 4 before Sprint 5 because combo orders create new position shapes (spreads) that the new risk gates should already understand — avoids a second pass on the gate chain.

---

## What's already in the ship pipeline

All of this is on `feat/robustness-sprint-1` pushed to origin, 15 commits total:

1. `f69b069` panic recovery + shutdown flag
2. `5074526` order drain on shutdown
3. `f904165` reconnect escalation
4. `aa58953` liveness supervision (Docker HEALTHCHECK + systemd unit)
5. `ec74491` deliver recovered panics to Discord (follow-up #1)
6. `e22cbb1` feed-age gate on watchdog heartbeat (follow-up #2)
7. `d8b0512` persist order intents before broker submission (Sprint 2 Phase A)
8. `415792e` resume broker open orders from journal on startup (Sprint 2 Phase B)
9. `97bd461` route `OMO_ORDER_JOURNAL_ENABLED` through config.Config (Sprint 3 cleanup A)
10. `35fd296` deliver bootstrap reconciliation alerts to Discord (Sprint 3 cleanup B)
11. `5a8f6a8` alpaca nil-guard Close() fix (discovered during live testing)
12. `6ddfd97` submit-limit-order + cancel-test-orders harness
13. `1beef74` journal-repo-smoke integration tool
14. *(2 earlier chore/test commits that pre-dated this session)*

Plus the sibling branch `tune/mpg-solo-pareto` (2 commits: AVWAP 2→3 / MACD 3→2 tuning + docs).

---

## Per-sprint summaries (for sprints without detailed plans yet)

### Sprint 5 — Options execution unlock (BAG combo orders)

**Goal:** atomic multi-leg option spread execution via IBKR BAG contracts. Unlocks vertical spread strategies that currently cannot be safely executed (leg risk: one leg fills, the other doesn't, you're left naked).

**Scope:**
- Domain: `OrderIntent` needs a `Legs []OrderLeg` field for combo orders, or a separate `ComboIntent` type
- IBKR adapter: build a `Contract{SecType:"BAG"}` with `ComboLegs` from the intent, submit via existing `SubmitOrder` path
- Risk gates: new or extended checks that understand spread notional (max loss = debit paid for long verticals, undefined for short spreads)
- Exit rules: handle the spread as a unit, not leg-by-leg
- Fill handling: the order stream gets one fill event for the combo, which must be decomposed into two position updates
- Anti-ratcheting on rolls (ThetaGang pattern) — when rolling a short put down, never set a new strike higher than the current one

**Reference patterns:** ThetaGang (`thetagang.md` §3), IBKR-trader (`ibkr_trader.md` §4)

**Effort:** Medium-Large. 1–2 weeks. Biggest single capability unlock per unit of work.

**Prerequisites:** none hard; ideally Sprint 4 lands first so the new spread shapes are covered by the new risk gates from day one.

---

### Sprint 6 — Signal quality improvements

**Goal:** incremental alpha + NaN hygiene, grounded in the dark-pool research.

**Scope:**
1. **Block-size filter on `darkpool_bars`** — only aggregate prints >1% of 20d ADV (Hatheway et al. 2017 — the only mainstream academic finding that supports informed content in dark flow)
2. **NaN fallback chains for IBKR market data** — cascading `last → midpoint → close → model price` to prevent NaN propagation into indicator calculations (ThetaGang pattern)
3. **Regime-conditional dark pool weighting** — scale dark pool confluence weight by realized volatility / VIX level (Ye 2022 "amplification effect" — dark informativeness rises with signal precision)

**Reference patterns:** `GHOST_PRINTS_RESEARCH.md`, `thetagang.md` §3

**Effort:** Small-Medium. ~3 days. Each item is a small localized change.

**Prerequisites:** none.

---

### Sprint 7 — Backtest fidelity

**Goal:** make strategy selection more trustworthy so tuning runs find real edges instead of backtest artifacts.

**Scope:**
1. **Pluggable fill model** in `SimBroker` — optimistic (instant at requested price) / realistic (spread-based slippage) / pessimistic (adverse selection). Interface-based so strategies can be validated under all three before shipping.
2. **Bar decomposition** for stop-loss testing — decompose each bar into OHLC price points in adaptive order (O→L→H→C for bullish bars, O→H→L→C for bearish), so intrabar stop triggers are tested. Today a stop can silently pass through a bar that hit it.
3. **Fee modeling per instrument** — per-contract option commissions (currently zero in backtest). Matters: at $0.65/contract with 5 contracts per trade and 100 trades/week, fees alone can eat 20% of a marginal edge.
4. **Concurrent backtest runner** — parallelize parameter sweeps across CPU cores (Nautilus `run_backtests()` pattern).

**Reference patterns:** `nautilus_trader.md` §4

**Effort:** Medium. ~1 week total, items can ship independently.

**Prerequisites:** none.

---

### Sprint 8+ — Nice-to-haves (backlog)

These stay in the backlog until live evidence surfaces a concrete need. Don't build them speculatively.

- **Intent-level retry queue** — transient broker errors (rate limit, socket blip) currently mark `rejected` and give up. A retry queue with exponential backoff would recover automatically.
- **Proper `resumeTracking` with `RegisterResumedOrder` API** — Sprint 2 took the degraded path (log only); upgrade only if live evidence shows the current reconciliation loop misses fills on matched broker orders.
- **Fill disambiguation for `lost` rows** — when reconciliation marks a row `lost`, query broker executions-by-id after the fact and flip to `filled` retroactively if the order actually executed while we were down.
- **Strategy state persistence (`on_save`/`on_load` hooks)** — strategies keep in-memory state (running averages, regime flags, anchors). Today a restart loses it all. Persistence would let strategies survive restarts with accumulated context.
- **Gate decision audit log** — persist every execution-gate rejection with reason + metadata, so operators can debug "why didn't we trade X?" post-hoc. Extends the intent journal.
- **Explicit order state machine** — formalize the implicit 15-state FSM that execution service tracks today. Nautilus pattern. Only worth it if we hit a bug from invalid state transitions.
- **Atomic connection state machine with CAS** — prevents reconnect races in the IBKR adapter. Only worth it if we hit a reconnect race in production.

---

## Decision rules

**"Should I start Sprint N now?"**

| Question | Rule |
|----------|------|
| Is Monday validation complete? | If yes → 3.5 is unblocked. If no → hold on all downstream sprints. |
| Has Sprint 3.5 shipped? | Nice to have before anything else, not a hard blocker. |
| Is the active sprint blocking live P&L? | Always prioritize P&L-critical work over optimization. |
| Does the sprint have a detailed plan? | If no → write it first. Don't dispatch the go-architect without `SPRINT_N_PLAN.md`. |
| Has the go-architect shipped successfully on this class of work before? | Sprints 1 + 2 established that it can. Dispatch with confidence. |

**"When do I write a detailed plan for a future sprint?"**

- Never write more than one sprint ahead in detail. Plans rot.
- Write the detailed plan **in the session where you intend to execute it**, not months before.
- Keep everything further out as one-paragraph summaries in this roadmap.

**"When do I update this roadmap?"**

- After every sprint ships (move to ✅, record the commit list)
- When a new idea surfaces that doesn't fit any existing sprint (add to Sprint 8+ backlog)
- When priorities shift based on live observations (reorder the quick state table)

---

## What's NOT in this roadmap

These were considered and explicitly rejected:

| Pattern | Source | Why skipped |
|---------|--------|-------------|
| Rust + Python hybrid | NautilusTrader | Go single-binary deploy is simpler at our scale |
| LLM-driven trading decisions | TradingAgents | 17–25 LLM calls per decision, no quantitative controls, no backtesting |
| gRPC scanner split | IBKR-trader | Their Go scanner is entirely stubbed; monolith is fine |
| Adversarial debate for risk | TradingAgents | Qualitative only; our gate chain is quantitative and deterministic |
| Crypto-only exchange adapters | barter-rs | Not relevant to equity/options |
| Full actor model | NautilusTrader | Over-engineering for ~34 symbols; goroutines + channels sufficient |
| Apache Feather audit files | NautilusTrader | TimescaleDB is our persistence layer — use SQL tables |
| IBC Watchdog | ThetaGang | Python-specific; systemd watchdog is simpler and language-agnostic |
| Crash-only design (`os._exit`) | NautilusTrader | Go deferred cleanup won't run; prefer graceful shutdown with timeout |

---

## Open questions

These don't block any sprint but are worth resolving eventually:

1. **Deployment mode direction** — Docker today, systemd unit file exists as forward-looking. When do we actually switch? Trigger would be needing proper systemd watchdog (Gap from Sprint 1 already closed via Docker HEALTHCHECK, so no urgency).
2. **Staging environment parity** — does staging have the same journal table + migrations? When does it get Sprint 2 deployed?
3. **Cost of Alpaca dark pool data** — is the SIP feed bill proportional to how much we use it? If yes, Sprint 6 block-size filter also reduces cost.
4. **Who reviews PRs on this branch** — branch is pushed but there are no reviewers set. Are we doing self-review + direct merge, or opening PRs with gh?

---

## References

- [`SPRINT_1_PLAN.md`](SPRINT_1_PLAN.md) — Sprint 1 detailed plan (shipped)
- [`SPRINT_2_PLAN.md`](SPRINT_2_PLAN.md) — Sprint 2 detailed plan (shipped)
- [`SPRINT_3_5_PLAN.md`](SPRINT_3_5_PLAN.md) — Sprint 3.5 detailed plan (next)
- [`SPRINT_4_PLAN.md`](SPRINT_4_PLAN.md) — Sprint 4 detailed plan (planned)
- [`ROBUSTNESS.md`](../../tmp/others/ROBUSTNESS.md) — 10-gap robustness analysis + priority matrix
- [`COMPARISON.md`](../../tmp/others/COMPARISON.md) — 6-repo feature comparison + tiered action items
- [`GHOST_PRINTS_RESEARCH.md`](../../tmp/others/GHOST_PRINTS_RESEARCH.md) — academic research informing Sprint 6
- Per-repo deep dives in `../../tmp/others/`: `nautilus_trader.md`, `thetagang.md`, `ibkr_trader.md`, `barter_rs.md`, `trading_agents.md`, `gofinance_ib.md`
