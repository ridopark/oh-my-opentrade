---
goal: AI-Powered Anchored VWAP Anchor Detection — Replace mechanical session-derived anchors with algo-detected swing/volume candidates ranked by LLM
version: 1.0
date_created: 2026-03-17
last_updated: 2026-03-17
owner: omo-core team
status: 'Planned'
tags:
  - feature
  - architecture
  - ai
  - strategy
  - avwap
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

This plan adds **AI-powered anchor detection** to the Anchored VWAP (AVWAP) strategy. The current system uses mechanical session-derived anchors (`pd_high`, `pd_low`, `or_high`, `or_low`, `session_open`) that reset daily and lack the structural significance real traders use for AVWAP anchoring. Real-world AVWAP anchoring requires swing highs/lows (structural reversals visible to all market participants), weekly time anchors, and volume rotation zones.

The architecture follows a **candidate generation → AI selection** pipeline:
1. **Phase 1**: Deterministic Go algorithms detect candidate anchor points (swing pivots, volume rotations, weekly opens) — ships independently with immediate value.
2. **Phase 2**: LLM-based selection ranks the top candidates using market context — extends existing `AIAdvisorPort`.
3. **Phase 3**: Integration wires the new resolver into the strategy Runner, replacing the daily-reset `SessionResolver` with multi-day persistent anchors.

## 1. Requirements & Constraints

### Functional Requirements

- **REQ-001**: Detect swing high/low pivot points using N-bar fractal algorithm (Williams Fractals) on streaming bars without repainting.
- **REQ-002**: Detect volume rotation zones (tight price range + above-average cumulative volume) and breakout points from those zones.
- **REQ-003**: Generate weekly open anchors — first bar of each trading week (Monday 09:30 ET for equity, Monday 00:00 UTC for crypto).
- **REQ-004**: Run swing detection across multiple timeframes (5m, 1h, 1d) and score multi-timeframe confluence.
- **REQ-005**: AI anchor selection ranks 10–20 candidates down to 5–7 selected anchors with confidence scores and rationale.
- **REQ-006**: Selected anchors persist across trading sessions in TimescaleDB (not daily reset).
- **REQ-007**: Anchors expire when price permanently breaks through by >3 SD from the AVWAP line computed from that anchor.
- **REQ-008**: Graceful fallback — if LLM is unavailable, use deterministic ranking: higher timeframe > lower timeframe, more confirmations > fewer, more recent > older.
- **REQ-009**: Existing mechanical anchors (`pd_high`, `or_high`, `session_open`, etc.) remain available as baseline candidates.

### Security & Privacy

- **SEC-001**: The `SelectAnchors` LLM prompt MUST NOT contain strategy DNA parameters, entry/exit rules, or proprietary logic. Only public market data (price, volume, time, regime) and candidate metadata may be sent.
- **SEC-002**: Follow the same privacy boundary established in `adapters/llm/advisor.go:buildPrompt()` — annotate the new prompt builder with the same privacy comment block.

### Architecture Constraints

- **CON-001**: Hexagonal architecture — Swing detection and volume profiling are **domain layer** (pure logic, zero external deps) in `backend/internal/domain/strategy/`. AI selection is a **port** (`ports/ai_advisor.go`) + **adapter** (`adapters/llm/`). Wiring is **app layer** (`app/strategy/`).
- **CON-002**: The `AnchoredVWAPCalc` math is correct and must NOT be modified — only the anchor point sources change.
- **CON-003**: `AVWAPState.ResetAnchors()` currently rebuilds the entire `AnchoredVWAPCalc` from scratch, losing running VWAP state. The updated version must handle **partial anchor updates** — adding/removing individual anchors while preserving running CumPV/CumV/M2 for unchanged anchors.
- **CON-004**: Crypto symbols have no session boundaries (24/7). Use UTC day boundaries and different N-bar parameters.
- **CON-005**: AI call fires only on session open, regime change, or significant new swing detection mid-session — NOT on every bar. Swing detection itself is O(1) amortized per bar.
- **CON-006**: The `anchorResolver` function signature on `Runner` is `func(symbol string, barTime time.Time, anchors []string) map[string]time.Time`. The new resolver must either conform to this signature or the `Runner` must be updated to accept a richer interface.
- **CON-007**: Migration numbering: next available is `022_`. Use `022_create_anchor_points.up.sql` / `022_create_anchor_points.down.sql`.

### Design Guidelines

- **GUD-001**: TDD — write tests first for each component. Swing detector is pure math; volume profiler is pure math; AI selector has a mock port for testing.
- **GUD-002**: Each phase ships independently. Phase 1 is valuable without AI. Phase 2 enhances Phase 1. Phase 3 wires everything together.
- **GUD-003**: Atomic commits per logical unit — one commit per new type, one per algorithm, one per test suite, one per integration point.
- **GUD-004**: All new Go files must include package-level godoc comments explaining the component's purpose and its position in the hexagonal architecture.

