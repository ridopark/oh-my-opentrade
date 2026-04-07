# Frontend: AVWAP Entry Check Breakdown

## Status: DONE

Build verified: `npm run build` passes with no new errors.

## Changed Files

### 1. `apps/dashboard/lib/types.ts`
- Added `EntryCheckResult` interface (`name`, `passed`, `reason`)
- Added optional `entryChecks?: EntryCheckResult[]` field to `EntryGatedPayload`

### 2. `apps/dashboard/components/avwap-confluence-matrix.tsx`
- Added `useState<string | null>` to track which symbol row is expanded
- When `blockingGate === "entry_specific"` and `entryChecks` exists, the badge becomes a clickable button with a chevron indicator
- Clicking toggles an expandable `<TableRow>` below the symbol row
- The detail row spans all 11 columns and renders a responsive 1-/2-column grid of entry checks
- Each check shows: pass/fail icon, mono-font entry type name, and reason text in zinc-500
- Non-entry_specific blocking gates remain as static badges (no click behavior)
- Hooks are called before the early return to satisfy `react-hooks/rules-of-hooks`

## UI Behavior
- Chevron rotates 180 degrees when expanded (CSS transition)
- Only one symbol can be expanded at a time (clicking another collapses the previous)
- Detail row has a subtly darker background (`bg-zinc-900/50`) to distinguish from data rows
