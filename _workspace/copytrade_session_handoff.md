# copytrade_v1 — session handoff (2026-04-23, end of day)

Pick up here next session. Tasks #4–#6 shipped and committed to main;
task #7 (paper dry-run) is blocked on one MUST-fix plus two SHOULD-fixes
that a post-commit /review and two agent consults surfaced.

## Commits on main (copytrade feature)

- `de305d0` feat(copytrade): mirror vetted Discord option trades end-to-end
- `fdd1cce` refactor(copytrade): consolidate lookup helpers and name magic tolerances
- `8f6f6a5` docs(go-hexagonal): note TOML array-of-tables shape and OnEvent stamping gap

Running omo-core in tmux is still on the OLD binary (built 11:11 today,
before the copytrade merge). Do NOT restart it with the new binary until
the MUST-fix below is applied — the sentinel symbol `__copytrade__` will
leak into the activation service and trigger WS subscribe attempts for a
fake ticker.

## Bootstrap already done (do not redo)

- `services/discord-copytrade/.env` filled with real channel URL + shared secret
- Root `.env` appended with matching `OMO_COPYTRADE_SECRET`
- Playwright bootstrap ran via user's terminal (NOT `run_in_background`,
  that detaches stdin and returns EOF before login completes). State saved
  to `services/discord-copytrade/state/storage_state.json` — 87 localStorage
  entries confirmed (Discord stores its auth token in localStorage, not
  cookies). State file is now user-owned.
- Sidecar image built.

## MUST fix before next restart

### 1. Sentinel symbol leak into activation/ingestion

`routing.symbols = ["__copytrade__"]` in `configs/strategies/copytrade_v1.toml`
seeds the per-symbol Init loop correctly (that part works), but the same
list flows through `cmd/omo-core/services.go:530-534` into
`svc.symRouterSpecs`, and the `symbolrouter.EmitFallbackForMissing` path
publishes `EffectiveSymbolsUpdated{Symbols:["__copytrade__"]}`.
`activation.Service.handleEffectiveSymbolsUpdated` then calls
`activateOne("__copytrade__")` which fetches historical bars (fails — no
such ticker) and `subscriber.SubscribeSymbols` which attempts a WS
subscribe for the fake ticker.

Minimal fix: in `cmd/omo-core/services.go:521-535`, skip the spec whose
`hookRef.Name == "copytrade_v1"` (or more generally, any spec whose
`spec.Routing.Symbols` are all sentinel-prefixed with `__`) from
`symRouterSpecs`. Leave the instance-creation loop alone — it still needs
the sentinel to seed state.

Cosmetic (defer to follow-up): `__copytrade__` still shows up in
`ListStrategies()` (dashboard `/api/strategies/` response) and
`SystemStartedPayload.StrategySymbols`. Harmless but ugly.

## SHOULD fix before paper dry-run (architect called these out)

Without these, state desyncs silently in exactly the scenarios paper mode
is supposed to catch. Recommended budget: 1–2 hours.

### 2. Ghost position on BTO reject or pre-fill STC (Correctness C)

Strategy writes `cst.Positions[key]` synchronously on BTO event at
`builtin/copytrade_v1.go:318-344`, independent of broker fill confirmation.
If `risk_sizer.handleOptionsSignal` returns `errOptionsChainEmpty` (empty
chain, IBKR reject), the BTO never reaches the broker but the strategy
thinks a position is open. A subsequent STC decrements `RemainingFrac` and
emits `CopytradeExitRequestPayload`; `findPositionByContract` returns nil
(no position exists) and the handler silently no-ops. State now permanently
diverged from broker.

Fix shape (architect-recommended, minimal):

- Add `Pending bool` to `copytradePosition`, set `true` at BTO commit.
- `handleFill` at `runner.go:2120` currently uses
  `findInstanceByStrategyAndSymbol(strategy, routingSymbol)` which will
  NOT find the copytrade instance (no per-symbol routing). Fall back to
  `findInstanceByStrategy(strategy)` (already extracted in fdd1cce) when
  the symbol-routed lookup misses AND the strategy is known to be
  event-driven.
- Strategy `OnEvent` handles `start.FillConfirmation`: find position by
  ContractSymbol, flip `Pending=false`.
- `handleSTC` refuses (warn log, no state mutation) when `pos.Pending`.
- Subscribe `EventOrderIntentRejected`/`EventOrderRejected` too; on match,
  delete the position key and decrement the generation counter back.

### 3. In-flight partial drops silently (Correctness D)

`positionmonitor.triggerExit` at `exit_eval.go:713` returns early when
`pos.HasExitInFlight()`. Strategy has already done
`pos.RemainingFrac *= (1 - fraction)` at `copytrade_v1.go:357` before the
`EmitDomainEvent` call. Two rapid "half out" posts land strategy at 0.25
residual while broker has 0.5.

