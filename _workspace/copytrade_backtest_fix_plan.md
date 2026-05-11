# Copytrade v1 backtest fix plan (rev 2)

Rev 1 reviewed by code-reviewer (APPROVE_WITH_REVISIONS) + qa-inspector (NEEDS_REVISION; 2 blocking issues). This rev addresses all flagged items.

Goal: make the HTTP backtest and replay backtest of `copytrade_v1` actually enter AND exit option trades from the scraped Discord BTO/STC feed.

## Evidence (from logs/omo-core.log, backtest_id `bt-b6f1e69f1e3f1323`, 2026-05-09 21:24:38)

- 196 signals loaded (79 BTO, 103 STC, 14 AVG) from `services/discord-copytrade/state/history_90d.jsonl`
- `_workspace/copytrade_replay/fills.csv`: 12 BUY rows, 0 SELL rows
- Log: `portfolio_guard: 12 open positions reached max 12`
- Log: `option fill rejected: leg_price exceeds sane multiple of reference premium (likely underlying-spot contamination)` (`backend/internal/app/execution/service.go:1749`)
- Log: `risk sizer: live ask above author ref premium + buffer -- rejecting` despite `pin_to_author_ref=true`
- Log contains **zero** STC-related messages
- 12 BUY fills clustered between 2026-02-09 and 2026-03-11; nothing after

## Three root causes

### RC1 -- STC silently dropped while BTO Pending (primary)

`backend/internal/app/strategy/builtin/copytrade_v1.go:374-380` in `handleSTC`:

```go
if pos.Pending {
    ctx.Logger().Warn("copytrade: STC refused -- BTO not yet confirmed by broker", ...)
    return cst, nil, nil
}
```

SimBroker fills BTO at next 1m-bar close. Any STC posted in the same minute is dropped. No queue.

### RC2 -- Risk-sizer buffer rejection in Paper despite pin

`backend/internal/app/strategy/risk_sizer.go:944-967`: even when env=Paper and `force_ref_premium` tag is set (so the fill will be pinned), the sizer rejects when `best.Ask > priceCap`. The pinned limit equals priceCap, so the rejection is comparing against a price we will never actually pay.

### RC3 -- leg_price safety check (defer)

`backend/internal/app/execution/service.go:1742-1750`. Hold off; depends on whether SimBroker actually fills at the pinned limit after Stage B.

### Cascade

RC1 -> 0 STCs close positions -> max_positions=12 saturates -> portfolio_guard rejects every later BTO. RC2 mutes the input upstream.

## Fix approach

### Stage A -- queue STCs while BTO is Pending

#### A.1 Wire instanceContext in `handleFill` (PRE-REQUISITE; blocker per QA)

File: `backend/internal/app/strategy/runner.go` lines 2543-2547.

Current `handleFill` only populates `now`/`logger`/`emit` on the instanceContext. `ctx.EmitDomainEvent` (`instance.go:389-407`) requires `c.runner != nil` to publish; otherwise it routes to `c.emit` which is wired to a no-op for fill handlers. Today `CopytradeOrphanFillPayload` emit at `copytrade_v1.go:574` is already silently swallowed (separate latent bug, but in scope here because Stage A.2 depends on the same fix).

Change: populate `runner`, `ctx`, `tenantID`, `envMode` on the instanceContext built in `handleFill`, mirroring how `handleCopytradeSignal` builds its context at `runner.go:2838-2845`.

Tests:
- New unit in `backend/internal/app/strategy/runner_test.go` (or nearest existing fill-handler test file): wire a FillConfirmation to a strategy whose OnEvent calls `EmitDomainEvent` with a sentinel payload; assert the bus saw the publish. Today this assertion fails; after A.1 it passes.

#### A.2 Drain queued STCs via `enqueueCopytradeCallback` (avoids deadlock per QA)

Files:
- `backend/internal/app/strategy/builtin/copytrade_v1.go`
- `backend/internal/app/strategy/runner.go` (for the callback enqueue, if a new entry point is needed)

`handleFill` holds `r.mu.Lock()` while invoking `inst.OnEvent` (`runner.go:2556-2558`). The in-process event bus is synchronous in syncMode backtests, so emitting `CopytradeExitRequest` from inside OnEvent would re-enter the position_monitor handler on the same goroutine while `r.mu` is held -- deadlock or undefined ordering.

