# Persona Panel Debate Mode

> Created: 2026-04-16
> Status: planned (A/B experiment)
> Partner: `backend/internal/app/debate/service.go`, `domain/signal_enrichment.go`
> Predecessor: Bull/Bear/Judge (stays as default; persona panel is opt-in)

## Motivation

The current Bull/Bear/Judge debate forces polarity: one agent argues FOR, one
AGAINST. This produces weak devil's-advocate arguments on obvious setups
because the Bear is performing skepticism, not reasoning from a genuinely
different analytical framework. A persona panel replaces forced polarity with
framework diversity -- each persona brings a different lens on the same data,
and their disagreements are real, not staged.

## Design (quant-reviewed)

### 3 personas, not 4

The quant review killed the Risk Manager persona. Rationale: the 8 deterministic
execution gates already handle risk (portfolio heat, sector exposure, directional
bias, etc.) and are faster, auditable, and reproducible. An LLM risk manager
would contradict the gates unpredictably and add latency for a function that
must be hard-gated, not soft-scored.

| Persona | Lens | Data sources (exclusive) |
|---|---|---|
| **Tape Reader** | Order flow, volume, absorption, sweep detection | Dark-pool block ratio, TFI, inducement flags, RVOL |
| **Quant** | Statistical edge, regime probability, mean-reversion math | devZ, regime type+strength, confluence score, historical PF/WR |
| **Contrarian** | Crowd positioning, sentiment extremes, fading consensus | 13F whale accumulation, funding rates, put/call skew, news sentiment |

Each persona sees ONLY its data slice -- no overlap. This enforces lens
separation so disagreements come from framework differences, not data
interpretation differences.

The **Contrarian** persona has the highest marginal value because the
deterministic system currently has zero crowd-positioning input. 13F, funding
rates, and put/call skew are qualitative signals that an LLM can synthesize
better than a threshold gate.

### Synthesizer

A 4th LLM call receives the 3 persona verdicts and produces the final
`SignalEnrichment`. Guardrails enforced in Go (not relying on LLM compliance):

- **Confidence aggregation**: minimum-weighted geometric mean.
  `conf = max(min_score, geomean) if min_score >= 0.3 else min_score`.
  This preserves veto power (any persona below 0.3 kills the trade) while
  allowing high-conviction consensus.
- **Groupthink guard**: if all 3 directions agree AND all scores > 0.8,
  apply a configurable haircut (default 0.9x) for mean-reversion strategies
  only (crowded MR setups lose edge). Trend/momentum strategies are not
  penalized for consensus. Per-strategy override via TOML:
  `groupthink_haircut = 0.9`.
- **Veto**: if any persona scores below 0.2, the synthesizer must output
  status=vetoed regardless of the LLM's text. NOTE: the 0.2 threshold is
  provisional -- log all sub-0.4 scores during the A/B without hard-vetoing,
  then calibrate post-hoc against actual trade outcomes before promoting.

### Output contract

Unchanged. The panel returns `domain.SignalEnrichment` with the same fields:
direction, confidence, risk_modifier, rationale, status. No downstream changes.
The `AdvisoryDecision` struct gains a `PersonaVerdicts []PersonaVerdict` field
for logging/telemetry, but it does not flow into the enrichment output.

### Latency budget

3 personas run in parallel via `errgroup`. Synthesizer runs sequentially after.
Budget: personas ~1.5s (parallel), synthesizer ~1s = ~2.5s total. Current
Bull/Bear/Judge is ~3-4s (sequential). Net: **persona panel is faster** despite
more LLM calls.

For 5m bars with 15-minute AVWAP window: 2.5s out of 300s per bar = <1% of
decision window. Acceptable up to 10s. Crypto (no window constraint) is even
less sensitive.

Timeout: 5s hard ceiling for the entire panel. If any persona hangs, errgroup
cancels all; fallback to deterministic enrichment (same as current timeout
behavior).

## Files

### Modified

- `backend/internal/ports/ai_advisor.go` -- add `PersonaPanelPort` interface:
  `RunPanel(ctx, PersonaPanelInput) (PersonaPanelResult, error)`.
  `PersonaPanelInput` carries typed data slices per persona (flow data,
  quant data, contrarian data) so the adapter doesn't cherry-pick fields.