### Patterns to Follow

- **PAT-001**: Follow the existing `AnchoredVWAPCalc` pattern — struct with methods, no global state, streaming/online computation.
- **PAT-002**: Follow the existing `AIAdvisorPort` + `DebateOption` functional options pattern for the new `SelectAnchors` method.
- **PAT-003**: Follow the existing `SessionResolver.ResolveAnchors()` signature pattern for backward compatibility.
- **PAT-004**: Follow the existing `debateRequest` internal carrier pattern for passing optional context to the LLM adapter.
- **PAT-005**: Follow the existing test patterns using `testify/assert` and `testify/require` with table-driven tests and `time.Date(2026, ...)` test fixtures.

## 2. Implementation Steps

### Phase 1: Deterministic Candidate Detection (Domain Layer)

- GOAL-001: Implement pure-Go algorithms for detecting candidate anchor points from streaming market bars. All code lives in `backend/internal/domain/strategy/` with zero external dependencies.

| Task     | Description | Completed | Date |
| -------- | ----------- | --------- | ---- |
| TASK-001 | **Create `candidate_anchor.go`** — Define `CandidateAnchor` type: `{ID string, Time time.Time, Price float64, Type CandidateAnchorType, Timeframe string, Strength float64, VolumeContext *VolumeRotationContext, Source string}`. Define `CandidateAnchorType` enum: `SwingHigh`, `SwingLow`, `VolumeRotation`, `WeeklyOpen`, `SessionDerived`. Define `VolumeRotationContext` struct: `{RotationBars int, AvgVolume float64, BreakoutVolume float64, PriceRange [2]float64}`. ID is `fmt.Sprintf("%s_%s_%d", Type, Timeframe, Time.Unix())`. | | |
| TASK-002 | **Test `candidate_anchor.go`** — Table-driven tests for `CandidateAnchor` creation, ID generation, type validation. Ensure zero-value safety. | | |
| TASK-003 | **Create `swing_detector.go`** — Implement `SwingDetector` struct with `NewSwingDetector(n int)` constructor. N is the number of bars required on each side for confirmation. Internal state: circular buffer of bars (size 2*N+1), `confirmed []CandidateAnchor` output buffer. Method `Push(bar Bar) []CandidateAnchor` — returns newly confirmed swings (0 or 1 per call). A swing high is confirmed when `bars[N].High > bars[i].High` for all `i ∈ [0,N) ∪ (N,2N]`. Swing low is symmetric with `bars[N].Low < bars[i].Low`. Strength = count of additional bars beyond N that also confirm (scan up to 2N extra bars from history). The detector is **streaming** — it never repaints; a confirmed swing's time is N bars in the past. Must handle the warmup period (first 2*N bars return empty). | | |
| TASK-004 | **Test `swing_detector.go`** — (a) Basic swing high detection with known bars. (b) Basic swing low detection. (c) No swing when bars are monotonically increasing/decreasing. (d) Multiple swings in a series. (e) Edge case: equal highs (tie-breaking: first bar wins). (f) Warmup period returns empty. (g) Strength scoring: N=5 swing with 3 extra confirmations has strength 8. (h) N=3 and N=5 produce different results on same data. | | |
| TASK-005 | **Create `volume_profile.go`** — Implement `VolumeProfiler` struct. Constructor: `NewVolumeProfiler(bucketSize float64, windowBars int)`. Internal: sliding window of bars, price-bucket histogram `map[int]float64` (bucket index = floor(price/bucketSize)). Method `Push(bar Bar) *CandidateAnchor` — returns a volume rotation anchor when detected, nil otherwise. Detection logic: (1) Compute value area (price range containing 70% of volume). (2) If value area width < 2*bucketSize AND cumulative volume in rotation > 1.5× average window volume → flag as rotation. (3) If next bar's close breaks above/below the value area with bar volume > 2× average → confirm rotation anchor at last bar before breakout. | | |
| TASK-006 | **Test `volume_profile.go`** — (a) Rotation detection with synthetic tight-range high-volume bars followed by breakout. (b) No rotation when volume is evenly distributed. (c) No rotation when price range is wide despite high volume. (d) Breakout upward vs downward produces different anchor prices. (e) Sliding window correctly drops old bars. (f) Edge case: single price bucket (all bars at same price). | | |
| TASK-007 | **Create `weekly_anchor.go`** — Implement `WeeklyAnchorDetector` struct. Constructor: `NewWeeklyAnchorDetector(isCrypto bool)`. Method `Push(bar Bar) *CandidateAnchor` — returns a weekly open anchor on the first bar of each new trading week. For equity: Monday bar with time >= 09:30 ET. For crypto: Monday bar with time >= 00:00 UTC. Internal state: `lastWeekNum int` to detect week transitions. ISO week number comparison. | | |
| TASK-008 | **Test `weekly_anchor.go`** — (a) Equity: first Monday 09:30 bar triggers anchor. (b) Equity: Friday bar does not trigger. (c) Crypto: Monday 00:00 UTC bar triggers. (d) Only one anchor per week (second Monday bar does not trigger). (e) Week rollover from Sunday to Monday (crypto). (f) Holiday weeks where Monday is missing (first Tuesday bar should NOT trigger — only Monday). | | |
| TASK-009 | **Create `multi_tf_scorer.go`** — Implement `MultiTimeframeScorer` struct. Method `Score(candidates []CandidateAnchor) []CandidateAnchor` — enriches each candidate's `Strength` field based on multi-timeframe confluence. Logic: group candidates by approximate time window (±5 bars worth of time for each timeframe). Candidates visible on both 5m and 1h get strength bonus +2. Visible on 5m, 1h, and 1d get +4. Timeframe weight: 1d=3, 1h=2, 5m=1. Final strength = base_strength × timeframe_weight + confluence_bonus. | | |
| TASK-010 | **Test `multi_tf_scorer.go`** — (a) Single-timeframe candidate gets only timeframe weight. (b) Two candidates at similar times from different timeframes get confluence bonus. (c) Candidates far apart in time do not get confluence. (d) 1d candidate scores higher than 5m candidate at same base strength. | | |

