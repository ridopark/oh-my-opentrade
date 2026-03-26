"use client";

import { useCallback, useEffect, useRef, useState } from "react";

export interface BacktestConfig {
  symbols: string[];
  from: string;
  to: string;
  timeframe: string;
  initialEquity: number;
  slippageBps: number;
  speed: string;
  noAi: boolean;
  strategies: string[];
  useDailyScreener: boolean;
  screenerTopN: number;
}

export interface BacktestBar {
  time: number;
  symbol: string;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  ema9?: number;
  ema21?: number;
  ema50?: number;
  ema200?: number;
}

export interface BacktestSignal {
  time: number;
  symbol: string;
  side: "buy" | "sell";
  kind: "entry" | "exit";
  strategy: string;
  strength: number;
}

export interface BacktestTrade {
  symbol: string;
  side: string;
  direction?: string; // "LONG", "SHORT", or "CLOSE"
  quantity: number;
  price: number;
  filled_at: string;
  strategy?: string;
  rationale?: string;
  regime?: string;
  vix_bucket?: string;
  market_context?: string;
  pnl?: number;
}

export interface BacktestMetrics {
  equity: number;
  total_pnl: number;
  total_return: number;
  trades: number;
  win_rate: number;
  max_drawdown: number;
  sharpe: number;
  profit_factor: number;
  open_positions: number;
}

export interface BacktestProgress {
  bars_processed: number;
  total_bars: number;
  pct: number;
  current_time: string;
  replay_speed: string;
}

export interface BacktestResult {
  initial_equity: number;
  final_equity: number;
  total_return_pct: number;
  total_pnl: number;
  trade_count: number;
  win_count: number;
  loss_count: number;
  win_rate_pct: number;
  max_drawdown_pct: number;
  sharpe_ratio: number;
  profit_factor: number;
  avg_win: number;
  avg_loss: number;
  largest_win: number;
  largest_loss: number;
}

type BacktestStatus = "idle" | "running" | "paused" | "completed" | "error" | "cancelled";

export interface UseBacktestReturn {
  status: BacktestStatus;
  backtestId: string | null;
  progress: BacktestProgress | null;
  setupStage: string | null;
  bars: Map<string, BacktestBar[]>;
  signals: BacktestSignal[];
  trades: BacktestTrade[];
  metrics: BacktestMetrics | null;
  equityCurve: { time: number; value: number }[];
  result: BacktestResult | null;
  error: string | null;
  run: (config: BacktestConfig) => Promise<void>;
  pause: () => Promise<void>;
  resume: () => Promise<void>;
  setSpeed: (speed: string) => Promise<void>;
  cancel: () => Promise<void>;
}

