# Prefer-Live-Chain for Same-Day Backtests (v4 — post third-pass review)

Status: v4 corrects v3's central misreading of the persistence layer. The `InsertRun` extension is unnecessary — `Save` is the existing method, `BacktestRunRow.Tags []string` already exists, the SQL already binds it inside a transaction. Auto-tag becomes a one-line conditional in `backtest_history_save.go`. Cache-key DTE assumption also corrected.

Other changes from v3:
- `Result` (not `BacktestResult` — name fix throughout) gains a single nested `ChainStats *ChainSourceStats` instead of two flat counters.
- Cache key includes `minDTE/maxDTE` (Alpaca adapter DOES filter by them).
- HTTP and CLI input layers fail-fast at request-parse boundary; runner-level validation remains as backstop.
- `omo-replay --tags` line dropped from rollout (flag doesn't exist).
- Mon/Wed criterion replaced with listed-strike-grid alignment (more durable).
- `backtest_handler_test.go` is a new file (didn't exist) — wording corrected.

## Goal

Make same-day backtests use the live Alpaca options chain instead of falling through to the BSM-priced synthetic chain. Closes the strike/expiry mismatch that produced a +$29k backtest vs -$3k live divergence on 2026-05-04 across 18 trades on the same signal stream.

## Non-goals

- True as-of-bar-time premium parity (option-quote-shadow snapshot stream — separate work).
- Per-symbol synthetic-chain calibration (skew, Mon/Wed weeklies, listed-strike spacing in synth).
- Live trading path. Read-only with respect to omo-core production.
- Dashboard UI for the toggle (deferred-but-trivial follow-up).

## Current state

Selector (`backend/internal/app/options/contract_selection.go:64`) and sizer (`backend/internal/app/strategy/risk_sizer.go:856-905`) are shared between live and backtest. Divergence is in the chain provider. Live: `alpaca.Adapter.GetOptionChain`. Backtest: `backtest.HistoricalOptionsAdapter` (DoltHub-primary, BSM-synth fallback) wired at `backend/internal/app/backtest/runner.go:527, 562, 599`. DoltHub lags real-time, so synth fires for "today" backtests.

omo-replay topology: canonical CLI backtest path is `cmd/omo-replay/backtest_runner.go:31-142`, invoked from `main.go:225-238`. omo-replay does NOT call `historyRepo.Save` — CLI runs do not persist to `backtest_runs`. Auto-tag is therefore HTTP-only; CLI runs cannot be auto-tagged.

HTTP backtest endpoint runs in-process with omo-core; `infra.alpacaData` is wired at `cmd/omo-core/http.go:108-112` and satisfies `ports.OptionsMarketDataPort`.

Persistence path (corrected from v3): the only public write method is `Save(ctx, BacktestRunRow, []BacktestTradeRow)` at `backend/internal/ports/backtest_history.go:13` / `adapters/timescaledb/backtest_history_repo.go:50`. `BacktestRunRow.Tags []string` already exists at `ports/backtest_history.go:51`, is bound at `repo.go:85` (column `$23`), and the entire write runs inside `tx.BeginTx`/`tx.Commit` at `repo.go:60-95`. Tags are already atomic with the parent row. v3's "extend `InsertRun`" was solving a non-problem.

Today's tag construction site: `adapters/http/backtest_history_save.go:86` writes `Tags: []string{}` as a hardcoded literal. Auto-tag is a single conditional appended here.

## Architectural call

Adapter-internal layering (DoltHub → live → synth), not a CompositePort. Lookup ordering depends on the per-call DTE-window predicate (`hasExpiryInDTERange`); a generic chain wrapper would either lose the predicate or leak it as a callback (CLAUDE.md rule 2).

## Proposed change

Add `liveFallback ports.OptionsMarketDataPort` to `HistoricalOptionsAdapter`. Lookup order:

```
DoltHub cache  -> live (if wired)  -> synth (if enabled)  -> empty
```

Off by default. Opt-in via `--prefer-live-chain` (CLI) or `prefer_live_chain: true` (HTTP).

### Files

**1. `backend/internal/app/backtest/historical_options_adapter.go`**

```go
liveFallback ports.OptionsMarketDataPort
liveStats    ChainSourceStats

func (a *HistoricalOptionsAdapter) SetLiveChainFallback(p ports.OptionsMarketDataPort) {
    a.liveFallback = p
}

type ChainSourceStats struct {
    HistHits   uint64
    LiveHits   uint64
    SynthHits  uint64
    LiveErrors uint64
}

func (a *HistoricalOptionsAdapter) StatsWithLive() ChainSourceStats { ... }
```

Existing `Stats() (histHits, synthHits int)` stays. Only one production call site (`runner.go:2063` — verify by grep at implementation time, line numbers drift) is updated to use `StatsWithLive()`.

In `GetOptionChain`, after DoltHub miss and before synth:
- Live error → WARN log with `{symbol, right, error}`. The Alpaca error already includes HTTP status (`alpaca: list option contracts failed (status %d): %s` at `options_rest.go:139,208`); operator disambiguates 401-vs-5xx without any key plumbing. Bump `liveStats.LiveErrors`. Continue to synth.
- Live empty → INFO `{symbol, right, source: "live", count: 0}`. Continue to synth.
- Live non-empty → DEBUG `{symbol, right, source: "live", count, expiries}`. Bump `liveStats.LiveHits`.

End-of-run summary log emits all four counters via `StatsWithLive()`.

Lookup-order behavior change documented: `loaded=true && !hasExpiryInDTERange` (DoltHub has monthlies, strategy wants weeklies) today falls to synth; with this change it falls to live first. Desired for the plan's goal but a behavior shift in `EnforceUniverseHistory`/coverage paths. Covered by a unit test.

**2. `backend/internal/app/backtest/runner.go`**

`RunConfig` (line ~45-80; verify by grep):

```go
PreferLiveChain   bool
LiveOptionsMarket ports.OptionsMarketDataPort   // caller-supplied; nil iff PreferLiveChain==false
```

Validation at runner start:

```go
if r.cfg.PreferLiveChain && r.cfg.LiveOptionsMarket == nil {
    return fmt.Errorf("prefer_live_chain=true but live options market not provided")
}
```

This is a backstop; HTTP and CLI layers fail-fast on the same predicate before reaching the runner (see files 4 and 5). The inverse (`PreferLiveChain==false && LiveOptionsMarket!=nil`) is a documented no-op — passing a port without setting the flag is silently ignored. Asymmetric; both directions could be gated, but the false-positive direction has no failure mode worth a check.

`*backtest.Result` (`collector.go:68`) gains one nested optional field:

```go
ChainStats *ChainSourceStats `json:"chain_stats,omitempty"`
```

Populated on `finalResult` before `r.result.Store(&finalResult)` (currently around `runner.go:2097` — verify at implementation). Nil when `PreferLiveChain` is false; `omitempty` keeps the JSON shape stable for legacy consumers. Consumers verified: HTTP handler (`backtest_handler.go:427`), HTTP results JSON encode (`:434`), sweep orchestrator (`sweep/orchestrator.go:234-237` — value-copy), sweep domain (`domain/sweep/sweep.go:29` — embedded by value), `backtest_history_save.go:43`, `omo-replay/backtest_runner.go:117`. All non-breaking.

WARN log at runner start if `r.cfg.PreferLiveChain && !sameCalendarDayET(r.cfg.From, r.now)`. "Any non-today calendar day in the runner's TZ" check, not 48h-from-now (a Saturday-AM run for Friday's session would slip through 48h).

The runner does NOT hold a `BacktestHistoryPort`. Tag-writing is HTTP-handler concern (see file 5).

**3. `backend/cmd/omo-replay/backtest_runner.go`**

Add `preferLiveChain bool` as the 18th positional arg to `runBacktestViaRunner`. Sole caller is `main.go:230`. No tests. TODO comment added pointing at a future config-struct refactor.

When `preferLiveChain && appCfg.Alpaca.APIKeyID != ""`:
- Reuse the Alpaca adapter at `backtest_runner.go:94`.
- Wrap via `internal/adapters/options/caching_market.go` (file 6) **here in the caller**.
- Pass to `RunConfig.LiveOptionsMarket`.

When `preferLiveChain && appCfg.Alpaca.APIKeyID == ""`: fail fast with a clear error before constructing the runner. Don't rely on the runner-layer backstop for this case — the caller has the information.

**4. `backend/cmd/omo-replay/main.go`**

```go
preferLiveChain := flag.Bool("prefer-live-chain", false,
    "Use live Alpaca chain as fallback before synth (default: false). "+
    "Only meaningful for same-day backtests. CLI runs are not persisted to backtest_runs "+
    "and are not auto-tagged.")
```

Plumb to `runBacktestViaRunner`. Legacy `main.go:420-518` branch untouched.

(Note: `omo-replay` has no `--tags` CLI flag; the rollout instruction in v3 referencing one was non-executable and is dropped from v4.)

**5. `backend/internal/adapters/http/backtest_handler.go`**

Add to `backtestRunRequest` struct:

```go
PreferLiveChain bool `json:"prefer_live_chain"`
```

Snake-case matches existing convention (`initial_equity`, `slippage_bps`).

Plumb to `RunConfig.PreferLiveChain`. Pass `infra.alpacaData` (already accessible) wrapped in the caching wrapper (file 6) as `RunConfig.LiveOptionsMarket`. Wrapper is constructed per-request in the handler, not in the runner.

Fail-fast 400 at request-parse boundary: if `req.PreferLiveChain && infra.alpacaData == nil`, return HTTP 400 with `prefer_live_chain requires Alpaca options data — not configured`. Better attribution than a runner-layer error string.

**Auto-tag write (the core simplification):** at `backend/internal/adapters/http/backtest_history_save.go:86`, replace:

```go
Tags: []string{},
```

with:

```go
Tags: chainSourceTags(res),
```

where `chainSourceTags(res *backtest.Result) []string` returns `[]string{"chain_source=live_now"}` when `res.ChainStats != nil && res.ChainStats.LiveHits > 0`, else `[]string{}`. The closure at `backtest_handler.go:346-348` already passes `res *backtest.Result` into `saveBacktestHistory`; this is the natural place to read `ChainStats`.

`Save` is already transactional. No port change, no SQL change, no signature sweep.

**6. `backend/internal/adapters/options/caching_market.go` (new)**

Lift the existing `newCachingOptionsMarket` from `cmd/omo-replay/options_cache.go:13-56`. Adapter decorator implementing `OptionsMarketDataPort` by wrapping another `OptionsMarketDataPort`.

**Cache key correction**: `alpaca.Adapter.GetOptionChain` (`adapter.go:372-383`) DOES use `minDTE/maxDTE` to compute `expiryFrom/expiryTo` before calling `rest.GetOptionChain`. The cache key MUST include DTE bounds — `(symbol, right, minDTE, maxDTE)` or canonicalized `(symbol, right, expiryFrom, expiryTo)`. v3's "verify before lifting" was hand-waving a correctness decision; the answer is verified here, key shape decided.

**Dedup decision**: dedupe in this PR. Replace the omo-replay copy at `options_cache.go` with an import of the new shared package. Two near-identical decorators in tree is the kind of speculative coupling CLAUDE.md rule 2 warns against. If the omo-replay legacy branch (`main.go:514`) breaks, fix it; the tests cover it.

### Tests

**Unit (new):** `backend/internal/app/backtest/historical_options_adapter_live_fallback_test.go`

Fake `ports.OptionsMarketDataPort` with call counts. Cover:
- DoltHub hit → never calls live or synth.
- DoltHub miss + live hit → never calls synth, `LiveHits++`.
- DoltHub miss + live empty → calls synth, INFO logged.
- DoltHub miss + live error → calls synth, `LiveErrors++`, WARN logged with HTTP status.
- DoltHub miss + live nil + synth disabled → empty result.
- `loaded=true && !hasExpiryInDTERange` (partial-load case) → falls to live, then synth.

**Unit (new):** `backend/internal/adapters/http/backtest_handler_test.go` (CREATE — file does not currently exist; the per-handler convention is `<file>_test.go` per `kakao_handler_test.go`, `decay_handler_test.go`, etc.)

POST JSON with `prefer_live_chain: true`. Assert:
- `RunConfig.PreferLiveChain == true && RunConfig.LiveOptionsMarket != nil`.
- On simulated `res.ChainStats.LiveHits > 0`, `Save` is called with `BacktestRunRow.Tags` containing `chain_source=live_now`.
- 400 returned when `prefer_live_chain=true` and `infra.alpacaData == nil`.

**Unit (new):** runner validation. `RunConfig{PreferLiveChain: true, LiveOptionsMarket: nil}` → runner returns explicit error.

**Unit (new):** caching wrapper key shape. Two calls with same `(symbol, right)` but different `(minDTE, maxDTE)` → wrapper calls underlying twice, not once.

**Existing:** all current tests pass unchanged.

**Manual smoke:**
- `omo-replay --backtest --prefer-live-chain` for today during RTH (09:30-16:00 ET): summary log shows `LiveHits > 0, SynthHits == 0`.
- HTTP backtest with `prefer_live_chain: true` for today during RTH: `backtest_runs.tags` row contains `chain_source=live_now`; `LiveHits > 0`.
- Strike-grid alignment: confirm a contract Alpaca lists at $2.50 increments (e.g. AMD around its current spot) appears in the selected chain at the actual listed strike, not at synth's integer-rounded strike.

### Acceptance criteria

1. Existing test suite passes unchanged.
2. New unit tests cover six adapter paths, runner validation, HTTP boundary parsing+tag-write, caching wrapper key shape.
3. `omo-replay --backtest --prefer-live-chain` for today during RTH:
   - Summary log shows `LiveHits > 0, LiveErrors == 0`.
   - Selected contracts use Alpaca's listed-strike grid (e.g. AMD $347.5, MSFT $420 at $2.50/$5 increments) instead of synth's integer rounding.
4. HTTP backtest for today with `prefer_live_chain: true` during RTH:
   - `backtest_runs.tags` contains `chain_source=live_now`.
   - `LiveHits > 0, LiveErrors == 0` in run log.
5. With flag off, behavior is byte-identical to current main.
6. With flag on but Alpaca not configured: HTTP 400 (handler), explicit CLI error (omo-replay), explicit runner error (defense-in-depth).

(Mon/Wed weekly assertion from v3 dropped — Alpaca-listing patterns are more variable than the assertion's "any non-Friday day" framing assumes; holiday rollovers and contract-listing windows produce false negatives. Listed-strike-grid alignment is the more durable check.)

## Blast radius

- **Off by default** → zero behavior change.
- When on, `GetOptionChain` calls that miss DoltHub hit Alpaca options API. Cache key `(symbol, right, minDTE, maxDTE)` is per-run; per-bar pricing is not refetched.
- **Live snapshot endpoint is now-only**: `/v1beta1/options/snapshots` ignores any historical timestamp. `ListOptionContractsAsOf` and `GetOptionDayBars` (`options_rest.go:460, 535`) exist for past-date reconstruction but are out of scope.
- All bars in a backtest see the chain pinned at the first call. A 09:35 ET vs 14:00 ET run on the same date may select different contracts.
- Concurrent runs: cache wrapper is constructed per-request (HTTP) or per-run (CLI), never shared.
- Silent-misconfig prevention: HTTP 400, CLI error, and runner backstop validation. `LiveErrors` counter + WARN logs surface "live attempted but failed" vs "live succeeded but empty."
- Live trading path untouched.
- Sweep orchestrator: deferred. `appsweep.NewOrchestrator` doesn't currently receive `OptionsMarketDataPort`; widening is a separate change. Until that ships, sweeps with `PreferLiveChain=true` will hit the runner-backstop error and refuse to run — better than silent fallback.

## Rollout

Single PR. After merge:
1. HTTP path: run today's backtest with `prefer_live_chain: true`. Confirm auto-tag in `backtest_runs.tags` and `LiveHits > 0`.
2. CLI path: `omo-replay --backtest --prefer-live-chain` for the same date. Confirm summary log shows live coverage.
3. Compare contract selections against today's live trades.
4. If selections align (within strike-tick tolerance), declare success. Leave the flag opt-in. Promote-to-default ONLY after sweep-orchestrator follow-up ships and CLI persistence is added (otherwise sweeps will runner-error and CLI runs won't be identifiable in `backtest_runs`).

## Resolved decisions (from review)

- **Persistence**: tag-write is one-line conditional in `backtest_history_save.go:86`. Existing `Save` is already transactional and `Tags` is already on `BacktestRunRow`. No port/repo signature change.
- **`Stats()` arity**: keep 2-tuple. Add `StatsWithLive() ChainSourceStats`. Update one log site.
- **Caching wrapper location**: `internal/adapters/options/caching_market.go`. Caller-constructed.
- **Caching wrapper key**: includes `(minDTE, maxDTE)` because Alpaca adapter filters by them.
- **Caching wrapper dedup**: do it in this PR.
- **Sweep orchestrator**: deferred. Backstop error catches sweep misuse until plumbing arrives.
- **Auto-tagging**: HTTP-only (CLI doesn't persist). Atomic via existing `Save`.
- **Per-call provenance logging**: required. DEBUG per-call + INFO summary.
- **Error-handling**: WARN with HTTP status from existing wrapped errors. No API-key plumbing.
- **`RunConfig` validation**: backstop. Primary fail-fast at HTTP-400 and CLI-construction.
- **Asymmetric validation**: `PreferLiveChain && !LiveOptionsMarket` is an error; the inverse is a no-op, documented.
- **`liveSpreadOIRejections`**: dropped (selector is source-agnostic).
- **`Result.ChainStats`**: nested optional pointer, not flat fields.

## Out of scope (future PRs, in priority order)

1. Listed-strike spacing in synth (`roundStrike` $2.50 / $5 / $10 tiers; per-symbol overrides).
2. Mon/Wed weekly expiries in synth `weeklyExpiries`.
3. IV skew curve in synth.
4. Snapshot-stream as-of-bar lookup (true historical premium parity).
5. Sweep orchestrator: widen `appsweep.NewOrchestrator` to take `OptionsMarketDataPort`.
6. omo-replay persistence (call `historyRepo.Save` so CLI runs land in `backtest_runs`); also enables auto-tagging for CLI.
7. Dashboard UI: `preferLiveChain` checkbox.
8. Optional `--require-live-chain` flag (fail-fast) if `--prefer-live-chain` proves too soft.
9. Refactor `runBacktestViaRunner` from 18 positional args to a config struct.

## Effort estimate

1.5 working days. The v3 "1h `InsertRun` signature extension" line item is removed (the work doesn't exist).

Breakdown:
- Adapter changes + struct stats + tests: 4h
- Caching wrapper lift + dedup omo-replay copy + cache-key fix + tests: 2.5h
- Runner validation + RunConfig + Result.ChainStats: 2h
- omo-replay CLI plumbing + flag + 18th-arg + early-fail: 1.5h
- HTTP request field + tag-write conditional + handler test creation + 400 fail-fast: 2.5h
- Manual smoke (HTTP + CLI + strike-grid alignment): 1h

## Files touched (summary)

- New: `backend/internal/adapters/options/caching_market.go`, `historical_options_adapter_live_fallback_test.go`, `backend/internal/adapters/http/backtest_handler_test.go`, runner-validation test, caching-wrapper key-shape test.
- Modified: `historical_options_adapter.go`, `runner.go`, `omo-replay/main.go`, `omo-replay/backtest_runner.go`, `omo-replay/options_cache.go` (delete or convert to import alias), `http/backtest_handler.go`, `http/backtest_history_save.go` (one-line tag-write), `cmd/omo-core/http.go` (one-line dep pass), `app/backtest/collector.go` (`Result.ChainStats` field).
- Untouched: live trading path, sweep orchestrator (deferred), dashboard (deferred), domain types, migrations, selector logic, `timescaledb/backtest_history_repo.go`, `ports/backtest_history.go`.
