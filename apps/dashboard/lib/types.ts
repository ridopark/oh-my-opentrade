// Domain types matching Go backend (backend/internal/domain/)

export type Direction = "LONG" | "SHORT";
export type EnvMode = "Paper" | "Live";
export type RegimeType = "TREND" | "TREND_UP" | "TREND_DOWN" | "BALANCE" | "REVERSAL";
export type Timeframe = "1m" | "5m" | "15m" | "1h" | "1d";

export type EventType =
  | "MarketBarReceived"
  | "MarketBarSanitized"
  | "MarketBarRejected"
  | "StateUpdated"
  | "RegimeShifted"
  | "SetupDetected"
  | "DebateRequested"
  | "DebateCompleted"
  | "OrderIntentCreated"
  | "OrderIntentValidated"
  | "OrderIntentRejected"
  | "OrderSubmitted"
  | "OrderAccepted"
  | "OrderRejected"
  | "FillReceived"
  | "PositionUpdated"
  | "KillSwitchEngaged"
  | "CircuitBreakerTripped"
  | "FormingBar"
  | "EntryGated"
  | "ORBPhaseUpdate"
  | "StrategyEvaluation"
  | "StrategySignalLifecycle";

// Base domain event envelope
export interface DomainEvent<T = unknown> {
  id: string;
  type: EventType;
  tenantId: string;
  envMode: EnvMode;
  occurredAt: string; // ISO 8601 timestamp
  idempotencyKey: string;
  payload: T;
}

// Advisory / Debate types (maps to domain.AdvisoryDecision)
export interface AdvisoryDecision {
  direction: Direction;
  confidence: number; // 0.0 – 1.0
  rationale: string;
  bullArgument: string;
  bearArgument: string;
  judgeReasoning: string;
}

export interface DebateEvent {
  symbol: string;
  timeframe: Timeframe;
  decision: AdvisoryDecision;
}

// OrderIntent (maps to domain.OrderIntent)
export type OrderIntentStatus =
  | "created"
  | "validated"
  | "rejected"
  | "submitted";

export interface OrderIntentEvent {
  id: string;
  symbol: string;
  direction: Direction;
  limitPrice: number;
  stopLoss: number;
  maxSlippageBPS: number;
  quantity: number;
  strategy: string;
  rationale: string;
  confidence: number;
  status?: OrderIntentStatus;
  reason?: string;
}

// State / Indicators (maps to domain.IndicatorSnapshot + MarketRegime)
export interface StateEvent {
  symbol: string;
  timeframe: Timeframe;
  regime: RegimeType;
  regimeStrength: number;
  rsi: number;
  ema9: number;
  ema21: number;
  vwap: number;
  volume: number;
  volumeSMA: number;
}

// MarketBar (maps to domain.MarketBar — payload of MarketBarReceived / MarketBarSanitized)
export interface MarketBarEvent {
  symbol: string;
  timeframe: Timeframe;
  time: string; // ISO 8601 timestamp
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  suspect: boolean;
  // Indicator fields from enriched REST /api/bars response
  ema9?: number;
  ema21?: number;
  ema50?: number;
  ema200?: number;
  avwaps?: Record<string, number>;
}

// StrategyDNA (maps to domain.StrategyDNA)
export interface StrategyDNA {
  id: string;
  version: number;
  description?: string;
  parameters: Record<string, string | number | boolean>;
  performanceMetrics: Record<string, number>;
}

export interface StrategyDNAEvent {
  current: StrategyDNA;
  previous: StrategyDNA | null;
}

// SSE stream event wrapper
export interface SSEMessage {
  event: EventType;
  data: DomainEvent;
}

// System health
export interface SystemHealth {
  containersRunning: number;
  containersTotal: number;
  eventBusActive: boolean;
  eventsPerMinute: number;
  marketOpen: boolean;
  marketStatus: string;
  uptime: string;
  lastEventAt: string;
}

// Performance Dashboard types (backend: /performance/dashboard + /performance/trades)

export interface PerformanceSummary {
  total_pnl: number;
  realized_pnl: number;
  unrealized_pnl: number;
  num_trades: number;
  winning_days: number;
  losing_days: number;
  win_rate: number | null;
  sharpe: number | null;
  max_drawdown_pct: number;
  gross_profit: number;
  gross_loss: number;
  profit_factor: number | null;
  sortino: number | null;
  expectancy: number | null;
  cagr: number | null;
}

export interface DrawdownPoint {
  time: string;
  drawdown_pct: number;
}

