# Gate Strategy-Level Underlying-Stop Exits on Option Positions

Status: DRAFT (no code written)
Drafted: 2026-04-29
Triggering incident: QQQ macd_only_v1 strategy-level swing-stop exit rejected at
position_gate (`position_gate: no_position_to_exit`) on 2026-04-29 14:05 UTC.
Same rejection on NET and HIMS yesterday at 18:55 / 19:05 UTC.
Owners: backend (`go-architect`), strategy correctness (`quant-analyst`)

## TL;DR

Strategy-side stop/target exits in `macd_only_v1`, `avwap_v4`, `break_retest_v1`,
and `overnight_z_v1` operate in **underlying** price space (swingLow/swingHigh
of underlying bars). When the strategy is options-routed, the broker holds the
**option leg** (e.g., `QQQ260505C00656000`), not the underlying. The strategy
emits the exit on the underlying symbol; `risk_sizer` has no exit-translation
branch, so the order intent reaches `position_gate` keyed on underlying and
gets rejected as "no position to exit."

This has been silently dead in production: every option-position close that
actually executed today and yesterday came from `position_monitor`'s
exit_monitor (PREMIUM_STOP / CHANDELIER / STAGNATION_EXIT / EOD_FLATTEN),
which speaks option symbols correctly. The strategy-level signal is noise.

`quant-analyst` argues the underlying stop is the wrong control for option
positions regardless of the routing fix: trigger lives in delta-space, P&L
lives in premium space (gamma + theta + vega), short-DTE theta dominates,
and the rolling WR gate already runs in premium space — a cross-space
feedback loop that converges on degenerate parameters.

This plan kills the broken signal at its source. It does NOT add the
risk_sizer translation that would make underlying-driven option exits work;
that infrastructure is intentionally not built because the trading-model
review concluded the feature is not worth wiring up.

## Decision

Gate strategy-side stop/target exit emission to skip when the open position
is an option leg. Instrument awareness is already implicit in the FillConfirmation
event (`e.Symbol` carries the OCC symbol on option fills); we detect it via the
existing `domain.IsOCCSymbol` helper and set a single bool on per-symbol state.

Per-strategy config flag preserves a legacy escape hatch but defaults off.

## Scope

Strategies in scope (confirmed by qa-inspector audit, all have
`options.enabled = true` in deployed configs and emit `start.SignalExit` with
the underlying symbol):

- `backend/internal/app/strategy/builtin/macd_v1.go` — exit sites at lines
  311, 320, 332, 341. State type: `BMState`.
- `backend/internal/app/strategy/builtin/avwap_v1.go` — exit sites at lines
  1099, 1119, 1476, 1497, 1565, 1586. State type: AVWAPState (verify name in
  file). Also has `ec.symbol` paths in the exit-checker that need the same
  gate.
- `backend/internal/app/strategy/builtin/break_retest_v1.go` — exit site at
  332. State type to verify.
- `backend/internal/app/strategy/builtin/overnight_z_v1.go` — exit sites at
  215, 233. State type to verify. **OZN trades MOC** — gating these affects
  end-of-day flat closes. Confirm exit_monitor's EOD_FLATTEN covers it before
  removing.

Out of scope (do NOT touch):

- `copytrade_v1.go` — already does the right thing via `pos.ContractSymbol`
  routing (qa-inspector flagged it as the exemplar). No changes.
- `crypto_revert_v1.go`, `crypto_tsm_v1.go` — crypto, no options branch in
  `risk_sizer`. The current behavior is correct for crypto spot/perp; gating
  would silently disable working exits.
- `ai_scalping_v1.go` — has the same shape but no deployed config. Latent
  only. Skip; revisit if/when it ships.
- Position-monitor exit_monitor (PREMIUM_STOP / CHANDELIER / STAGNATION_EXIT /
  EOD_FLATTEN). Untouched and intended to be the only exit path on options
  after this plan lands.
- `risk_sizer.go` exit-translation. Explicitly NOT building. Future strategy
  that genuinely needs underlying-driven option exits brings its own plan.