### Phase 2: AI Anchor Selection (Port + Adapter)

- GOAL-002: Extend the existing `AIAdvisorPort` with a `SelectAnchors` method that sends candidate metadata to the LLM and receives ranked selections. Implement in the `llm` adapter package.

| Task     | Description | Completed | Date |
| -------- | ----------- | --------- | ---- |
| TASK-011 | **Create `anchor_selection.go` in `domain/strategy/`** — Define domain types: `AnchorSelection{SelectedAnchors []SelectedAnchor, Rationale string}`, `SelectedAnchor{CandidateID string, AnchorName string, Rank int, Confidence float64, Reason string}`. `AnchorName` is a stable key used by `AnchoredVWAPCalc.AddAnchor()` (e.g., `"swing_high_1h_1710000000"`). Add `NewAnchorSelection()` constructor with validation: at least 1 selected anchor, all confidences in [0,1], ranks are 1-indexed and unique. | | |
| TASK-012 | **Test `anchor_selection.go`** — Validation tests: (a) Valid selection passes. (b) Empty selection fails. (c) Duplicate ranks fail. (d) Confidence out of range fails. (e) Zero-value safety. | | |
| TASK-013 | **Extend `ports/ai_advisor.go`** — Add `SelectAnchors` method to `AIAdvisorPort` interface: `SelectAnchors(ctx context.Context, req AnchorSelectionRequest) (*strategy.AnchorSelection, error)`. Define `AnchorSelectionRequest` struct in ports package: `{Symbol domain.Symbol, Candidates []strategy.CandidateAnchor, CurrentPrice float64, Regime domain.MarketRegime, Indicators domain.IndicatorSnapshot}`. Add `AnchorSelectionOption` type alias to `DebateOption` for future extensibility. | | |
| TASK-014 | **Update `adapters/llm/noop_advisor.go`** — Add `SelectAnchors` method that returns `nil, ErrAIDisabled`. This ensures the noop adapter satisfies the updated interface. | | |
| TASK-015 | **Implement `SelectAnchors` in `adapters/llm/advisor.go`** — New method on `Advisor`. Build a structured prompt with: (a) Symbol and current price. (b) Market regime (type + strength). (c) List of 10–20 candidates with metadata: ID, type, time, price, timeframe, strength, distance from current price as %, volume context if present, how many times price has crossed this level (touch count — computed by caller). (d) Response template requiring JSON: `{selected_anchors: [{candidate_id, rank, confidence, reason}], rationale}`. (e) System prompt: "You are an institutional VWAP trader selecting anchor points..." (f) Parse response, validate against `NewAnchorSelection()`, return. Rate-limit guard: reuse existing `minInterval` mutex. Privacy boundary: same as `buildPrompt()` — only public market data. | | |
| TASK-016 | **Test `SelectAnchors` adapter** — (a) Unit test with `httptest.Server` returning valid JSON → successful parse. (b) Test with malformed JSON → error. (c) Test with missing candidate_id → validation error. (d) Test rate limiting. (e) Test NEUTRAL/empty selection → nil result. (f) Verify prompt does NOT contain any proprietary strategy parameters (parse the sent request body and assert absence of DNA keywords). | | |

### Phase 3: Anchor Persistence & Expiry

- GOAL-003: Persist active anchors in TimescaleDB so they survive restarts and span multiple trading sessions. Implement expiry logic.

