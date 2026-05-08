# tradingthetrend_v1 - Pre-Register and Live-Deploy Gate

Locked: 2026-05-08. Edits after this date require an Amendment block at the
bottom with date, change, reason, **a co-signer (second person, not the
author), AND a fresh held-out symbol seed**. Then the entire backtest re-runs
from scratch. Quiet edits to thresholds are forbidden.

This doc fixes the rules of evaluation BEFORE any backtest is run, AND the
rules of live promotion after the backtest passes. The point is to remove the
temptation to tune a knob until the curve looks good, and to remove the
temptation to ship live without the operational guardrails. If results miss
any criterion below, the strategy is shelved. Tuning after seeing results is
forbidden.

Scope: this doc is binding for both (a) the backtest evaluation and (b) the
live paper-then-real promotion path. It does NOT lock the live real-money
sizing decision; that is a separate gate after sustained paper performance.

---

## 1. Hypothesis

Discord author "TradingTheTrend" posts a morning options watchlist of the form:

    TICKER STRIKE[c|p] > TRIGGER

The hypothesis is: after filtering by a break-and-retest entry rule and
applying mechanical exits, this signal source has positive expectancy on
options after realistic spread + slippage costs, and the edge survives a
held-out symbol split.

Null: the watchlist has no edge over a random-4-ticker daily basket subjected
to identical entry/exit logic, drawn from the same liquidity and volatility
deciles as the TTT pick on that day.

If the null cannot be rejected, the author's contribution is audience effects
(self-fulfilling breakouts) or selection noise, not alpha.

---

## 2. Data

- Historical TTT messages: ~60 days scraped from the channel via the
  discord-tradingthetrend sidecar's scrape_history.py. Stored as JSONL with
  one row per parsed signal: {message_id, posted_at, ticker, strike, right,
  trigger, raw_line, scrape_time}.
- **Re-scrape mandate**: a second scrape is run 7 calendar days after the
  first. The two scrapes are diffed for deletions or edits to the original
  posts. Detected deletions are added to the dataset (with a "deleted"
  flag) and counted; deletion rate is reported. > 5% deletion rate = treat
  the dataset as edited and discount results.
- Underlying minute bars: existing omo-data Timescale store.
- Option chain: HistoricalOptionsAdapter (DoltHub primary, live fallback)
  per-minute snapshots with bid/ask/IV/Greeks at the contract level.
- ATR for retest band: 14-period on the underlying's 5-min bars (matches
  break_retest_v1 default).
- **Regime classification**: VIX percentile (vs trailing 5-year) and S&P 500
  realized-vol percentile of the backtest window MUST be reported in the
  results. If the window sits below the 30th percentile (i.e. unusually calm
  bull regime), explicitly flag the result as regime-suspect.

Data integrity sanity checks (run BEFORE producing PF):
- Per-day post count distribution (expect ~5/day on weekdays, near-zero on
  weekends).
- Per-ticker frequency cross-checked against the symbol's market-cap and ADV
  base rate. If losing-name tickers are systematically underrepresented,
  treat the dataset as edited and discount results.
- Gap detection: any weekday with zero posts gets logged and reviewed before
  inclusion.
- Minimum unique symbols across the dataset: >= 15. Below this floor, the
  universe is too narrow and results are inadmissible regardless of headline
  metrics.

---

## 3. Entry rule (locked)

Three-phase state machine, modeled on break_retest_v1.go. Author's TRIGGER
is the breakout level for calls (mirror inversion for puts).

Phase A - Watching:
- Activated on signal arrival (event published from sidecar).
- Required: the bar that first closes above TRIGGER + breakout_buffer_atr *
  ATR must satisfy:
  - body / range >= body_range_ratio (default 0.5)
  - bar range >= atr_breakout_mult * ATR (default 1.5)
  - bar volume >= vol_surge_mult * VolumeSMA (default 1.5)
  - wick / range <= max_wick_ratio (default 0.4)
  - bullish (close > open for calls; inverse for puts)
- On satisfaction: advance to Phase B.

Phase B - WaitingRetest:
- Bar.Low must touch [TRIGGER - retest_band_atr * ATR, TRIGGER + retest_band_atr * ATR]
  (default retest_band_atr = 0.15).
- Invalidation: bar.Close < TRIGGER - invalidation_atr * ATR -> Idle, signal
  dead for the day.
- Expiry: BarsSinceBreakout > retest_expiry_bars (default 20) -> Idle.
- On satisfaction: advance to Phase C.

Phase C - Confirming:
- Next bar must close above TRIGGER + breakout_buffer_atr * ATR with
  body/range >= body_range_ratio (bullish for calls).
