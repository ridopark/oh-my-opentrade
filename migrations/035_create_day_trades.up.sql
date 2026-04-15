-- Sprint 4.5: PDT day-trade tracker hypertable.
--
-- A "day trade" is a round-trip (open+close) of the same symbol in the
-- same trading session. The PDT rule (FINRA) restricts accounts flagged
-- PatternDayTrader with equity < $25k to fewer than 4 day trades in any
-- rolling 5 business-day window.
--
-- We record each observed round-trip here. The gate counts rows for
-- (account_id, trading_date) and rejects the 4th same-day round-trip.

CREATE TABLE IF NOT EXISTS day_trades (
    account_id    TEXT NOT NULL,
    trading_date  DATE NOT NULL,
    symbol        TEXT NOT NULL,
    qty_traded    INT  NOT NULL,
    opened_at     TIMESTAMPTZ NOT NULL,
    closed_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (account_id, trading_date, symbol, opened_at, closed_at)
);

-- TimescaleDB hypertable on closed_at for time-partitioning. The PK
-- includes closed_at which is also the partition key.
SELECT create_hypertable('day_trades', 'closed_at', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_day_trades_account_date
    ON day_trades (account_id, trading_date);
