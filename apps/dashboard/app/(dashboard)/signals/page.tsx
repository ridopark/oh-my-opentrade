"use client";

import { useState, useEffect, useCallback } from "react";
import { useSignalProgress, useEventListener } from "@/lib/event-stream";
import { SignalProgressTable } from "@/components/signal-progress-table";
import { BottomPanel, type BarLogEntry, type BottomTab } from "@/components/bottom-panel";
import type { DomainEvent, StrategySignalEvent, StrategySignalsResponse, RegimeType } from "@/lib/types";

export default function SignalMonitorPage() {
  const { avwapProgress, macdProgress, connected } = useSignalProgress();

  const [bottomTab, setBottomTab] = useState<BottomTab>("signals");
  const [recentSignalEvents, setRecentSignalEvents] = useState<StrategySignalEvent[]>([]);
  const [regimeBySymbol, setRegimeBySymbol] = useState<Record<string, { regime: RegimeType; strength: number; rsi: number }>>({});
  const [barLog, setBarLog] = useState<BarLogEntry[]>([]);

  // Blocked signals only persist to DB (no StrategySignalLifecycle SSE push),
  // so we re-poll /api/signals/recent to surface each bar-close batch.
  useEffect(() => {
    const loadRecent = () => {
      const today = new Date();
      today.setHours(0, 0, 0, 0);
      const from = today.toISOString();
      fetch(`/api/signals/recent?from=${from}&limit=200`)
        .then((r) => r.json())
        .then((data: StrategySignalsResponse) => {
          if (data.items?.length) {
            setRecentSignalEvents(data.items);
          }
        })
        .catch(() => {});
    };

    loadRecent();

    const interval = setInterval(() => {
      if (document.visibilityState === "visible") loadRecent();
    }, 30_000);

    const onVis = () => {
      if (document.visibilityState === "visible") loadRecent();
    };
    document.addEventListener("visibilitychange", onVis);

    return () => {
      clearInterval(interval);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, []);

  const handleSignalLifecycle = useCallback((evt: DomainEvent) => {
    const sig = evt.payload as StrategySignalEvent;
    if (!sig?.Symbol || !sig?.TS) return;
    setRecentSignalEvents((prev) => {
      if (prev.some((s) => s.SignalID === sig.SignalID && s.Status === sig.Status)) return prev;
      return [sig, ...prev].slice(0, 200);
    });
  }, []);

  const handleStateUpdated = useCallback((evt: DomainEvent) => {
    const snap = evt.payload as {
      Symbol: string;
      Timeframe: string;
      RSI: number;
      anchorRegimes?: Record<string, { Type: RegimeType; Strength: number }>;
    };
    if (!snap?.Symbol) return;
    const currentRegime = snap.anchorRegimes?.[snap.Timeframe];
    if (!currentRegime) return;
    setRegimeBySymbol((prev) => ({
      ...prev,
      [snap.Symbol]: { regime: currentRegime.Type, strength: currentRegime.Strength, rsi: snap.RSI },
    }));
  }, []);

  const handleBarLog = useCallback((evt: DomainEvent) => {
    const bar = evt.payload as { symbol: string; timeframe: string; time: string; open: number; high: number; low: number; close: number; volume: number };
    if (!bar?.symbol || !bar?.time) return;
    const eventType = evt.type === "FormingBar" ? "forming" as const : "bar" as const;
    setBarLog((prev) => [{
      receivedAt: Date.now(),
      eventType,
      symbol: bar.symbol,
      timeframe: bar.timeframe,
      time: bar.time,
      open: bar.open,
      high: bar.high,
      low: bar.low,
      close: bar.close,
      volume: bar.volume,
    }, ...prev].slice(0, 200));
  }, []);

  useEventListener("StrategySignalLifecycle", handleSignalLifecycle);
  useEventListener("StateUpdated", handleStateUpdated);
  useEventListener("MarketBarSanitized", handleBarLog);
  useEventListener("FormingBar", handleBarLog);

  return (
    <div className="flex flex-col gap-4 h-full">
      <div className="flex items-center justify-between shrink-0">
        <h1 className="text-2xl font-bold tracking-tight">Signals</h1>
        <div className="flex items-center gap-2">
          <span className={`h-2 w-2 rounded-full ${connected ? "bg-emerald-500" : "bg-red-500"}`} />
          <span className="text-xs text-zinc-500">
            {connected ? "Live" : "Disconnected"}
          </span>
        </div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        <SignalProgressTable avwapProgress={avwapProgress} macdProgress={macdProgress} />
      </div>
      <BottomPanel
        bottomTab={bottomTab}
        setBottomTab={setBottomTab}
        recentSignalEvents={recentSignalEvents}
        regimeBySymbol={regimeBySymbol}
        onSymbolClick={() => {}}
        barLog={barLog}
        avwapProgress={avwapProgress}
        macdProgress={macdProgress}
      />
    </div>
  );
}
