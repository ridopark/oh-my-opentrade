# Option market-impact Tier 1 — backtest size-vs-volume modeling

Add a per-fill market-impact term to the SimBroker option fill path so the
backtest stops treating a 1-contract order and a 50-contract order on the
same strike as size-equivalent. Today's fill model applies a tiered
half-spread (`backend/internal/adapters/simbroker/broker.go:1054
computeOptionEntryPrice`, `optionSpreadPct` line 1111) but is
size-independent. The realism deflator
(`backend/internal/app/realism/realism.go`) applies a post-hoc PF/Sharpe
haircut (`PFDecayFactor=1.5`) for unmodeled execution costs — useful as an
aggregate sanity-check, useless as a per-trade signal because the strategy
can't *react* to it during backtesting.

This plan adds a per-trade mechanism so strategies see their own size
effect at the bar level (and can choose to skip illiquid strikes or
downsize). Calibrated for the active strategy stack: AVWAP_v4 (1.6 PF,
3755 trades/yr) and MACD_only_v1 (1.13 PF, 981 trades/yr), buying options
across the 34-symbol equity universe.

Both agents (quant-analyst, go-architect) verdict: SHIP-WITH-MODS.

## Goal

Establish two invariants:

1. **Behavioral**: when both new knobs are zero (the default), the option
   fill path is byte-identical to today. Existing AVWAP_v4 / MACD
   PF-gate runs reproduce 1.5989/3755 and 1.1265/981 unchanged. Hashing
   the `/backtest/run` results JSON pre/post must match exactly.
2. **Modeling**: when knobs are non-zero, a fill of `qty` contracts
   against a bar with `bar_volume` contracts pays `impact_bps =
   scale_bps * sqrt(qty * 100 / max(bar_volume, vol_floor))` in adverse
   slippage on top of the existing tiered half-spread, with a hard
   rejection at `qty * 100 / bar_volume > MaxParticipationPct`.

The proof: five unit tests pinning baseline parity, cap rejection,
sqrt-impact at 1%/5%/10% participation, direction symmetry on sells, and
exit-path parity. Plus a PF-gate byte-equality run on
`--from 2025-04-22 --to 2026-04-25` with knobs unset.

## Functional form (model)

Square-root, not linear. Linear under-penalizes exactly where live
diverges most (HIMS / RIVN / RBLX / AFRM / BA short-DTE OTM, where bar
volume often dips to 50–200 contracts and a 4-lot is suddenly 2–8% of
bar volume). Empirics from Muravyev & Pearson (2020) and Christoffersen
et al. show option effective spreads scale closer to `sqrt(size/depth)`
than linear once participation crosses ~1%. For mega-cap liquid names
(SPY/AAPL/MSFT) at retail size, both forms agree numerically; sqrt
costs nothing there and degrades gracefully on thin names.

    impact_bps = scale_bps * sqrt(qty * 100 / max(bar_volume, vol_floor))
    exit_impact_bps = impact_bps * exit_urgency_multiplier
    fill_price = base_price + (impact_bps / 10000) * base_price * direction

`base_price` is the post-half-spread price returned by today's
`computeOptionEntryPrice` / `computeOptionExitPrice`. `direction` is +1
on long entries / short exits (we pay up), −1 on short entries / long
exits (we get hit). The spread is *not* removed; impact stacks on top.

## Asymmetric exit treatment

Forced exits cross the spread because the fill must happen — stop-loss,
EOD-flatten, signal-driven exits with no patience budget. `1.5x`
multiplier on exit impact is conservative (Madhavan reports
forced-liquidation costs 1.4–2.2x patient costs in equities; options
should be at least as bad). Hardcoded constant for ship-1; expose as a
knob in Tier 1.5 if the empirics demand it.

For ship-1 we mark *every* exit as urgent (apply 1.5x). Differentiating
take-profit (patient) vs stop-loss (urgent) requires the broker to know
intent type, which it doesn't today. Treat all exits as urgent — this
is conservative, and the strategy stack does not currently use limit
exits with patient cancel logic.

## Hard volume floor