export function useBacktest(): UseBacktestReturn {
  const [status, setStatus] = useState<BacktestStatus>("idle");
  const [backtestId, setBacktestId] = useState<string | null>(null);
  const [progress, setProgress] = useState<BacktestProgress | null>(null);
  const [bars, setBars] = useState<Map<string, BacktestBar[]>>(new Map());
  const [signals, setSignals] = useState<BacktestSignal[]>([]);
  const [trades, setTrades] = useState<BacktestTrade[]>([]);
  const [metrics, setMetrics] = useState<BacktestMetrics | null>(null);
  const [equityCurve, setEquityCurve] = useState<{ time: number; value: number }[]>([]);
  const [result, setResult] = useState<BacktestResult | null>(null);
  const [setupStage, setSetupStage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const esRef = useRef<EventSource | null>(null);
  const backtestIdRef = useRef<string | null>(null);
  const progressRef = useRef<BacktestProgress | null>(null);

  const barsRef = useRef<Map<string, BacktestBar[]>>(new Map());
  const signalsRef = useRef<BacktestSignal[]>([]);
  const tradesRef = useRef<BacktestTrade[]>([]);
  const equityCurveRef = useRef<{ time: number; value: number }[]>([]);
  const rafRef = useRef<number | null>(null);
  const dirtyRef = useRef(false);

  const flushToState = useCallback(() => {
    if (!dirtyRef.current) return;
    dirtyRef.current = false;
    const barsCopy = new Map<string, BacktestBar[]>();
    for (const [sym, arr] of barsRef.current) {
      barsCopy.set(sym, [...arr]);
    }
    setBars(barsCopy);
    setSignals([...signalsRef.current]);
    setTrades([...tradesRef.current]);
    setEquityCurve([...equityCurveRef.current]);
  }, []);

  const scheduleFlush = useCallback(() => {
    dirtyRef.current = true;
    if (rafRef.current === null) {
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = null;
        flushToState();
      });
    }
  }, [flushToState]);

  const cleanup = useCallback(() => {
    if (esRef.current) {
      esRef.current.close();
      esRef.current = null;
    }
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = null;
    }
  }, []);

  useEffect(() => cleanup, [cleanup]);

  const connectSSE = useCallback((id: string) => {
    cleanup();
    const es = new EventSource(`/api/backtest/${id}/events`);
    esRef.current = es;

    const eventTypes = [
      "backtest:setup",
      "backtest:candle",
      "backtest:signal",
      "backtest:signal_enriched",
      "backtest:trade",
      "backtest:intent",
      "backtest:intent_rejected",
      "backtest:metrics",
      "backtest:progress",
      "backtest:complete",
    ];

    for (const type of eventTypes) {
      es.addEventListener(type, (e: MessageEvent) => {
        try {
          const envelope = JSON.parse(e.data);
          const data = envelope.data ?? envelope;

          switch (type) {
            case "backtest:setup": {
              const stage = (data as { stage?: string }).stage ?? null;
              setSetupStage(stage);
              break;
            }
            case "backtest:candle": {
              const bar = data as BacktestBar;
              const sym = bar.symbol;
              const existing = barsRef.current.get(sym) ?? [];
              existing.push(bar);
              barsRef.current.set(sym, existing);
              scheduleFlush();
              break;
            }
            case "backtest:signal": {
              signalsRef.current.push(data as BacktestSignal);
              scheduleFlush();
              break;
            }
            case "backtest:trade": {
              tradesRef.current.push(data as BacktestTrade);
              scheduleFlush();
              break;
            }
            case "backtest:metrics": {
              const m = data as BacktestMetrics;
              setMetrics(m);
              if (m.equity > 0 && progressRef.current?.current_time) {
                const t = Math.floor(new Date(progressRef.current.current_time).getTime() / 1000);
                const lastEntry = equityCurveRef.current[equityCurveRef.current.length - 1];
                if (!lastEntry || lastEntry.time < t) {
                  equityCurveRef.current.push({ time: t, value: m.equity });
                  scheduleFlush();
                }
              }
              break;
            }
            case "backtest:progress": {
              const p = data as BacktestProgress;
              progressRef.current = p;
              setProgress(p);
              setSetupStage(null);
              break;
            }
            case "backtest:complete": {
              setStatus("completed");
              fetchResults(id);
              break;
            }
          }
        } catch {
        }
      });
    }

     es.onerror = () => {};
   }, [cleanup, scheduleFlush]);

  const fetchResults = async (id: string) => {
    try {
      const res = await fetch(`/api/backtest/${id}/results`);
      if (res.ok) {
        const data = await res.json();
        setResult(data);
      }
    } catch {
    }
  };

  const run = useCallback(async (config: BacktestConfig) => {
    setStatus("running");
    setError(null);
    setResult(null);
    setProgress(null);
    setSetupStage(null);
    setMetrics(null);
    barsRef.current = new Map();
    signalsRef.current = [];
    tradesRef.current = [];
    equityCurveRef.current = [];
    progressRef.current = null;
    setBars(new Map());
    setSignals([]);
    setTrades([]);
    setEquityCurve([]);

    try {
      const res = await fetch("/api/backtest/run", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          symbols: config.symbols,
          from: config.from,
          to: config.to,
          timeframe: config.timeframe,
          initial_equity: config.initialEquity,
          slippage_bps: config.slippageBps,
          speed: config.speed,
          no_ai: config.noAi,
          strategies: config.strategies.length > 0 ? config.strategies : undefined,
          use_daily_screener: config.useDailyScreener,
          screener_top_n: config.screenerTopN || 5,
        }),
      });
      const data = await res.json();
      if (!res.ok) {
        setStatus("error");
        setError(data.error ?? "Failed to start backtest");
        return;
      }
      const id = data.backtest_id;
      setBacktestId(id);
      backtestIdRef.current = id;
      connectSSE(id);
    } catch (err) {
      setStatus("error");
      setError(err instanceof Error ? err.message : "Failed to start backtest");
    }
  }, [connectSSE]);

  const controlAction = useCallback(async (action: string, speed?: string) => {
    const id = backtestIdRef.current;
    if (!id) return;
    await fetch(`/api/backtest/${id}/control`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action, speed }),
    });
  }, []);

  const pause = useCallback(async () => {
    await controlAction("pause");
    setStatus("paused");
  }, [controlAction]);

  const resume = useCallback(async () => {
    await controlAction("resume");
    setStatus("running");
  }, [controlAction]);

  const setSpeed = useCallback(async (speed: string) => {
    await controlAction("set_speed", speed);
  }, [controlAction]);

  const cancel = useCallback(async () => {
    const id = backtestIdRef.current;
    if (id) {
      await fetch(`/api/backtest/${id}/status`, { method: "DELETE" });
    }
    cleanup();
    setStatus("cancelled");
  }, [cleanup]);

  return {
    status,
    backtestId,
    progress,
    setupStage,
    bars,
    signals,
    trades,
    metrics,
    equityCurve,
    result,
    error,
    run,
    pause,
    resume,
    setSpeed,
    cancel,
  };
}
