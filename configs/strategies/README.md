# Strategy Configuration Reference

Each strategy is defined in a TOML file with the following sections.

---

## Top-Level

| Variable | Type | Description |
|---|---|---|
| `schema_version` | int | Config schema version. Currently `2`. |

---

## `[strategy]`

Strategy metadata — no runtime effect.

| Variable | Type | Description |
|---|---|---|
| `id` | string | Unique identifier used for routing and logging. |
| `version` | string | Semver version string. |
| `name` | string | Human-readable display name. |
| `description` | string | Brief summary of what the strategy does. |
| `author` | string | Who created the config (`"system"` for built-ins). |
| `created_at` | string | ISO 8601 creation timestamp. |

---

## `[lifecycle]`

Controls whether the strategy is active and where it can trade.

| Variable | Type | Description |
|---|---|---|
| `state` | string | Current state: `LiveActive`, `PaperActive`, `Paused`, `Stopped`. |
| `paper_only` | bool | If `true`, the strategy can only run in paper trading mode. |

---

## `[routing]`

Determines which symbols and timeframes the strategy receives data for.

| Variable | Type | Description |
|---|---|---|
| `symbols` | string[] | List of ticker symbols to trade (e.g. `"AAPL"`, `"BTC/USD"`). |
| `timeframes` | string[] | Bar intervals to subscribe to (e.g. `"1m"`, `"5m"`). |
| `priority` | int | Higher value = higher priority when strategies conflict on the same symbol. |
| `conflict_policy` | string | How to resolve conflicts: `"priority_wins"` lets the higher-priority strategy take precedence. |
| `exclusive_per_symbol` | bool | If `true`, only one instance of this strategy runs per symbol. |
| `watchlist_mode` | string | `"intersection"` means only trade symbols that appear in both the strategy list and the active watchlist. |
| `asset_classes` | string[] | Asset class filter: `"EQUITY"`, `"CRYPTO"`. |
| `allowed_directions` | string[] | Signal directions this strategy may emit: `"LONG"`, `"SHORT"`. Omit to allow all. Entry signals with disallowed directions are filtered before enrichment. Exit signals are never filtered. |

---

## `[params]`

Strategy-specific tuning parameters. Variables differ by strategy type.

### Common (all strategies)

| Variable | Type | Default | Description |
|---|---|---|---|
| `allow_regimes` | string[] | — | Market regimes the strategy is allowed to trade in (`"BALANCE"`, `"REVERSAL"`, `"TREND"`). |
| `cooldown_seconds` | int | — | Minimum seconds between trades on the same symbol. |
| `max_trades_per_day` | int | — | Hard cap on trades per symbol per session. |
| `stop_bps` | int | — | Initial stop-loss distance in basis points (25 bps = 0.25%). |
| `limit_offset_bps` | int | — | Limit order offset from signal price in basis points. |
| `risk_per_trade_bps` | int | — | Risk budget per trade as basis points of account equity. |
| `max_position_bps` | int | — | Max position size as basis points of account equity (1000 bps = 10%). |
### Market Quality Guards (opt-in)

These params add pre-execution gates that reject entries when market conditions are unfavorable. All are opt-in — omit them entirely to disable. They apply to any strategy type (equity, crypto, options).

| Variable | Type | Default | Description |
|---|---|---|---|
| `max_spread_bps` | float | *(disabled)* | Max bid-ask spread in basis points. Entries are hard-rejected when the live spread exceeds this threshold. |
| `allowed_hours_start` | string | *(disabled)* | Start of allowed trading window in `"HH:MM"` format. Requires `allowed_hours_end`. |
| `allowed_hours_end` | string | *(disabled)* | End of allowed trading window in `"HH:MM"` format. Must be later than start (intra-day only). |
| `allowed_hours_tz` | string | `"UTC"` | IANA timezone for the trading window (e.g. `"America/New_York"`). |
| `skip_weekends` | bool | `false` | If `true`, hard-reject all entries on Saturday and Sunday. |

**Why these exist:** Crypto markets trade 24/7, but liquidity is heavily concentrated during US equity hours (8 AM – 5 PM ET). Since the 2024 spot ETF launches, ~46% of BTC volume occurs during US market hours. Off-hours spreads widen 2–3x, and weekend volume has hit all-time lows. For scalping strategies, wider spreads directly eat into the profit target — if your `stop_bps` is 45 and the spread alone is 40 bps, the expected value is negative before fees.

**Spread guard vs. slippage guard:** The existing slippage guard checks whether the market price has drifted from your intended limit price. The spread guard is different — it checks whether the market microstructure itself is too thin to trade profitably, regardless of where your limit sits.