`vol_floor = 20` contracts, hardcoded. Without this, a bar with 1
contract traded would return participation = qty * 100 / 1 = qty * 100%
→ infinite impact → the helper rejects every trade on a strike with one
sparse minute, including ones live would have filled fine on a resting
limit order. Floor it at 20 to clip the long tail.

If `bar_volume == 0` (no trades that minute) or the port returns an
error, the helper no-ops: returns the unmodified base_price, accepted =
true. Worst-case-constant behavior would secretly punish every backtest
the moment Alpaca options coverage is patchy. Document explicitly in
the helper's doc comment; emit a debug log on the broker logger, NOT a
warning, so backtest output stays clean.

## Files touched

- `backend/internal/ports/option_bar_volume.go` — NEW. Define
  `OptionBarVolumePort` interface:

      type OptionBarVolumePort interface {
          BarVolume(ctx context.Context, occ domain.Symbol, ts time.Time,
                    tf domain.Timeframe) (int64, error)
      }

  Narrow port. Do NOT extend `OptionMarketDataPort` (the live
  Quote/Greeks port speaks underlying+strike+expiry+right and answers
  "now," not OCC at historical timestamp — different shape).

- `backend/internal/adapters/simbroker/option_bar_volume.go` — NEW.
  Adapter that wraps an Alpaca historical-bars fetcher with a small
  per-OCC LRU. On first lookup for an OCC, fetches the bar series
  spanning the backtest's `[from, to]` window, caches it, and answers
  subsequent `BarVolume` calls with a binary-search by `ts`. Bounded ctx
  deadline (50–100ms) on the underlying fetch so a misconfigured Alpaca
  client doesn't turn every option fill into a slow path. Mirrors the
  `omo-replay/main.go:373-382` cache pattern.

- `backend/internal/adapters/simbroker/broker.go`:
  - Add field `optionBarVolume ports.OptionBarVolumePort` (nil when
    knobs are zero — never constructed).
  - Add fields `optionMaxParticipationPct float64` and
    `optionImpactScaleBps float64` parallel to `optionExitSpreadMult`.
  - Add `var ErrParticipationCap = errors.New("simbroker: option
    participation cap exceeded")` as a typed error.
  - Add private helper `applyParticipationImpact(intent
    domain.OrderIntent, basePrice float64, isShortEntry, isExit bool)
    (price float64, err error)`. Plug in at the *end* of
    `computeOptionEntryPrice` (after both the live-quote branch and the
    tiered-half-spread branch) AND at the matching point in
    `computeOptionExitPrice` (line ~854). Share, do not duplicate.
  - Helper short-circuits to `(basePrice, nil)` when both knobs are
    zero OR when `optionBarVolume == nil` — provably unreachable on
    default-OFF runs.

- `backend/internal/adapters/simbroker/broker.go` `Config` struct (line
  ~30) — add `OptionMaxParticipationPct float64` and
  `OptionImpactScaleBps float64`. Plain `float64`, NOT `*float64`. Zero
  values fit the OFF semantics: zero impact term × any participation =
  zero.

- `backend/internal/adapters/http/backtest_handler.go:23
  backtestRunRequest` — add the same two `float64` fields. Default-zero
  on missing JSON keys = default-OFF. Forward to
  `BacktestInfraOptions` and on into `simbroker.Config`.

- `backend/internal/app/bootstrap/backtest.go` `BacktestInfraOptions`
  (or wherever the request → infra config wiring lives) — pass through
  the two floats. Construct the `OptionBarVolumePort` adapter only when
  both knobs are non-zero; otherwise pass nil.

- `backend/internal/adapters/http/backtest_handler.go:343` — extend the
  "realism knobs resolved" info log to include
  `option_max_participation_pct` and `option_impact_scale_bps`. Audit
  trail every dashboard-driven run.

- `backend/internal/adapters/simbroker/option_entry_spread_test.go` —
  extend with the five new tests (see Test plan).

- `backend/internal/adapters/simbroker/option_exit_test.go` — extend
  with exit-path parity tests for impact (sym subset of entry tests).