| Task     | Description | Completed | Date |
| -------- | ----------- | --------- | ---- |
| TASK-017 | **Create migration `022_create_anchor_points.up.sql`** — Table: `anchor_points(id TEXT PRIMARY KEY, symbol TEXT NOT NULL, anchor_time TIMESTAMPTZ NOT NULL, price FLOAT8 NOT NULL, anchor_type TEXT NOT NULL, timeframe TEXT NOT NULL, strength FLOAT8 NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT 'algo', ai_rank INT, ai_confidence FLOAT8, ai_reason TEXT, volume_context JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), expired_at TIMESTAMPTZ, expired_reason TEXT)`. Indexes: `(symbol, expired_at)` partial index where `expired_at IS NULL`, `(symbol, anchor_type)`. Create `022_create_anchor_points.down.sql` with `DROP TABLE anchor_points;`. | | |
| TASK-018 | **Create `ports/anchor_store.go`** — Define `AnchorStorePort` interface: `Save(ctx, []strategy.CandidateAnchor) error`, `LoadActive(ctx, symbol string) ([]strategy.CandidateAnchor, error)`, `Expire(ctx, anchorID string, reason string) error`, `SaveSelection(ctx, symbol string, sel strategy.AnchorSelection) error`. | | |
| TASK-019 | **Implement `adapters/timescaledb/anchor_store.go`** — Implement `AnchorStorePort` with `database/sql`. `Save()` uses `ON CONFLICT (id) DO UPDATE SET strength = EXCLUDED.strength` to upsert. `LoadActive()` queries `WHERE symbol=$1 AND expired_at IS NULL ORDER BY strength DESC`. `Expire()` sets `expired_at = NOW()` and `expired_reason`. `SaveSelection()` updates `ai_rank`, `ai_confidence`, `ai_reason` for selected IDs. | | |
| TASK-020 | **Test `anchor_store.go`** — Integration tests with test DB: (a) Save and load round-trip. (b) Expire removes from active set. (c) SaveSelection updates AI fields. (d) Upsert updates strength. | | |

### Phase 4: AI Anchor Resolver (App Layer Orchestration)

- GOAL-004: Create the `AIAnchorResolver` in the app layer that orchestrates candidate detection → AI selection → anchor persistence, and integrates with the strategy `Runner`.

| Task     | Description | Completed | Date |
| -------- | ----------- | --------- | ---- |
| TASK-021 | **Create `app/strategy/ai_anchor_resolver.go`** — Struct `AIAnchorResolver` with dependencies: `SwingDetector` per timeframe per symbol, `VolumeProfiler` per symbol, `WeeklyAnchorDetector` per symbol, `AIAdvisorPort`, `AnchorStorePort`, logger. Constructor: `NewAIAnchorResolver(advisor ports.AIAdvisorPort, store ports.AnchorStorePort, logger *slog.Logger) *AIAnchorResolver`. Methods: (1) `RegisterSymbol(symbol string, isCrypto bool)` — creates detector instances. (2) `OnBar(symbol string, bar strategy.Bar, tf string)` — feeds bar to appropriate detectors, accumulates new candidates. (3) `ResolveAnchors(ctx context.Context, symbol string, barTime time.Time, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, anchorNames []string) (map[string]time.Time, error)` — full pipeline: load persisted + generate new candidates → score → AI select (or fallback) → return anchor times. (4) `ExpireAnchors(ctx context.Context, symbol string, avwapCalc *strategy.AnchoredVWAPCalc, currentPrice float64)` — check each active anchor; if price > AVWAP + 3×SD or price < AVWAP - 3×SD consistently for 10+ bars, expire it. | | |
| TASK-022 | **Implement fallback ranking in `AIAnchorResolver`** — Method `fallbackRank(candidates []strategy.CandidateAnchor) *strategy.AnchorSelection` — deterministic ranking without LLM: sort by `(timeframe_weight × 10 + strength)` descending, take top 7. Timeframe weights: `1d=3, 1h=2, 5m=1`. Break ties by recency (more recent first). Convert to `AnchorSelection` with confidence = `strength / max_strength`. | | |
| TASK-023 | **Test `ai_anchor_resolver.go`** — (a) Mock `AIAdvisorPort` returning selection → verify correct anchors passed through. (b) Mock `AIAdvisorPort` returning error → verify fallback ranking used. (c) Mock `AnchorStorePort` → verify Save/LoadActive/Expire calls. (d) Test `ExpireAnchors` with price beyond 3 SD. (e) Test `ResolveAnchors` preserves existing mechanical anchors (`session_open`, `pd_high`, etc.) alongside new candidates. (f) Test `RegisterSymbol` creates correct detector configs for crypto vs equity. | | |

### Phase 5: Integration with Strategy Runner

- GOAL-005: Wire the `AIAnchorResolver` into the strategy `Runner` and update `AVWAPState.ResetAnchors()` to handle partial updates without losing running VWAP state for unchanged anchors.

