// Domain types matching Go backend (backend/internal/domain/)

export type Direction = "LONG" | "SHORT";
export type EnvMode = "Paper" | "Live";
export type RegimeType = "TREND" | "BALANCE" | "REVERSAL";
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
  | "CircuitBreakerTripped";

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