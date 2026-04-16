# AI Screener Migration: omo-core → omo-data

> Created: 2026-04-16
> Status: planned
> Effort: 2 days
> Risk: low (DB is already the hand-off boundary)

## Why

The AI screener is a scheduled data pipeline (fetch universe → snapshot →
score → rank → persist to DB). It runs once daily at 08:35 ET and takes
~14 minutes. Having it in omo-core means:

- Every omo-core restart triggers a catch-up re-run (14 min of wasted
  compute + Discord spam)
- The screener competes for CPU/memory with the live trading hot path
- If omo-core crashes during a screener run, live trading is also dead
- omo-data already runs identical scheduled jobs (IV collector, earnings,
  13F, DoltHub options) with the same pattern

## Current wiring

```
omo-core startup:
  1. Create AIService with deps (Alpaca, DB, SpecStore, Notifier, EventBus)
  2. AIService.Start():
     a. bootstrapFromDB() → reads ai_screener_results → publishes
        EventAIScreenerCompleted per strategy → symbol router picks up
     b. schedulerLoop() → waits for 08:35 ET → RunAIScreen() → persists
        results to DB → publishes EventAIScreenerCompleted → notifies Discord
  3. Symbol router subscribes to EventAIScreenerCompleted → resolves
     effective symbols per strategy
```

## Target wiring

```
omo-data:
  1. Create AIService with deps (Alpaca, DB, SpecStore, Notifier)
  2. AIService runs as a scheduled job (same as IV collector):
     - 08:35 ET: RunAIScreen() → persist to DB → notify Discord
     - No event bus needed — omo-data is fire-and-forget to DB

omo-core startup:
  1. bootstrapFromDB() → reads ai_screener_results from DB (same as today)
     → publishes EventAIScreenerCompleted internally → symbol router picks up
  2. No schedulerLoop, no catch-up logic, no AIService instance at all
  3. Symbol router unchanged — still subscribes to EventAIScreenerCompleted
     from the internal bootstrap publish
```

## Key insight

The event bus coupling is already broken: omo-core's bootstrap reads from
DB and re-publishes events internally. The scheduled run also writes to DB
first, then publishes. **The DB is the real hand-off, not the event bus.**
Moving the writer to omo-data just makes this explicit.

## File changes

### omo-data (new wiring)

**`backend/cmd/omo-data/main.go`**
- Add `AIService` creation after existing scheduled services (~line 306)
- Deps: Alpaca adapter (already exists), TimescaleDB (exists), Discord
  notifier (exists), SpecStore (new — needs `store_fs.NewStore`)
- Schedule: same `ivcollector.Service` pattern — `Start(ctx)` → internal
  timer loop at 08:35 ET
- Remove event bus dependency from AIService constructor (omo-data doesn't
  have one; the bus arg can be nil since RunAIScreen only publishes to bus
  after DB persist, and omo-data doesn't need to publish)

**`backend/internal/app/screener/ai_service.go`**
- Make `Bus` field optional (nil-safe the `Publish` call in RunAIScreen)
- Remove `needsCatchUpScreen` and `lastCatchUpDate` — catch-up logic
  only existed because omo-core restarts; omo-data is long-running
- Keep `bootstrapFromDB` exported — omo-core still calls it at startup

### omo-core (remove screener scheduling)

**`backend/cmd/omo-core/services.go`**
- Remove `AIService` creation (~lines 824-857)
- Remove `aiScreener.Start(ctx)` call
- Keep the bootstrap read path: on startup, call
  `screener.BootstrapFromDB(repo, specStore, eventBus)` → publishes
  `EventAIScreenerCompleted` per strategy → symbol router picks up
- Extract `bootstrapFromDB` into a standalone function (not a method on
  AIService) so omo-core doesn't need to instantiate the full service

### No changes needed

- `backend/internal/app/symbolrouter/` — unchanged, subscribes to
  `EventAIScreenerCompleted` from omo-core's internal bootstrap publish
- `backend/internal/app/screener/ai_service.go` core logic — `RunAIScreen`,
  `pass0`, `enrichHTF`, `callModel` all stay the same
- `ai_screener_results` DB table — unchanged
- Strategy TOML configs — unchanged

## SpecStore in omo-data

omo-data currently has no SpecStore (it doesn't know about strategy specs).
The screener needs it to read `spec.Screening.Description` for each
strategy. Two options:

**Option A (simple):** omo-data reads strategy TOMLs from the bind-mounted
`/configs/strategies/` directory using the same `store_fs.NewStore` that
omo-core uses. The configs are already bind-mounted into omo-data's
container (they're in the `configs/` COPY in Dockerfile.data).

**Option B (decoupled):** Add a `screener_config` table with
(strategy_key, screening_description, asset_classes, routing_symbols).
omo-core writes it on startup; omo-data reads it for the screener.
Over-engineered for now.

**Recommendation:** Option A. The TOML files are already in both containers.

## Migration steps (ordered)

1. Make AIService.Bus nil-safe (1 hour)
   - Guard the `Publish` calls in RunAIScreen with `if s.bus != nil`
   - Extract `BootstrapFromDB` as a standalone function

2. Wire AIService into omo-data (2-3 hours)
   - Add SpecStore (store_fs) initialization
   - Create AIService with nil bus
   - Start as scheduled service after IV collector

3. Remove AIService from omo-core (1 hour)
   - Delete creation + Start call
   - Replace with standalone `BootstrapFromDB` call at startup
   - Remove catch-up logic (needsCatchUpScreen, lastCatchUpDate)

4. Test (2-3 hours)
   - Verify omo-data screener runs at 08:35 ET
   - Verify omo-core bootstrap reads results from DB
   - Verify symbol router receives events on omo-core startup
   - Verify omo-core restart does NOT trigger screener re-run
   - Verify Discord notification fires from omo-data

5. Rebuild omo-data Docker image + restart both services

## Rollback

If the migration breaks, revert steps 2-3:
- Re-add AIService to omo-core services.go
- Remove AIService from omo-data main.go
- Both changes are mechanical wiring, no logic change

## Timeline

| Step | Effort |
|---|---|
| Nil-safe bus + extract bootstrap | 1h |
| Wire into omo-data | 2-3h |
| Remove from omo-core | 1h |
| Test + Docker rebuild | 2-3h |
| **Total** | **~1 day** |