| Task     | Description | Completed | Date |
| -------- | ----------- | --------- | ---- |
| TASK-024 | **Update `AVWAPState.ResetAnchors()` in `builtin/avwap_v1.go`** — Change from full rebuild to partial update. New signature: `ResetAnchors(anchorTimes map[string]time.Time)` (unchanged). New logic: (1) For each anchor in `anchorTimes`: if anchor already exists in `Calc` with same time → keep it (preserve CumPV/CumV/M2). If anchor is new or time changed → add/replace. (2) For each anchor in `Calc` not in `anchorTimes` → remove it. (3) Reset `AboveCount`/`BelowCount` only for changed/new anchors. (4) Do NOT reset `TradesToday` — that's session-scoped. Extract existing VWAP states via `Calc.States()` and selectively restore via `Calc.Restore()`. | | |
| TASK-025 | **Add `RemoveAnchor(name string)` to `AnchoredVWAPCalc`** — New method in `domain/strategy/anchored_vwap.go`. Deletes the named entry from `c.anchors` map. Returns bool indicating if it existed. | | |
| TASK-026 | **Test partial `ResetAnchors`** — (a) Existing anchor with unchanged time retains VWAP state. (b) New anchor is added with fresh state. (c) Removed anchor is deleted. (d) Changed anchor time resets that anchor's state but not others. (e) `AboveCount`/`BelowCount` reset only for changed anchors. | | |
| TASK-027 | **Update `Runner` anchor resolver interface** — Change `Runner.anchorResolver` from `func(symbol string, barTime time.Time, anchors []string) map[string]time.Time` to a richer interface: `type AnchorResolverFunc func(ctx context.Context, symbol string, barTime time.Time, anchorNames []string, regime domain.MarketRegime, indicators domain.IndicatorSnapshot) (map[string]time.Time, error)`. Update `SetAnchorResolver()` and `resolveSessionAnchors()` to use the new signature. The existing `SessionResolver.ResolveAnchors()` is wrapped in an adapter closure that ignores the extra parameters. | | |
| TASK-028 | **Update `Runner.handleBar()` trigger logic** — Currently triggers anchor resolution on date change only. Add triggers: (1) Date change (existing). (2) Regime change — compare current regime with last resolved regime, if different → re-resolve. (3) New significant swing detected mid-session (check a channel/flag set by `AIAnchorResolver.OnBar()`). Store `lastResolvedRegime map[string]domain.RegimeType` on Runner. | | |
| TASK-029 | **Wire in `services.go`** — In `initStrategyPipeline()`: (1) Create `AnchorStorePort` adapter from TimescaleDB. (2) Create `AIAnchorResolver` with `svc.aiAdvisor` and anchor store. (3) Register symbols from pipeline. (4) Set anchor resolver on `strategyRunner` using `AIAnchorResolver.ResolveAnchors`. (5) Feed bars to `AIAnchorResolver.OnBar()` — subscribe to `MarketBarSanitized` in the resolver's own `Start()` method, or have the Runner call it. Preference: Runner calls `OnBar` directly to maintain ordering guarantees. | | |
| TASK-030 | **Test full integration** — (a) End-to-end test with Runner + mock AIAnchorResolver: bars flow through → swings detected → AI called → anchors set on strategy state → AVWAP values computed correctly. (b) Test regime change triggers re-resolution. (c) Test fallback when AI is noop. (d) Test anchor persistence across simulated restart (save states, restore, verify VWAP continuity). | | |

### Phase 6: Backtest Support

- GOAL-006: Extend `SessionResolver` to include algo-detected candidates for backtest scenarios where LLM calls are not practical.

| Task     | Description | Completed | Date |
| -------- | ----------- | --------- | ---- |
| TASK-031 | **Extend `SessionResolver` with swing detection** — Add a post-processing step after `Load()` that runs `SwingDetector` on loaded 1m bars to pre-compute swing highs/lows for each session. Store as additional `SessionData` fields: `SwingHighs []CandidateAnchor`, `SwingLows []CandidateAnchor`. `ResolveAnchors()` can then resolve names like `"swing_high_1"` (most recent swing high), `"swing_low_1"`, `"weekly_open"`. | | |
| TASK-032 | **Test backtest swing detection** — (a) Load synthetic bars → verify swings detected at correct times. (b) Verify `ResolveAnchors("swing_high_1", ...)` returns the most recent swing high time. (c) Verify backward compatibility — existing anchor names (`pd_high`, etc.) still resolve correctly. | | |

## 3. Alternatives

