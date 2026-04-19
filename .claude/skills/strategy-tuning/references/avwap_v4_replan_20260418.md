# avwap_v4 tuning re-plan — 2026-04-18

Output of a deep hour-gate A/B session that concluded the session's own results
were not shippable. Documents findings, honest self-critique, and a phased plan
for the follow-up work. Referenced from a future tuning session to avoid
repeating the same mistakes.

## What today's session established

### Findings (real)
- The 09:35-09:50 gate's edge survives 3x slippage stress (gap 0.93 → 0.88 PF
  when slippage raised 10→30bps). Opening-drive mechanism is real, not a
  fill-model artifact.
- avwap_v4 has **bimodal alpha** by time-of-day:
  - Morning drive 09:30-09:59 ET: ~90% of no-gate P&L (~$589k on 2142 trades)
  - Late-day 15:15-15:45 ET: +$46k on 176 trades, but confounded by
    EOD_FLATTEN (5.7-min avg hold on the 15:30 bucket)
  - Middle hours: roughly neutral, `midday_trap_shield` filtering the worst
- The live comment "09:50+ was breakeven" is **factually wrong**. 09:45 bucket
  is PF 2.08 on 747 trades (+$134k). The live gate is too narrow.
- Even with no gate, PF 2.35 on 34 symbols is healthy — confluence score ≥ 8
  carries the strategy; the gate is cream, not core.

### Red flags still unresolved
- Sharpe 7+ across all variants even at 30bps slippage. Candidates:
  1. Only 6/34 symbols use DoltHub real bid/ask; 28/34 use BSM synthetic with
     software-layer spread/IV/crush friction.
  2. Universe curve-fit — the 34 roster may itself be the overfit.
  3. Compounding on $100k base inflates late-period returns.

### Methodology failures
- B-prime (09:35-09:54) was declared winner on "9/9 gates." Independent review
  (quant-analyst self-critique + risk-manager cold read) concluded:
  - Split-half was **in-sample-twice**: the 09:55 cliff was identified on the
    full year, then "validated" on halves that both saw the selection.
  - Best-of-4 menu selection inflates false-positive rate (~23% at α=0.05).
  - The original "09:50+ was breakeven" comment is the same bucket-selection
    logic as "09:55+ is catastrophic" — one was labeled overfit, the other
    almost shipped.
