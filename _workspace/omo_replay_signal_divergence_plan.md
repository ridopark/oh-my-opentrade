# omo-replay vs HTTP backtest signal divergence

Status: DRAFT - pending acknowledgment
Drafted: 2026-05-01
Branch target: main
Related: PR #72 (fill model parity), PR #73 (indicator + AVWAP wiring restoring --backtest runnability), PR #67 (indicator-single-driver).

## Problem

After PRs #72 + #73 made `omo-replay --backtest` runnable end-to-end again, a smoke test on the same config that the HTTP `/backtest/run` path was producing 6 trades on revealed that omo-replay produces only 1.

Repro (run today, 2026-05-01):

    ./backend/bin/omo-replay -backtest -from 2026-05-01 -to 2026-05-02 \
      -symbols SOXL,MRVL,AFRM,MU,OXY -initial-equity 100000 \
      -slippage-bps 10 -speed max -output-json /tmp/result.json

omo-replay output:

    Trade Count:  1
    Total P&L:    $222.78
    Trade Log:    [avwap_v4] AFRM260515C00062500 buy/sell

HTTP `/backtest/run` (backtest_id `bt-c1e2744b4d8e073d`) on same date / symbols / equity / slippage:

    Trade Count:  6
    Total P&L:    $1,677.40
    Trades:       MU $545C (avwap_v4), OXY $58P (avwap_v4),
                  AFRM $62C (avwap_v4), AFRM $64C (macd_only_v1),
                  MRVL $163C (macd_only_v1, open),
                  SOXL $120C (avwap_v4), SOXL $126C (macd_only_v1)

Key observations from omo-replay's stderr log:

- SOXL bars load (499 bars), warmup completes, ORB session runs, AVWAP anchors resolve. No `EMIT SIGNAL` line for SOXL across the whole run.
- macd_only_v1 fires zero `EMIT SIGNAL` lines for any of the 5 symbols in omo-replay vs multiple in HTTP.
- avwap_v4 fires for AFRM only.
- The single AFRM trade picks a different contract than HTTP: omo-replay picked May 15 $62.5 strike, HTTP picked May 8 $62 and May 15 $64.

So omo-replay is missing both:
1. macd_only_v1 signals entirely (across all 5 symbols).
2. avwap_v4 signals on 4 of 5 symbols (only AFRM fires).

This is a strategy-level signal-emission divergence, not a fill-model issue.

## Hypotheses

Ranked by suspicion based on what we already saw fail under PR #67's indicator unification:

### H1: macd_only_v1 is not registered in omo-replay's strategy registry

omo-core wires `Indicator: svc.indicator` plus `SpecStore: svc.specStore` and registers all spec-defined strategies. omo-replay constructs a `specStore` and `strategyDeps`, but the spec registry might be loading a subset, or filtering by `lifecycle.state` differently between the two paths. macd_only_v1 has `lifecycle.state = "PaperActive"` and `paper_only = true`; if omo-replay's spec filter excludes PaperActive specs (or expects `Live*` state), every macd_only_v1 instance never registers.

Cheap test: grep omo-replay's stderr for `macd_only_v1` instance registration. If absent, H1 is confirmed.

### H2: Per-shard symbol slabbing routes some symbols to a shard whose strategy registry is missing the relevant strategy

omo-replay shards into 8 workers (clamped to len(symbols)=5 here). PR #67 split per-shard indicator services and PR #73 split per-shard StrategyDeps. If `BuildStrategyShard` instantiates only the strategies whose `Routing.Symbols` intersects the slab, but the slab partitioning differs between omo-replay and HTTP, a strategy can end up on a shard that owns symbols not in its `routing.symbols` list.

Cheap test: `grep "instance registered" /tmp/omo-replay-stderr.log | sort` and compare against the HTTP backtest log for the same symbol set.

### H3: Indicator state divergence between omo-replay and HTTP at signal-evaluation time

PR #67 unified the warmup driver, but the per-shard indicator services in omo-replay and the in-process indicator service in HTTP could initialize differently (e.g. different bar-count seed, different timezone for `replaySessionOpen`, different HTF aggregation). Strategies that condition on indicator state thresholds (RSI, ADX, MACD histogram) would emit on one path and not the other.

Cheap test: dump indicator snapshots at the same `(symbol, ts)` from both paths (the existing parity-diag logging does this) and diff.

### H4: AVWAP wiring is completed for sharded mode but session-state resolution differs

The PR #73 fix builds AVWAPFn that dispatches per-symbol via `shardedPipeline.ShardForSymbol`. If `ShardForSymbol` returns the wrong shard for some symbol, the runner will see the wrong AVWAP values (or nil), and AVWAP-pinch entry conditions won't fire. avwap_v4 firing on AFRM only suggests AFRM's shard happens to be set up correctly while others are mis-routed.

Cheap test: log a one-line `[shard %d owns symbol %s]` at startup and verify each symbol resolves to the shard whose monitor was warmed for it.