The runner already has the right pattern: `enqueueCopytradeCallback` / `DrainCopytradeCallbacks` at `runner.go:189-199, 2912-2940`. Reuse it.

Change in copytrade_v1:

- Add `QueuedSTCs []start.CopytradeSignal` (slice) to `copytradePosition`.
- In `handleSTC`: when `pos.Pending`, append the inbound `sig` to `pos.QueuedSTCs` instead of dropping. Log info-level `copytrade: STC queued -- awaiting BTO confirmation` (keep observability).
- In `handleFillConfirmation`: after flipping `Pending=false`, snapshot `pos.QueuedSTCs`, clear it, then for each queued sig, enqueue a callback (via the existing runner copytrade-callback queue) that synthesizes a `CopytradeExitRequest` and publishes it. The drain must NOT mutate `cst.Positions` mid-iteration: iterate the captured snapshot, not the live slice.
- Reuse `handleSTC`'s body for the actual STC handling. Factor the post-Pending-gate portion into `dispatchSTC(...)` so both inline (live, BTO already filled) and queued-drain paths call the same code without recursion through `OnEvent`.
- In `handleEntryRejection`: if a BTO is rejected and the position has `QueuedSTCs`, log a single `copytrade: discarding N queued STCs due to BTO rejection` warning and drop them.
- In `expireStalePending` (`copytrade_v1.go:235` / the helper that deletes Pending positions at TTL): same as EntryRejection -- log a single `copytrade: discarding N queued STCs due to BTO TTL expiry` and drop. Verify QueuedSTCs is not silently lost without a log.

Concurrency note: `OnEvent` is serialized per shard, and copytrade is pinned to one shard via the sentinel symbol (`configs/strategies/copytrade_v1.toml:19`), so single-threaded state mutation is guaranteed -- no mutex needed.

Tests in `backend/internal/app/strategy/builtin/copytrade_v1_test.go` (extend existing file):

1. BTO -> STC (while Pending) -> FillConfirmation: queued STC drains and emits CopytradeExitRequest.
2. BTO -> 3 STCs (while Pending) -> FillConfirmation: all 3 drain in arrival order.
3. BTO -> STC partial -> STC "all out" (both while Pending) -> FillConfirmation: drain runs partial then full; second drain detects fully-closed position and short-circuits the rest.
4. BTO -> EntryRejection: queued STCs discarded with single warn; no orphan exits.
5. BTO -> (TTL expiry triggers expireStalePending): queued STCs discarded with single warn; no orphan exits.
6. STC arrives with no prior BTO at all: existing drop behavior preserved (do not enqueue against nonexistent position).
7. (A.1 unit, in runner_test.go) FillConfirmation handler context populates runner+ctx+envMode so EmitDomainEvent reaches the bus.

Verify: `go test ./backend/internal/app/strategy/... ./backend/internal/app/strategy/builtin/...` green.

### Stage B -- bypass price-buffer rejection in Paper with pinned ref

File: `backend/internal/app/strategy/risk_sizer.go` lines 944-977.

Current code computes `priceCap = forced.RefPremium * (1 + bufferPct)` and rejects when `best.Ask > priceCap`. The pinned-paper design says `fillPrice = priceCap`, so the rejection is gating on something we will not pay.

Change: when `event.EnvMode == domain.EnvModePaper && forcedOK && forced.RefPremium > 0`, do NOT reject on `best.Ask > priceCap`. Set `limit_price = priceCap` (already the case at line 968) and proceed.

Tag-as-gate proof: grep confirms `force_ref_premium` is only set by copytrade strategy (`builtin/copytrade_v1.go:333-335` under `if cst.Config.PinAuthorRef`). Sizer reads via `risk_sizer_force_contract.go:28,67-72`. No alternate producer of the tag exists today.

Tests in the existing risk sizer test file (locate via grep for `live ask above author ref premium`):
- Paper env + `force_ref_premium` tag + live ask 3x ref premium: NOT rejected (today: rejected). New.
- Live env + `force_ref_premium` tag + live ask 3x ref premium: STILL rejected (do not regress live).
- Paper env, NO `force_ref_premium` tag, live ask 3x ref premium: keep current behavior (sizer should NOT misroute non-copytrade paper orders).
- Limit price equals ref premium + buffer (existing pin behavior unaffected).

