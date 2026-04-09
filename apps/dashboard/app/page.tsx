"use client";

import { useState, useEffect } from "react";
import { useSignalProgress } from "@/lib/event-stream";
import { SignalProgressTable } from "@/components/signal-progress-table";
import { BottomPanel, type BarLogEntry, type BottomTab } from "@/components/bottom-panel";
import type { StrategySignalEvent, RegimeType } from "@/lib/types";

export default function SignalMonitorPage() {
  const { avwapProgress, macdProgress, orbProgress, connected } = useSignalProgress();

  const [bottomTab, setBottomTab] = useState<BottomTab>("signals");
  const [recentSignalEvents, setRecentSignalEvents] = useState<StrategySignalEvent[]>([]);
  const [regimeBySymbol, setRegimeBySymbol] = useState<Record<string, { regime: RegimeType; strength: number; rsi: number }>>({});
  const [barLog, setBarLog] = useState<BarLogEntry[]>([]);

  // SSE for live signals, regime, and bar log
  useEffect(() => {
    const es = new EventSource("/api/events");

    es.addEventListener("StrategySignalLifecycle", (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as { payload: StrategySignalEvent };
        const sig = envelope.payload;
        if (!sig?.Symbol || !sig?.TS) return;
        setRecentSignalEvents((prev) => [sig, ...prev].slice(0, 50));
      } catch { /* noop */ }
    });

    es.addEventListener("StateUpdated", (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as {
          payload: {
            Symbol: string;
            Timeframe: string;
            RSI: number;
            anchorRegimes?: Record<string, { Type: RegimeType; Strength: number }>;
          };
        };
        const snap = envelope.payload;
        if (!snap?.Symbol) return;
        const currentRegime = snap.anchorRegimes?.[snap.Timeframe];
        if (!currentRegime) return;
        setRegimeBySymbol((prev) => ({
          ...prev,
          [snap.Symbol]: { regime: currentRegime.Type, strength: currentRegime.Strength, rsi: snap.RSI },
        }));
      } catch { /* noop */ }
    });

    const handleBarLog = (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as { type?: string; payload: { symbol: string; timeframe: string; time: string; open: number; high: number; low: number; close: number; volume: number } };
        const bar = envelope.payload;
        if (!bar?.symbol || !bar?.time) return;
        const eventType = envelope.type === "FormingBar" ? "forming" as const : "bar" as const;
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
      } catch { /* noop */ }
    };

    es.addEventListener("MarketBarSanitized", handleBarLog);
    es.addEventListener("FormingBar", handleBarLog);

    return () => es.close();
  }, []);

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
        <SignalProgressTable avwapProgress={avwapProgress} macdProgress={macdProgress} orbProgress={orbProgress} />
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
        orbProgress={orbProgress}
      />
    </div>
  );
}