export interface EquityPoint {
  time: string; // RFC3339
  equity: number;
  cash: number;
  drawdown_pct: number;
}

export interface DailyPnlEntry {
  date: string; // YYYY-MM-DD
  realized_pnl: number;
  unrealized_pnl: number;
  trade_count: number;
  max_drawdown: number;
}

export interface PerformanceDashboard {
  range: { from: string; to: string; bucket: string };
  summary: PerformanceSummary;
  equity: EquityPoint[];
  daily_pnl: DailyPnlEntry[];
  drawdown: DrawdownPoint[];
}

export interface StrategyRow {
  strategy: string;
  realized_pnl: number;
  fees: number;
  total_trades: number;
  win_count: number;
  loss_count: number;
  win_rate: number | null;
  profit_factor: number | null;
  gross_profit: number;
  gross_loss: number;
}

export interface TradeEntry {
  time: string; // RFC3339
  trade_id: string;
  symbol: string;
  side: string;
  quantity: number;
  price: number;
  commission: number;
  status: string;
}

export interface TradesResponse {
  items: TradeEntry[];
  next_cursor?: string;
}

// DNA Approval types (backend: /api/dna/approvals)
export interface DnaVersionJSON {
  id: string;
  strategyKey: string;
  contentToml: string;
  contentHash: string;
  detectedAt: string; // RFC3339
}

export interface DnaApprovalJSON {
  id: string;
  versionId: string;
  status: "pending" | "approved" | "rejected";
  decidedBy: string | null;
  decidedAt: string | null; // RFC3339
  comment: string | null;
  createdAt: string; // RFC3339
}

export interface DnaApprovalWithVersion {
  approval: DnaApprovalJSON;
  version: DnaVersionJSON;
}

export interface DnaDiffResponse {
  baseToml: string;
  newToml: string;
}

// Historical orders API types (backend: GET /orders)

export interface ThoughtLogEntry {
  bull_argument: string;
  bear_argument: string;
  judge_reasoning: string;
}

export interface HistoricalOrder {
  time: string; // RFC3339
  intent_id: string;
  broker_order_id: string;
  symbol: string;
  side: string;
  quantity: number;
  limit_price: number;
  stop_loss: number;
  status: string;
  strategy: string;
  rationale: string;
  confidence: number;
  filled_at?: string; // RFC3339
  filled_price?: number;
  filled_qty?: number;
  thought_log?: ThoughtLogEntry;
}

export interface HistoricalOrdersResponse {
  items: HistoricalOrder[];
  next_cursor?: string;
}

// ---------------------------------------------------------------------------
// Per-Strategy Performance types (backend: /api/strategies/)
// ---------------------------------------------------------------------------

export interface StrategyInfo {
  id: string;
  name: string;
  version: string;
  symbols: string[];
  priority: number;
  active: boolean;
}

export interface StrategyPerfSummary {
  totalRealizedPnl: number;
  totalFees: number;
  totalTrades: number;
  winCount: number;
  lossCount: number;
  winRate: number;
  profitFactor: number;
  grossProfit: number;
  grossLoss: number;
  maxDrawdown?: number;
  sharpe?: number;
}

export interface StrategyDailyPnLEntry {
  Day: string; // RFC3339 (Go default — no json tag)
  Strategy: string;
  RealizedPnL: number;
  Fees: number;
  TradeCount: number;
  WinCount: number;
  LossCount: number;
  GrossProfit: number;
  GrossLoss: number;
}

export interface StrategyEquityPointEntry {
  Time: string; // RFC3339 (Go default — no json tag)
  Strategy: string;
  Equity: number;
  RealizedPnLToDate: number;
  FeesToDate: number;
  TradeCountToDate: number;
}

export interface SymbolAttribution {
  symbol: string;
  realizedPnl: number;
  tradeCount: number;
  winCount: number;
  lossCount: number;
}

export interface StrategyDashboard {
  strategy: string;
  summary: StrategyPerfSummary;
  dailyPnl: StrategyDailyPnLEntry[];
  equityCurve: StrategyEquityPointEntry[];
  bySymbol: SymbolAttribution[];
}

export interface StateSnapshot {
  strategy: string;
  symbol: string;
  kind: string;
  asOf: string; // RFC3339
  payload: Record<string, unknown>;
}