- **ALT-001**: **LLM-only anchor detection** — Have the LLM analyze raw OHLCV bars directly and identify anchors. Rejected because: (a) too many tokens per call (sending 100+ bars), (b) LLM is not reliable for precise numerical pattern matching, (c) latency too high for real-time use, (d) no fallback when LLM is down.
- **ALT-002**: **Pre-trained ML model for swing detection** — Train a classifier on labeled swing data. Rejected because: (a) adds ML inference dependency, (b) Williams Fractals is already the gold standard that institutional traders use, (c) domain algo is deterministic and debuggable.
- **ALT-003**: **Separate microservice for anchor detection** — Run detection in a dedicated service. Rejected because: (a) adds operational complexity, (b) current single-binary architecture is a feature, (c) latency of inter-process communication unnecessary for O(1) per-bar computation.
- **ALT-004**: **Add `SelectAnchors` as a completely new port** instead of extending `AIAdvisorPort`. Rejected because: the method shares the same LLM infrastructure (HTTP client, rate limiting, API key, provider routing) and should reuse the existing adapter. A separate port would duplicate configuration and connection management.

## 4. Dependencies

- **DEP-001**: `backend/internal/domain/strategy/anchored_vwap.go` — Existing `AnchoredVWAPCalc`, `AnchorPoint`, `AnchoredVWAPState` types. No modifications to VWAP math. New `RemoveAnchor()` method added.
- **DEP-002**: `backend/internal/ports/ai_advisor.go` — Existing `AIAdvisorPort` interface. Extended with `SelectAnchors()` method (breaking change to interface — all implementations must be updated).
- **DEP-003**: `backend/internal/adapters/llm/advisor.go` — Existing `Advisor` struct. Extended with new method implementing `SelectAnchors()`.
- **DEP-004**: `backend/internal/adapters/llm/noop_advisor.go` — Existing `NoOpAdvisor`. Must implement `SelectAnchors()` returning `ErrAIDisabled`.
- **DEP-005**: `backend/internal/app/strategy/runner.go` — Existing `Runner` struct. `anchorResolver` field type changes. `handleBar()` gets additional trigger logic.
- **DEP-006**: `backend/internal/app/strategy/builtin/avwap_v1.go` — Existing `AVWAPState.ResetAnchors()` method. Updated for partial anchor management.
- **DEP-007**: `backend/internal/app/backtest/session.go` — Existing `SessionResolver`. Extended with swing detection support.
- **DEP-008**: `backend/cmd/omo-core/services.go` — Wiring code for the production binary.
- **DEP-009**: TimescaleDB — New `anchor_points` table (migration 022).
- **DEP-010**: `github.com/stretchr/testify` — Test assertions (already in go.mod).

## 5. Files

### New Files

- **FILE-001**: `backend/internal/domain/strategy/candidate_anchor.go` — `CandidateAnchor` type, `CandidateAnchorType` enum, `VolumeRotationContext` struct. Pure value objects with validation.
- **FILE-002**: `backend/internal/domain/strategy/candidate_anchor_test.go` — Unit tests for candidate anchor creation and validation.
- **FILE-003**: `backend/internal/domain/strategy/swing_detector.go` — `SwingDetector` struct implementing Williams Fractal / N-bar pivot detection on streaming bars.
- **FILE-004**: `backend/internal/domain/strategy/swing_detector_test.go` — Comprehensive tests for swing detection including edge cases.
- **FILE-005**: `backend/internal/domain/strategy/volume_profile.go` — `VolumeProfiler` struct implementing volume rotation and breakout detection.
- **FILE-006**: `backend/internal/domain/strategy/volume_profile_test.go` — Tests for volume profile computation and rotation detection.
- **FILE-007**: `backend/internal/domain/strategy/weekly_anchor.go` — `WeeklyAnchorDetector` for weekly open anchor generation.
- **FILE-008**: `backend/internal/domain/strategy/weekly_anchor_test.go` — Tests for weekly anchor detection (equity vs crypto).
- **FILE-009**: `backend/internal/domain/strategy/multi_tf_scorer.go` — `MultiTimeframeScorer` for cross-timeframe confluence scoring.
- **FILE-010**: `backend/internal/domain/strategy/multi_tf_scorer_test.go` — Tests for multi-timeframe scoring.
- **FILE-011**: `backend/internal/domain/strategy/anchor_selection.go` — `AnchorSelection` and `SelectedAnchor` domain types with validation.
- **FILE-012**: `backend/internal/domain/strategy/anchor_selection_test.go` — Validation tests.
- **FILE-013**: `backend/internal/ports/anchor_store.go` — `AnchorStorePort` interface for anchor persistence.
- **FILE-014**: `backend/internal/adapters/timescaledb/anchor_store.go` — TimescaleDB implementation of `AnchorStorePort`.
- **FILE-015**: `backend/internal/adapters/timescaledb/anchor_store_test.go` — Integration tests for anchor store.
- **FILE-016**: `backend/internal/app/strategy/ai_anchor_resolver.go` — `AIAnchorResolver` orchestrating candidate detection → AI selection → persistence.
- **FILE-017**: `backend/internal/app/strategy/ai_anchor_resolver_test.go` — Unit tests with mocked ports.
- **FILE-018**: `migrations/022_create_anchor_points.up.sql` — Create `anchor_points` table.
- **FILE-019**: `migrations/022_create_anchor_points.down.sql` — Drop `anchor_points` table.

