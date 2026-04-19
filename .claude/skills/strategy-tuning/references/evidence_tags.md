# Evidence Tags

Every factual claim in a tuning report, backtest analysis, or Discord
notification must carry one of three tags so the reader can separate
measurement from reasoning from modeling choice.

## The three tags

- `[actual]` — directly observed in verified tool output: a backtest result
  file, a database query, a diff, a log line, a test assertion. The source
  must be reproducible.
- `[inference]` — a conclusion drawn from `[actual]` evidence by reasoning.
  The reader should be able to see how the conclusion follows from the
  data.
- `[assumption]` — an input that was not measured: slippage bps, fill
  timing, capacity estimate, cost model, parameter prior, scenario setup.
  Must be stated so the reader can challenge it.

No fourth category. If a number has no tag, do not state it.

## Examples

```
[actual] PF 1.604 on holdout (2025-12-20 → 2026-04-14, 127 trades).
[actual] The largest winner contributed $3,214 of $8,910 gross profit.
[inference] Residual Sharpe of 0.42 after SPY/VVIX factors suggests the
  strategy is mostly a long-beta long-vol proxy, not novel alpha.
[assumption] Backtest uses slippage_bps = 5; live IBKR options fills
  historically show 8-12 bps on this universe, so live PF should be
  discounted by roughly 15%.
```

## When to use each

- Backtest metrics, trade counts, file paths, commit hashes, config values,
  code references → `[actual]`.
- Cross-strategy comparisons, alpha decay judgments, regime calls, "this
  change improved X because Y" narratives → `[inference]`.
- Slippage, commission, fill model, forward-looking capacity, simulation
  parameters, counterfactual scenarios → `[assumption]`.

## Anti-patterns

- "The strategy is robust." → untagged narrative, drop or tag as
  `[inference]` and cite what evidence supports it.
- "PF improved from 1.16 to 1.60." → `[actual]` only if both numbers come
  from the same holdout under the same slippage and symbol universe. If
  the comparison crosses definitions, split into two `[actual]` lines and
  an `[inference]` that names the delta.
- Mixing `[actual]` and `[inference]` in one sentence. Split them.

## Scope

Applies to:
- strategy-tuning skill pass summaries and Discord notifications
- backtest-analysis skill reports
- quant-analyst agent outputs when feeding the tuning loop
- Any memory file or plan document that downstream decisions will trust