export interface StrategySignalEvent {
  TS: string; // RFC3339 (Go default — no json tag)
  Strategy: string;
  SignalID: string;
  Symbol: string;
  Kind: string; // entry, exit, scale_in, scale_out
  Side: string; // BUY, SELL
  Status: string; // generated, validated, executed, suppressed, rejected, debate_override
  Reason: string;
  Confidence: number;
  Payload: Record<string, unknown> | null;
}

export interface StrategySignalsResponse {
  items: StrategySignalEvent[];
  next_cursor?: string;
}

// Entry check breakdown for AVWAP entry_specific gate
export interface EntryCheckResult {
  name: string;
  passed: boolean;
  reason: string;
}

// Signal Formation Progress types (maps to domain.EntryGatedPayload / ORBPhaseUpdatePayload)

export interface BarSnapshot {
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

export interface EntryGatedConfluence {
  score: number;
  maxScore: number;
  fib: boolean;
  fibDetail: string;
  keyLevel: boolean;
  keyLevelDetail: string;
  candle: boolean;
  candleDetail: string;
  band: boolean;
}

export interface EntryGatedIndicators {
  rsi: number;
  volumeRatio: number;
  avwapBias: string;
  slopeBPS: number;
  aboveCount: Record<string, number>;
  belowCount: Record<string, number>;
}

export interface EntryGatedPayload {
  symbol: string;
  strategy: string;
  setupType: string;
  gatesPassed: number;
  gatesTotal: number;
  blockingGate: string;
  blockingDetail: string;
  confluence: EntryGatedConfluence;
  indicators: EntryGatedIndicators;
  bar: BarSnapshot;
  entryChecks?: EntryCheckResult[];
}

export interface ORBPhaseRange {
  high: number;
  low: number;
  valid: boolean;
  barCount: number;
  expectedBars: number;
  windowMinutes: number;
}

export interface ORBPhaseBreakout {
  direction: string;
  rvol: number;
  breakClose: number;
  breakTime: string;
}

export interface ORBPhaseRetest {
  touched: boolean;
  touchPrice: number;
  barsSinceBreak: number;
  maxRetestBars: number;
  holdConfirmed: boolean;
}

export interface ORBPhaseFVG {
  active: boolean;
  high: number;
  low: number;
}

export interface ORBPhaseUpdatePayload {
  symbol: string;
  phase: string;
  range: ORBPhaseRange;
  breakout: ORBPhaseBreakout;
  retest: ORBPhaseRetest;
  confidence: number;
  fvg: ORBPhaseFVG;
  bar: BarSnapshot;
}

// ---------------------------------------------------------------------------
// Strategy Liveness (Phase 1 — polled)
// Backend: GET /api/strategies/{id}/liveness
// ---------------------------------------------------------------------------

export type DecisionOutcome = "HOLD" | "ENTRY" | "EXIT" | "SUPPRESSED" | string;

export interface DecisionReason {
  at: string; // RFC3339
  outcome: DecisionOutcome;
  summary: string;
  tags?: Record<string, string>;
}

export interface SymbolLiveness {
  symbol: string;
  lastTickAt: string | null; // RFC3339 or null if never
  lastEvalAt: string | null;
  lastSignalAt: string | null;
  evalCount: number;
  barsToday: number;
  signalCount: number;
  fillCount: number;
  feedType: string;
  feedLastProcessedAt: string | null;
  feedHealthy: boolean;
  lastDecision?: DecisionReason | null;
  // Phase 3: 60-slot rolling bars-per-minute ring (oldest → newest).
  // Optional because older backends don't emit it — UI renders an empty
  // placeholder when missing so the sparkline column still lines up.
  barsPerMinute?: number[];
}

export interface StrategyLiveness {
  strategy: string;
  symbols: SymbolLiveness[];
  asOf: string; // RFC3339
}

// ---------------------------------------------------------------------------
// Data source health (Phase 3 — dashboard-wide strip)
// Backend: GET /api/health/datasources
// ---------------------------------------------------------------------------

export interface DataSource {
  id: string;          // stable key: "ibkr" | "alpaca" | "omo-data" | "database" | ...
  label: string;       // display name
  healthy: boolean;
  lastEventAt: string | null; // RFC3339 or null if never seen
  detail?: string;     // optional human-readable status (e.g. last error)
}

export interface DataSourceHealth {
  sources: DataSource[];
  asOf: string;        // RFC3339
}

// SSE payload emitted by backend LivenessTracker.RecordEval (Phase 2)
export interface StrategyEvaluationPayload {
  strategy: string;
  symbol: string;
  at: string; // RFC3339
  evalCount: number;
  barsToday: number;
  lastDecision?: DecisionReason | null;
}
