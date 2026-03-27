---
name: Bug - EOD FLATTEN fires immediately on every trade
description: Daily screener backtest mode has every trade exiting at EOD FLATTEN at the same bar as entry. Entry and exit timestamps are identical (same minute). This started after the screener refactoring. The position monitor tick loop is disabled in backtest mode, and EvalExitRules uses barTime correctly. Need to investigate if the evaluateEODFlatten is receiving wrong time or if the calendar check is failing for 2026 dates.
type: project
---

Every trade in the daily screener backtest exits immediately via EOD FLATTEN at the same bar as entry.
**Why:** Likely clock issue — either currentBarTime isn't updated before EvalExitRules, or the new effectiveSymbols refactoring changed the order of operations causing the first EvalExitRules to run before currentBarTime is set to the actual bar time.
**How to apply:** Check the order of currentBarTime.Store(minTime) vs EvalExitRules calls in the replay loop. Also verify Jan 20, 2026 is handled correctly by the NYSE calendar.