### Modified Files

- **FILE-020**: `backend/internal/domain/strategy/anchored_vwap.go` — Add `RemoveAnchor(name string) bool` method.
- **FILE-021**: `backend/internal/ports/ai_advisor.go` — Add `SelectAnchors()` to `AIAdvisorPort` interface, add `AnchorSelectionRequest` struct.
- **FILE-022**: `backend/internal/adapters/llm/advisor.go` — Implement `SelectAnchors()` method with prompt builder and response parser.
- **FILE-023**: `backend/internal/adapters/llm/noop_advisor.go` — Add `SelectAnchors()` returning `ErrAIDisabled`.
- **FILE-024**: `backend/internal/app/strategy/runner.go` — Update `anchorResolver` type, update `resolveSessionAnchors()`, add regime-change trigger, add bar forwarding to `AIAnchorResolver`.
- **FILE-025**: `backend/internal/app/strategy/builtin/avwap_v1.go` — Update `ResetAnchors()` for partial anchor updates.
- **FILE-026**: `backend/internal/app/backtest/session.go` — Extend `SessionResolver` with swing detection support.
- **FILE-027**: `backend/cmd/omo-core/services.go` — Wire `AIAnchorResolver` in `initStrategyPipeline()`.

## 6. Testing

### Unit Tests (Domain Layer — Pure Logic)

- **TEST-001**: `swing_detector_test.go` — 8+ test cases covering basic detection, edge cases, warmup, strength scoring, different N values. All use synthetic bar data with known swing points.
- **TEST-002**: `volume_profile_test.go` — 6+ test cases covering rotation detection, breakout confirmation, sliding window, edge cases.
- **TEST-003**: `weekly_anchor_test.go` — 6+ test cases covering equity/crypto weekly opens, week transitions, holidays.
- **TEST-004**: `multi_tf_scorer_test.go` — 4+ test cases for confluence scoring across timeframes.
- **TEST-005**: `candidate_anchor_test.go` — Validation and construction tests.
- **TEST-006**: `anchor_selection_test.go` — Validation tests for `AnchorSelection` and `SelectedAnchor`.

### Unit Tests (Adapter Layer)

- **TEST-007**: `advisor_test.go` (extended) — `TestSelectAnchors_*` tests with httptest server for the LLM adapter. Test valid response, malformed response, rate limiting, privacy boundary verification.
- **TEST-008**: `noop_advisor_test.go` (extended) — Verify `SelectAnchors()` returns `ErrAIDisabled`.

### Unit Tests (App Layer)

- **TEST-009**: `ai_anchor_resolver_test.go` — 6+ test cases with mocked `AIAdvisorPort` and `AnchorStorePort`. Test the full orchestration pipeline, fallback ranking, expiry logic, symbol registration.
- **TEST-010**: `avwap_v1_test.go` (extended) — Test partial `ResetAnchors()`: preserved state for unchanged anchors, fresh state for new, removal of old.
- **TEST-011**: `runner_test.go` (extended) — Test updated anchor resolution triggers: date change, regime change.

### Integration Tests

- **TEST-012**: `anchor_store_test.go` — Integration tests with TimescaleDB test container or test database. Save/load/expire/upsert round-trips.
- **TEST-013**: End-to-end pipeline test — Runner + AIAnchorResolver + mock LLM → verify correct anchors flow through to AVWAP computation.

### Test Strategy

All development follows **TDD Red-Green-Refactor**:
1. **RED**: Write a failing test that defines the expected behavior.
2. **GREEN**: Write the minimum code to make the test pass.
3. **REFACTOR**: Clean up while keeping tests green.

Run command: `cd backend && go test ./internal/domain/strategy/... ./internal/app/strategy/... ./internal/adapters/llm/... -v -count=1`

## 7. Risks & Assumptions

### Risks