Verify: `go test ./backend/internal/app/strategy/...` green.

### Stage C -- DEFERRED

The `leg_price exceeds sane multiple of reference premium` check at `execution/service.go:1749`. If after Stage A + Stage B applied and rebuilt, the log still emits this rejection for copytrade fills, do a focused Stage C plan. Likely fix: in pinned-paper, the simbroker should fill at `priceCap` and the check should be a no-op; if it trips, the simbroker is reading the chain price instead of the limit.

## Out of scope (with rationale)

- `asset_class="EQUITY"` on position_monitor log lines for OCC contracts. QA traced: exit rules attach via `bootstrap.go:444-450` fallback (only one spec exists for copytrade_v1, no asset-class mismatch fork), but `Position.AssetClass=EQUITY` propagates to `exit_eval.go:93-94,335` (session-close logic) and could subtly skew chandelier-trail behavior. Defer until we see whether Stages A+B alone produce a clean fills.csv. If post-fix fills look right, fix the asset_class propagation as a follow-up. Root-cause hint: trace where `intent.AssetClass` is set on copytrade BTOs -- likely defaults to EQUITY in `start.NewSignal` or the sizer, should be OPTION.
- Dashboard UI changes (backend already defaults `copytrade_history`).
- nworkers race theory (debunked).
- Synthetic chain pricing accuracy (orthogonal to pin design).

## Blast radius

- Stage A.1 (instanceContext wiring): touches the FillConfirmation handler path. Risk: any strategy currently relying on the no-op behavior of `EmitDomainEvent` inside OnEvent under FillConfirmation. Grep confirms only `copytrade_v1.go:574` emits from a FillConfirmation path and it emits `CopytradeOrphanFillPayload` -- enabling it is a feature win, not a regression. No other strategy emits from FillConfirmation today.
- Stage A.2 (queue + drain): copytrade-only state. Live unaffected because live BTO fills before any STC arrives (steady-state QueuedSTCs is empty).
- Stage B: risk sizer; gate is `env==Paper && force_ref_premium tag present`. Tag has a single producer (grep proof above), so live and non-copytrade paper paths unchanged.

## Success criteria

After Stages A + B applied, rebuilt, and omo-core restarted:

1. `go test ./backend/internal/app/strategy/... ./backend/internal/app/strategy/builtin/... ./backend/internal/app/risk/...` green.
2. `POST /backtest/run` with body `{"strategies":["copytrade_v1"],"from":"2026-01-27","to":"2026-04-23","speed":"max"}` returns 202 and runs to completion.
3. `_workspace/copytrade_replay/fills.csv` (latest run):
   - Contains both BUY and SELL rows.
   - Unique SELL contract_symbol count >= 0.6 * unique BUY contract_symbol count. (Stronger than rev 1's arbitrary floor: catches the cascade failure mode where partials close 1 contract but most positions stay open.)
   - Zero rows where author_pnl_per_contract is hand-derivable but realized pnl shows nothing.
4. New backtest log slice in `logs/omo-core.log`:
   - Zero `STC refused -- BTO not yet confirmed` messages (queue absorbs them).
   - Zero `live ask above author ref premium + buffer -- rejecting` messages from copytrade_v1 in Paper env.
   - Some `copytrade: STC queued -- awaiting BTO confirmation` messages followed by drain successes (positive evidence the new path runs).
5. PnL parity check: `author_stated.csv` author_pnl_per_contract and the realized pnl reconstructed from `fills.csv` agree within +/-15% per position (pin-to-ref design implies near-exact match; gross divergence means RC3 fired and we need Stage C).

If 2/3/4/5 fails, capture the new evidence, return to investigation, draft Stage C (and possibly C') plan, re-execute.

## Execution order

1. Stage A.1 (runner instanceContext wiring) + its unit test
2. Stage A.2 (queue + drain in copytrade_v1.go) + tests 1-6
3. Stage B (risk_sizer Paper bypass) + tests
4. `go build ./...` and `go test ./...` green
5. Restart omo-core in `omo-core` tmux session (currently in crash loop on crypto pipeline -- out of scope, but verify it comes up clean for backtest)
6. `curl POST /backtest/run` as in Success Criteria #2
7. Inspect new fills.csv + log slice
8. Branch on outcome per Success Criteria
