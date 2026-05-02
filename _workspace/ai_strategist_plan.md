# AI Strategist v1

Status: DRAFT (no code written)
Drafted: 2026-04-30
Branch: ai-strategiest
Replaces: existing AI/LLM debate code (ai_scalping_v1, app/debate,
adapters/llm/advisor, signal_debate_enricher, ai_anchor_resolver,
ports/ai_advisor)
Owners: backend (`go-architect`), strategy correctness (`quant-analyst`)

## TL;DR

A new live-only builtin strategy `ai_strategist_v1` that uses Claude (Anthropic
SDK) with tool use to make entry decisions on a small allowlist of symbols
every 15 minutes. Exits stay rule-based and owned by existing position_monitor
/ strategy exit infrastructure. Replaces all current AI/LLM code on the same
branch. Audited via existing `order_intents.meta` JSONB column — no schema
migration. Backtest binary cannot construct or load this strategy.

## Decision

- Live-only. `EnvMode=Backtest` rejected at three layers (build separation,
  constructor guard, spec loader).
- Entries only. Strategy emits `OrderIntent`s; existing exit machinery
  (trailing stops, EOD flatten, premium stop, position_monitor) owns the
  lifecycle. The strategy never emits an exit signal.
- 15-minute decision cadence per symbol. Bars between are no-ops.
- Tool-use loop with max 5 hops, structured JSON-schema output.
- Audit via `order_intents.meta.agent` JSONB. No new audit table.
- HITL deferred to v2. v1 ships with hard `max_intent_usd` cap; first soak day
  runs at `max_intent_usd = 0` (decide-and-log only, no broker submit).
- Symbol allowlist starts at ~5 hand-picked liquid names.

## Resolved (locked 2026-04-30)

- Model: `claude-sonnet-4-6`.
- Account visibility: request includes ALL open positions in the same IBKR
  account (not just AI-owned). Lets the model reason about account-wide
  exposure / correlation / margin pressure.
- Tools (4): `get_recent_news`, `get_recent_fills`, `get_market_context`,
  `check_risk_budget`. `get_market_context` replaces the originally proposed
  `get_correlated_signals` because it returns full canonical
  `IndicatorSnapshot`s (RSI, EMAs, VWAP, ATR, MACD, ADX, Regime, HTF) plus
  derived slopes — strictly richer signal than entry events.
- Pre-loaded peer context: every request carries the snapshot for SPY, QQQ,
  and the target's sector ETF. Tool-call lookup is reserved for ad-hoc peer
  drill-down against a configured ~30-symbol allowlist.
- Cost ceiling at the parameters above: ~$0.04-0.05 per fired Decide,
  ~$3-5/day, ~$60-100/month at 5-symbol soak. Account-wide positions and
  peer context add ~$8-10/month combined.

## Scope

In:
- Replace AI surface: delete debate/advisor/ai_anchor stack
- New `StrategistPort` + Anthropic adapter with tool use + prompt caching
- New `ai_strategist_v1` builtin strategy
- Wire only into `cmd/omo-core` (NOT `cmd/omo-backtest`)
- Spec loader rejects on backtest binary
- Soak config with $0 cap

Out:
- HITL approval workflow (v2)
- Agent-decided exits
- Multi-model debate / ensemble
- Crypto symbols (US equity only for v1)
- Backtest support (explicit non-goal)
- Backwards-compat with old debate API (being deleted)

DO NOT touch:
- `cmd/omo-backtest` must not import strategist adapter
- Existing exit/SL/position_monitor infrastructure
- `adapters/llm/json_cleanup.go` (utility, reused)

## PR breakdown

### PR 1 — Port + adapter

Files added:
- `backend/internal/ports/strategist.go`
- `backend/internal/adapters/llm/strategist.go`
- `backend/internal/adapters/llm/strategist_test.go`
- `backend/internal/adapters/llm/noop_strategist.go`

`StrategistPort`:
```
type StrategistPort interface {
    Decide(ctx context.Context, req StrategistRequest) (*StrategistDecision, error)
}
```

`StrategistRequest` fields:
- Symbol, Snapshot (`domain.IndicatorSnapshot`), Regime (`domain.MarketRegime`)
- Slopes: derived `{ema9_slope_1h, ema21_slope_1h, vwap_slope_1h,
  macd_hist_slope_1h}` for the target symbol (cheaper than shipping bar
  history; no risk of the model fumbling the math)
- OpenPositionForSymbol (nil if flat)
- AccountOpenPositions: ALL open positions in the IBKR account across every
  strategy — symbol, side, size, unrealized P&L, entry strategy. Lets the
  model see correlation/exposure ("we're already long 3 semis, skip NVDA")
- RecentFills (last N for this symbol from journal)
- AccountState (cash available, net liquidation, max risk per trade USD,
  daily P&L, daily-loss-limit headroom)
- PeerContext: pre-loaded `{snapshot, regime, slopes}` for SPY, QQQ, and the
  target's sector ETF. Pays ~1800 input tokens per call but avoids a tool
  round-trip for the most-common context need.