## Method signatures

    // OptionBarVolumePort — historical per-contract bar volume lookup.
    type OptionBarVolumePort interface {
        BarVolume(ctx context.Context, occ domain.Symbol,
                  ts time.Time, tf domain.Timeframe) (int64, error)
    }

    // ErrParticipationCap is returned by SubmitOrder when the order's
    // contract count would exceed OptionMaxParticipationPct of the
    // contemporaneous bar volume. Distinct from price-related rejections
    // so the runner can record it as a "size rejected" event.
    var ErrParticipationCap = errors.New("simbroker: option participation cap exceeded")

    // applyParticipationImpact returns the post-impact fill price plus
    // an error if the participation cap was breached. When both knobs are
    // zero, returns (basePrice, nil) without consulting the port.
    func (b *Broker) applyParticipationImpact(intent domain.OrderIntent,
        basePrice float64, isShortEntry, isExit bool) (float64, error)

The `isShortEntry` flag controls direction (already a parameter on the
existing entry-spread code). The `isExit` flag selects the
`exit_urgency_multiplier` path.

## Order of operations (entry path)

1. `SubmitOrder` (broker.go:262) computes `basePrice =
   computeOptionEntryPrice(intent, isShortEntry)` — which already
   applies the tiered half-spread.
2. `basePrice, err = b.applyParticipationImpact(intent, basePrice,
   isShortEntry, false)`. If `err == ErrParticipationCap`, return the
   error from `SubmitOrder` so the runner skips this intent.
3. Final: existing `slippageBPS` multiplier at broker.go:319 stacks on
   top.

Net order: spread → impact → flat slippage. Each layer is independent
and tunable.

## Order of operations (exit path)

Same as entry, applied inside `computeOptionExitPrice` (line ~854),
with `isExit=true`. The exit path goes through `executeStopLoss`,
`executeEODFlatten`, `executeProgrammaticExit` — all forced. Treating
every exit as urgent (1.5x) is the ship-1 default.

## Test plan

In `option_entry_spread_test.go`, add five table-driven subtests using
a `mockOptionBarVolume` struct mirroring the existing
`mockOptionLiveDataBA` pattern:

- **TestOptionImpact_KnobsZeroIsByteIdentical**: both knobs at 0,
  `mockOptionBarVolume` returns 100 (would otherwise trigger). Run
  `SubmitOrder` across the cheap/standard/rich tier prices. Assert the
  fill is byte-identical to today's tiered-spread output. Pin: the
  default-OFF behavior is unobservably equal to baseline.

- **TestOptionImpact_CapRejects**: `MaxParticipation=2.0%`, qty=10,
  mockVol=100 → participation=10% > 2%. Assert `SubmitOrder` returns
  `ErrParticipationCap`, no order is recorded in `b.orders`. Pin: cap
  produces a typed error and clean state.

- **TestOptionImpact_SqrtAtKnownParticipation**: `scale_bps=50, qty=1`,
  mockVol={10000, 2000, 1000} producing 1%/5%/10% participation. Assert
  fill carries `+ 50 * sqrt(0.01) = 5 bps`, `+ 50 * sqrt(0.05) = 11.18
  bps`, `+ 50 * sqrt(0.10) = 15.81 bps` adverse offset over the
  post-half-spread base. Pin: sqrt math.

- **TestOptionImpact_DirectionSymmetry**: same inputs as the sqrt
  test but on a SELL-to-open. Assert impact is *subtracted* from the
  bid, not added. Pin: direction polarity.

- **TestOptionImpact_VolumeFloor**: mockVol=5 (below floor of 20),
  qty=2. Assert participation is computed against the floor (qty * 100
  / 20 = 10%), not raw vol (qty * 100 / 5 = 40%). Pin: floor clips the
  near-zero-volume tail.

In `option_exit_test.go`:

- **TestOptionImpact_ExitPathParity**: same matrix as
  TestOptionImpact_SqrtAtKnownParticipation but driving
  `computeOptionExitPrice`. Assert exit impact equals entry impact ×
  1.5 (exit_urgency_multiplier). Pin: exit path is impacted, with
  asymmetry.

Integration:

- **PF-gate byte-equality**: rerun AVWAP_v4 and MACD_only_v1 via
  `/backtest/run` on `--from 2025-04-22 --to 2026-04-25` with knobs
  omitted. Hash the result JSON; compare to a hash captured on
  `main` immediately before the merge. Must match exactly. Pin: the
  PF-gate baselines (1.5989/3755 and 1.1265/981) are byte-preserved.

## Validation gates

When the new knobs are set to the proposed defaults
(`OptionImpactScaleBps=50, OptionMaxParticipationPct=10`), expect:

- AVWAP_v4 PF drops from 1.5989 toward the realism deflator's
  `live_pf` of ~1.40, with most of the loss concentrated in
  thin-name short-DTE trades. Anything in [1.30, 1.55] is healthy.
  PF below 1.20 means `scale_bps` is too aggressive; PF essentially
  unchanged from 1.5989 means it's too soft.
- MACD_only_v1 PF moves less (it's already at 1.1265, less headroom
  to deflate). Anything in [1.00, 1.13] is healthy.
- Trade count drops by ~5–15% across both strategies as cap
  rejections eat the most-illiquid intents.

If trade-count drops by more than 30%, the cap is eating alpha rather
than long-tail risk — relax `MaxParticipationPct` and lean on
`OptionImpactScaleBps` instead.

Log every cap rejection with `(symbol, dte, moneyness,
participation_pct, strategy)` so we can spot-check whether the rejected
trades are random or whether the cap is selecting against the
strategy's high-PF tail.

## Sequencing

Single PR. ~300 LOC across simbroker + ports + http handler + tests.

1. New port + adapter (lazy + LRU + ctx deadline).
2. Helper + typed error.
3. Wire the two knobs through `backtestRunRequest` →
   `BacktestInfraOptions` → `simbroker.Config`.
4. Tests (5 unit + 1 PF-gate hash equality).
5. Build, run unit tests, run the PF-gate hash check on `main`'s
   binary then on the PR's binary. Verdict gates merge.

## Blast radius

Touched code: option fill path in SimBroker, plus a new port and
adapter. Affected callers:

- `/backtest/run` HTTP handler — gets two new request fields.
  Default-zero is provably unreachable in the broker, so existing
  callers (the strategy-tuning skill, the dashboard backtest page) are
  unaffected unless they explicitly pass non-zero values.
- `omo-replay --backtest` CLI — separate code path. Does NOT use the
  HTTP request shape; uses CLI flags. Tier 1 deliberately does not
  expose knobs there. (Adding CLI flags is a 5-line follow-up if
  needed; out of scope for ship-1.)
- Live trading — never uses SimBroker. Untouched.

When knobs are zero (default), the new port is never constructed, no
Alpaca call is made, no field on `Broker` other than the two new
constants is initialized to non-default.

## Failure modes

1. **Bar volume unavailable.** Port returns `(0, err)` or `(0, nil)`.
   Helper no-ops: returns the unmodified base_price, accepted = true.
   Documented in the helper doc comment. Debug-level log on the broker
   logger.

2. **Same-bar repeat fills.** Each `SubmitOrder` queries volume
   independently — they share the same denominator. Two 5% fills in
   the same bar each pass an 8% cap individually, but the second fill
   fundamentally lifted the price the first one moved. Tier 1 does NOT
   track per-bar consumed volume. Acceptable for ship-1 because the
   active strategy stack rarely double-fills the same OCC in one bar
   (verify with a count on a sample backtest before merging). TODO
   referencing a per-`(symbol, barTime)` consumed-volume map for Tier 1.5.

3. **Double-counting with realism deflator.** `realism.PFDecayFactor =
   1.5` is calibrated against backtest output that has NO impact
   model. Turning impact on AND leaving `realism.Compute` running
   deflates twice. Right move: keep both running, document in the
   dashboard that `LiveProfitFactor` is now belt-and-suspenders
   conservative when impact is enabled. Don't retune `PFDecayFactor`
   until 90 days of paper-fill data (per `realism.go:84` disclaimer)
   lets us calibrate impact-on backtest PF against live PF.

4. **First network call in broker hot path.** Bounded ctx deadline
   (50ms), nil-port short-circuit on default-OFF, lazy + LRU caching.
   The adapter pre-fetches the full `[from, to]` bar series for an OCC
   on first touch and answers subsequent `BarVolume` lookups from
   memory. Steady-state cost is negligible after the first lookup per
   contract.

