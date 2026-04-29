# Fix backtest ResetAnchors infinite-reset loop (AVWAP bias parity)

## Goal
Restore parity between live and omo-replay backtest by stopping the per-bar
`ResetAnchors` loop that holds `CalcBarCount` at 1, so AVWAP's bias gate can
establish in backtest the same way it does in live. Symptom: backtest
avwap_v4 blocks all entries with `bias: no directional bias established`
while live blocks 0; backtest produces ~1 trade per full session whereas
live and other backtest paths produce 10-20.

## Root cause
`backend/internal/app/strategy/runner.go:resolveAIAnchors` builds a `merged`
anchor map from `r.anchorResolver` + AI's `resolved`. When AI is disabled
but `aiAnchorResolver != nil` (default in omo-replay backtest), AI's
fallback returns `len(resolved) == 1` (typically `session_open` only),
bypassing the empty-guard at L458. `r.anchorResolver` in the backtest path
also returns only `session_open`. `merged` therefore contains a single
anchor; `ar.ResetAnchors(merged)` drops `pd_high` / `pd_low` and zeroes
`CalcBarCount`. Next bar `hasMissingAnchor(symbol)` returns true so the AI
branch re-fires at `runner.go:1423`, looping forever.

Trace (60-bar AAPL replay, post-instrumentation):
- UpdateCalc calls: 60
- ResetAnchors calls: 60 (perfect 1:1)
- "AI anchor resolution complete ... anchors=1" log: 60
- `CalcBarCount` value distribution: every value is 1
- Pre-warmup CalcBarCount was 800 (warmup correctly seeded; first
  ResetAnchors after warmup zeroed it)

Why live doesn't have the bug: in live, `r.anchorResolver` returns all
configured anchors (`session_open`, `pd_high`, `pd_low`) at L482-486, so
`merged` ends up complete after the merge → `hasMissingAnchor` returns
false → no re-resolve loop.

## Approach (preserve existing anchors during merge)
At `runner.go:482-491`, before `ar.ResetAnchors(merged)`, seed `merged`
with any **existing** anchor times the AVWAP state already holds for
anchor names that neither `r.anchorResolver` nor AI returned. The merge
becomes additive rather than destructive: anchors set during `Init` or a
prior `resolveSessionAnchors` survive across AI re-resolutions.

Pseudocode (final order: existing -> anchorResolver -> AI):
```go
merged := make(map[string]time.Time)
// 1. Start from existing state so anchors not re-resolved survive.
for _, name := range ar.AnchorNames() {
    if t, ok := ar.AnchorTime(name); ok && !t.IsZero() {
        merged[name] = t
    }
}
// 2. anchorResolver overlays (live: full set; backtest: usually session_open).
if r.anchorResolver != nil {
    for k, v := range r.anchorResolver(symbol, bar.Time, ar.AnchorNames()) {
        merged[k] = v
    }
}
// 3. AI overlays last (highest authority for dynamic anchors).
for k, v := range resolved {
    merged[k] = v
}
ar.ResetAnchors(merged)
```

Requires a new accessor `AnchorTime(name string) (time.Time, bool)` on the
`anchorResettable` interface. `AVWAPState` already exposes `HasAnchor` via
`s.Calc.AnchorPoints()`; add a sibling method that returns the
`AnchorTime`.

## Files
- `backend/internal/app/strategy/runner.go`
  - Extend `anchorResettable` interface with `AnchorTime(name string) (time.Time, bool)`.
  - Modify the merge block in `resolveAIAnchors` (lines ~482-491).
  - Mirror the same fix in `resolveSessionAnchors` if it has the same
    pattern (verify before editing — may already be safe because it
    triggers only on date change).
- `backend/internal/app/strategy/builtin/avwap_v1.go`
  - Add `func (s *AVWAPState) AnchorTime(name string) (time.Time, bool)`
    returning `s.Calc.AnchorPoints()[name].AnchorTime` (or zero/false).
  - Diag log statements at `UpdateCalc`, `ResetAnchors`, and bias eval
    stay in place during verification, removed after green.

## Verification
1. **Build**: `cd backend && go build ./...`
2. **Unit tests**: `cd backend && go test ./internal/app/strategy/...`
3. **AAPL trace replay** (narrow window, instrumentation still in):
   - Run: `/tmp/omo-replay --backtest --emit-gated-diag --from 2026-04-29T13:30:00Z --to 2026-04-29T14:30:00Z --strategies avwap_v4 --symbols AAPL --no-ai=true --output-json=_workspace/aapl_diag_postfix.json`
   - Expect: `[avwap-diag] UpdateCalc` count grows monotonically past 10 within ~10 bars; `BiasEval` log eventually shows `bias="LONG"` or `"SHORT"` non-empty.
   - Expect: `ResetAnchors` calls drop sharply (only on session boundary or genuine new-session, not per-bar).
4. **Full-day replay + SQL diff**:
   - Run: `/tmp/omo-replay --backtest --emit-gated-diag --from 2026-04-29 --to 2026-04-30 --strategies avwap_v4,macd_only_v1 --no-ai=true`
   - Capture new run_id from log; bucket reasons:
     ```sql
     SELECT regexp_replace(reason, ': .*', '') AS reason_class, COUNT(*)
     FROM strategy_signal_events
     WHERE payload->>'tag' = 'backtest_<new_run_id>'
       AND status = 'blocked' AND strategy = 'avwap_v4'
     GROUP BY 1 ORDER BY 2 DESC;
     ```
   - Expect: `bias` count drops from 2210 to near-0; `slope` / `confluence` / `entry_specific` populate at counts comparable to live's distribution
     (live target was: slope=1536, entry_specific=523, regime=261, confluence=105).
5. **Trade count sanity**: backtest output JSON shows >1 trade for a full
   session (pre-fix produced 1 MRVL trade only).

## Risks / blast radius
- **Live**: existing-anchor seed is harmless because `r.anchorResolver`
  already returns the complete set; existing entries are overwritten by
  the same values. Zero behavioral change for live.
- **Backtest**: anchors persist across re-resolution. Edge case — a
  stale anchor time from a prior session could survive. Mitigated by the
  fact that `resolveSessionAnchors` runs on date change and overwrites
  via the anchorResolver overlay, and AI overlay overrides anything
  dynamic. If still concerning, gate the existing-anchor seed by checking
  the existing anchor's session date matches `bar.Time` session date.
- **AI re-enable later**: when `--no-ai=false` and AI returns the full
  set of dynamic anchors, the merge order (AI last) means AI wins. No
  regression.
- **MACD drift (separate bug)**: not addressed by this plan; tracked
  separately.

## Cleanup (after green)
1. Remove the three `[avwap-diag]` log statements from `avwap_v1.go`.
2. File a separate ticket for MACD post-warmup drift (~0.09 max diff,
   14/78 pairs > 0.01) as bug #2 from the parity diff.

## Out of scope
- MACD drift fix.
- Wiring AI anchor resolver to actually return dynamic anchors in
  backtest.
- Refactoring the dual-branch `aiAnchorResolver != nil` /
  `anchorResolver != nil` logic in `handleBarCore` — invasive and not
  load-bearing for this bug.

## Reference data
- Diff reproducible from this conversation's run_id `1777491531` and
  live untagged rows for `2026-04-29`.
- Live binary started stamping `payload.bar.time` after commit 942d244b
  was deployed mid-day, so live-side bar.time coverage is ~15 bars
  (15:20-15:35 ET only). Tomorrow's run will have full coverage if live
  is restarted before market open.
