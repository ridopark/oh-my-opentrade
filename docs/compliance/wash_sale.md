# Wash-sale journal (IRS §1091)

## What it is

IRS §1091: a realized loss on a security is disallowed for tax purposes
if the taxpayer acquires a "substantially identical" security within 30
days before or after the sale. The disallowed loss is added to the cost
basis of the replacement lot rather than being deducted in the current
year.

This is a **tax** rule, not a trading rule. Nothing about it requires
us to block orders — brokers, exchanges, and regulators don't care. The
accountant does, at year end.

## How we detect it

`backend/internal/app/compliance/wash_sale.go` — `WashSaleJournal`
exposes `OnRealizedLoss(LossEvent)`. Callers hook it into whatever
emits realized P&L (the execution service's `LedgerWriter` is the
natural candidate). On each loss event, the journal calls
`BuyLookup.BuysInWindow(symbol, at-30d, at+30d)`, filters out the buy
leg of the losing trade itself, and writes one `WashSaleRow` per
remaining match via `JournalSink.RecordWashSale`.

The window boundary is inclusive: a buy exactly 30 days before (or
after) the loss counts; 31 days does not.

Sink: `timescaledb.WashSaleRepo` → `wash_sales` table (migration
`034_create_wash_sales`).

## What it does NOT do

**No gate.** This is observational. The journal never blocks, cancels,
or rejects an order. Operators and accountants consume the `wash_sales`
table out-of-band for year-end reconciliation.

## Config

No config. The journal is always-on wherever a `JournalSink` and
`BuyLookup` are wired.
