# AVWAP Entry Check Breakdown -- Backend Spec

## EntryCheckResult JSON Shape

```json
{
  "name": "pinch",
  "passed": false,
  "reason": "gap 12 bps outside [20, 80]"
}
```

Fields:
- `name` (string): one of `"pinch"`, `"cap_reclaim"`, `"gap_reclaim"`, `"pullback"`, `"handoff"`, `"breakout"`, `"bounce"`
- `passed` (bool): `true` if the entry type fired, `false` if it was blocked
- `reason` (string): short human-readable explanation; `"fired"` when passed is true

## Updated EntryGatedPayload JSON Shape

```json
{
  "symbol": "AAPL",
  "strategy": "avwap",
  "setupType": "multi",
  "gatesPassed": 4,
  "gatesTotal": 5,
  "blockingGate": "entry_specific",
  "blockingDetail": "confluence met but no entry type conditions satisfied",
  "entryChecks": [
    { "name": "pinch", "passed": false, "reason": "gap 12 bps outside [20, 80]" },
    { "name": "cap_reclaim", "passed": false, "reason": "no capitulation anchor" },
    { "name": "gap_reclaim", "passed": false, "reason": "no recent cross-below" },
    { "name": "pullback", "passed": false, "reason": "trend bars 2 < 5" },
    { "name": "handoff", "passed": false, "reason": "history 3 < 4 bars" },
    { "name": "breakout", "passed": false, "reason": "hold bars 1 < 3" },
    { "name": "bounce", "passed": false, "reason": "price not touching AVWAP" }
  ],
  "confluence": { "score": 4, "maxScore": 10, "...": "..." },
  "indicators": { "rsi": 55.2, "volumeRatio": 1.3, "...": "..." },
  "bar": { "open": 150.0, "high": 151.0, "low": 149.5, "close": 150.5, "volume": 12000 }
}
```

The `entryChecks` field is omitted when empty (e.g., when blockingGate is not `"entry_specific"`).

## Changed Files

| File | Change |
|------|--------|
| `backend/internal/domain/event.go` | Added `EntryCheckResult` struct; added `EntryChecks` field to `EntryGatedPayload` |
| `backend/internal/app/strategy/builtin/avwap_v1.go` | Added `entryChecks` transient field to `AVWAPState`; added `recordCheck`/`recordCheckPassed` helpers; added `maxAboveCount`/`maxHistLen` helpers; instrumented all 7 evaluate functions with failure reasons; wired `entryChecks` into both `emitEntryGated` and `EmitSignalProgress` payloads; reset collector at top of `evaluateEntries` |