- **Retest-quality gate is LOCKED ON** (pullback depth, retest avg volume,
  confirm body ratio, confirm directional). Borrowed from orb_v1. No knob
  to disable; disabling would be a tuning escape and is not permitted under
  this prereg.
- On satisfaction: arm pending entry. Order goes out at next bar open as a
  marketable limit on the locked OCC contract.

Hard cutoff: no entries after entry_cutoff_et = 13:30 ET regardless of
phase. Phase machine resets at session close.

Same-day window definition: a signal's validity window is from the post
timestamp to 13:30 ET on the same calendar trading day in America/New_York.
After 13:30 ET, no new entries are taken regardless of phase. Phase machine
resets at the session close (16:00 ET America/New_York). Both backtest and
live MUST use this clock-time definition; no fuzzy "still within reasonable
window" interpretations.

Trigger-drift gate (live + backtest must match): at the moment the entry
would fire, if underlying is already > trigger_drift_pct (default 0.5%)
past trigger relative to the breakout candle's close, reject.

Exit-bar timing parity: backtest evaluates exits on bar close and fills at
NEXT bar's open. Live evaluates the same way (no intra-bar fills modeled).
Any deviation is a parity bug, not a feature.

---

## 4. Exit rules (locked)

Order-of-evaluation each bar after entry:

1. Hard max stop on premium (DTE-tiered):
   - DTE 1: -25% (skip 0DTE entirely; min_dte=2 prevents).
   - DTE 2-4: -30%.
   - DTE 5-7 (weekly): -40%.
2. EOD flatten: close eod_flatten_minutes_before_close = 15 minutes before
   regular close.
3. Time-stop: if breakout has not extended +0.5 * ATR within
   **time_stop_bars = 12** (60 minutes on 5-min bars), exit at next bar
   open. Locked at 12; no per-symbol or per-DTE override.
4. Chandelier trail on UNDERLYING (NOT premium):
   - HHV(underlying high, chandelier_lookback=20) - chandelier_atr_mult * ATR.
   - Default chandelier_atr_period = 14, chandelier_atr_mult = 2.5.
   - Close the option when underlying touches the trail.

If multiple exits trip on the same bar, the hard max stop wins.

---

## 5. Risk and execution gates

These are not just live-trading gates; they apply identically in the
backtest to keep paper and live aligned. Live-only gates are in Section 5c.

- Freshness (live): drop any signal where (now - posted_at) > 60 seconds.
  In backtest, use post timestamp from JSONL; entry can fire any bar in the
  same-day window per Section 3.
- Strike availability: drop entry if at the entry minute the option contract
  has zero volume OR bid < $0.05 OR spread > max_spread_pct. max_spread_pct
  = 5% of mid (3% on S&P 500 / mega-cap underlyings, override list locked
  at backtest start).
- **Min OI: 500 contracts as of prior session close.** Snapshot is
  prior-day-close, not intra-day. Reject below.
- Halt / LULD: drop if underlying was halted in the prior 5 minutes.
  **Backtest data source for halts: LULD events from the omo-data store. If
  not available for a date, that date is excluded from the backtest entirely
  - we do NOT silently treat it as "no halts."**
- **Earnings blackout: drop signals where the underlying has earnings
  scheduled within +/- 1 trading day of the signal date.** Earnings dates
  pulled from the omo-data store. No exception, no override.
- Fill model:
  - Entry: mid + 0.5 * spread + 1 cent cushion (2 cents on names with
    spread > $0.10). **Spread basis: the spread at the CONFIRM BAR's last
    tick**, not the prior-minute snapshot. This captures adverse selection
    on options that just moved on the confirm.
  - Exit: mid - 0.5 * spread - 1 cent cushion (2 cents on names with spread
    > $0.10). **Symmetric to entry. Removing the exit cushion would inflate
    PF ~3-5% by under-modeling forced-seller adverse selection.**

Misses are counted, not silently dropped: chain-miss rate, retest-never-fired
rate, freshness-fail rate, halt-blackout rate, earnings-blackout rate. All
reported in the backtest output. **If chain-miss rate > 25%, the dataset is
too thin and results are inadmissible.**

Definition of "trade" for the >= 200 floor in Section 6: a trade is a FILLED
ENTRY. Signal-attempted-but-filtered events are NOT trades and do not count
toward the floor. **Failing the 200-trade floor is itself a SHELVE, not a
"loosen filters and re-run" trigger.**

### 5a. Capital and concurrency (binding live + reported in backtest)

These gates apply to the live deployment AND must be modeled in the backtest
output (concurrent-position counts, per-symbol notional caps).