### H5: omo-replay's snapshotFnRouted doesn't reach the shard's indicator state

`snapshotFnRouted` (the MarketDataFn passed in StrategyDeps) is a single function constructed once. With per-shard indicators, the MarketDataFn may need to dispatch by symbol the way AVWAPFn does. If snapshotFnRouted always queries one shard's indicator (the wrong one for most symbols), all 4 non-AFRM symbols see stale or zero indicators and emit no signals.

Cheap test: `grep snapshotFnRouted /tmp/omo-replay-stderr.log` -- or just read the function definition in main.go and check whether it's shard-aware.

## Investigation plan

Two phases. Phase A is read-only diagnosis; Phase B is the fix.

### Phase A: diagnose

A1. Run a paired backtest with `--emit-gated-diag` (omo-replay flag exists per main.go grep) on omo-replay and the HTTP path. Diff `strategy_signal_events.reason` distribution by class. Per `.claude/skills/backtest-analysis/SKILL.md`, this is the canonical runner-bug signal.

A2. Read `cmd/omo-replay/main.go` lines 256-300 and locate `snapshotFnRouted`. Confirm whether it's shard-aware. If not, that's H5.

A3. Add a single startup log line per shard:

    log.Info().Int("shard", i).Strs("symbols", slabSyms).Msg("shard slab")

Run on the 5-symbol smoke set. Tag each symbol with the shard index that supposedly owns it. Then verify in the run that each symbol's `EMIT SIGNAL` (or absence) lines log the same shard index. If a symbol's monitor is on shard X but the strategy registry instance is on shard Y, that's H2/H4.

A4. For SOXL specifically (the symbol that motivated this whole thread): grep `instance registered.*SOXL` in omo-replay stderr and confirm both `avwap_v4:4.3.0:SOXL` and `macd_only_v1:3.0.0:SOXL` appear. If macd_only_v1 is absent, H1 is the cause.

A5. If A4 confirms instances exist but they emit no signal: read parity-diag `EntryGated` lines for SOXL in omo-replay. Compare `blocking_gate` and `score` values to the HTTP backtest's parity-diag output for the same `(symbol, ts)`. Divergence here points at indicator-state drift (H3) or strategy-config drift between the two paths.

Phase A success criterion: the divergence is attributable to exactly one of H1-H5, with concrete log evidence cited inline in the writeup.

### Phase B: fix

The fix shape depends on which H wins:

- **H1**: extend omo-replay's spec store filter to accept `PaperActive` specs (matching HTTP's filter). Single-line change in spec store wiring. Estimated 5 LOC + test.

- **H2/H4**: ensure shard partitioning is consistent across monitor and strategy registry. Possibly deduplicate the partitioning logic into a single helper and call it from both wiring sites. Estimated 30-50 LOC + integration test.

- **H3**: align omo-replay's bar/indicator seeding with HTTP. Likely a missing seed step in the omo-replay warmup loop. Estimated 20 LOC + parity test that asserts indicator snapshots at fixed `(symbol, ts)` match between paths.

- **H5**: make `snapshotFnRouted` shard-aware via the same `ShardForSymbol` pattern PR #73 added for AVWAPFn. Estimated 15 LOC + test.

Phase B success criterion: omo-replay --backtest on the 5-symbol smoke set produces the same trade list (modulo deterministic fill-model differences) as HTTP `/backtest/run` for the same config. Allowed delta: trade count within +/-1, every contract symbol present in HTTP must also appear in omo-replay (or vice versa), P&L within +/-5% on the matching trades.

## Out of scope

- Strategy-config divergence between omo-replay and HTTP that's intentional (e.g. omo-replay's `--no-ai` overrides). Audit step A4 should distinguish these from bugs.
- Performance differences. omo-replay's per-shard direct-dispatch model is faster than HTTP's bus-routed model; that's by design. We're looking for behavioral parity, not throughput parity.
- Adding skew to the synthetic chain. That's tracked by the routine firing 2026-05-15.

## Estimated effort

- Phase A: 1-2 hours (mostly log diffing).
- Phase B: 0.5-2 days depending on which H wins.

## Acceptance criteria

- [ ] Phase A produces a one-page writeup naming the H that explains the divergence, with cited log evidence.
- [ ] Phase B implementation in a single PR (matching project SOLID convention -- one concern per PR).
- [ ] Smoke test repro: omo-replay vs HTTP on the 5-symbol set produces matching trade lists per the criterion above.
- [ ] No regression in PR #72 and PR #73 paths -- both remain green and the omo-replay binary still runs end-to-end.

## Open questions

1. Does Phase B's matching criterion need to be byte-identical (including fill prices) or just structurally identical (same contracts, same direction, same approximate qty)? Defaulted to structural in the criterion above; confirm before implementing the parity test.
2. Should omo-replay continue to use the sharded pipeline by default, or switch to non-sharded for parity testing? Sharded is faster but adds the per-shard wiring surface that PR #73 patched. Worth a separate decision after Phase A names the cause.
