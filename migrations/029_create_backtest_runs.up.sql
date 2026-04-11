-- Durable history of completed backtest runs so the dashboard can browse,
-- filter, and compare results across restarts. Config snapshot + headline
-- metrics + drill-in payloads live on the parent row; per-trade detail is
-- split into a child table to keep list queries cheap and because a single
-- run can produce 1k-3k trades.

CREATE TABLE IF NOT EXISTS backtest_runs (
    id              UUID PRIMARY KEY,
    ran_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Config snapshot (captured at run-start, frozen on the row so later
    -- strategy/DNA edits do not retroactively change history).
    strategies      TEXT[] NOT NULL,
    symbols         TEXT[] NOT NULL,
    period_start    TIMESTAMPTZ NOT NULL,
    period_end      TIMESTAMPTZ NOT NULL,
    initial_equity  DOUBLE PRECISION NOT NULL,
    slippage_bps    INTEGER NOT NULL,
    no_ai           BOOLEAN NOT NULL DEFAULT false,

    -- Headline metrics surfaced in the history list.
    pf              DOUBLE PRECISION NOT NULL,
    win_rate        DOUBLE PRECISION NOT NULL,
    expectancy      DOUBLE PRECISION NOT NULL,
    max_drawdown    DOUBLE PRECISION NOT NULL,
    sharpe          DOUBLE PRECISION NOT NULL,
    trade_count     INTEGER NOT NULL,
    win_count       INTEGER NOT NULL,
    loss_count      INTEGER NOT NULL,
    net_pnl         DOUBLE PRECISION NOT NULL,
    total_return    DOUBLE PRECISION NOT NULL,
    final_equity    DOUBLE PRECISION NOT NULL,

    -- Detail payloads, only read on drill-in.
    equity_curve    JSONB NOT NULL,   -- [{t: unix, eq: float}, ...] daily samples
    dna_snapshot    JSONB NOT NULL,   -- {strategyId: {params...}, ...}

    -- User-editable.
    tags            TEXT[] NOT NULL DEFAULT '{}',
    pinned          BOOLEAN NOT NULL DEFAULT false,
    notes           TEXT
);

CREATE INDEX IF NOT EXISTS idx_backtest_runs_ran_at
    ON backtest_runs(ran_at DESC);

CREATE INDEX IF NOT EXISTS idx_backtest_runs_strategies
    ON backtest_runs USING GIN(strategies);

CREATE INDEX IF NOT EXISTS idx_backtest_runs_symbols
    ON backtest_runs USING GIN(symbols);

CREATE INDEX IF NOT EXISTS idx_backtest_runs_tags
    ON backtest_runs USING GIN(tags);

CREATE INDEX IF NOT EXISTS idx_backtest_runs_pinned
    ON backtest_runs(pinned) WHERE pinned;


-- One row per fill (entry or exit), mirroring collector.TradeRecord. Kept as
-- fills rather than round-trips so we don't duplicate pairing logic from the
-- collector; the UI can pair entries with exits at render time if needed.
CREATE TABLE IF NOT EXISTS backtest_run_trades (
    run_id           UUID NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
    seq              INTEGER NOT NULL,   -- 0-based order within the run
    symbol           TEXT NOT NULL,
    side             TEXT NOT NULL,      -- BUY | SELL
    direction        TEXT,               -- LONG | SHORT | CLOSE (for round-trip pairing)
    quantity         DOUBLE PRECISION NOT NULL,
    price            DOUBLE PRECISION NOT NULL,
    filled_at        TIMESTAMPTZ NOT NULL,
    pnl              DOUBLE PRECISION NOT NULL DEFAULT 0,
    strategy_id      TEXT,
    rationale        TEXT,
    regime           TEXT,
    vix_bucket       TEXT,
    market_context   TEXT,
    PRIMARY KEY (run_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_backtest_run_trades_symbol
    ON backtest_run_trades(symbol);
