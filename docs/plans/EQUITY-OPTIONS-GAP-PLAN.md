# Equity + Options Gap-Closure Plan

> Created: 2026-04-15
> Scope: non-crypto gaps identified by the codebase-analysis audit
> Partner docs: [`docs/plans/ROADMAP.md`](ROADMAP.md), [`docs/plans/SPRINT_4_PLAN.md`](SPRINT_4_PLAN.md),
> [`docs/MFT-crypto-strategies/SHARED-INFRA.md`](../MFT-crypto-strategies/SHARED-INFRA.md)

This plan closes 12 gaps in the equity + options side of the system that either
degrade live P&L, overstate backtest edge, or risk regulatory breach. Four are
already on the roadmap (Sprint 4, 5, 7). Eight are new. None of them argue for
a Bloomberg-class data vendor; the few that need paid data are satisfied by
Theta Data (~$40-160/mo) or free public sources.

Crypto infrastructure is planned separately in
[`SHARED-INFRA.md`](../MFT-crypto-strategies/SHARED-INFRA.md) and runs on a
parallel track.

---

## Quick state table

| # | Gap | Impact | Effort | Track | Status |
|---|---|---|---|---|---|
| G1 | Portfolio heat / sector / directional risk gates | Critical | 3-4d | Sprint 4 | 📋 detailed plan exists |
| G2 | 3-state kill switch (ACTIVE / REDUCING / HALTED) | Critical | 1d | Sprint 4 | 📋 planned |
| G3 | PDT / Reg-T enforcement, wash-sale journal | High (compliance) | 3-5d | **NEW Sprint 4.5** | 🆕 |
| G4 | Live options IV / Greeks via Theta Data | High (backtest honesty) | 3-5d + vendor | **NEW Sprint 6.1** | 🆕 |
| G5 | Backtest fidelity (look-ahead, latency, option fees) | High | 1w | Sprint 7 | 📋 planned |
| G6 | Option entry-side bid-ask spread realism | Medium | 2-3d | **NEW Sprint 6.2** | 🆕 |
| G7 | Multi-leg (BAG) combo orders | High (unlocks strategies) | 2-3w | Sprint 5 | 📋 planned |
| G8 | Earnings blackout gate | Medium | 1-2d | **NEW Sprint 4.6** | 🆕 |
| G9 | Macro event calendar (FOMC / CPI / NFP) | Medium | 2-3d | **NEW Sprint 4.6** | 🆕 |
| G10 | Corporate actions on historical option chains | Medium | 2-3d | **NEW Sprint 6.3** | 🆕 |
| G11 | Survivorship-bias guard on backtest universe | Medium | 2-3d | **NEW Sprint 7 add-on** | 🆕 |
| G12 | Early assignment / dividend-ex-date modeling | Medium (conditional) | 4-5d | Deferred | ⏸ |

Parking lot (no immediate ROI, listed for honesty): dark-pool ATS attribution,
NBBO quote streams, Form 4 insider data, FINRA short interest, multi-account
execution, chaos test harness, NTP-validation on startup, audit-log table for
manual overrides, dashboard per-strategy attribution.

---

## Track A — Risk gates (Sprint 4 + 4.5 + 4.6)

These ship first. They're the only items where a gap, if exploited by a bad
day, loses real money or invites regulatory action. Everything else can wait.

### Sprint 4 — aggregate risk + kill switch (existing plan, execute)

- `portfolio_heat_guard` — sum of open per-trade risk as % of equity; hard cap
- `sector_exposure_guard` — max N% of equity in any sector bucket
- `directional_bias_guard` — cap on net long-short delta across the book
- `DailyLossBreaker` gains a REDUCING state between ACTIVE and HALTED —
  in REDUCING, strategies can only submit exit orders, no new entries

Detailed scope lives in `SPRINT_4_PLAN.md`. Execute that first before any new
work below.

**Gate:** 24h paper run with all four new gates wired, each firing at least
once on synthetic stress inputs.

### Sprint 4.5 — regulatory enforcement (new)

Three discrete adapters, one new table.

1. **PDT counter** — extend `execution.Service` with a day-trade counter
   keyed on (account_id, trading_date). Read `pattern_day_trader` flag from
   IBKR account summary on boot. If flag is true AND account equity < $25K
   AND today's day-trade count >= 3, reject new day-trade entries at the
   gate (new `pdt_guard`).
2. **Reg-T margin check** — at order submission, compute post-fill margin
   usage against broker `EffectiveBuyingPower`. Reject if it would breach
   50% initial / 25% maintenance.