- SessionTime (CDT, market session label)

`StrategistDecision` fields:
- Action: `enter | hold` (no exit; explicit)
- Side: `long | short`
- SizeHintUSD, StopLoss, Target, Confidence (0-1)
- Reasoning (capped length), Factors []string
- ModelID, TokensIn, TokensOut, LatencyMs, RawJSON

Adapter:
- Anthropic SDK, prompt caching on the system prompt (per `claude-api` skill)
- Tool-use loop, max 5 hops, hard timeout
- Strict JSON-schema validation on the final decision; on schema fail returns
  hold with confidence 0 and reason="schema_violation"
- Tools exposed mid-decision (all backed by ports; no leaking of strategy
  internals like BMState/AVWAPState):
  - `get_recent_news(symbol, hours)` — wraps existing `ports.NewsPort`
  - `get_recent_fills(symbol, n)` — queries `OrderIntentJournal`
  - `get_market_context(symbols []string)` — returns `{snapshot, regime,
    slopes}` per symbol. Bounded by a configured ~30-symbol peer allowlist
    and a hard cap of 5 symbols per call. Reuses existing snapshot
    infrastructure; no new computation.
  - `check_risk_budget()` — wraps existing risk_sizer state. Kept as a tool
    rather than front-loading because the model often won't need it once
    `AccountState` is in the request

Adapter tests use a fake transport. No network in unit tests.

PR ~600 LOC. No wiring. Lands independently.

### PR 2 — Builtin strategy

Files added:
- `backend/internal/app/strategy/builtin/ai_strategist_v1.go`
- `backend/internal/app/strategy/builtin/ai_strategist_v1_test.go`

Init guards (all three required):
1. `EnvMode == Backtest` → `ErrBacktestNotSupported`
2. `StrategistPort == nil` → `ErrStrategistPortRequired`
3. Symbol not in `spec.Allowlist` → `ErrSymbolNotAllowed`

Decide cadence:
- Per-symbol state holds `lastDecideAt`
- Skip if `bar.Time - lastDecideAt < 15min`
- Skip if open position exists for symbol (entries only)
- Otherwise: assemble request, call `port.Decide`, validate

Post-LLM validation:
- `Confidence >= spec.MinConfidence` (default 0.65)
- `SizeHintUSD <= spec.MaxIntentUSD` (hard cap)
- `|StopLossDistance| <= spec.MaxStopDistanceBps` (sanity)
- Target/Stop directional sanity (target > entry > stop for long, inverted
  for short)

On valid Enter:
- Build `OrderIntent` through existing `risk_sizer` (applies position-size cap)
- Marshal `AgentDecisionMeta` into `intent.Meta["agent"]`:
  ```
  {
    "request_id", "model", "tokens_in", "tokens_out", "latency_ms",
    "trace_id", "confidence", "factors", "reasoning", "decision_raw_json"
  }
  ```
- Emit through normal pipeline (entry_gated_writer → journal → broker)

Tests:
- backtest rejection
- nil-port rejection
- symbol-allowlist rejection
- 15min throttle
- skip-if-position-open
- confidence floor
- size cap
- directional sanity (target/stop inversion)
- meta marshalling shape (golden JSON)

PR ~500 LOC.

### PR 3 — Wiring + delete old AI code

Files modified:
- `backend/cmd/omo-core/main.go` — construct strategist adapter, register
  builtin
- `backend/internal/app/strategy/spec_loader.go` — error if spec references
  `ai_strategist_v1` and registry has no port
- `configs/strategies/README.md` — remove `ai_mode` row, document the new
  strategy

Files deleted (~2200 LOC):
- `backend/internal/app/debate/` (entire package)
- `backend/internal/adapters/llm/advisor.go` + `advisor_test.go`
- `backend/internal/adapters/llm/options_advisor_test.go`
- `backend/internal/adapters/llm/risk_assessor.go`
- `backend/internal/adapters/llm/signal_context_test.go`
- `backend/internal/adapters/llm/smoke_test.go`
- `backend/internal/adapters/llm/noop_advisor.go` (replaced by noop_strategist)
- `backend/internal/app/strategy/ai_anchor_resolver.go` + test
- `backend/internal/app/strategy/signal_debate_enricher.go` + test
- `backend/internal/app/strategy/builtin/ai_scalping_v1.go` + test
- `backend/internal/ports/ai_advisor.go`

Files kept:
- `backend/internal/adapters/llm/json_cleanup.go`

Pre-delete checklist (run before opening PR 3):
- `grep -rn "AIAdvisorPort\|RequestDebate\|SelectAnchors\|ai_scalping_v1\|ai_mode" backend/ configs/`
  must come back empty after PR 1+2 land and the soak config is written.
- Any deployed config referencing `ai_mode` or `ai_scalping_v1` must be
  scrubbed in PR 4 (or earlier) first.

PR diff: ~50 LOC added, ~2200 LOC deleted.

### PR 4 — Soak config

Files added:
- `configs/strategies/ai_strategist_v1.toml`