**When to use `max_spread_bps`:**
- Scalping strategies where the profit target is small (< 100 bps). Set to roughly half your `stop_bps`.
- Crypto pairs on Alpaca, where off-hours BTC/ETH spreads widen from ~10 bps to 25–45 bps and altcoins (SOL, DOGE, PEPE) can exceed 50–100 bps.
- Any strategy where Alpaca's 0.25% taker fee (25 bps per side, 50 bps round-trip) means the spread must be tight for the trade to be profitable.

**When to use the trading window:**
- Crypto scalping — restrict to 08:00–17:00 ET weekdays when Alpaca's liquidity providers are most active.
- Equity strategies that underperform during pre-market or after-hours due to thin order books.
- Combine `skip_weekends = true` with the hours window for crypto to avoid both weekend illiquidity and overnight dead zones.

**Example: crypto scalping config**

```toml
[params]
stop_bps = 45
max_spread_bps = 25
allowed_hours_start = "08:00"
allowed_hours_end = "17:00"
allowed_hours_tz = "America/New_York"
skip_weekends = true
```

This configuration rejects crypto entries when:
1. The bid-ask spread exceeds 25 bps (market too thin).
2. The time is before 8 AM or after 5 PM Eastern (off-hours liquidity cliff).
3. It's Saturday or Sunday (weekend volume at all-time lows post-ETF).

Signals generated outside these windows are simply dropped — the strategy will continue generating signals, but the execution layer silently rejects them. Exit orders are never blocked by these guards (you always want to be able to close a position).

### AVWAP strategies (`avwap`, `avwap_v2`, `crypto_avwap_v2`)

| Variable | Type | Description |
|---|---|---|
| `anchors` | string[] | VWAP anchor points. Values: `pd_high` (previous-day high), `pd_low` (previous-day low), `on_high` (overnight high), `on_low` (overnight low), `or_high` (opening-range high), `or_low` (opening-range low), `session_open`. |
| `breakout_enabled` | bool | Enable breakout signals when price crosses above an AVWAP level. |
| `hold_bars` | int | Number of bars price must hold above AVWAP to confirm a breakout. |
| `volume_mult` | float | Volume must be this multiple of average volume to validate a breakout. |
| `bounce_enabled` | bool | Enable bounce signals when price touches AVWAP support and reverses. |
| `rsi_bounce_max` | float | Maximum RSI value to qualify as an oversold bounce entry. |
| `exit_hold_bars` | int | Bars to hold before exit logic activates. |
| `direction` | string | Trade direction: `"LONG"`, `"SHORT"`, or `"BOTH"`. Only in crypto variant. |
| `asset_class` | string | Asset class hint for routing/risk: `"EQUITY"` or `"CRYPTO"`. |
| `require_higher_lows` | bool | Require a higher-lows pattern before allowing entry (v2 only). |
| `higher_lows_bars` | int | Number of bars to check for the higher-lows pattern (v2 only). |
| `midday_trap_shield` | bool | Block entries during low-volume midday chop (v2 only). |
| `midday_volume_mult` | float | Volume multiplier required to override midday shield (equity v2 only). |

### ORB strategy (`orb_break_retest`)

| Variable | Type | Description |
|---|---|---|
| `orb_window_minutes` | int | Duration of the opening range in minutes (e.g. first 30 min). |
| `min_rvol` | float | Minimum relative volume (vs. average) to consider a breakout valid. |
| `min_confidence` | float | Minimum signal confidence score (0.0–1.0) to enter. |
| `breakout_confirm_bps` | int | Price must exceed the range boundary by this many bps to confirm breakout. |
| `touch_tolerance_bps` | int | How close price must come to the range boundary to count as a "retest" (bps). |
| `hold_confirm_bps` | int | Price must hold above breakout level by this many bps after retest. |
| `max_retest_bars` | int | Maximum bars to wait for a retest after the initial breakout. |
| `allow_missing_bars` | int | Number of bars allowed to be missing (gaps) during retest window. |
| `max_signals_per_session` | int | Max breakout signals to act on per session. |

### AI Scalping strategies (`ai_scalping`, `crypto_ai_scalping`)

| Variable | Type | Description |
|---|---|---|
| `rsi_long` | float | RSI threshold for long entry (buy when RSI drops below this). |
| `rsi_short` | float | RSI threshold for short entry (sell when RSI rises above this). |
| `stoch_long` | float | Stochastic oscillator threshold for long entry. |
| `stoch_short` | float | Stochastic oscillator threshold for short entry. |
| `rsi_exit_mid` | float | RSI midpoint used as mean-reversion exit target. |
| `ai_enabled` | bool | Enable AI overlay for signal enrichment. |
| `ai_mode` | string | AI integration mode. `"async_debate_adjust"` runs bull/bear debate asynchronously. |
| `ai_timeout_seconds` | int | Max seconds to wait for AI response before proceeding without it. |
| `ai_min_confidence` | float | Minimum AI confidence to accept its adjustment. |
| `ai_veto_on_strong_opposite` | bool | If `true`, AI can veto a trade when it has strong conviction in the opposite direction. |
| `size_mult_min` | float | Minimum position size multiplier (AI can scale down to this). |
| `size_mult_base` | float | Baseline position size multiplier (1.0 = no adjustment). |
| `size_mult_max` | float | Maximum position size multiplier (AI can scale up to this). |

