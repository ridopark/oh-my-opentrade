# Pattern Day Trader (PDT) enforcement

## What it is

FINRA Rule 4210: a broker-dealer customer account flagged as a Pattern
Day Trader must maintain equity of at least $25,000. Below that
threshold, the account is restricted to three day trades in any rolling
five-business-day window. A fourth same-window day trade freezes the
account for 90 days (or until equity is topped up).

A "day trade" is defined as the round-trip (open + close) of the same
security within the same trading session.

## How we detect it

Two components under `backend/internal/app/risk/`:

- `PDTTracker` — in-memory FIFO lot tracker. `RecordOpen` registers a
  new opening fill; `RecordClose` matches it against queued open lots
  and, when `opened_at` and `closed_at` fall on the same date, records
  a day trade against `(account_id, trading_date)`. Each completed
  same-day round-trip is also persisted to the `day_trades` hypertable
  via `DayTradeSink` (`timescaledb.DayTradeRepo`).

- `PDTGuard` — the gate-facing wrapper. At decision time it reads the
  `PatternDayTrader` flag from `ports.AccountPort.GetAccountBuyingPower`
  and account equity from a pluggable `EquitySource`, then consults
  `PDTTracker.HasSameDayOpen` and `DayTradeCount` to decide.

The fill-event hook that pushes opens/closes into `PDTTracker` is
**not yet wired** at the execution service — until that hook lands,
`PDTTracker.DayTradeCount` returns 0 and the gate passes through. This
is deliberate and documented in `EQUITY-OPTIONS-GAP-PLAN.md §Sprint 4.5`.

## What the gate blocks

`pdt_guard` (`backend/internal/app/gate/exec_pdt.go`) rejects an order
intent when ALL of the following hold:

1. `TradingConfig.PDTEnforcement = "strict"` (default empty string means
   "off" — operators must opt in).
2. `BuyingPower.PatternDayTrader = true`.
3. Account equity < $25,000.
4. The intent is an exit on a symbol where the tracker holds a
   same-day open lot — i.e. closing it would complete a same-day
   round-trip.
5. `PDTTracker.DayTradeCount(account, today) >= 3`.

Entries are never blocked by this gate — an entry alone cannot create
a round-trip.

## Config

```yaml
trading:
  pdt_enforcement: "strict"  # "off" disables; "" defaults to off
```