Spec body (suggested):
```
symbols: ["AAPL", "NVDA", "TSLA", "SPY", "QQQ"]
cadence_minutes: 15
min_confidence: 0.65
max_intent_usd: 0          # DECIDE-AND-LOG SOAK
max_stop_distance_bps: 250
model: "claude-sonnet-4-6"
options.enabled: false      # equity-only for soak
```

Soak protocol:
- Day 1: `max_intent_usd = 0`. Inspect every decision in
  `order_intents.meta.agent`. Verify tool-call traces look sane.
- Day 2-5: `max_intent_usd = 500`. Watch fills.
- Day 6+: `max_intent_usd = 1000`. Reassess.

PR <100 LOC.

## Tests

Unit:
- Adapter happy/error paths (PR 1)
- Strategy guards / throttle / validation / meta shape (PR 2)
- Spec-loader rejection on missing port (PR 3)

Integration:
- Live wiring smoke test in `cmd/omo-core` (PR 3)
- Pipeline test: synthetic Enter decision produces `OrderIntent` with the
  expected `meta.agent` shape

No backtest tests by design — the strategy is not constructible in the
backtest binary.

## Verification (post-deploy, soak day 1)

```
-- volume sanity
SELECT count(*) FROM order_intents
WHERE meta ? 'agent' AND created_at > now() - interval '1 day';
-- expect: 5 symbols x 26 windows x decision-rate, single-digit-to-low-double-digit

-- shape check
SELECT meta->'agent'->>'model', meta->'agent'->>'confidence', symbol
FROM order_intents WHERE meta ? 'agent'
ORDER BY created_at DESC LIMIT 50;
-- expect: model=claude-sonnet-4-6, confidence>=0.65, symbol in allowlist

-- soak guard: nothing actually submitted
SELECT count(*) FROM order_intents
WHERE meta ? 'agent' AND status NOT IN ('rejected','pending_submit');
-- expect: 0 while max_intent_usd=0
```

Loki: confirm tool-use loop never exceeds 5 hops.
Anthropic dashboard cost ceiling (Sonnet 4.6, 5-symbol soak):
- Per-call (3 tool hops typical): ~10K fresh input + ~600 output =
  ~$0.04-0.05
- Daily (~65 fired Decides): ~$3-5
- Monthly (22 trading days): ~$60-100
- Hard ceiling if every window fires with 5 hops: ~$10/day, ~$220/month.
  Pages someone if observed cost exceeds this.

## Rollback

- Per-strategy: remove `configs/strategies/ai_strategist_v1.toml` from
  deployed set; restart omo-core. Strategy never registers.
- Full revert: `git revert` PR 3 restores deleted code. PR 1+PR 2 are dormant
  without wiring.
- Kill switch (optional, in PR 2): `feature_flag` row checked at top of
  `Decide()` — single UPDATE disables without restart.

## Blast radius

- Net new code: ~1100 LOC across ~6 new files, ~3 modified files
- Deleted code: ~2200 LOC across the old debate/advisor stack
- Net diff: -1100 LOC
- Hot-path impact: zero in backtest (compile-time excluded), bounded in live
  (one LLM call per 15 min per allowed symbol; fully decoupled from per-bar
  pipeline)
- Config-shape change: removes `ai_mode` knob; any deployed config still
  referencing it must be scrubbed before PR 3 lands

## Out of scope / non-goals

- Backtest support for the AI strategy (explicit non-goal)
- HITL approval workflow (v2)
- Agent-decided exits (existing exit infra owns lifecycle)
- Multi-model ensemble / self-debate
- DNA / auto-tuning over prompt content
- Crypto symbols
- Options legs in v1 soak (`options.enabled = false`); equity-only

## Open questions to resolve before PR 1

1. Prompt template: hard-coded in adapter, or externalized to
   `prompts/ai_strategist_v1.md`? Externalizing lets you tune without
   recompiling.
2. Trace correlation: reuse existing observability `trace_id`, or mint a
   new `agent_request_id` for cross-call correlation within a single Decide?
   (Suggest both.)
3. Symbol seed list: AAPL/NVDA/TSLA/SPY/QQQ default — confirm or substitute.
4. Peer-context allowlist for `get_market_context`: who picks the ~30
   symbols, and is it per-target or global? Suggest global for v1
   (sector ETFs + top liquidity names) to keep config flat.
5. Cache TTL: stick with default 5min, or use extended (1h) caching on the
   system prompt? At 15min cadence the default cache is cold every call;
   1h would amortize across ~4 calls per symbol per hour. Marginal cost
   delta; pick based on whichever is simpler in the SDK at build time.

## Suggested commit shape

- PR 1: `feat(strategist): add StrategistPort + Anthropic adapter with tool use`
- PR 2: `feat(ai_strategist_v1): live-only LLM entry strategy on 15min cadence`
- PR 3: `refactor(ai): wire ai_strategist_v1, delete legacy debate/advisor`
- PR 4: `feat(configs): add ai_strategist_v1 soak config (decide-only)`
