# Parity Observability Follow-up

After the live-vs-backtest parity investigation closes out, promote the
ad-hoc `parity-diag *` log lines to a real observability layer.

## Trigger to start

Open this when:
- The remaining unexplained live picks (MRVL, SNOW, RIVN) have been root-
  caused, OR
- The team needs ongoing parity SLO tracking, OR
- Log volume from `parity-diag *` lines becomes a maintenance burden.

## Recommended approach (option 1+3 hybrid)

`parity_observations` TimescaleDB hypertable + selected Prometheus gauges,
fed from a single `ParityRecorder` interface.

### Schema sketch

```sql
CREATE TABLE parity_observations (
    ts          TIMESTAMPTZ NOT NULL,
    env_mode    TEXT NOT NULL,          -- 'Paper' | 'Live' | 'Backtest'
    backtest_id TEXT,                   -- NULL for live
    symbol      TEXT NOT NULL,
    strategy    TEXT NOT NULL,
    stage       TEXT NOT NULL,          -- 'BarReceived' | 'IndicatorSnapshot'
                                        -- | 'EntryGated' | 'SignalCreated'
                                        -- | 'RiskSized' | 'OrderSubmitted'
                                        -- | 'FillRecorded'
    payload     JSONB NOT NULL
);
SELECT create_hypertable('parity_observations', 'ts');
CREATE INDEX ON parity_observations (symbol, ts DESC);
CREATE INDEX ON parity_observations (backtest_id, ts) WHERE backtest_id IS NOT NULL;
-- 30-day retention; live and backtest both write here.
```

### Interface seam

```go
type ParityRecorder interface {
    Record(ctx context.Context, stage string, symbol string, payload map[string]any)
}
```

Wired via DI at startup. Live impl: writes to DB + Prom gauges. Backtest
impl: writes to DB only. Tests: no-op.

Diag sites collapse to single-line calls:
```go
r.parity.Record(ctx, "EntryGated", sym, map[string]any{
    "score": p.Confluence.Score,
    "components": p.Confluence.Components,
    "avwap_anchors": p.AVWAPState,
    "dp_rolling_mean": rollingMean,
    ...
})
```

### Stages to instrument

From the investigation discussion 2026-04-27:
1. BarReceived — symbol, time, OHLCV, source (WS vs DB)
2. IndicatorSnapshot — RSI, EMA9/21/50/200, VWAP, ATR, regime
3. HTFAggregation (5m bar boundary) — OHLCV
4. EntryGated — already captured by current parity-diag log
5. SignalCreated — already captured by current parity-diag log
6. RiskSized — symbol, signal strength, qty, premium, contract chosen
7. OrderSubmitted — broker_order_id, side, qty, limit, instrument
8. FillRecorded — broker_order_id, fill_qty, fill_price

### Cardinality budget

- 34 symbols × 3 strategies × 2 env modes = ~200 series per Prom metric.
- ~10 metrics → 2k series. Comfortable.
- DB volume: ~2.5M rows/day across all stages at peak. Hypertable +
  30-day retention handles this.

### Grafana dashboards (post-implementation)

- "Live vs latest backtest, per symbol, slope_bps drift over time"
- "Per-stage event count, live vs backtest, divergence highlighted"
- "DP rolling mean drift between live and backtest, per symbol, last 5 days"

## Cleanup of current diag

When this follow-up ships:
- Remove `parity-diag EntryGated` / `parity-diag SignalCreated` log
  emits in `runner.go:emitDomainEvent` and `runner.go:emitSignal`.
- Remove the `json` import added for those lines if no other use.
- Remove the dpRolling snapshot helper inlined in those sites.

## Cross-references

- Today's parity fixes:
  - `44bed1be` UTC normalization in livedarkpool
  - `65fa2511` partial-cache DB fallback in backtest's SessionResolver
  - `1c415107` open-qty guard in execution.insertFillLeg
- Diag log lines deployed (uncommitted): runner.go emitDomainEvent /
  emitSignal blocks captured 2026-04-27 evening for MRVL/all-symbols
  investigation.