Fix shape (minimal, short-path):

- In `handleCopytradeExitRequest`, pre-check `target.HasExitInFlight()`
  BEFORE mutating `CustomState`. If in-flight, return a warn log and
  publish a new `EventCopytradeExitRejected` (or reuse a similar channel)
  with payload `{ContractSymbol, Fraction, Reason: "exit_in_flight"}`.
- Strategy subscribes to that rejection, rolls `RemainingFrac /=
  (1 - fraction)` back.

Cleaner long-term (architect-preferred): move strategy `RemainingFrac`
commit to after a successful `CopytradeExitAccepted` event. Needs a new
event pair; larger blast radius.

## DEFER to follow-up commit (before LIVE flip — paper is fine without)

### A. HTTP body-size unbounded

`copytrade_handler.go:82` `json.NewDecoder(r.Body).Decode(&req)`. Add
`r.Body = http.MaxBytesReader(w, r.Body, 16<<10)` before Decode. One-liner.

### B. TenantID/EnvMode not stamped on strategy-emitted payloads

`CopytradeExitRequestPayload` and `ChandelierTrailArmPayload` leave
TenantID/EnvMode zero. Handlers fall through to "empty means don't filter"
which is correct in single-tenant paper but becomes a cross-tenant
close-anything bug the moment a second tenant exists.

Architect-preferred fix (option iii, NOT option i): in
`runner.emitDomainEvent` at `runner.go:2042-2084`, extend the existing type
switch to stamp `TenantID` / `EnvMode` into the payload struct before
publishing. Keeps strategies ignorant of infra concerns. Then flip
`findPositionByContract` to require non-empty tenant/env so the "empty =
any" contract is gone.

### Other deferred items

- **Snapshot-on-OnEvent.** `handleCopytradeSignal` mutates strategy state
  but never calls the snapshot persister (bar-driven strategies snapshot
  in the post-bar path). If omo-core restarts between BTO and the next
  forced-snapshot, the BTO is lost on disk but the broker position is
  live. On restart, next STC finds `gen == 0` and drops. Fix: emit
  `EventStrategyStateSnapshot` after `inst.OnEvent` returns in
  `runner.go:2471`.
- **Dedupe map in-memory only.** 120s freshness window replay on restart
  will re-accept any signal the sidecar retries. Paper: produces
  duplicated trades, not corruption. Fix: short journal on boot, or lower
  `freshness_ttl_secs` during testing.
- **Fail-closed logging.** `runner.go:2394` logs "no active copytrade_v1
  instance" at Debug level. If the instance fails to bootstrap (spec load
  error, TOML typo), every Discord signal drops silently. Bump to Warn and
  fire a one-shot `r.notifier` alert on startup.
- **Hard-coded `"system"` tenant at HTTP ingress.** `copytrade_handler.go:118`
  constructs the event with TenantID `"system"` and `EnvModePaper`. Fine
  for paper. For live, pull from config.
- **Rate limiting + IP allowlist on `/internal/copytrade/signal`.** None
  today. Paper: acceptable (localhost sidecar only). Live: add per-IP
  leaky bucket + `OMO_COPYTRADE_ENABLED=false` override.
- **Observability.** No metrics on accepted/rejected signal counts. Add
  `copytrade_signal_accepted_total{action,author}` +
  `copytrade_signal_rejected_total{reason}` before live flip.
- **OCC precision** for non-standard strikes ($150.125 etc.). No author
  trades these today. Document and defer.

## Known behavior notes (not bugs, reference)

- Instance.OnEvent does NOT stamp `StrategyInstanceID` on returned signals
  (only OnBar does). `handleCopytradeSignal` stamps it post-hoc in
  `runner.go:2494`. Documented in
  `.claude/skills/go-hexagonal/SKILL.md` "Gotchas" section.
- TOML `[[params.partial_fractions]]` decodes to `[]map[string]any`, not
  `[]any`. `parsePartialFractions` in `copytrade_v1.go:85` handles both.
  Also documented in go-hexagonal SKILL.md.
- PeakPremium seeding uses STC post price, not BTO fill price
  (`copytrade_v1.go:380`). Divergent from actual peak-so-far tracking but
  matches author intent. Log line only, no fix.

## Session-start prompt for next run

> Resuming copytrade_v1. Read
> `_workspace/copytrade_session_handoff.md`. Apply the MUST fix (sentinel
> symbol leak in services.go) and the two SHOULD fixes (ghost position +
> in-flight partial). Then rebuild-commit-restart, start the sidecar
> container, and tail both logs for the dry run.