- P(B-prime's +0.27 Sharpe survives true OOS) ~ 25% per quant.

### Decision
No live change. Working tree reverted to HEAD. Shipped zero parameter changes
from this session.

## Phased plan for follow-up

### Phase 1 — Methodology doc (45 min, no code, no risk)

Deliverable: `.claude/skills/strategy-tuning/references/wfa_protocol.md`
codifying:
- Anchored walk-forward only (train/test split pre-registered).
- Default: 2025-04→2025-12 train, 2026-01→2026-04 locked holdout.
- Decision criteria frozen BEFORE looking at test results.
- Menu-selection correction: with N variants, require best-of-N lower bound
  > threshold + σ·√(2·ln(N)).
- "Gates passed" only meaningful on held-out, pre-registered tests.
- Bias toward no-change when in doubt.

### Phase 2 — Fill-model realism

#### 2A. Expand DoltHub coverage (~3 hrs)

Current: 6/34 symbols have real bid/ask (AMD, HIMS, NFLX, PLTR, SPY, TSLA).

Scope:
- Verify current DoltHub coverage (memory is 22 days old — check
  `adapters/dolthub/client.go` + `app/optionsimport/service.go`).
- Import remaining ~28 symbols: AAPL, AFRM, AMZN, AVGO, BA, COIN, CRM, GOOGL,
  HOOD, IWM, JPM, LLY, META, MRNA, MRVL, MSFT, MU, NET, NVDA, OXY, QQQ, RBLX,
  RIVN, SMCI, SNOW, SOFI, SOXL, XOM.
- Memory: ~90s/symbol one-time, cached after. Not all symbols may be in
  DoltHub — document which use BSM fallback.
- Tag backtest output to separate real-data trades from BSM trades so future
  reports can decompose.

Files:
- `backend/internal/adapters/dolthub/client.go` — availability query
- `backend/internal/app/optionsimport/service.go` — orchestrator
- Likely a new subcommand under `backend/cmd/omo-backfill/` or reuse pattern

Risk: low. Additive data, no production code path touched.

#### 2B. Re-validate baselines on expanded data (~1 hr)

Scope:
- Re-run A (09:35-09:50) and D (09:35-15:45) after 2A imports complete.
- Compare to today's numbers. Expected: Sharpe drops from 7+ to 2-4 range;
  absolute PF likely lower.
- If Sharpe stays > 5, fill model is not the driver. Investigate universe
  curve-fit before continuing.
- Report per-symbol PnL contribution to check for concentration in 3-4 names.

Files: no code change.

### Phase 3 — Inducement detector (~1-1.5 days, medium risk)

Spec already in memory (`project_inducement_detector.md`). Implements liquidity
sweep detection as Factor 7 in `computeConfluence()`.

Changes:
1. Domain structs: `SwingLevel`, `PendingInducement` (~30 LOC)
2. `AVWAPState` additions with ring-buffer logic (~50 LOC in avwap_v1.go)
3. Detection function: `detectInducement(bar, state, cfg)` per 4-step
   algorithm (candidate → reversal → volume → direction) (~100 LOC)
4. Confluence integration: Factor 7 at `avwap_v1.go:2775+` (~30 LOC)
5. Config struct: 8 TOML params in `AVWAPConfig` (~20 LOC)
6. TOML wiring in `avwap_v4.toml` with `inducement_enabled = false` default
7. Unit tests for detection edge cases (~200 LOC)

Total: ~450 LOC new + 200 LOC tests across 2-3 files.

Validation (from memory spec):
- Baseline with inducement disabled
- Enable scoring-only; compare per-trade WR/PF for tagged vs untagged
- Target: PF delta ≥ 0.3 for inducement-tagged subset
- Sweep `breach_min_bps ∈ {3,5,10}`, `reversal_bars ∈ {2,3,5}` only after
  baseline passes

Risk: medium. Real feature on core strategy. Bounded by disabled-by-default
safety.

## Sequencing

```
Phase 1 (methodology, 45 min, no risk)
     ↓
Phase 2A (DoltHub backfill, ~3 hrs, low risk)
     ↓
Phase 2B (re-validate baselines, ~1 hr, no risk)
     ↓
Phase 3 (inducement, ~1-1.5 days, medium risk)
     ↓
Phase 4 (revisit hour-gate with real fill model + inducement, TBD)
```

Go/no-go after Phase 2B: if Sharpe stays unrealistic after fill-model fix,
pause Phase 3 and investigate universe curve-fit.

## What NOT to do

- Do not ship any hour-gate change without a pre-registered OOS test passing.
- Do not continue parameter sweeps on the current fill model — any ranking
  from it is suspect until Phase 2 lands.
- Do not treat split-half on in-sample-selected configs as validation.
- Do not assume Sharpe 7 is a design success — it's a diagnostic signal that
  something is mismodeled.

## Backtest results from this session

Archived at `.claude/skills/strategy-tuning/_workspace/avwap_hour_ab_20260418/`:
- `variant_A_baseline.json` — 09:35-09:50 @ 10bps (current live)
- `variant_B_0935_1030.json` — 09:35-10:30 @ 10bps
- `variant_Bprime_0935_0954.json` — 09:35-09:54 @ 10bps (rejected winner)
- `variant_D_0935_1545.json` — 09:35-15:45 @ 10bps (no gate)
- `variant_A_30bps.json` / `variant_D_30bps.json` — slippage stress
- `Bprime_H1_2025Q2Q3.json` / `Bprime_H2_2025Q4_2026Q1.json` — split-half
- `pair_A_avwap_macd.json` / `pair_Bprime_avwap_macd.json` — pair validation