- `execution/service.go:1030` second exact-match bug (qa-inspector). Latent
  for now because no caller reaches it once the strategy-side exit is gated
  off. Note for future.

## Code changes

### 1. New shared field on per-strategy state

For each affected strategy's state struct, add:

    IsOptionPosition bool `json:"isOptionPosition,omitempty"`

Set on entry FillConfirmation when `domain.IsOCCSymbol(domain.Symbol(e.Symbol))`
returns true. Cleared on exit fill / EntryRejection (the existing zero-out
blocks; new field added to those resets).

Rationale for a bool over storing the OCC symbol: we are NOT routing the exit
through the OCC, only suppressing the underlying signal. A bool is the smallest
state increment that captures the gate condition. If a future plan revives
strategy-driven option exits, it can promote the bool to a string at that time.

### 2. Per-strategy config flag

Add to each strategy's config struct (e.g., `BMConfig` for macd):

    UnderlyingExitsOnOptions bool `json:"underlyingExitsOnOptions,omitempty"`

Default false (gate the signal off). Exposed in TOML under each strategy:

    [strategies.macd_only_v1.exits]
    underlying_exits_on_options = false  # default; explicit for clarity

Setting `true` preserves legacy behavior — useful for offline replay or
diagnostics if we ever need to compare. Default false because production
already runs without it (every signal has been rejected).

### 3. Gate at exit-evaluation entry

For macd_v1.go, the gate goes at the top of the exit block (currently line
305):

    if bmSt.PositionSide != "" {
        if bmSt.IsOptionPosition && !cfg.UnderlyingExitsOnOptions {
            // Strategy-level underlying stop/target is suppressed for option
            // positions; exit_monitor (PREMIUM_STOP/CHANDELIER/STAGNATION/EOD)
            // owns the close.
            bmSt.PrevMACDHist = ind.MACDHistogram
            return bmSt, nil, nil
        }
        // ... existing exit logic
    }

Mirror the same gate at the equivalent block in each affected strategy.
Match each strategy's existing return shape (state, signals, error) — do not
introduce a shared helper that has to know all three return signatures.

### 4. FillConfirmation handler — record class

In the existing `OnEvent → start.FillConfirmation → entry branch` (macd_v1.go:716):

    case bmSt.PendingEntry != "":
        bmSt.PositionSide = bmSt.PendingEntry
        bmSt.PendingEntry = ""
        bmSt.PendingEntryAt = time.Time{}
        bmSt.EntryFillPrice = e.Price
        bmSt.IsOptionPosition = domain.IsOCCSymbol(domain.Symbol(e.Symbol))

In the exit-fill branch and the unexpected-fill branch, add
`bmSt.IsOptionPosition = false` next to the other zero-outs so state is clean
on flat.

### 5. Telemetry

In the suppression branch, log once per gated bar at debug level (so we can
confirm the gate is firing and verify a clean signal log):

    ctx.Logger().Debug("strategy-level exit suppressed for option position",
        "symbol", symbol,
        "strategy", instanceID.String(),
        "stop_price", bmSt.StopPrice,
        "target_price", bmSt.TargetPrice,
    )

Debug level — this should be quiet in normal operation but recoverable for
diagnostics.

## Tests

One unit test per strategy. Pattern:

    TestMACDStrategy_OptionPositionExitsSuppressed:
      1. Bring state to PositionSide=Buy, IsOptionPosition=true,
         StopPrice=658.51, TargetPrice=665, EntryFillPrice=10.20
         (mimics post-entry FillConfirmation on an OCC symbol).
      2. Feed a bar where bar.Low=658.00 (would normally trigger swing stop).
      3. Assert returned signals slice is empty.
      4. Assert state's PositionSide and StopPrice are unchanged
         (gate did not corrupt state).
      5. Same with bar.High=666 (would normally trigger target).
      6. Flip cfg.UnderlyingExitsOnOptions=true and re-run from step 2;
         assert one SignalExit emitted (legacy path still works behind flag).