- **Combined author-mirror budget bucket** shared across copytrade_v1 and
  tradingthetrend_v1. Cap = 1.3 * single-strategy cap. Both strategies
  consult the bucket pre-trade and reject on cap breach.
- **Per-symbol notional cap aggregates across both strategies** (one bucket
  key per underlying ticker). If copytrade is long NVDA calls and TTT
  signals NVDA, the per-symbol notional applies to the SUM, not each
  strategy independently.
- **Max concurrent open positions across the combined bucket: 4**. 5th
  signal during 4-position state is rejected. Backtest must report
  max-concurrent-day-count distribution.
- **Per-minute fire-rate cap**: max 2 entries / 5 min across the combined
  bucket. Live and backtest both enforce.
- **Sector / beta cluster cap**: max 2 simultaneous open positions in the
  same sector (GICS sector) OR within the same 1.5-beta band. Backtest
  reports cluster-violation count.
- **Position-conflict policy**: shared paper account but isolate by OCC
  symbol so the position-monitor close path does not cross-trigger between
  strategies. Verify via parity test that closing a copytrade position does
  not affect a tradingthetrend position on a different OCC symbol of the
  same underlying.
- **Account equity floor**: $30,000. Below this, no new entries from EITHER
  author-mirror strategy. Existing positions managed normally to exit;
  hard-stop and EOD still fire.
- **PDT enforcement**: rolling 5-day day-trade counter. Reject 4th day
  trade. Strategies share the counter (a copytrade day-trade and a TTT
  day-trade both decrement the same budget). Counter must be wired BEFORE
  any live deployment.

### 5b. Kill switches (live-only)

Each is auto-triggered (no human in the loop required) and disables the
strategy for the day; manual ack required to re-enable.

- Combined author-mirror DD > 15% from the bucket's high-water mark in any
  rolling 5 trading days -> disable both strategies until manual review.
- Single-strategy DD > 12% from high-water in 5 trading days -> disable
  that strategy only.
- Sidecar heartbeat missing > 45 seconds during RTH -> disable that
  strategy.
- Parse error rate > 10% over last 20 messages -> disable that strategy
  (signals upstream channel format change).
- Realized slippage > 3x modeled for 5 consecutive trades -> disable that
  strategy.
- Account equity within 10% of PDT $30k floor -> disable new entries from
  both strategies (existing exits run).
- Single-day net loss > 8% of deployed capital -> disable both strategies
  for the day.
- Tail event detected (single-trade loss > 1.5x the per-trade premium-stop
  budget, indicating a gap-through) -> disable the responsible strategy
  pending review.
- Broker/venue auth failure or > 3 rejected orders in 5 minutes -> disable
  both strategies pending manual broker check.

### 5c. Live-only gates (do NOT apply to backtest)

- 60-second sidecar message-age gate (per Section 5).
- 15-second heartbeat from sidecar to omo-core; 3 missed beats during RTH =
  auto-disable.
- Sidecar restart MUST NOT replay messages buffered before downtime; on
  restart, seed the seen_ids cache with the last 50 visible message IDs
  before resuming forward emission. (Same pattern as discord-copytrade.)
- Burner Discord account: same account as discord-copytrade per user
  decision; treat the account as expendable and assume it gets banned
  eventually. No payment method, no PII tied to it.

---

## 6. Pass criteria (locked)

ALL of the following must be met. ANY single fail = shelve.

- **Trade count** after all filters: >= 200 (where "trade" = filled entry,
  per Section 5).
- **Profit factor** (point estimate): >= 1.30 if trade count >= 300; **>= 1.40
  if trade count is between 200 and 299** (compensates for wider CI on
  smaller samples).
- **Profit factor** (day-bootstrapped 5th percentile, 5000 resamples,
  resample DAYS as units): >= 1.00.
- **Sharpe** (daily returns of strategy P&L, annualized): >= 1.0.
- **Max drawdown**: <= 25% of **deployed capital** (NOT premium paid;
  options leverage means premium DD and capital DD diverge - the binding
  measure is capital).
- **Win rate**: >= 30%. PF can be inflated by one fat tail with a 5% WR;
  this floor catches that.
- **Concentration**:
  - Single-ticker contribution to gross profit: <= 30%.
  - Top-3 trades contribution to gross profit: <= 40%.
  - **Top-10-day P&L > 80% of gross profit = reject** (this is the
    quantitative definition of "edge concentrated in < 10 days").