---

## `[regime_filter]`

Optional market-regime gate applied before any signal is evaluated.

| Variable | Type | Description |
|---|---|---|
| `enabled` | bool | Whether the regime filter is active. |
| `allowed_regimes` | string[] | Only trade when the detected regime is in this list. |
| `min_atr_pct` | float | Minimum ATR as a percentage of price to consider the market "active enough". |

---

## `[hooks]`

Connects the strategy config to its signal-generation engine.

| Variable | Type | Description |
|---|---|---|
| `signals.engine` | string | Signal engine type: `"builtin"` or `"yaegi"` (user-defined Go plugins). |
| `signals.name` | string | Name of the signal generator to invoke (e.g. `"orb_v1"`, `"avwap_v1"`, `"ai_scalping_v1"`). |

---

## `[options]`

Options contract selection rules (ORB strategy only, currently).

| Variable | Type | Description |
|---|---|---|
| `enabled` | bool | Whether to route trades through options instead of equity. |

### `[options.defaults]`

| Variable | Type | Description |
|---|---|---|
| `min_dte` | int | Minimum days to expiration. |
| `max_dte` | int | Maximum days to expiration. |
| `target_delta_low` | float | Lower bound of target delta range. |
| `target_delta_high` | float | Upper bound of target delta range. |
| `min_open_interest` | int | Minimum open interest for liquidity. |
| `max_spread_pct` | float | Maximum bid-ask spread as a fraction of mid price. |
| `max_iv` | float | Maximum implied volatility (1.0 = 100%). |

### `[options.regime_overrides.<REGIME>]`

Override option defaults when a specific regime is detected. Same fields as `[options.defaults]`.

---

## `[dynamic_risk]`

Scales position size and stop distance based on signal confidence.

| Variable | Type | Description |
|---|---|---|
| `enabled` | bool | Whether dynamic risk scaling is active. |
| `min_confidence` | float | Signals below this confidence get minimum risk allocation. |
| `risk_scale_min` | float | Minimum risk scale factor (applied at lowest confidence). |
| `risk_scale_max` | float | Maximum risk scale factor (applied at highest confidence). |
| `stop_tight_mult` | float | Multiplier to tighten stop when confidence is high. |
| `stop_wide_mult` | float | Multiplier to widen stop when confidence is low. |
| `size_tight_mult` | float | Multiplier to reduce size when confidence is low. |
| `size_wide_mult` | float | Multiplier to increase size when confidence is high. |

---

## `[risk_revaluation]`

Periodically re-evaluates open position risk.

| Variable | Type | Description |
|---|---|---|
| `enabled` | bool | Whether periodic risk revaluation is active. |
| `interval_minutes` | int | How often (in minutes) to re-evaluate position risk. |

---

## `[[exit_rules]]`

Array of exit rule definitions. Each entry has a `type` and a `[exit_rules.params]` block.

| Exit Type | Params | Description |
|---|---|---|
| `TRAILING_STOP` | `pct` — trail distance as decimal (0.02 = 2%) | Trailing stop that follows price up and exits on pullback. |
| `PROFIT_TARGET` | `pct` — target as decimal (0.03 = 3%) | Exit when unrealized profit reaches the target percentage. |
| `EOD_FLATTEN` | `minutes_before_close` — minutes before market close | Flatten all positions before end of day. |
| `MAX_LOSS` | `pct` — max loss as decimal (0.025 = 2.5%) | Hard stop: exit if position loss exceeds this percentage. |
| `VOLATILITY_STOP` | `atr_multiplier` — multiple of ATR for stop distance | ATR-based dynamic stop that adapts to current volatility. |
| `SD_TARGET` | `sd_level` — standard deviations from entry | Take profit at N standard deviations from entry price. |
| `STEP_STOP` | *(none)* | Ratcheting stop that steps up as price makes new highs. |
| `STAGNATION_EXIT` | `minutes` — max stagnation time; `sd_threshold` — min price movement in SDs; `profit_gate_pct` — min profit to skip exit | Exit if price stagnates (moves less than `sd_threshold` SDs in `minutes`), unless profit exceeds the gate. |
| `MAX_HOLDING_TIME` | `minutes` — max holding duration | Force exit after holding a position for this many minutes. |
