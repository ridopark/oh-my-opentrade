# SSE silent-drop fix — handoff (2026-04-24)

Commit: 19b00b11 `fix(sse): stop silent event drops on bar-close bursts`
File touched: `backend/internal/adapters/sse/handler.go` (+27 / -10)

## Symptom

Dashboard Signals page showed signals from one 5-min bar close but skipped the next.
Example: at 10:07 ET, table still showed only 09:59:59 ET entries; refreshing the page
pulled in the missed 10:04:59 entries via `/api/signals/recent` + the backend's
`SignalProgressSnapshots()` sent on new SSE connect.

Reproducible across multiple bar closes in the morning session. Only live push was
affected; persistence and HTTP API were fine.

## Root cause

`sse/handler.go` broadcasts to each client via a per-client Go channel. The old
design had three stacked flaws that guaranteed silent loss on bar-close bursts:

1. Channel buffer = 64 (`handler.go:182`). A 5-min bar close fires ~60 EntryGated
   events (34 symbols x 2 strategies) plus MarketBarSanitized, StateUpdated,
   StrategyEvaluation, EnrichedBar — easily 200+ events back-to-back.

2. Broadcast was non-blocking (`select { case ch <- evt: ... default: drop++ }`).
   When the channel filled, events were dropped with zero log output.

3. The kick rule was "100 **consecutive** drops." Any single successful send reset
   the counter to 0, so oscillating bursts (drop, land, drop, land...) never tripped
   the kick. The client stayed "connected" from the server's point of view while
   missing events. No "disconnecting slow consumer" log ever fired.

Net effect: silent data loss on every bar-close burst; refresh masked it because
`SignalProgressSnapshots()` replays the latest-per-symbol EntryGated on new connect.

## Fix

In `handler.go`:
- Per-client buffer: 64 -> **2048** (`sseBufferSize` const). Absorbs a full
  bar-close burst with headroom.
- Drop counter: `int` (consecutive, reset on success) -> `atomic.Int64`
  (**cumulative**, never resets). `kicked` flag switched to `atomic.Bool`
  guarded by `CompareAndSwap` so concurrent broadcasts can't double-close `c.ch`.
- Kick threshold: 100 consecutive -> **1000 cumulative**. A pinned reader
  accumulates drops monotonically and gets kicked cleanly.
- Logging: first drop now emits `WARN "SSE: event dropped (client channel full)"`
  so silent loss is visible in Loki; kick log keeps `total_dropped`.

## Verification (post-restart, 2026-04-24 ~10:46 ET)

Captured 4 min 11 s of SSE via the Next.js dashboard proxy (`http://localhost:8000/api/events`)
straddling the 10:49:59 ET 5-min bar close:

```
18064 FormingBar
  254 MarketBarSanitized
  254 EnrichedBar
  220 MarketBarReceived
  170 StateUpdated
  120 EntryGated           <- 60 snapshot on connect + 60 live at bar close
   60 StrategyEvaluation
   26 RegimeShifted
    0 "SSE: event dropped"   (grep on logs/omo-core.log)
```

EntryGated `occurredAt` histogram confirms live delivery:
- 60 events at 14:44 (snapshot)
- 23 events at 14:49 + 37 events at 14:50 (= 60 live from 14:49:59 bar close)

Backend broadcasts, Next.js proxy forwards, events reach the client in real time.

## Residual concerns (not fixed)

These were flagged during investigation but are separate from the drop bug:

1. **Duplicate EventSource per page.** `apps/dashboard/app/(dashboard)/signals/page.tsx`
   opens two SSE connections: one via `useSignalProgress()` (=> `useEventStream`
   wrapping `new EventSource`) at line 10, plus a direct `new EventSource("/api/events")`
   at line 34. Other pages do the same pattern. Each connection is a separate
   backend client channel doing redundant JSON marshal + write.

2. **`onerror` has no reconnect backoff.** `apps/dashboard/lib/event-stream.ts:47`
   sets error state and relies on the browser's native EventSource retry. In dev,
   Next.js HMR / visibility changes churn these connections (saw 5-8 reconnect
   cycles in a minute during today's session).

3. **Thread-safety inside `broadcast()`.** The old `c.dropCount++` under `RLock`
   was racy if the bus ever fans out subscription callbacks concurrently. The fix
   moved drop/kick state onto atomics, but the `h.clients` map iteration under
   `RLock` is still the existing pattern — untouched here.

## Next recommended work (#3 from the investigation)

Consolidate all dashboard SSE into a single shared EventSource with client-side
fanout.

- Create one module-level EventSource subscriber in `lib/event-stream.ts` that
  holds a Map<EventType, Set<listener>>.
- Hooks (`useEventStream`, `useSignalProgress`, `useDebateEvents`, etc.) register
  listeners against the shared instance instead of opening their own connection.
- On the signals page, remove the direct `new EventSource("/api/events")` in
  favor of a typed hook backed by the shared connection.
- Add explicit reconnect-with-backoff (1s -> 2s -> 5s -> 10s capped) on `onerror`.

Benefits: halves backend client count (two per page -> one per tab), removes
duplicate JSON marshal on the server, makes the "0 drops at 2048 buffer" claim
robust even if more pages are added later.

Estimated blast radius: `lib/event-stream.ts` rewrite, plus ~6 call-site updates
(`hooks/use-strategy-evaluation-stream.ts`, `components/strategy/ActivityFeed.tsx`,
`signals/page.tsx`, `live/page.tsx`, `lib/use-chart-data.ts` — which uses its own
EventSource for candlestick streaming, leave as-is for now).

## Recheck recipe (if silent-drop symptom returns)

```
# 1. Drop log check — must stay zero under healthy load:
grep -c "SSE: event dropped" logs/omo-core.log

# 2. Live end-to-end via Next.js proxy across a bar close
#    (pick a time ~1 min before XX:X4:59 or XX:X9:59 ET = 5-min boundary):
timeout 90 curl -sN -H "Accept: text/event-stream" \
    http://localhost:8000/api/events > /tmp/sse.log 2>&1
grep "^event:" /tmp/sse.log | sort | uniq -c
grep -A1 "^event: EntryGated" /tmp/sse.log | grep occurredAt \
    | grep -oE 'occurredAt":"[^"]+' | awk -F'T' '{print $2}' | cut -c1-5 | sort -u

# Expect EntryGated occurredAt to include the most recent bar-close minute
# (e.g. 14:44 AND 14:49 if window straddles 14:49:59Z close). If only snapshot
# appears with no live close, the issue is no longer in `broadcast()` — look
# at the bus subscription path (strategy_runner publishing EntryGated) or the
# Next.js proxy buffering in `apps/dashboard/app/api/events/route.ts`.

# 3. If drops ARE logged, look for the ^SSE: disconnecting slow consumer
#    line and its total_dropped field. 1000 cumulative drops in practice
#    means something worse than a 5-min bar burst — check for an event type
#    unexpectedly firing in a hot loop.
```