- **RISK-001**: **LLM response quality** — The LLM may not consistently select the most relevant anchors. Mitigation: the fallback ranking algorithm provides a solid baseline, and the LLM selection is an enhancement, not a requirement. A/B test LLM-selected vs algo-ranked anchors in paper trading before relying on LLM.
- **RISK-002**: **LLM latency on session open** — `SelectAnchors` adds ~2-5s of latency on session open. Mitigation: (a) the call is async — the strategy can use previously active anchors while the new selection is in flight. (b) Fire the call 30s before anticipated session open. (c) Set a 5s context timeout matching the existing debate enricher pattern.
- **RISK-003**: **Interface breaking change** — Adding `SelectAnchors()` to `AIAdvisorPort` is a breaking interface change. All implementations must be updated simultaneously. Mitigation: there are only two implementations (`Advisor` and `NoOpAdvisor`), both in the same package. Update both in a single commit.
- **RISK-004**: **Anchor proliferation** — Without proper expiry, the number of active anchors may grow unbounded. Mitigation: (a) expiry at 3 SD deviation. (b) Hard cap of 20 active anchors per symbol — expire oldest/weakest when cap is reached. (c) Age-based expiry: anchors older than 20 trading sessions are automatically expired.
- **RISK-005**: **Swing detector repainting** — If implemented incorrectly, the swing detector could retroactively change confirmed swings. Mitigation: the N-bar lag design ensures a swing is only confirmed after N future bars have passed. Extensive test coverage for this invariant.
- **RISK-006**: **Volume profile bucket size sensitivity** — Wrong bucket size can produce too many or too few rotations. Mitigation: bucket size should be ATR-based (e.g., `ATR / 4`) rather than a fixed dollar amount. Parameterize and tune during paper trading.

### Assumptions

- **ASSUMPTION-001**: The existing `AnchoredVWAPCalc` math (Welford's online variance for SD bands) is correct and does not need modification. Verified by existing test suite.
- **ASSUMPTION-002**: The LLM (Claude/GPT) can effectively rank anchor points given sufficient structured context (candidate metadata, price distance, touch count, regime). This is a reasonable assumption given that anchor selection is a well-understood concept that can be explained in natural language.
- **ASSUMPTION-003**: TimescaleDB is available for anchor persistence. If the DB is temporarily unreachable, in-memory candidates serve as fallback.
- **ASSUMPTION-004**: 5-minute bars are the primary timeframe for swing detection in live trading. 1-hour and 1-day bars are used for multi-timeframe confluence via the existing `BarAggregator`.
- **ASSUMPTION-005**: The `BarAggregator` already produces 1h and 1d bars from 1m bars. The `AIAnchorResolver` can subscribe to aggregated HTF bars for multi-timeframe swing detection.

## 8. Commit Strategy

Each commit should be atomic, self-contained, and pass all existing tests plus new tests for the introduced code.

### Commit Sequence

```
Phase 1 (domain layer — each commit independently valuable):
  1. feat(domain): add CandidateAnchor type and CandidateAnchorType enum
  2. feat(domain): add SwingDetector with Williams Fractal N-bar pivot detection
  3. feat(domain): add WeeklyAnchorDetector for weekly open anchors
  4. feat(domain): add VolumeProfiler with rotation and breakout detection
  5. feat(domain): add MultiTimeframeScorer for cross-TF confluence scoring
  6. feat(domain): add AnchorSelection and SelectedAnchor types

Phase 2 (port + adapter — atomic interface change):
  7. feat(ports): add SelectAnchors to AIAdvisorPort and AnchorSelectionRequest
  8. feat(adapters/llm): implement SelectAnchors with structured prompt and response parser
  9. feat(adapters/llm): add SelectAnchors noop implementation

Phase 3 (persistence):
  10. feat(migrations): add anchor_points table (migration 022)
  11. feat(ports): add AnchorStorePort interface
  12. feat(adapters/timescaledb): implement AnchorStorePort

Phase 4 (app layer orchestration):
  13. feat(domain): add RemoveAnchor method to AnchoredVWAPCalc
  14. refactor(avwap): update ResetAnchors for partial anchor updates
  15. feat(app/strategy): add AIAnchorResolver with candidate detection + AI selection + fallback

Phase 5 (integration):
  16. refactor(runner): update anchorResolver to richer interface with regime/indicator context
  17. feat(runner): add regime-change trigger for anchor re-resolution
  18. feat(services): wire AIAnchorResolver into production pipeline

Phase 6 (backtest):
  19. feat(backtest): extend SessionResolver with swing detection for historical anchors
```

## 9. Related Specifications / Further Reading

- [docs/ARCHITECTURE.md](../ARCHITECTURE.md) — Hexagonal architecture overview
- [docs/STRATEGY_SYSTEM.md](../STRATEGY_SYSTEM.md) — Strategy v2 pipeline documentation
- [docs/AI_DEBATE.md](../AI_DEBATE.md) — AI debate system (existing LLM integration)
- [backend/internal/domain/strategy/anchored_vwap.go](../../backend/internal/domain/strategy/anchored_vwap.go) — AVWAP math implementation
- [backend/internal/adapters/llm/advisor.go](../../backend/internal/adapters/llm/advisor.go) — LLM adapter with privacy boundary
- Williams, B. (1995). "Trading Chaos" — Williams Fractals definition
- [Anchored VWAP](https://www.investopedia.com/terms/a/anchored-vwap.asp) — Investopedia reference