5. **Volume floor cliff.** With floor=20, bars at 19 contracts and
   bars at 21 contracts produce the same impact for the same qty. A
   strategy could theoretically game this by sizing right at the floor
   — but the cap (default 10%) bounds this above, and the active
   strategy stack does not size dynamically per fill anyway. Not a
   concern for ship-1.

## Tier 1.5 follow-ups (not in scope)

- **Bucket-keyed defaults**: quant-analyst's lookup table varying `c`
  and `MaxPart` by `bar_volume` tier. Codify after Tier 1 produces
  empirical PF distributions across volume buckets.
- **OI blend**: `effective_depth = max(bar_volume, 0.05 * OI)` for
  sparse-volume bars where DoltHub OI is populated. Requires
  confirming OI coverage is broad enough across the universe.
- **Per-`(symbol, barTime)` consumed-volume tracking**: same-bar
  repeat fills consume budget shared across orders. Skip until a count
  shows >5% of trades are same-bar same-symbol.
- **`omo-replay --backtest` CLI flag exposure**: copy the two HTTP
  request fields onto the CLI. ~5 LOC.
- **Tier 2 (square-root with implied-vol scaling)**: `c_iv = c_base *
  sigma_proxy(iv)` so high-IV strikes pay proportionally more. Needs
  IV per fill bar from DoltHub. Punt until Tier 1's empirical PF
  haircut indicates the constant scale is too crude.
- **Calibration against paper-fill ledger**: at 90+ days of live
  paper trades, fit `scale_bps` to the observed live-vs-mid divergence
  per (volume bucket, DTE bucket, moneyness). Until then, defaults are
  stress-tester values, not calibrated values.

## Open questions to resolve before coding

1. **Lazy fetch granularity.** When the helper first hits an unseen
   OCC, does the adapter fetch (a) just the surrounding minute bar via
   a tight `[ts-5m, ts+5m]` Alpaca call, or (b) the full
   `[backtest.From, backtest.To]` series so subsequent lookups are
   cache hits? Option (b) is one big upfront call per OCC with all
   subsequent lookups O(log n) binary search. Option (a) is many small
   calls. Recommend (b) — Alpaca's bars endpoint paginates well and
   the cache eats steady-state cost.

2. **OCC normalization.** SimBroker's intent uses
   `intent.Symbol` (OCC string). The Alpaca options bar fetch uses the
   same OCC format. Confirm no mid-conversion happens between
   `domain.Symbol` and the fetcher — the existing `omo-replay` cache
   already round-trips this; reuse the same key shape.

3. **HTTP request validation.** Should the handler reject non-zero
   `OptionMaxParticipationPct` outside [0.1, 100] or
   `OptionImpactScaleBps` outside [0, 1000]? Soft reject (clamp +
   log) vs hard reject (HTTP 400). Recommend hard reject — these
   knobs change PF materially; silently clamping a typo is a
   stealth-bug surface. Consistent with the existing `slippage_bps`
   validation pattern at handler.go:160-ish.

4. **Lazy port construction trigger.** Build the
   `OptionBarVolumePort` adapter when EITHER knob is non-zero, or only
   when BOTH are non-zero? Recommend EITHER — cap-only mode (rejection
   without bps impact) is a useful diagnostic configuration.

## Reconciliation with previous parity work

The recent warmup-parity restructure (`f236d71a`, `a655d800`) closed
the *signal* parity gap between live and backtest — equity 1m monitor
calc state is now byte-equal at runtime entry. Tier 1 market impact
addresses the *execution* parity gap that the parity-diag harness
explicitly does not cover. The two are complementary:

- Parity-diag work pinned that the same SIGNAL fires in live and
  backtest at the same bar. ✓ done.
- Tier 1 will pin that the FILL price the strategy gets is realistic
  for its size, so the strategy can avoid sizing into illiquidity
  during backtest tuning. To-do.

Together: live and backtest will agree on entry triggers and roughly
agree on realized P&L per trade once paper-fill data lets us calibrate
`scale_bps`.
