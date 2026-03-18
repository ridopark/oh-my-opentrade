"use client";

import { useCallback, useRef, useState } from "react";

export interface SweepRange {
  key: string;
  min: number;
  max: number;
  step: number;
}

export interface SweepStartConfig {
  ranges: SweepRange[];
  target_metric: string;
  symbols: string[];
  strategies: string[];
  from: string;
  to: string;
  timeframe: string;
  initial_equity: number;
  slippage_bps: number;
  no_ai: boolean;
  max_concurrency: number;
}

export interface SweepRunResult {
  index: number;
  params: Record<string, number>;
  metrics: {
    total_pnl: number;
    trade_count: number;
    win_rate_pct: number;
    max_drawdown_pct: number;
    sharpe_ratio: number;
    profit_factor: number;
    initial_equity: number;
    final_equity: number;
  };
  duration_ms: number;
}

export interface SweepProgress {
  completed: number;
  total: number;
  pct: number;
}

type SweepStatus = "idle" | "running" | "completed" | "error" | "cancelled";

export function useSweep(strategyId: string) {
  const [status, setStatus] = useState<SweepStatus>("idle");
  const [sweepId, setSweepId] = useState<string | null>(null);
  const [progress, setProgress] = useState<SweepProgress>({ completed: 0, total: 0, pct: 0 });
  const [runs, setRuns] = useState<SweepRunResult[]>([]);
  const [finalRuns, setFinalRuns] = useState<SweepRunResult[]>([]);
  const [error, setError] = useState<string | null>(null);
  const esRef = useRef<EventSource | null>(null);

  const start = useCallback(async (config: SweepStartConfig) => {
    setStatus("running");
    setRuns([]);
    setFinalRuns([]);
    setError(null);
    setProgress({ completed: 0, total: 0, pct: 0 });

    try {
      const res = await fetch(`/api/strategies/sweep/${strategyId}/start`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(config),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: "Start failed" }));
        throw new Error(err.error);
      }
      const data = await res.json();
      const id = data.sweep_id as string;
      setSweepId(id);
      setProgress(p => ({ ...p, total: data.total_runs }));

      const es = new EventSource(`/api/strategies/sweep/${strategyId}/events/${id}`);
      esRef.current = es;

      es.addEventListener("sweep:progress", (e) => {
        const d = JSON.parse(e.data);
        setProgress({ completed: d.completed, total: d.total, pct: d.pct });
      });

      es.addEventListener("sweep:run_complete", (e) => {
        const run = JSON.parse(e.data) as SweepRunResult;
        setRuns(prev => [...prev, run]);
      });

      es.addEventListener("sweep:done", (e) => {
        const result = JSON.parse(e.data);
        setFinalRuns(result.runs ?? []);
        setStatus("completed");
        es.close();
      });

      es.onerror = () => {
        if (status === "running") setError("Connection lost");
        es.close();
      };
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to start sweep");
      setStatus("error");
    }
  }, [strategyId, status]);

  const cancel = useCallback(async () => {
    if (!sweepId) return;
    esRef.current?.close();
    await fetch(`/api/strategies/sweep/${strategyId}/cancel/${sweepId}`, { method: "DELETE" });
    setStatus("cancelled");
  }, [strategyId, sweepId]);

  const apply = useCallback(async (runIndex: number) => {
    if (!sweepId) return;
    const res = await fetch(`/api/strategies/sweep/${strategyId}/apply/${sweepId}/${runIndex}`, { method: "POST" });
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: "Apply failed" }));
      throw new Error(err.error);
    }
  }, [strategyId, sweepId]);

  const reset = useCallback(() => {
    esRef.current?.close();
    setStatus("idle");
    setSweepId(null);
    setRuns([]);
    setFinalRuns([]);
    setProgress({ completed: 0, total: 0, pct: 0 });
    setError(null);
  }, []);

  return { status, sweepId, progress, runs, finalRuns, error, start, cancel, apply, reset };
}
