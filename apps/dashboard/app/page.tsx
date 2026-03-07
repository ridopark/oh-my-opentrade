"use client";

import React, { useState, useMemo, useEffect, useRef, useCallback } from "react";
import dynamic from "next/dynamic";
import { useSearchParams, useRouter } from "next/navigation";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ArrowUp, ArrowDown, Activity, Zap } from "lucide-react";
import { useChartData, type OHLCBar } from "@/lib/use-chart-data";
import { useStrategyList } from "@/hooks/queries";
import type { ChartSignal } from "@/components/trading-signal-chart";
import type { StrategySignalEvent } from "@/lib/types";

const TradingChart = dynamic(() => import("@/components/trading-signal-chart"), {
  ssr: false,
  loading: () => (
    <div className="flex items-center justify-center h-full text-slate-500">
      Loading Chart Engine...
    </div>
  ),
});

const TIMEFRAMES = ["1m", "5m", "15m", "1h", "1d"] as const;
type Timeframe = (typeof TIMEFRAMES)[number];

export default function TradingSignalPage() {
  const [timeframe, setTimeframe] = useState<Timeframe>("1m");
  const [containerDimensions, setContainerDimensions] = useState({ width: 0, height: 0 });
  const containerRef = useRef<HTMLDivElement>(null);
  const [indicators, setIndicators] = useState({ ma: true, bb: true, rsi: true });

  const [signals, setSignals] = useState<ChartSignal[]>([]);
  const [recentSignalEvents, setRecentSignalEvents] = useState<StrategySignalEvent[]>([]);

  const { data: strategies } = useStrategyList();
  const availableSymbols = useMemo(() => {
    if (!strategies || strategies.length === 0) return [];
    const set = new Set<string>();
    for (const s of strategies) {
      for (const sym of s.symbols) set.add(sym);
    }
    return Array.from(set).sort();
  }, [strategies]);

  const searchParams = useSearchParams();
  const router = useRouter();
  const paramSymbol = searchParams.get("symbol") ?? "";
  const [symbol, setSymbolState] = useState<string>(paramSymbol);

  const setSymbol = useCallback((s: string) => {
    setSymbolState(s);
    const params = new URLSearchParams(searchParams.toString());
    params.set("symbol", s);
    router.replace(`?${params.toString()}`, { scroll: false });
  }, [searchParams, router]);

  useEffect(() => {
    if (!symbol && availableSymbols.length > 0) {
      setSymbol(availableSymbols[0]);
    } else if (symbol && availableSymbols.length > 0 && !availableSymbols.includes(symbol)) {
      setSymbol(availableSymbols[0]);
    }
  }, [symbol, availableSymbols, setSymbol]);

  const chartSymbols = useMemo(() => symbol ? [symbol] : [], [symbol]);
  const { barsBySymbol, loading, loadMore } = useChartData(timeframe, "/api/events", chartSymbols.length > 0 ? chartSymbols : undefined);
  const bars: OHLCBar[] = barsBySymbol[symbol] ?? [];

  useEffect(() => {
    const es = new EventSource("/api/events");

    es.addEventListener("StrategySignalLifecycle", (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as { payload: StrategySignalEvent };
        const sig = envelope.payload;
        if (!sig?.Symbol || !sig?.TS) return;

        const side = sig.Side?.toLowerCase() === "sell" ? "sell" as const : "buy" as const;
        const time = Math.floor(new Date(sig.TS).getTime() / 1000);

        setSignals((prev) => {
          const next = [...prev, { time, side, strategy: sig.Strategy, confidence: sig.Confidence }];
          return next.slice(-200);
        });

        setRecentSignalEvents((prev) => [sig, ...prev].slice(0, 20));
      } catch {
        // noop
      }
    });

    es.onerror = () => {};

    return () => es.close();
  }, []);

  useEffect(() => {
    if (!containerRef.current) return;

    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setContainerDimensions({
          width: entry.contentRect.width,
          height: entry.contentRect.height,
        });
      }
    });

    ro.observe(containerRef.current);
    return () => ro.disconnect();
  }, []);

  const toggleIndicator = (key: keyof typeof indicators) => {
    setIndicators((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  const lastBar = bars.length > 0 ? bars[bars.length - 1] : null;
  const prevBar = bars.length > 1 ? bars[bars.length - 2] : null;
  const lastPrice = lastBar?.close ?? 0;
  const prevPrice = prevBar?.close ?? lastPrice;
  const priceChange = lastPrice - prevPrice;
  const priceChangePercent = prevPrice !== 0 ? (priceChange / prevPrice) * 100 : 0;
  const isPositive = priceChange >= 0;

  const symbolSignals = signals.filter((s) => {
    const earliest = bars.length > 0 ? bars[0].time : 0;
    return s.time >= earliest;
  });

  return (
    <div className="flex flex-col min-h-[calc(100vh-1.5rem)] md:h-[calc(100vh-2rem)] overflow-y-auto md:overflow-hidden gap-4 pt-12 md:pt-0">
      <header className="flex flex-wrap items-center justify-between p-4 bg-card border rounded-lg shadow-sm gap-3 md:gap-6">
        <div className="flex items-center gap-6 w-full md:w-auto justify-between md:justify-start">
          <div className="flex items-center gap-3">
            <select
              value={symbol}
              onChange={(e) => setSymbol(e.target.value)}
              className="text-xl md:text-2xl font-bold tracking-tight bg-card border border-border rounded-md px-3 py-1.5 pr-8 cursor-pointer text-foreground appearance-none bg-[length:16px_16px] bg-[position:right_8px_center] bg-no-repeat hover:border-emerald-500/50 focus:border-emerald-500 focus:ring-1 focus:ring-emerald-500/30 focus:outline-none transition-colors"
              style={{ backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='%2310b981' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E")` }}
            >
              {availableSymbols.map((sym) => (
                <option key={sym} value={sym}>
                  {sym}
                </option>
              ))}
            </select>
            {!loading && bars.length > 0 && (
              <span className="relative flex h-3 w-3">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-3 w-3 bg-emerald-500" />
              </span>
            )}
            {loading && (
              <span className="text-xs text-muted-foreground animate-pulse">Loading...</span>
            )}
          </div>

          <div className="flex flex-col">
            <span className="text-xl md:text-2xl font-mono font-medium text-foreground">
              ${lastPrice.toFixed(2)}
            </span>
            {prevBar && (
              <div
                className={`flex items-center text-sm font-medium ${isPositive ? "text-emerald-500" : "text-red-500"}`}
              >
                {isPositive ? (
                  <ArrowUp className="w-4 h-4 mr-1" />
                ) : (
                  <ArrowDown className="w-4 h-4 mr-1" />
                )}
                {Math.abs(priceChange).toFixed(2)} ({Math.abs(priceChangePercent).toFixed(2)}%)
              </div>
            )}
          </div>
        </div>

        <div className="flex items-center gap-2 bg-muted/30 p-1 rounded-md">
          {TIMEFRAMES.map((tf) => (
            <button
              key={tf}
              onClick={() => setTimeframe(tf)}
              className={`px-3 py-1 text-sm font-medium rounded-sm transition-all ${
                timeframe === tf
                  ? "bg-emerald-600 text-white shadow-sm"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground"
              }`}
            >
              {tf}
            </button>
          ))}
        </div>

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => toggleIndicator("ma")}
            className={
              indicators.ma
                ? "border-cyan-500/50 bg-cyan-500/10 text-cyan-500 hover:bg-cyan-500/20"
                : "text-muted-foreground"
            }
          >
            EMA
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => toggleIndicator("bb")}
            className={
              indicators.bb
                ? "border-indigo-500/50 bg-indigo-500/10 text-indigo-500 hover:bg-indigo-500/20"
                : "text-muted-foreground"
            }
          >
            BB
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => toggleIndicator("rsi")}
            className={
              indicators.rsi
                ? "border-violet-500/50 bg-violet-500/10 text-violet-500 hover:bg-violet-500/20"
                : "text-muted-foreground"
            }
          >
            RSI
          </Button>
        </div>
      </header>

      <div className="flex flex-col md:flex-row flex-1 gap-4 overflow-hidden">
        <div
          className="flex-1 bg-card border rounded-lg shadow-sm relative overflow-hidden min-h-[300px] md:min-h-[500px]"
          ref={containerRef}
        >
          {containerDimensions.width > 0 && containerDimensions.height > 0 && bars.length > 0 && (
             <TradingChart
              data={bars}
              signals={symbolSignals}
              width={containerDimensions.width}
              height={containerDimensions.height}
              symbol={symbol}
              timeframe={timeframe}
              showEMA={indicators.ma}
              showBollinger={indicators.bb}
              showRSI={indicators.rsi}
              onLoadMore={loadMore}
            />
          )}
          {!loading && bars.length === 0 && (
            <div className="absolute inset-0 flex items-center justify-center">
              <p className="text-sm text-muted-foreground">
                No bar data for {symbol} ({timeframe}). Waiting for market data...
              </p>
            </div>
          )}
        </div>

        <aside className="w-full md:w-80 flex-shrink-0 flex flex-col gap-4 overflow-y-auto pr-1">
          <Card>
            <CardHeader className="pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground flex items-center gap-2">
                <Activity className="w-4 h-4" />
                Market Info
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="grid grid-cols-2 gap-4">
                <div className="flex flex-col">
                  <span className="text-xs text-muted-foreground">Symbol</span>
                  <span className="text-lg font-bold">{symbol}</span>
                </div>
                <div className="flex flex-col">
                  <span className="text-xs text-muted-foreground">Timeframe</span>
                  <span className="text-lg font-bold">{timeframe}</span>
                </div>
                <div className="flex flex-col">
                  <span className="text-xs text-muted-foreground">Bars</span>
                  <span className="text-lg font-bold">{bars.length}</span>
                </div>
                <div className="flex flex-col">
                  <span className="text-xs text-muted-foreground">Signals</span>
                  <span className="text-lg font-bold">{symbolSignals.length}</span>
                </div>
                {lastBar && (
                  <>
                    <div className="flex flex-col">
                      <span className="text-xs text-muted-foreground">High</span>
                      <span className="text-lg font-bold text-emerald-500">
                        ${lastBar.high.toFixed(2)}
                      </span>
                    </div>
                    <div className="flex flex-col">
                      <span className="text-xs text-muted-foreground">Low</span>
                      <span className="text-lg font-bold text-red-500">
                        ${lastBar.low.toFixed(2)}
                      </span>
                    </div>
                  </>
                )}
              </div>
            </CardContent>
          </Card>

          <Card className="flex-1">
            <CardHeader className="pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground flex items-center gap-2">
                <Zap className="w-4 h-4" />
                Live Signals
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              {recentSignalEvents.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  No signals yet. Signals appear when strategies generate buy/sell decisions.
                </p>
              )}
              {recentSignalEvents.slice(0, 8).map((sig, idx) => (
                <div
                  key={`${sig.SignalID}-${idx}`}
                  className="flex items-center justify-between border-b border-border/50 last:border-0 pb-3 last:pb-0"
                >
                  <div className="flex items-center gap-3">
                    <Badge
                      variant={sig.Side?.toLowerCase() === "buy" ? "default" : "destructive"}
                      className={
                        sig.Side?.toLowerCase() === "buy"
                          ? "bg-emerald-500 hover:bg-emerald-600"
                          : ""
                      }
                    >
                      {sig.Side?.toUpperCase()}
                    </Badge>
                    <div className="flex flex-col">
                      <span className="font-bold text-sm">{sig.Symbol}</span>
                      <div className="flex items-center gap-1 text-xs text-muted-foreground">
                        <span>{sig.Strategy}</span>
                        <span>&middot;</span>
                        <span>{sig.Kind}</span>
                        <span>&middot;</span>
                        <span>{sig.Status}</span>
                      </div>
                    </div>
                  </div>
                  <div className="flex flex-col items-end">
                    <span className="font-mono text-xs text-muted-foreground">
                      {new Date(sig.TS).toLocaleTimeString()}
                    </span>
                    {sig.Confidence > 0 && (
                      <div className="flex items-center gap-1">
                        <div className="w-12 h-1.5 bg-secondary rounded-full overflow-hidden">
                          <div
                            className={`h-full ${sig.Side?.toLowerCase() === "buy" ? "bg-emerald-500" : "bg-red-500"}`}
                            style={{ width: `${sig.Confidence * 100}%` }}
                          />
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>
        </aside>
      </div>
    </div>
  );
}