One additional test on the FillConfirmation classification:

    TestMACDStrategy_FillConfirmationDetectsOptionLeg:
      1. Send FillConfirmation with Symbol="QQQ260505C00656000" (valid OCC).
      2. Assert state.IsOptionPosition == true.
      3. Send FillConfirmation with Symbol="QQQ" (equity).
      4. Assert state.IsOptionPosition == false.

Replay/backtest tests already in the suite must still pass unchanged
(IsOptionPosition default false → all existing equity-only fixtures are
unaffected).

## Verification (post-deploy)

- Tomorrow morning's session: zero `position_gate: no_position_to_exit`
  rejections from the four gated strategies. SQL:

      SELECT count(*) FROM strategy_signal_events
      WHERE ts::date = CURRENT_DATE
        AND kind = 'exit'
        AND status = 'rejected'
        AND reason = 'position_gate: no_position_to_exit'
        AND strategy IN ('macd_only_v1','avwap_v4','orb_break_retest','overnight_z_v1');

  Expected: 0. Yesterday: 2 (NET, HIMS). Today: 1 (QQQ).

- Spot-check that exit_monitor still closes option positions: same SQL with
  `reason LIKE 'exit_monitor:%'` should be > 0 by EOD as it was yesterday.

- Debug log shows `strategy-level exit suppressed for option position` lines
  during option positions (sanity that the gate path is wired, not just dead).

## Rollback

Single config knob per strategy. To revert behavior temporarily:

    [strategies.macd_only_v1.exits]
    underlying_exits_on_options = true

That re-enables the broken signal (which will keep getting rejected at the
gate). Rollback is therefore "make production noisy again," not "restore
working behavior" — because the working behavior is exit_monitor and that's
unchanged.

If the gate itself is found buggy (e.g., bad classification), full revert is
the commit revert.

## Blast radius

- Code: ~5 LOC per strategy × 4 strategies + config field × 4 + 1 helper
  call per strategy. Total ~30 LOC of substantive change, plus 4-5 short
  unit tests.
- Behavior on equity-only strategy positions: unchanged
  (IsOptionPosition stays false).
- Behavior on options-routed positions: stop and target signals from the
  strategy no longer fire. Closes happen via exit_monitor as they already do
  today (verified: 100% of executed option closes yesterday came from
  exit_monitor).
- Replay/backtest paths: unchanged. Backtest fills route through the same
  FillConfirmation handler; if the backtest ever exercises option-routed
  positions, the gate will fire there too. This is intentional — replay
  parity should mirror live.
- Performance: a single bool comparison at the top of each exit block.
  Negligible.

## Out of scope / explicit non-goals

- No risk_sizer change. The translation infrastructure go-architect proposed
  is good design but builds support for a feature quant says is mispriced.
  Not building it.
- No execution/service.go:1030 fix. That bug only matters if strategy-side
  option exits are revived. Documented for future.
- No exit_monitor changes. exit_monitor is the system of record for option
  closes.
- No deletion of dead exit-emit code from the strategies. The signal path
  still runs for equity positions (which are valid for these strategies in
  some configs); the gate is conditional, not unconditional removal.

## Open questions

- Does `overnight_z_v1` actually run options in any deployed config? OZN
  was confirmed by qa-inspector to have `options.enabled=true` but its
  behavior near MOC may differ. Confirm with one log query before gating it.
  If it relies on the strategy-side exit for the EOD close (rather than
  exit_monitor's EOD_FLATTEN), gating would leak overnight risk. This is
  the only file in scope that needs explicit production-evidence verification
  before flip.

- `avwap_v4` config currently has `strategy_exits_priority` mentioned in
  copytrade tests. Confirm the new `underlying_exits_on_options` flag does
  not collide with or invert that one.

## Suggested commit shape

Two commits, both behind tests:

1. `feat(strategy): gate underlying-priced exits on option positions`
   — new field, FillConfirmation classification, gate in macd + avwap +
   break_retest + (conditionally) overnight_z. Tests for each.

2. `chore(configs): default underlying_exits_on_options=false in deployed configs`
   — explicit setting in each strategy TOML (clarity > implicit default).