- **Held-out split**: 30% of SYMBOLS held out at random with **seed =
  20260508** (locked integer). Held-out PF must satisfy:
  - **held_PF >= 0.85 * in-sample PF**, AND
  - **held_PF >= 1.20** absolute.
  Both conditions must hold (logical AND, not OR).
- **Diagnostic time-holdout** (NOT a pass gate, but reported): hold out the
  last 20% of trading days (walk-forward, no shuffle). If symbol-OOS passes
  but time-OOS PF collapses below 1.0, flag as regime-fit and require an
  explicit reviewer note before live promotion.
- **Control arm**: same break-and-retest logic on a random-4-ticker daily
  basket. Control draws PER TTT-pick from the same ADV decile AND the same
  20-day realized-vol decile as the TTT pick (matched on liquidity and
  vol). TTT must beat the control arm's PF by >= 20% (TTT_PF / control_PF
  >= 1.20).

---

## 7. Reject conditions (any one = shelve)

- Any pass criterion above fails.
- PF dominated by 1-2 mega-winners (top-3 trade contribution > 40% gross
  profit; restated for emphasis).
- Edge collapses on held-out symbols.
- Edge does not beat random-basket control by the 20% margin.
- Chain-miss rate > 25% (data too thin).
- Survivorship suspicion in scrape (per Section 2 sanity checks); deletion
  rate > 5%.
- **Single-day worst loss > 8% of deployed capital** (in any backtest day).
- **Tail event in backtest**: any single-trade loss exceeding the per-trade
  premium-stop budget by > 1.5x. Indicates premium stops do not actually
  cap loss; strategy is unsafe regardless of headline metrics.
- **Max concurrent open positions exceeded 4** on any day in the backtest
  (sizing-vs-capital mismatch).
- **Sector / beta cluster cap exceeded** more than 5 times in the backtest
  window.

If shelved, do NOT tune a knob and re-run. The mechanical exit framework
can be reused for future author-following strategies; the TTT signal source
itself is declared dead.

---

## 8. What we will NOT do

- Tune any of the knobs in Section 3, 4, 5, or 5a after looking at backtest
  output. Knobs are locked at the defaults listed.
- Run multiple backtests and report the best.
- Re-scrape the channel after seeing results to "fix" the dataset.
- Lower the Section 6 thresholds because results are close.
- Drop the held-out split or control arm because they hurt the headline
  number.
- Change the held-out random seed (20260508) after seeing results.
- Disable the retest-quality gate because the backtest looks better
  without it.

If a single knob must change after locking (e.g. discovered DoltHub data
quirk), add an Amendment block with: date, change, reason, **co-signer
(second person, not the author of this doc)**, AND a NEW held-out symbol
seed. The full backtest re-runs from scratch under the amended rules. No
silent edits, no single-author amendments.

---

## 9. Confounders explicitly listed

Things that will look like edge but are not:

- Entry-time selection bias: scraping might miss late-edited posts;
  signals that didn't pan out may have been deleted. Mitigation in
  Section 2 (re-scrape diff).
- Bar-grain artifacts: minute bars hide intra-bar slippage. Realistic fill
  model in Section 5 mitigates but does not eliminate.
- DoltHub quote staleness: option chain snapshots may lag underlying ticks.
  Quantify by comparing chain timestamps to underlying bar timestamps.
- **Subscriber audience effects**: a self-fulfilling breakout that happens
  within seconds of the post is partly manufactured. The retest filter
  helps (audience fades out by retest time), but does not fully isolate.
- **Regime**: 60 days is one regime. A bull-trend dataset will inflate
  breakout results. VIX percentile reporting (Section 2) and time-holdout
  diagnostic (Section 6) are the controls.
- **Options leverage masking equity-equivalent drawdown**. A 25% premium DD
  is not the same as a 25% account DD. The binding max-DD measure
  (Section 6) is "% of deployed capital", not premium.
- **Earnings overlap**: a single big earnings move can dominate. Mitigated
  by Section 5 earnings blackout.
- **Counterparty / venue concentration**: all options route to one broker.
  If that broker has known unreliability (per memory:
  feedback_alpaca_paper_unreliable), backtest results assume execution
  that may not be available live. Document broker assumption in Section 13.

---

## 10. Sign-off

Author of pre-register: J.P. (ridopark)
Date: 2026-05-08
Strategy ID: tradingthetrend_v1
**Backtest harness commit SHA: TBD - LOCK BEFORE RUN, NOT AT EXECUTION
TIME.** The SHA must be recorded in this section before the first backtest
invocation; running with an unlocked SHA invalidates the result.
**Held-out random seed: 20260508 (locked).**
**Co-signer for any future amendment: TBD (must NOT be the author).**

