# Sunset note: whale_pullback_v1

Date: 2026-05-06
Status: Shelved. Lifecycle moved to Deprecated.
Driver: post-fix backtest revealed the strategy has no edge.

## What happened

whale_pullback_v1 shipped at lifecycle PaperActive on 2026-05-06
(commit 1acd9f8a). Pass-1 tuning showed PF 0.485 -> 0.983 at 10 bps,
which was reverted (commit 5c95e667) after the result failed holdout.

A backtest fill-event bug was then discovered (see
_workspace/whale_pullback_v1_backtest_fill_event_finding.md): the
sharded slice pipeline delivered FillReceived too late for the
strategy's PendingEntry -> PositionSide handshake, leaving exits
silently dead. PR #89 fixed the bug.

The first honest backtest after the fix produced PF 0.388 train (9mo)
/ 0.450 holdout (3mo) at default params, with -$76,541 / -$44,620 P&L
on $100k equity and DD 76.86% / 45.43%. The pre-fix PF 0.983 result
was a mirage: dead exits let losing entries mean-revert to scratch by
EOD, masking that the entry signal itself was broken.

## Quant verdict

Full session: 4624 trades on train, every axis bleeding. Best symbol
PF: SOFI 0.93. Best 30-min entry window PF: 09:30 = 0.97. Best
confluence band PF: 60-69 = 0.47 (non-monotonic — confluence stack
adds zero edge for this setup). Of all losers, only 22.2% were ever
profitable (MFE > 0.1%) — entry-quality problem, not exit-tuning.

Verdict: shelve. No parameter configuration produces PF >= 1.0 on
both train and holdout because the entries are wrong, not the exits.

## Diagnostics done before shelving

1. Bucketing 4 ways by volatility profile. B1 ETFs PF 0.138 (dead),
   B2 mega-cap PF 0.271, B3 mid-cap growth PF 0.426, B4 high-vol
   mid-cap PF 0.638. Best bucket below the 800-trade noise floor for
   coordinate descent. Universe pruning to the cluster
   (SOFI/HOOD/SOXL/HIMS/AMD/SNOW) lifted baseline to PF 0.611 (n=2212)
   but still unprofitable.
2. Sub-slice analysis on the cluster identified one slice clearing
   the quant's PF 1.0 / n 300 bar: SHORT entries 09:30-10:30 ET on
   the cluster, n=313 train PF 1.017, n=39 holdout PF 1.661.
   Generalizes directionally but holdout n is small. Magnitude
   ~2% annual at 10 bps. Edge is structural (fading morning highs on
   liquid high-vol mid-caps via session VWAP being immature in the
   first hour) rather than the whale-pullback thesis itself.
3. EOD_FLATTEN removal test confirmed the apparent PF 17 on
   EOD-clipped trades was survivorship bias: removing the rule made
   PF worse (0.611 -> 0.571 on cluster train) because trades that
   would have closed profitably at EOD instead ran further and hit
   MAX_LOSS. -$7,023 net swing.

## Why long-only equity can't capture the edge

The morning-fade-SHORT signal is the only sub-slice with edge. IBKR
deployment is constrained to long-only equity in this stack — the
bearish edge is unreachable without options. Therefore the strategy
has no path to profitability under the operational constraints.

## What stays in the codebase

- Strategy code:
  backend/internal/app/strategy/builtin/whale_pullback_v1.go
  Kept for history. Deprecated lifecycle prevents router assignment.
- Domain primitives (REUSABLE):
  backend/internal/domain/strategy/ema_rolling.go (EMARolling)
  backend/internal/domain/strategy/volume_histogram.go (VolumeHistogram
  with POC, HVNBins, HasHVNInRange — useful for any HVN clear-path
  filter or value-area overlay in future strategies).
- Backtest fill-event fix (PR #89): orthogonal infrastructure
  improvement that benefits every backtest going forward.
- Harness learnings codified during this session:
  .claude/skills/backtest-analysis/SKILL.md "Parameter inertness
  signals strategy state divergence, not a tuning failure".
  .claude/agents/strategy-tuner.md Phase 1 parameter-inertness
  preflight.

## Pointers to source artifacts

- _workspace/whale_pullback_v1_backtest_fill_event_finding.md
- _workspace/whale_pullback_v1_backtest_fill_event_plan.md
- _workspace/whale_pullback_v1_train_baseline_10bps.json
- _workspace/whale_pullback_v1_holdout_baseline_10bps.json
- _workspace/whale_pullback_v1_train_cluster_10bps.json
- _workspace/whale_pullback_v1_train_cluster_no_eod_10bps.json
- _workspace/whale_pullback_v1_train_extreme_10bps.json
- PR #89: backtest fill-event fix, commits 27e85462 / bdfbef16 /
  bc2e4103 / a6f37263

## What replaces it

avwap_long_v1 (TOML-only fork of avwap_v4_equity, long-only equity
intraday, gated on Phase 0 viability test). See
_workspace/avwap_long_v1_plan.md.
