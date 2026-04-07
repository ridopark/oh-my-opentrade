# QA Report: AVWAP Entry Check Breakdown — Integration Coherence

**Date:** 2026-04-01
**Scope:** EntryCheckResult struct + entryChecks field across Go backend and Next.js dashboard
**Verdict:** PASS with 1 advisory finding (dead code)

---

## 1. Go JSON Tags vs TypeScript Field Names

| Field | Go JSON tag | TS field | Match |
|-------|------------|----------|-------|
| `EntryCheckResult.Name` | `"name"` | `name: string` | PASS |
| `EntryCheckResult.Passed` | `"passed"` | `passed: boolean` | PASS |
| `EntryCheckResult.Reason` | `"reason"` | `reason: string` | PASS |
| `EntryGatedPayload.EntryChecks` | `"entryChecks,omitempty"` | `entryChecks?: EntryCheckResult[]` | PASS |

**Go source:** `backend/internal/domain/event.go` lines 192, 212-216
**TS source:** `apps/dashboard/lib/types.ts` lines 374-378, 422

---

## 2. SSE Event Flow Completeness

| Step | Location | Status |
|------|----------|--------|
| Backend publishes `EventEntryGated` with `EntryGatedPayload` (including `EntryChecks`) | `backend/internal/app/strategy/builtin/avwap_v1.go` lines 1819, 1926 | PASS |
| SSE handler subscribes to `EventEntryGated` | `backend/internal/adapters/sse/handler.go` line 46 | PASS |
| SSE handler marshals `wireEvent.Payload` (type `any`) via `json.Marshal` which follows JSON tags on `EntryGatedPayload` | `backend/internal/adapters/sse/handler.go` lines 58, 214 | PASS |
| SSE emits `event: EntryGated\ndata: {...}\n\n` | `backend/internal/adapters/sse/handler.go` line 188 | PASS |
| Frontend `useSignalProgress` subscribes to `"EntryGated"` event type | `apps/dashboard/lib/event-stream.ts` line 173 | PASS |
| Frontend parses `evt.payload as EntryGatedPayload` (which includes `entryChecks?`) | `apps/dashboard/lib/event-stream.ts` line 187 | PASS |
| `avwap-confluence-matrix.tsx` accesses `row.entryChecks` with guard | `apps/dashboard/components/avwap-confluence-matrix.tsx` lines 204, 237 | PASS |

---

## 3. Import Verification

| Import | File | Status |
|--------|------|--------|
| `EntryCheckResult` imported in types.ts | N/A (defined in same file, line 374) | PASS |
| `EntryCheckResult` imported in `avwap-confluence-matrix.tsx` | Line 4: `import type { EntryCheckResult, EntryGatedPayload } from "@/lib/types"` | PASS |
| `EntryGatedPayload` imported in `event-stream.ts` | Line 4: `import type { ..., EntryGatedPayload, ... } from "@/lib/types"` | PASS |

---

## 4. Optional Field / Nil Handling

| Check | Status |
|-------|--------|
| Go `EntryChecks` uses `omitempty` -- nil/empty slice omits field from JSON | PASS (`json:"entryChecks,omitempty"` at event.go:192) |
| TS `entryChecks` is optional (`?`) | PASS (types.ts:422) |
| Component guards with `row.entryChecks &&` before rendering expand button | PASS (avwap-confluence-matrix.tsx:204) |
| Component guards with `row.entryChecks &&` before rendering grid row | PASS (avwap-confluence-matrix.tsx:237) |
| Backend resets `entryChecks` slice each bar via `s.entryChecks[:0]` | PASS (avwap_v1.go:1712) |

---

## 5. Entry Check Names (7 expected)

Expected names from Go `EntryCheckResult` comment (event.go:213):
`pinch`, `cap_reclaim`, `gap_reclaim`, `pullback`, `handoff`, `breakout`, `bounce`

Actual `recordCheck` / `recordCheckPassed` calls in `avwap_v1.go`:

| Name | recordCheck (fail) | recordCheckPassed (success) | Match |
|------|-------------------|----------------------------|-------|
| `"pinch"` | lines 1216, 1220, 1320 | line 1716 | PASS |
| `"cap_reclaim"` | line 888 | line 1722 | PASS |
| `"gap_reclaim"` | lines 1328, 1404 | line 1728 | PASS |
| `"pullback"` | lines 1047, 1206 | line 1734 | PASS |
| `"handoff"` | lines 1414, 1575 | line 1740 | PASS |
| `"breakout"` | lines 896, 1039 | line 1746 | PASS |
| `"bounce"` | lines 1583, 1697 | line 1752 | PASS |

All 7 check names are consistent between the Go comment, the `recordCheck` calls, and the `recordCheckPassed` calls. The `evaluateEntries` function (line 1711) calls all 7 evaluate functions in priority order: pinch, cap_reclaim, gap_reclaim, pullback, handoff, breakout, bounce.

---

## 6. Advisory Finding: Dead Code

**Severity:** Low (no runtime impact)

`AVWAPConfluenceMatrix` component (`apps/dashboard/components/avwap-confluence-matrix.tsx`) is **exported but never imported or rendered** by any other component or page. The entry checks grid UI it contains is therefore unreachable.

The `signal-progress-table.tsx` component receives `avwapProgress` data and renders the unified signal table, but does **not** import `AVWAPConfluenceMatrix` and does **not** reference `entryChecks` anywhere.

**Impact:** The entry check breakdown UI works correctly in isolation (types match, guards are present), but users will not see it unless `AVWAPConfluenceMatrix` is mounted in a page or the signal-progress-table is updated to show entry checks inline.

**Recommendation:** Either import and render `AVWAPConfluenceMatrix` in the signal dashboard page, or integrate the `EntryChecksGrid` sub-component into `signal-progress-table.tsx`.

---

## Summary

| Category | Result |
|----------|--------|
| JSON tag alignment | PASS (4/4 fields) |
| SSE flow end-to-end | PASS (7/7 steps) |
| Imports | PASS (3/3 checked) |
| Optional/nil handling | PASS (4/4 guards) |
| Entry check names | PASS (7/7 names) |
| Component reachability | ADVISORY (dead code) |

**Overall: All integration boundaries are coherent. The only issue is that the matrix component is not yet wired into the page tree.**
