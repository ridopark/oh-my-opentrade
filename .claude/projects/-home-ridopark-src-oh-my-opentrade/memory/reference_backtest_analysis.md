---
name: Backtest Analysis Script
description: Python script at scripts/backtest_analysis.py for detailed backtest performance metrics, risk analysis, and Monte Carlo projections
type: reference
---

`scripts/backtest_analysis.py` — standalone Python script for deep backtest analysis.

Sections: detailed performance metrics, per-symbol breakdown, long/short analysis, risk analysis (drawdown, consecutive losses, position sizing), statistical significance (t-test, bootstrap CI), entry-time bucketed analysis, symbol recommendations, annualized projections with Monte Carlo simulation.

Requires: numpy, pandas, scipy.

Usage: paste trade data into the `trades` list, then `python scripts/backtest_analysis.py`.
