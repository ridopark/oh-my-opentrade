---
name: No rebuild for TOML-only changes
description: Skip go build/restart when only strategy TOML configs change — omo-core reads them dynamically
type: feedback
---

Don't rebuild and restart omo-core when changes are only in TOML strategy configs (configs/strategies/*.toml). The server reads config files dynamically. Only rebuild when Go source code changes.

**Why:** Rebuilding + restarting wastes ~20 seconds per backtest iteration and is unnecessary for parameter tuning.
**How to apply:** During strategy tuning, only rebuild/restart after ENGINE_CHANGEs (Go code modifications). For PARAM_CHANGEs (TOML edits), just run the backtest directly.