Once backtest is run, append a Results block here with the metrics from
Section 6. Do not delete the original Section 6 numbers.

### 10a. Live promotion gate (paper -> larger paper -> real)

The backtest passing is necessary but not sufficient for live deployment.
The promotion path is:

Stage 1: 60 paper sessions on LIVE signals (sidecar in place, real Discord
feed, paper broker). Sizing at 25% of pre-reg-passed size.

Stage 1 pass criteria (separate from backtest criteria):
- Realized slippage within 1.5x of backtest-modeled slippage on >= 90% of
  fills.
- No kill-switch trigger (Section 5b) in the 60 sessions other than known
  data-feed gaps.
- PF on the 60-session sample within 0.20 of backtest PF (point estimate).
- Sidecar uptime during RTH >= 99% across the 60 sessions.
- Author posting cadence stable (no > 5 consecutive missed days, no abrupt
  format change).

Stage 2: 20 paper sessions at 50% of full size. Same criteria.

Stage 3: 20 paper sessions at 100% of full size. Same criteria.

ONLY after Stages 1-3 are all clean does a real-money decision get made,
and that decision is OUT OF SCOPE FOR THIS DOC. A separate doc must lock
real-money sizing, real-money kill-switch criteria, and the
go/no-go-live signoff.

### 10b. Decommission criteria (auto-shelve)

Once live, the strategy auto-shelves on any of:

- 20-day rolling PF < 1.0 for 5 consecutive trading days.
- 60-day rolling PF < 1.20 (the original pre-reg pass bar minus a margin).
- Author goes silent for > 10 consecutive trading days.
- Author posts a regime change (different product class, different
  grammar, different time-of-day).
- Single-month max DD > 20% of deployed capital.
- Realized slippage > 2x modeled for 10 consecutive trades.

Auto-shelve = strategy disabled, all positions closed at next-bar open
under EOD-flatten logic, manual ack required for any restart.

---

## 11. Model risk and validation owner

The validator MUST NOT be the author of the strategy or this doc. The
validator owns:
- Independent re-run of the backtest from the locked SHA before live
  deployment.
- Sign-off on results vs Section 6 criteria. Sign-off is a written entry
  in this doc, not a verbal ack.
- Quarterly re-validation: re-run on rolling 60-day window, report
  drift vs original backtest.

Author/validator separation is a HARD requirement; if no second person is
available, the strategy does NOT go live regardless of backtest results.

Validator: TBD (must be assigned before backtest-pass review).

---

## 12. Production monitoring plan (live)

Required before Stage 1 paper deployment begins:

- Real-time P&L dashboard for the combined author-mirror bucket and each
  strategy individually.
- Per-trade slippage attribution: trigger price, fill price, modeled
  slippage, realized slippage, delta. Logged to omo-data; queryable.
- Alerting:
  - Kill-switch trigger -> Discord/SMS/whatever the operator uses.
  - Sidecar heartbeat failure -> alert within 60s.
  - Single-trade loss > per-trade stop budget * 1.5x -> alert immediately.
  - Daily P&L crossing -8% of deployed capital -> alert immediately.
- On-call: the operator (J.P.) is on-call during RTH for the live deployment.
  Out-of-hours, EOD-flatten and kill switches are the only safety net.
- P&L attribution / reconciliation: end-of-day reconcile broker fills vs
  modeled fills; flag deltas > $5 per fill or > $25/day total.

If any of these is not in place when Stage 1 paper begins, Stage 1 timer
does NOT start. The 60-session count requires monitoring to be live.

---

## 13. Counterparty / venue / data dependencies

The strategy has external dependencies that are not under our control. They
must be documented and have a failover or sunset plan.

- **Discord**: signal source. If channel access is lost (account ban, ToS
  takedown, channel deletion), strategy is disabled until access restored.
  No alternative source.
- **Broker (IBKR paper, then real)**: order routing. IBKR is the primary;
  no failover broker is configured. If IBKR is down, strategy is disabled.
- **DoltHub option chain history**: backtest input only, not live. Failover
  to live API capture if DoltHub is unavailable for the backtest window.
- **omo-data Timescale**: underlying bars and earnings dates. Single point
  of failure for both backtest and live. No failover; if unavailable,
  strategy is disabled.
- **Alpaca paper** (per memory: feedback_alpaca_paper_unreliable):
  explicitly NOT used for this strategy's live paper or real deployment.
  IBKR paper is the validation venue.

Document the broker, data vendor, and Discord channel SLAs (or lack
thereof) before live promotion. Where SLA is "best effort," that is the
documented assumption.

---

## Amendments

(none)
