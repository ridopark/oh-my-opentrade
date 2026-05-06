# Plan: avwap_long_v1 — long-only equity intraday strategy

Date: 2026-05-06
Status: Plan, awaiting Phase 0 validation gate before TOML write.
Driver: this session's diagnostic that shelved whale_pullback_v1, plus
operational constraint that IBKR is long-only equity in this deployment.

## 1. Context

whale_pullback_v1 was shelved on 2026-05-06 after the backtest fill-event
fix (PR #89) revealed that pre-fix tuning produced PF 0.983 only because
exits were silently dead; with exits live, defaults bleed at PF 0.388
train / 0.450 holdout (DD 45-77%, -$76.5k / -$44.6k on $100k equity).
Quant verdict (full transcript in
_workspace/whale_pullback_v1_sunset_note.md): no plausible parameter
configuration produces PF >= 1.0 on both windows because 78% of losers
never went green (entry-quality problem, not exit-tuning).

Sub-slice diagnostic surfaced one signal that cleared the noise floor:
SHORT entries between 09:30-10:30 ET on a high-vol mid-cap cluster
(SOFI/HOOD/SOXL/HIMS/AMD/SNOW) had PF 1.017 train (n=313) and PF 1.661
holdout (n=39). However, IBKR live deployment is constrained to long
equity (no shorting); the bearish edge is unreachable without options,
and this work is scoped to equity only.

This plan therefore designs a long-only equity intraday strategy that
inherits avwap_v4_equity's tuned machinery and folds in this session's
universe-pruning learning, gated on a Phase 0 viability test because we
do NOT have direct session data on long-only avwap performance.

## 2. Constraints (locked)

- Equity only. No options.
- Long-only at the broker (IBKR limit).
- Intraday with EOD flatten (15 min before close).
- Reuse the existing avwap builtin engine. No Go code changes in this
  iteration; future ENGINE_CHANGEs are out of scope.
- Train: 2025-05-06 -> 2026-01-31. Holdout: 2026-02-01 -> 2026-05-06.
- Slippage: 10 bps for ranking, 20 bps stress as ship gate.
- $100k initial equity, max_positions=6, max_per_group=3.

## 3. Honest data position

What we know from this session:

- ETFs (SPY/QQQ/IWM) and stable mega-caps (XOM/MSFT/NVDA at default
  config) bleed badly on the whale_pullback engine. Likely transfers to
  avwap engine but not directly tested.
- High-vol mid-caps (SOFI/HOOD/SOXL/HIMS/AMD/SNOW + likely RIVN, AFRM,
  COIN, MRNA, RBLX, MU) are where edge concentrates.
- Time-of-day matters: morning is where the SHORT signal lived. We do
  NOT have data on which window has the LONG edge for avwap.
- EOD_FLATTEN's apparent PF 17 on whale_pullback was survivorship bias.
  Removal made things worse (-$7k swing). Keep EOD_FLATTEN.
- Direction asymmetry on this universe: SHORT side had edge, LONG side
  was bleeding (LONG 09:30-10:30 holdout PF 0.56). Long-only locks us
  to the bleeding side per session data.

What we do NOT know:

- Whether avwap_v4_equity (NOT whale_pullback) has a long-only edge on
  this universe. Different engine, different setups, different
  confluence stack. This is the Phase 0 question.
- Whether the morning concentration applies to long-only avwap setups.
  For shorts it does; for longs we have no signal.

## 4. Phase 0 — long-only avwap viability gate

Goal: cheap go/no-go before writing 270 LOC of new TOML.

Steps:

1. Run avwap_v4_equity with allowed_directions=["LONG"] only on the
   train window at 10 bps. Save to
   _workspace/avwap_v4_equity_long_only_train_10bps.json.
2. Same on the holdout window. Save to
   _workspace/avwap_v4_equity_long_only_holdout_10bps.json.
3. Compute PF, WR, DD, trade count, sharpe per window.
4. Quant analysis on the long-only train baseline. Per-symbol
   breakdown, per-hour breakdown, regime split.

Decision rule:

- Train PF >= 0.95 AND holdout PF >= 0.95 -> proceed to Phase 2.
- Either window PF in [0.85, 0.95) -> structural diagnostic before
  Phase 2; quant identifies the leverage, then re-evaluate.
- Either window PF < 0.85 -> stop. The avwap engine does not have a
  long-only edge on this universe and a new spec with the same engine
  will not fix that. Either change the universe choice, change the
  engine selection (e.g., a momentum or breakout-only engine), or
  shelve the long-only equity ambition entirely.

Phase 0 cost: 2 backtests (~2-3 minutes), 1 quant call.

## 5. Phase 1 — Sunset whale_pullback_v1

Independent of Phase 0 outcome. Touches:

- configs/strategies/whale_pullback_v1.toml: lifecycle.state ->
  "Deprecated", paper_only stays true, add a note in the description
  pointing at the sunset note doc.
- _workspace/whale_pullback_v1_backtest_fill_event_finding.md:
  postscript section linking to the sunset note.
- _workspace/whale_pullback_v1_backtest_fill_event_plan.md: postscript
  section linking to the sunset note.
- _workspace/whale_pullback_v1_sunset_note.md (new): one-page summary
  of why it was shelved and what evidence drove the verdict.

Indicators (EMARolling, VolumeHistogram) and tests stay in place.
Strategy code in backend/internal/app/strategy/builtin/whale_pullback_v1.go
stays for history. Deprecated lifecycle prevents the router from
assigning instances.

## 6. Phase 2 — New configs/strategies/avwap_long_v1.toml

Gated on Phase 0 passing. Forked from avwap_v4_equity.toml. Engine
unchanged: [hooks].signals = { engine = "builtin", name = "avwap" }.

Deltas vs avwap_v4_equity:

| Field                        | avwap_v4_equity      | avwap_long_v1                                |
|------------------------------|----------------------|----------------------------------------------|
| id, version, name            | avwap_v4_equity 4.3.0| avwap_long_v1 1.0.0                          |
| lifecycle.state              | PaperActive          | PaperActive (start paper)                    |
| routing.priority             | 80                   | 75                                           |
| routing.symbols              | full 34-sym universe | high-vol mid-caps; exact list per Phase 0    |
| allowed_directions           | ["LONG", "SHORT"]    | ["LONG"]                                     |
| allow_regimes                | TREND_UP, TREND_DOWN | TREND_UP only (longs in TREND_DOWN fade trend)|
| min_slope_bps                | 4.0                  | 4.0 (revisit if Phase 0 suggests otherwise)  |
| pullback_enabled             | false                | TBD per Phase 0 (may be cleaner long setup)  |
| min_confluence_score         | 10                   | initial value per Phase 0 baseline; sweep up |
| max_trades_per_day           | 2                    | 2 (keep)                                     |
| cooldown_seconds             | 3600                 | 3600 (keep, revisit if data warrants)        |
| EOD_FLATTEN min before close | 15                   | 15 (confirmed by removal test)               |

Inherited unchanged from avwap_v4_equity:

- Multi-anchor AVWAP: session_open, pd_high, pd_low.
- Confluence stack: fib, key_level, candle, band, dp_confluence,
  inducement (Factor 7), dp_z_conditioning. cofire_veto in shadow.
- Exit suite: MAX_LOSS 1.2%, CHANDELIER_TRAIL 0.8%/0.6% (sweep),
  STAGNATION 180 min, EOD_FLATTEN 15 min before close.
- midday_trap_shield (block 11-13 ET unless volume >= 1.5x).
- enforce_avwap_bias (only LONG above AVWAP).
- require_capitulation_for_shorts (no-op for long-only but kept as
  belt-and-braces in case allowed_directions is ever expanded).
- dynamic_risk enabled.
- options.enabled = false.
- max_positions=6, max_per_group=3.

## 7. Phase 3 — Validate avwap_long_v1

Same gates as Phase 0. Train + holdout at 10 bps, quant analysis, 20
bps stress as ship/no-ship gate.

Steps:

1. Train baseline: avwap_long_v1 on 2025-05-06 -> 2026-01-31 at 10 bps.
2. Holdout baseline: 2026-02-01 -> 2026-05-06 at 10 bps.
3. Quant analysis on train baseline. Identify priority tuning levers.
4. Optional: one tuning pass per the strategy-tuning skill, with
   holdout PF non-degradation gating every accepted change. Bounded
   to <= 10 backtests; abort if no clear improvement after 5.
5. 20 bps stress test on the best variant. Required: PF >= 0.95 at 20
   bps on both windows AND no holdout regression > 5% vs the 10 bps
   variant.

Promote to PaperActive only if all gates pass. Otherwise document the
result and either shelve avwap_long_v1 or schedule a follow-up
ENGINE_CHANGE design pass.

## 8. Files touched (TOML + docs only)

- configs/strategies/whale_pullback_v1.toml (deprecate)
- _workspace/whale_pullback_v1_backtest_fill_event_finding.md (postscript)
- _workspace/whale_pullback_v1_backtest_fill_event_plan.md (postscript)
- _workspace/whale_pullback_v1_sunset_note.md (new)
- _workspace/avwap_long_v1_plan.md (this file)
- configs/strategies/avwap_long_v1.toml (new, gated on Phase 0)

No Go code changes in this iteration.

## 9. Blast radius

- whale_pullback_v1 is paper-only, no live capital. Deprecation only
  affects router assignment; no rollback story needed.
- avwap_long_v1 starts at lifecycle PaperActive. Won't ship to live
  without explicit promotion.
- Engine code is untouched. avwap builtin handles the new spec via
  configurable params. Worst case: spec produces zero trades (gate
  too tight) or losing trades (signal absent on long-only) — neither
  affects existing strategies or live capital.
- Slot allocation: avwap_long_v1 priority 75 sits below avwap_v4_equity
  (80) and above default 70. In multi-strategy backtests with avwap_v4
  (priority 80) the new strategy yields slots when both fire. Existing
  paper strategies are unaffected.

## 10. Risks and mitigations

- Phase 0 fails -> stop. Saves ~270 LOC of dead config and a tuning
  pass. Real risk is "user expects a deliverable; gate failure means
  none." Mitigated by the explicit go/no-go upfront.
- avwap engine has long-side edge but our universe choice misses it ->
  Phase 0 quant call should surface this; per-symbol PF breakdown
  will identify which names contribute.
- Long-only is structurally weak in this regime (the data hint is
  that the cluster's edge was on the SHORT side) -> we accept this
  and live with whatever PF the data supports. If the universe just
  doesn't have a viable long-only edge, the right move is to find a
  different setup or a different universe, not to overfit.
- Slippage stress kills the edge -> 20 bps gate catches this; we don't
  promote anything that fails 20 bps.

## 11. Out of scope for this iteration

- HVN clear-path veto using VolumeHistogram primitive (ENGINE_CHANGE).
- EMA-pullback-after-VWAP-break setup as a NEW entry type
  (ENGINE_CHANGE).
- Reintroducing SHORT side via inverse ETFs (SOXS/SQQQ etc.) as a
  separate strategy.
- Moving the morning-fade-SHORT insight into avwap_v4 (options) as a
  per-symbol override or new entry mode.
- Live promotion of avwap_long_v1 — paper only after Phase 3.

## 12. Acknowledgment requested

This plan is TOML-only on the new spec, gated on Phase 0. Request
explicit go-ahead to:
- proceed with Phase 0 (run 2 backtests, quant analysis, decide
  Phase 2 / stop / reassess), OR
- adjust constraints first (e.g., reconsider universe choice,
  different time window prior to Phase 0).