- `backend/internal/app/strategy/signal_debate_enricher.go` -- read
  `debate_mode` from strategy params; call `PersonaPanelPort.RunPanel` when
  set to `"persona_panel"`, then feed raw verdicts into the domain aggregator.
- Strategy TOML configs -- add `debate_mode = "persona_panel"` (opt-in)

### New

- `backend/internal/domain/persona_verdict.go` -- `PersonaVerdict` struct
  (persona name, direction, confidence, risk_modifier, reasoning) and
  `PersonaPanelResult` ([]PersonaVerdict, raw). Kept separate from
  `AdvisoryDecision` so the Bull/Bear/Judge path doesn't carry a meaningless
  field. The enricher maps `PersonaPanelResult` -> `AdvisoryDecision` at the
  app layer.
- `backend/internal/domain/persona_aggregator.go` -- pure domain function
  `AggregatePersonaVerdicts(verdicts []PersonaVerdict, opts AggregationOpts)
  -> AggregatedVerdict`. Implements: geometric mean, veto floor, groupthink
  haircut. This is business logic -- it belongs in domain, not in the LLM
  adapter (architect review finding #1).
- `backend/internal/domain/persona_aggregator_test.go` -- unit tests for
  aggregation edge cases (veto floor, groupthink, geomean math).
- `backend/internal/adapters/llm/persona_panel.go` -- implements
  `PersonaPanelPort`: builds 3 persona prompts, runs them in parallel via
  errgroup, builds synthesizer prompt, calls LLM, parses raw verdicts.
  Returns `PersonaPanelResult` -- does NOT apply guardrails (that's domain).
- `backend/internal/adapters/llm/persona_prompts.go` -- system prompt
  constants and user prompt builders per persona (data injection points)
- `backend/internal/adapters/llm/persona_panel_test.go` -- adapter unit tests

### Not changed

- `domain/signal_enrichment.go` -- output type unchanged
- `domain/advisory.go` -- AdvisoryDecision unchanged (no PersonaVerdicts bolt-on)
- `adapters/llm/advisor.go` -- existing Bull/Bear/Judge untouched
- Execution service, risk engine, position monitor -- no changes

### Data availability audit (architect review finding #5)

| Persona | Data field | Available today? | Source |
|---|---|---|---|
| Tape Reader | dp_block_ratio | Yes | darkpool_bars table, monitor enrichment |
| Tape Reader | tfi_value | Yes | strategy state (bar-sign fallback) |
| Tape Reader | inducement_flag | Yes | crypto_inducement detector |
| Tape Reader | relative_volume | Yes | monitor RVOL computation |
| Quant | dev_z | Yes | session VWAP calculator |
| Quant | regime_type/strength | Yes | regime detector |
| Quant | confluence_score | Yes | confluence scorer |
| Quant | historical_pf/wr | Yes | daily_pnl + strategy_daily_pnl tables |
| Contrarian | whale_13f_score | Yes | whale13f service |
| Contrarian | funding_rate | Partial | IBKR crypto only; HL adapter not shipped |
| Contrarian | put_call_ratio | No | needs new data source (Alpaca options OI) |
| Contrarian | news_sentiment | Partial | Alpaca news (pre-cached, not live NLP) |

The Contrarian persona can launch with 13F + news sentiment + whatever
funding data is available. put_call_ratio is deferred until Theta Data or
Alpaca options flow is wired (Sprint 6.1).

## Prompt structure (sketch)

Each persona returns JSON:
```json
{"direction": "LONG|SHORT|NEUTRAL", "confidence": 0.0-1.0,
 "risk_modifier": "TIGHT|NORMAL|WIDE", "reasoning": "..."}
```

**Tape Reader system prompt:**
"You are an institutional tape reader analyzing real-time order flow for a
short-term trade. Evaluate ONLY the flow data provided. Do not speculate
about fundamentals, news, or macro. Return your verdict as JSON."

User prompt injects: dp_block_ratio, tfi_value, tfi_lookback, inducement_flag,
relative_volume, large_print_count.

**Quant system prompt:**
"You are a quantitative analyst evaluating statistical edge. Assess regime
probability and mean-reversion math. Return your verdict as JSON."

User prompt injects: dev_z, regime_type, regime_strength, confluence_score,
historical_pf, historical_wr, historical_expectancy.

**Contrarian system prompt:**
"You are a contrarian analyst looking for crowded trades and sentiment
extremes. Identify whether the consensus trade is too crowded to be
profitable. Return your verdict as JSON."

User prompt injects: whale_13f_score, funding_rate (crypto), put_call_ratio,
news_sentiment_polarity, news_headline_count.

**Synthesizer system prompt:**
"You receive 3 independent analyst verdicts on a proposed trade. Produce a
final verdict weighing their reasoning. If all 3 agree, note potential
groupthink risk. Return JSON with direction, confidence, risk_modifier,
strongest_for (best case for the trade), strongest_against (best case
against), synthesis (your reasoning)."

## Config

```toml
# In any strategy's TOML params section:
debate_mode = "persona_panel"   # default: "bull_bear_judge"
```

Read at runtime via `specStore.GetLatest().Params["debate_mode"]`. Absent or
unrecognized values default to `"bull_bear_judge"`.

## Test plan

### Unit tests (persona_panel_test.go)

1. **Data isolation**: each persona prompt builder receives only its permitted
   fields; assert forbidden fields (e.g., "RSI" in Tape Reader) are absent.
2. **Groupthink guard**: feed 3 identical LONG verdicts with scores > 0.8 on
   an MR strategy; assert confidence reduced by 10%.
3. **Veto floor**: one persona scores 0.15; assert final status = vetoed.
4. **Geometric mean**: scores [0.9, 0.8, 0.7] -> geomean ~0.798; assert
   within 0.01.
5. **Timeout**: one persona hangs; assert RunPanel returns fallback enrichment
   within 5s.
6. **Mock HTTP**: canned persona JSONs; assert final AdvisoryDecision fields.

### A/B paper-trading protocol

- **Duration**: 25 trading days per arm minimum (quant analysis: ~250 signals
  needed at 10/day for 0.3 PF improvement at 95% confidence / 80% power).
- **Method**: shadow scoring -- both modes score every signal; only one
  executes. Tag enrichment events with `debate_mode` in thought_log.
- **Metrics**: (a) veto precision (did vetoed trades actually lose?),
  (b) confidence calibration (binned confidence vs actual win rate),
  (c) latency p50/p99.
- **Decision gate**: persona panel must match or beat Bull/Bear/Judge on veto
  precision and not degrade p99 latency beyond 6s.
- **Calendar**: ~2 months total. Can halve by running both shadow-score on
  every signal and comparing offline.

## Rollback

- `debate_mode` defaults to `"bull_bear_judge"`. Rollback = remove the TOML
  line.
- No DB migration, no domain schema change, no wire-protocol change.
- Per-strategy granularity: can roll back AVWAP while keeping crypto on
  persona panel.
- `PersonaPanelPort` is injected only when at least one strategy uses
  `debate_mode = "persona_panel"`. Otherwise the code path is dead.

## Effort

| Task | Days |
|---|---|
| Domain + port changes | 0.5 |
| Persona prompt engineering | 1.0 |
| Panel adapter (parallel + synthesizer + guardrails) | 1.5 |
| Enricher wiring + config read | 0.5 |
| Tests (unit + integration scaffolding) | 1.0 |
| A/B setup + monitoring dashboard | 0.5 |
| **Total** | **5.0** |

A/B runs asynchronously for 25 trading days after shipping. No additional
engineering time during the A/B unless bugs surface.

## Sequencing

This is independent of the equity gap plan (Sprint 4+) and crypto infra
(SHARED-INFRA gaps). Can run in parallel on a separate branch. Ships behind
a TOML flag so it's safe to merge to main even before the A/B completes --
no strategy uses it until explicitly opted in.

## Decision: when to promote persona panel to default

After the A/B, promote if:
1. Veto precision >= Bull/Bear/Judge (no regression)
2. Confidence calibration slope >= 0.5 (higher confidence = higher win rate)
3. p99 latency < 6s
4. At least 200 scored signals in each arm

If any condition fails, keep Bull/Bear/Judge as default and iterate on prompts.