3. **Wash-sale journal** — new `wash_sales` hypertable. On every realized
   loss, scan the prior 30 days and post 30 days for same-symbol buys that
   trigger wash-sale disallowance. Write a journal row; tax export tool
   reads from it.

**Gate:** synthetic test suite proving all three gates fire correctly;
docs/compliance/ folder gets a one-pager for each rule.

### Sprint 4.6 — event-aware execution gates (new)

Two adapters + one new gate.

1. **`earnings_blackout_gate`** — reads existing `earnings_calendar` table
   (Finnhub loads it daily). If current symbol has an earnings announcement
   in (-1, +1) trading days, reject entry intents. Configurable per strategy
   in DNA TOML: `earnings_blackout = "strict" | "permissive" | "off"`.
2. **Economic calendar port** — new `MacroCalendarPort` served by a FRED +
   Finnhub adapter. Fields: event name, scheduled time, impact bucket
   (high/medium/low), actual vs consensus when released.
3. **`macro_event_gate`** — reject entries within ±30 min of high-impact
   events (FOMC rate decisions, FOMC minutes, CPI, PPI, NFP, PCE). Exits
   and stops are always allowed.

**Gate:** both gates fire on a historical replay of a known macro day
(pick any recent FOMC) and an earnings day.

---

## Track B — Options correctness (Sprint 5 + 6.1 + 6.2 + 6.3)

Options strategies are deprecated today (ORB sunset 2026-04-12) but the
roadmap calls Sprint 5 "the biggest capability unlock per effort." This
track is the prerequisite stack to make options a first-class strategy
class again.

### Sprint 5 — multi-leg BAG combo orders (existing plan, execute)

Already scoped in the roadmap. Ship the IBKR BAG contract wrapper, extend
`OrderIntent` to hold legs with ratios, teach every gate about net-notional
vs per-leg notional, update the position monitor to track combo P&L as a
unit.

**Gate:** paper-execute a vertical call spread, read back the position,
confirm P&L tracks per combo not per leg.

### Sprint 6.1 — live options IV / Greeks feed (new)

1. Sign up for Theta Data (entry tier ~$40-80/mo, step up if needed).
2. Write `adapters/thetadata/` — REST client + WebSocket for option NBBO,
   IV, Greeks, and OI per contract. Follow the pattern in
   `adapters/alpaca/options_rest.go`.
3. New `OptionMarketDataPort` interface (or extend existing); promote Theta
   Data to primary, Alpaca snapshots to fallback, DoltHub to historical
   backfill only.
4. Replace `debate/service.go:517-531` synthetic-IV-from-ATR path with a
   live lookup when Theta is reachable; keep ATR fallback for resilience.
5. Replace `simbroker/broker.go:computeOptionExitPrice` tiered-spread
   approximation with Theta-sourced bid/ask for same-day exits.

**Cost:** $40-160/mo depending on tier. Audit after 30 days; downgrade if
we don't use the higher-tier endpoints.

**Gate:** backtest a closed ORB trade (pick one from March data), compare
synthetic-IV P&L vs Theta-sourced P&L, document the delta.

### Sprint 6.2 — entry-side option spread realism (new)

Today `simbroker/broker.go:639-650` applies a tiered half-spread only at
exit, not entry. Fix: apply the same spread model at entry — taking buyer
pays ask, seller receives bid. Once 6.1 ships, replace the tier table with
Theta-sourced live spread.

**Gate:** a before/after diff on ORB backtest P&L shows entry spread now
reduces win rate by 2-8% (matches the overstatement we currently have).

### Sprint 6.3 — corporate actions on historical option chains (new)

1. New `corporate_actions` hypertable — (symbol, action_type, effective_date,
   ratio, cash_component). Populated from IBKR corporate actions feed +
   Alpaca splits endpoint.
2. Extend `domain/historical_option_chain.go` loader to apply retroactive
   strike adjustments when spanning an action date.
3. Add a survivorship filter: if underlying is delisted after a trade's
   entry date, mark chain rows as "post-delisting" so backtest can exclude
   them cleanly.

**Gate:** backtest across a known 2024 split date produces contract
selections that match real strikes pre- and post-split.

---

## Track C — Backtest fidelity (Sprint 7 + add-on)

### Sprint 7 — fill model, latency, fees (existing plan, execute)

Implement per plan. Three items:

1. Pluggable fill models: `optimistic` (current behavior), `realistic`
   (next-bar open for market orders, mid-minus-buffer for limits),
   `pessimistic` (next-bar-open plus slippage).
2. Explicit latency budget: order submission adds configurable delay
   before fill evaluation (default 50ms for equities, 200ms for options).
3. Fee schedules: Alpaca zero-commission equity, IBKR tiered options
   ($0.65/contract + exchange fees), SEC/TAF/FINRA pass-throughs.

Look-ahead audit: the same-bar exit issue documented in
`AVWAP_CHANDELIER_REALISM_ASSESSMENT_2026-04-14.md` §3 is closed by
switching AVWAP-style exits to the `realistic` fill model.

**Gate:** regression on all strategies with `realistic` fills; compare
against live paper over the same window. Sharpe delta should be <0.3
if strategy is truly alpha-bearing.

### Sprint 7 add-on — survivorship bias (new)

Add a `universe_history` table with (date, symbol) rows proving a symbol
was tradable on that date. Backtest loader filters bars by universe
membership. Seed from an IEX/Polygon universe snapshot or an IBKR universe
pull. Delisted tickers get proper termination dates.

**Gate:** run the AVWAP backtest over a 2-year window that includes a
known delisting (pick one from S&P 500 2024 changes). Equity curve should
not include the delisted ticker's price-goes-to-zero run.

---

## Track D — Deferred (explicitly parked)

These are real gaps, but paying them back now has low ROI relative to
Tracks A-C. Reopen if/when the trigger event happens.

| Gap | Reopen trigger |
|---|---|
| G12 Early assignment / dividend-ex-date | We ship a short-options strategy (covered calls, cash-secured puts) |
| Dark-pool ATS attribution | Evidence our current DP confluence is saturated and misses trades |
| NBBO quote stream | We add a microstructure signal (VPIN, CLOB imbalance) |
| Form 4 / insider data | Research validates it as a 13F-complementary signal |
| FINRA short interest | We add a short-squeeze or hard-to-borrow strategy |
| Multi-account execution | AUM or business case demands running two IBKR gateways |
| Chaos/fault-injection test harness | A production incident is traced to an untested disconnect path |
| NTP validation on startup | Compliance audit requires timestamped order-event trail |
| Audit log table for manual overrides | Regulatory or LP pressure surfaces |
| Dashboard per-strategy attribution | Live page gets used enough for attribution gaps to sting |

---

## Sequencing

```
(now) Sprint 3.5  — order journal flag removal (gated on Monday run)
       │
       ├─ Track A ─ Sprint 4  — risk gates + 3-state kill switch
       │             │
       │             └─ Sprint 4.5 — PDT/Reg-T/wash-sale
       │             └─ Sprint 4.6 — earnings + macro calendar gates
       │
       ├─ Track C ─ Sprint 7 — fill model / latency / fees
       │             │
       │             └─ Sprint 7 add-on — survivorship bias
       │
       └─ Track B ─ Sprint 5 — BAG combo orders
                     │
                     └─ Sprint 6.1 — Theta Data integration
                     └─ Sprint 6.2 — entry-side option spread
                     └─ Sprint 6.3 — corporate actions
```

Track A is the priority ladder: the risk gaps cost real money if exploited,
and Sprint 4 already has a detailed plan. Track B is the capability unlock —
nothing here bites today because options aren't running, but nothing big
happens on the options side until BAG + Theta ship. Track C improves trust
in every backtest-driven decision we make but doesn't directly change live
P&L, so it runs in parallel with whichever engineer isn't on Track A.

Realistic calendar if one engineer works through this serially:
- **Weeks 1-2**: Sprint 4 + 4.5 + 4.6 (risk + compliance + event gates)
- **Weeks 3-4**: Sprint 7 + add-on (backtest honesty)
- **Weeks 5-7**: Sprint 5 (BAG combos — big, careful rollout)
- **Weeks 8-9**: Sprint 6.1 + 6.2 + 6.3 (options data + realism + CA)

With two engineers, Tracks A and C run in parallel and the full thing lands
in ~6 weeks. Crypto infra runs on its own track at the same time.

---

## Cost summary

| Item | One-time | Recurring |
|---|---|---|
| Theta Data (Sprint 6.1) | — | $40-160/mo |
| FRED API (Sprint 4.6) | — | Free |
| Everything else | — | Free |

No data-vendor spend beyond Theta Data. Bloomberg is not on this plan and
should not be reopened until the AUM-or-LP-gate in
[`SHARED-INFRA.md`](../MFT-crypto-strategies/SHARED-INFRA.md) trips.

---

## Gate for adding new gaps

New gaps discovered during execution get logged to the parking lot in this
doc. A gap graduates into an active sprint only if:

1. It blocks a strategy that has a real paper-validated edge, OR
2. It has caused an incident in the last 30 days, OR
3. It is a compliance / regulatory item with a firm date.

Otherwise it stays parked until a sprint has headroom.
