"use client";

import React, { useState, useRef, useEffect, useMemo, useCallback, useImperativeHandle, forwardRef } from "react";
import {
  createChart,
  ColorType,
  CandlestickSeries,
  HistogramSeries,
  LineSeries,
  CrosshairMode,
  type IChartApi,
  type ISeriesApi,
  type Time,
} from "lightweight-charts";
import { SignalMarkerOverlay, type SignalMarkerData } from "@/lib/signal-markers";
import { ORBBoxOverlay, computeORBRanges } from "@/lib/orb-box-overlay";
import {
  useBacktest,
  type BacktestConfig,
  type BacktestBar,
  type BacktestTrade,
  type BacktestMetrics,
  type BacktestProgress,
  type BacktestResult,
} from "@/lib/use-backtest";
import { Button } from "@/components/ui/button";

/** Extract a human-readable exit reason from the rationale string.
 *  e.g. "exit_monitor:VOLATILITY_STOP:..." → "VOL_STOP" */
function parseExitReason(rationale?: string): string | null {
  if (!rationale) return null;
  const m = rationale.match(/exit_monitor:([^:]+)/);
  if (m) return m[1].replace(/_/g, " ");
  if (rationale.includes("avwap_exit")) return "AVWAP EXIT";
  if (rationale.includes("passthrough") && rationale.includes("exit")) return "TREND REVERSAL";
  return null;
}

/** Format a UTC unix timestamp as ET string using Intl. */
function formatET(utcSeconds: number, opts: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat("en-US", { timeZone: "America/New_York", hour12: false, ...opts }).format(new Date(utcSeconds * 1000));
}

const SPEED_OPTIONS = ["1x", "2x", "5x", "10x", "max"] as const;
const TIMEFRAMES = ["1m", "5m", "15m", "1h"] as const;

function formatCurrency(v: number) {
  return v.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

function formatPct(v: number) {
  return `${v >= 0 ? "+" : ""}${v.toFixed(2)}%`;
}

export default function BacktestPage() {
  const bt = useBacktest();
  const chartGridRef = useRef<ChartGridHandle>(null);
  const tradeLogRef = useRef<TradeLogHandle>(null);
  const [availableSymbols, setAvailableSymbols] = useState<string[]>([]);
  const [availableStrategies, setAvailableStrategies] = useState<{ id: string; name: string; state: string }[]>([]);
  const [orbWindowMinutes, setOrbWindowMinutes] = useState(30);

  const handleScrollToTime = useCallback((symbol: string, isoTime: string) => {
    const utcSeconds = Math.floor(new Date(isoTime).getTime() / 1000);
    chartGridRef.current?.scrollToTime(symbol, utcSeconds);
  }, []);

  useEffect(() => {
    fetch("/api/backtest/symbols")
      .then((r) => r.json())
      .then((data) => { if (Array.isArray(data)) setAvailableSymbols(data); })
      .catch(() => {});
    fetch("/api/backtest/strategies")
      .then((r) => r.json())
      .then((data) => { if (Array.isArray(data)) setAvailableStrategies(data); })
      .catch(() => {});
    fetch("/api/strategies/config/orb_break_retest/config")
      .then((r) => r.ok ? r.json() : null)
      .then((data) => { if (data?.params?.orb_window_minutes) setOrbWindowMinutes(data.params.orb_window_minutes); })
      .catch(() => {});
  }, []);

  const defaults: BacktestConfig = useMemo(() => ({
    symbols: ["SPY", "AAPL"],
    from: new Date(Date.now() - 7 * 86400000).toISOString().split("T")[0],
    to: new Date().toISOString().split("T")[0],
    timeframe: "1m",
    initialEquity: 100000,
    slippageBps: 5,
    speed: "5x",
    noAi: true,
    strategies: [],
  }), []);

  const [config, setConfig] = useState<BacktestConfig>(defaults);
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    try {
      const saved = localStorage.getItem("backtest-config");
      if (saved) setConfig((prev) => ({ ...prev, ...JSON.parse(saved) }));
    } catch {}
    setHydrated(true);
  }, []);

  // Filter out stale strategies/symbols that no longer exist on the server.
  useEffect(() => {
    if (!hydrated || availableStrategies.length === 0) return;
    const validIds = new Set(availableStrategies.map((s) => s.id));
    setConfig((prev) => {
      const filtered = (prev.strategies || []).filter((s) => validIds.has(s));
      if (filtered.length === prev.strategies?.length) return prev;
      return { ...prev, strategies: filtered };
    });
  }, [hydrated, availableStrategies]);

  useEffect(() => {
    if (hydrated) {
      try { localStorage.setItem("backtest-config", JSON.stringify(config)); } catch {}
    }
  }, [config, hydrated]);

  const symbolsInData = useMemo(() => Array.from(bt.bars.keys()).sort(), [bt.bars]);

  const handleRun = async () => {
    await bt.run(config);
  };

  const updateConfig = <K extends keyof BacktestConfig>(key: K, value: BacktestConfig[K]) => {
    setConfig((prev) => ({ ...prev, [key]: value }));
  };

  const isRunning = bt.status === "running" || bt.status === "paused";

  const [bottomTab, setBottomTab] = useState<"trades" | "results" | "equity">("trades");

  return (
    <div className="flex flex-col min-h-[calc(100vh-3rem)]">
      <TopBar
        config={config}
        updateConfig={updateConfig}
        onRun={handleRun}
        isRunning={isRunning}
        status={bt.status}
        progress={bt.progress}
        setupStage={bt.setupStage}
        availableSymbols={availableSymbols}
        availableStrategies={availableStrategies}
        onPause={bt.pause}
        onResume={bt.resume}
        onSetSpeed={async (s) => { updateConfig("speed", s); await bt.setSpeed(s); }}
        onCancel={bt.cancel}
      />

      <div className={`mt-2 ${symbolsInData.length === 0 ? "flex-1" : ""}`} style={symbolsInData.length > 0 ? { minHeight: symbolsInData.length <= 2 ? 300 : `${Math.ceil(symbolsInData.length / (symbolsInData.length <= 4 ? 2 : symbolsInData.length <= 6 ? 3 : 4)) * 280}px` } : undefined}>
        <ChartGrid ref={chartGridRef} symbols={symbolsInData} bars={bt.bars} trades={bt.trades} orbWindowMinutes={orbWindowMinutes} onTradeClick={(trade) => {
          setBottomTab("trades");
          setTimeout(() => tradeLogRef.current?.scrollToTrade(trade), 50);
        }} />
      </div>

      <div className="min-h-[250px] mt-1 rounded-t-lg border border-border bg-card flex flex-col">
        <div className="flex items-center gap-0 border-b border-border shrink-0">
          {(["trades", "results", "equity"] as const).map((tab) => (
            <button
              key={tab}
              onClick={() => setBottomTab(tab)}
              className={`px-4 py-2 text-xs font-mono transition-colors relative ${
                bottomTab === tab
                  ? "text-foreground"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {tab === "trades" ? `Positions (${Math.floor(bt.trades.length / 2)})` : tab === "results" ? "Results" : "Equity Curve"}
              {bottomTab === tab && (
                <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-emerald-500" />
              )}
            </button>
          ))}
          <div className="ml-auto pr-3 flex items-center gap-3">
            {bt.metrics && (
              <div className="flex items-center gap-4 text-[10px] font-mono">
                <span className="text-muted-foreground">P&L <span className={`${(bt.result?.total_pnl ?? bt.metrics?.total_pnl ?? 0) >= 0 ? "text-emerald-400" : "text-red-400"}`}>{formatCurrency(bt.result?.total_pnl ?? bt.metrics?.total_pnl ?? 0)}</span></span>
                <span className="text-muted-foreground">Trades <span className="text-foreground">{bt.result?.trade_count ?? bt.metrics?.trades ?? 0}</span></span>
                <span className="text-muted-foreground">Win <span className="text-foreground">{(bt.result?.win_rate_pct ?? bt.metrics?.win_rate ?? 0).toFixed(1)}%</span></span>
                <span className="text-muted-foreground">Sharpe <span className="text-foreground">{(bt.result?.sharpe_ratio ?? bt.metrics?.sharpe ?? 0).toFixed(3)}</span></span>
              </div>
            )}
          </div>
        </div>

        <div className="flex-1 min-h-0 overflow-hidden">
          {bottomTab === "trades" && <TradeLogInline ref={tradeLogRef} trades={bt.trades} onScrollToTime={handleScrollToTime} />}
          {bottomTab === "results" && <MetricsPanelInline metrics={bt.metrics} result={bt.result} initialEquity={config.initialEquity} />}
          {bottomTab === "equity" && <EquityCurveInline data={bt.equityCurve} />}
        </div>
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, string> = {
    running: "bg-emerald-500/20 text-emerald-400",
    paused: "bg-amber-500/20 text-amber-400",
    completed: "bg-blue-500/20 text-blue-400",
    error: "bg-red-500/20 text-red-400",
    cancelled: "bg-slate-500/20 text-slate-400",
  };
  return (
    <span className={`px-2.5 py-0.5 text-xs font-mono rounded-full ${colors[status] ?? "bg-slate-500/20 text-slate-400"}`}>
      {status}
    </span>
  );
}

function TopBar({
  config, updateConfig, onRun, isRunning, status, progress, setupStage, availableSymbols, availableStrategies, onPause, onResume, onSetSpeed, onCancel,
}: {
  config: BacktestConfig;
  updateConfig: <K extends keyof BacktestConfig>(key: K, val: BacktestConfig[K]) => void;
  onRun: () => void;
  isRunning: boolean;
  status: string;
  progress: BacktestProgress | null;
  setupStage: string | null;
  availableSymbols: string[];
  availableStrategies: { id: string; name: string; state: string }[];
  onPause: () => void;
  onResume: () => void;
  onSetSpeed: (s: string) => void;
  onCancel: () => void;
}) {
  const [symbolsOpen, setSymbolsOpen] = useState(false);
  const [strategiesOpen, setStrategiesOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const stratDropdownRef = useRef<HTMLDivElement>(null);
  const pct = progress?.pct ?? 0;
  const inputCls = "bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500";
  const pillCls = (active: boolean) => `px-2 py-1 text-[10px] font-mono rounded transition-colors ${active ? "bg-white/10 text-foreground" : "text-muted-foreground hover:bg-white/5"}`;

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) setSymbolsOpen(false);
      if (stratDropdownRef.current && !stratDropdownRef.current.contains(e.target as Node)) setStrategiesOpen(false);
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const toggleSymbol = (sym: string) => {
    const current = config.symbols;
    const next = current.includes(sym) ? current.filter((s) => s !== sym) : [...current, sym].sort();
    updateConfig("symbols", next);
  };

  const toggleStrategy = (id: string) => {
    const current = config.strategies;
    const next = current.includes(id) ? current.filter((s) => s !== id) : [...current, id].sort();
    updateConfig("strategies", next);
  };

  return (
    <div className="rounded-lg border border-border bg-card px-4 py-2.5 flex items-center gap-4 flex-wrap">
      <h1 className="text-sm font-semibold text-foreground shrink-0">Backtest</h1>

      <div className="flex items-center gap-1.5 relative" ref={dropdownRef}>
        <span className="text-[10px] text-muted-foreground uppercase">Symbols</span>
        <button
          onClick={() => setSymbolsOpen(!symbolsOpen)}
          className={`${inputCls} w-48 text-left flex items-center justify-between`}
        >
          <span className="truncate">
            {config.symbols.length === 0 ? "Select..." : [...config.symbols].sort().join(", ")}
          </span>
          <span className="text-muted-foreground ml-1">{symbolsOpen ? "\u25B2" : "\u25BC"}</span>
        </button>
        {symbolsOpen && (
          <div className="absolute top-full left-0 mt-1 z-50 w-56 max-h-64 overflow-y-auto rounded-lg border border-border bg-card shadow-xl">
            {availableSymbols.map((sym) => {
              const selected = config.symbols.includes(sym);
              return (
                <button
                  key={sym}
                  onClick={() => toggleSymbol(sym)}
                  className={`w-full px-3 py-1.5 text-xs font-mono text-left flex items-center gap-2 hover:bg-white/5 transition-colors ${selected ? "text-emerald-400" : "text-muted-foreground"}`}
                >
                  <span className={`w-3.5 h-3.5 rounded border flex items-center justify-center text-[9px] ${selected ? "border-emerald-500 bg-emerald-500/20" : "border-border"}`}>
                    {selected && "\u2713"}
                  </span>
                  {sym}
                </button>
              );
            })}
          </div>
        )}
      </div>

      <div className="flex items-center gap-1.5 relative" ref={stratDropdownRef}>
        <span className="text-[10px] text-muted-foreground uppercase">Strategy</span>
        <button
          onClick={() => setStrategiesOpen(!strategiesOpen)}
          className={`${inputCls} w-40 text-left flex items-center justify-between`}
        >
          <span className="truncate">
            {config.strategies.length === 0 ? "All" : config.strategies.join(", ")}
          </span>
          <span className="text-muted-foreground ml-1">{strategiesOpen ? "\u25B2" : "\u25BC"}</span>
        </button>
        {strategiesOpen && (
          <div className="absolute top-full left-0 mt-1 z-50 w-64 max-h-64 overflow-y-auto rounded-lg border border-border bg-card shadow-xl">
            {availableStrategies.map((strat) => {
              const selected = config.strategies.includes(strat.id);
              return (
                <button
                  key={strat.id}
                  onClick={() => toggleStrategy(strat.id)}
                  className={`w-full px-3 py-1.5 text-xs text-left flex items-center gap-2 hover:bg-white/5 transition-colors ${selected ? "text-emerald-400" : "text-muted-foreground"}`}
                >
                  <span className={`w-3.5 h-3.5 rounded border flex items-center justify-center text-[9px] shrink-0 ${selected ? "border-emerald-500 bg-emerald-500/20" : "border-border"}`}>
                    {selected && "\u2713"}
                  </span>
                  <span className="font-mono">{strat.id}</span>
                  <span className="text-[10px] text-muted-foreground/50 truncate">{strat.name}</span>
                </button>
              );
            })}
          </div>
        )}
      </div>

      <div className="flex items-center gap-1.5">
        <span className="text-[10px] text-muted-foreground uppercase">From</span>
        <input type="date" value={config.from} onChange={(e) => updateConfig("from", e.target.value)} className={`${inputCls} w-28`} />
        <span className="text-[10px] text-muted-foreground uppercase">To</span>
        <input type="date" value={config.to} onChange={(e) => updateConfig("to", e.target.value)} className={`${inputCls} w-28`} />
      </div>

      <div className="flex items-center gap-0.5">
        {TIMEFRAMES.map((tf) => (
          <button key={tf} onClick={() => updateConfig("timeframe", tf)} className={pillCls(config.timeframe === tf)}>{tf}</button>
        ))}
      </div>

      <div className="flex items-center gap-0.5">
        {SPEED_OPTIONS.map((s) => (
          <button key={s} onClick={() => { updateConfig("speed", s); if (isRunning) onSetSpeed(s); }} className={pillCls(config.speed === s)}>{s}</button>
        ))}
      </div>

      <div className="flex items-center gap-1.5">
        <span className="text-[10px] text-muted-foreground uppercase">Eq</span>
        <input type="number" value={config.initialEquity} onChange={(e) => updateConfig("initialEquity", Number(e.target.value))} className={`${inputCls} w-20`} />
        <span className="text-[10px] text-muted-foreground uppercase">Slip</span>
        <input type="number" value={config.slippageBps} onChange={(e) => updateConfig("slippageBps", Number(e.target.value))} className={`${inputCls} w-12`} />
      </div>

      <label className="flex items-center gap-1 text-[10px] text-muted-foreground cursor-pointer shrink-0">
        <input type="checkbox" checked={config.noAi} onChange={(e) => updateConfig("noAi", e.target.checked)} className="rounded border-border h-3 w-3" />
        No AI
      </label>

      <div className="flex items-center gap-2 ml-auto shrink-0">
        {isRunning && !progress && (
          <>
            <div className="h-4 w-4 animate-spin rounded-full border-b-2 border-emerald-500" />
            <span className="text-[10px] font-mono text-muted-foreground">{setupStage ?? "Starting…"}</span>
            <button onClick={onCancel} className="px-1.5 py-0.5 text-[10px] font-mono rounded text-red-400 hover:bg-red-500/10 transition-colors">✕</button>
          </>
        )}

        {isRunning && progress && (
          <>
            <button onClick={status === "paused" ? onResume : onPause}
              className="px-2 py-1 text-xs font-mono rounded bg-white/10 text-foreground hover:bg-white/15 transition-colors">
              {status === "paused" ? "▶" : "⏸"}
            </button>
            <div className="w-20">
              <div className="h-1 rounded-full bg-white/5 overflow-hidden">
                <div className="h-full rounded-full bg-emerald-500 transition-all duration-300" style={{ width: `${pct}%` }} />
              </div>
            </div>
            <span className="text-[10px] font-mono text-muted-foreground w-8 text-right">{pct.toFixed(0)}%</span>
            <button onClick={onCancel} className="px-1.5 py-0.5 text-[10px] font-mono rounded text-red-400 hover:bg-red-500/10 transition-colors">✕</button>
          </>
        )}

        {!isRunning && (
          <Button onClick={onRun} disabled={config.symbols.length === 0} size="sm" className="h-7 text-xs px-4">
            {status === "completed" ? "Run Again" : "Run"}
          </Button>
        )}

        {status !== "idle" && <StatusBadge status={status} />}
      </div>
    </div>
  );
}

export interface ChartGridHandle {
  scrollToTime: (symbol: string, utcSeconds: number) => void;
}

/** Find which position index a given trade belongs to */
function findPositionForTrade(positions: Position[], trade: BacktestTrade): number {
  const tradeTime = trade.filled_at ?? "";
  for (let i = 0; i < positions.length; i++) {
    const p = positions[i];
    if (p.entryTime === tradeTime || p.exitTime === tradeTime) return i;
  }
  return -1;
}

const ChartGrid = forwardRef<ChartGridHandle, {
  symbols: string[];
  bars: Map<string, BacktestBar[]>;
  trades: BacktestTrade[];
  orbWindowMinutes?: number;
  onTradeClick?: (trade: BacktestTrade) => void;
}>(function ChartGrid({
  symbols,
  bars,
  trades,
  orbWindowMinutes = 30,
  onTradeClick,
}, ref) {
  const [expandedSymbol, setExpandedSymbol] = useState<string | null>(null);
  const chartRefs = useRef<Map<string, IChartApi>>(new Map());
  const pendingScroll = useRef<{ symbol: string; utcSeconds: number } | null>(null);

  const applyScroll = useCallback((chart: IChartApi, utcSeconds: number) => {
    const ts = chart.timeScale();
    const halfWindow = 30 * 60;
    ts.setVisibleRange({
      from: (utcSeconds - halfWindow) as Time,
      to: (utcSeconds + halfWindow) as Time,
    });
  }, []);

  const registerChart = useCallback((symbol: string, chart: IChartApi | null) => {
    if (chart) {
      chartRefs.current.set(symbol, chart);
      // Apply pending scroll if this is the chart we were waiting for
      if (pendingScroll.current && pendingScroll.current.symbol === symbol) {
        const { utcSeconds } = pendingScroll.current;
        pendingScroll.current = null;
        // Small delay to let chart render data first
        setTimeout(() => applyScroll(chart, utcSeconds), 100);
      }
    } else {
      chartRefs.current.delete(symbol);
    }
  }, [applyScroll]);

  useImperativeHandle(ref, () => ({
    scrollToTime(symbol: string, utcSeconds: number) {
      // If already expanded with this chart, scroll directly
      const chart = chartRefs.current.get(symbol);
      if (chart && expandedSymbol === symbol) {
        applyScroll(chart, utcSeconds);
        return;
      }
      // Store pending scroll and expand — the new chart will pick it up via registerChart
      pendingScroll.current = { symbol, utcSeconds };
      setExpandedSymbol(symbol);
    },
  }), [expandedSymbol, applyScroll]);

  if (symbols.length === 0) {
    return (
      <div className="flex items-center justify-center h-full rounded-lg border border-border bg-card text-muted-foreground text-sm">
        Run a backtest to see charts
      </div>
    );
  }

  // Expanded: single chart fills the entire grid
  if (expandedSymbol && symbols.includes(expandedSymbol)) {
    return (
      <div className="h-full flex flex-col gap-1">
        <div className="flex items-center gap-2 px-1">
          <button
            onClick={() => setExpandedSymbol(null)}
            className="text-xs text-muted-foreground hover:text-foreground flex items-center gap-1"
          >
            ← Grid
          </button>
          <span className="text-xs font-bold text-foreground">{expandedSymbol}</span>
        </div>
        <div className="flex-1 min-h-0">
          <MiniChart
            symbol={expandedSymbol}
            bars={bars.get(expandedSymbol) ?? []}
            trades={trades.filter((t) => t.symbol === expandedSymbol)}
            orbWindowMinutes={orbWindowMinutes}
            onChartReady={(chart) => registerChart(expandedSymbol, chart)}
            onMarkerClick={(idx) => {
              const symTrades = trades.filter((t) => t.symbol === expandedSymbol);
              if (symTrades[idx]) onTradeClick?.(symTrades[idx]);
            }}
          />
        </div>
      </div>
    );
  }

  // Grid view
  const cols = symbols.length <= 2 ? symbols.length : symbols.length <= 4 ? 2 : symbols.length <= 6 ? 3 : 4;

  return (
    <div
      className="grid gap-2"
      style={{
        gridTemplateColumns: `repeat(${cols}, 1fr)`,
        gridAutoRows: "260px",
      }}
    >
      {symbols.map((sym) => (
        <div key={sym} className="relative group">
          <div className="h-full min-h-0">
            <MiniChart
              symbol={sym}
              bars={bars.get(sym) ?? []}
              trades={trades.filter((t) => t.symbol === sym)}
              orbWindowMinutes={orbWindowMinutes}
              onChartReady={(chart) => registerChart(sym, chart)}
              onMarkerClick={(idx) => {
                const symTrades = trades.filter((t) => t.symbol === sym);
                if (symTrades[idx]) onTradeClick?.(symTrades[idx]);
              }}
            />
          </div>
          <button
            onClick={() => setExpandedSymbol(sym)}
            className="absolute top-1 right-1 z-10 opacity-0 group-hover:opacity-100 transition-opacity p-1 rounded bg-background/80 backdrop-blur-sm text-muted-foreground hover:text-foreground"
            title="Expand"
          >
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <polyline points="15 3 21 3 21 9" /><polyline points="9 21 3 21 3 15" />
              <line x1="21" y1="3" x2="14" y2="10" /><line x1="3" y1="21" x2="10" y2="14" />
            </svg>
          </button>
        </div>
      ))}
    </div>
  );
});

function MiniChart({
  symbol,
  bars,
  trades,
  orbWindowMinutes = 30,
  onChartReady,
  onMarkerClick,
}: {
  symbol: string;
  bars: BacktestBar[];
  trades: BacktestTrade[];
  orbWindowMinutes?: number;
  onChartReady?: (chart: IChartApi | null) => void;
  onMarkerClick?: (tradeIndex: number) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const candleRef = useRef<ISeriesApi<"Candlestick", Time> | null>(null);
  const volumeRef = useRef<ISeriesApi<"Histogram", Time> | null>(null);
  const ema9Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema21Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema50Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema200Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const overlayRef = useRef<SignalMarkerOverlay | null>(null);
  const orbOverlayRef = useRef<ORBBoxOverlay | null>(null);
  const lastBarCountRef = useRef(0);

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = createChart(containerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "rgba(148, 163, 184, 0.8)",
        fontFamily: "var(--font-geist-mono, monospace)",
        fontSize: 9,
      },
      grid: {
        vertLines: { color: "rgba(148, 163, 184, 0.05)" },
        horzLines: { color: "rgba(148, 163, 184, 0.05)" },
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: "rgba(148, 163, 184, 0.2)", width: 1 as const, style: 3 as const, labelBackgroundColor: "#1f2937" },
        horzLine: { color: "rgba(148, 163, 184, 0.2)", width: 1 as const, style: 3 as const, labelBackgroundColor: "#1f2937" },
      },
      rightPriceScale: { borderColor: "rgba(148, 163, 184, 0.1)", scaleMargins: { top: 0.05, bottom: 0.15 } },
      localization: {
        timeFormatter: (time: number) => {
          return formatET(time, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
        },
      },
      timeScale: {
        borderColor: "rgba(148, 163, 184, 0.1)", timeVisible: true, rightOffset: 5, fixLeftEdge: true, fixRightEdge: true,
        tickMarkFormatter: (time: number) => {
          return formatET(time, { hour: "2-digit", minute: "2-digit" });
        },
      },
    });
    chartRef.current = chart;
    onChartReady?.(chart);

    const volume = chart.addSeries(HistogramSeries, {
      priceScaleId: "", priceFormat: { type: "volume" }, lastValueVisible: false, priceLineVisible: false,
    });
    chart.priceScale("").applyOptions({ scaleMargins: { top: 0.85, bottom: 0 }, visible: false });
    volumeRef.current = volume;

    const candle = chart.addSeries(CandlestickSeries, {
      upColor: "#10b981", downColor: "#ef4444", borderVisible: false,
      wickUpColor: "#10b981", wickDownColor: "#ef4444",
    });
    candleRef.current = candle;

    const ema9 = chart.addSeries(LineSeries, {
      color: "rgba(251, 191, 36, 0.7)", lineWidth: 1, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema9Ref.current = ema9;

    const ema21 = chart.addSeries(LineSeries, {
      color: "rgba(139, 92, 246, 0.7)", lineWidth: 1, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema21Ref.current = ema21;

    const ema50 = chart.addSeries(LineSeries, {
      color: "rgba(236, 72, 153, 0.6)", lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema50Ref.current = ema50;

    const ema200 = chart.addSeries(LineSeries, {
      color: "rgba(249, 115, 22, 0.5)", lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema200Ref.current = ema200;

    const overlay = new SignalMarkerOverlay();
    candle.attachPrimitive(overlay);
    overlayRef.current = overlay;

    const orbOverlay = new ORBBoxOverlay();
    candle.attachPrimitive(orbOverlay);
    orbOverlayRef.current = orbOverlay;

    // Click and hover handlers for signal markers
    const containerEl = containerRef.current;
    const handleChartClick = (e: MouseEvent) => {
      if (!overlayRef.current || !onMarkerClick) return;
      const rect = containerEl?.getBoundingClientRect();
      if (!rect) return;
      const x = e.clientX - rect.left;
      const y = e.clientY - rect.top;
      const idx = overlayRef.current.hitTest(x, y);
      if (idx >= 0) onMarkerClick(idx);
    };
    const handleChartMouseMove = (e: MouseEvent) => {
      if (!overlayRef.current) return;
      const rect = containerEl?.getBoundingClientRect();
      if (!rect) return;
      const x = e.clientX - rect.left;
      const y = e.clientY - rect.top;
      const hit = overlayRef.current.hitTest(x, y) >= 0;
      containerEl!.style.cursor = hit ? "pointer" : "";
    };
    containerEl.addEventListener("click", handleChartClick);
    containerEl.addEventListener("mousemove", handleChartMouseMove);

    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        chart.applyOptions({ width: entry.contentRect.width, height: entry.contentRect.height });
      }
    });
    observer.observe(containerEl);

    return () => {
      containerEl.removeEventListener("click", handleChartClick);
      containerEl.removeEventListener("mousemove", handleChartMouseMove);
      observer.disconnect();
      onChartReady?.(null);
      chart.remove();
      chartRef.current = null;
      candleRef.current = null;
      volumeRef.current = null;
      ema9Ref.current = null;
      ema21Ref.current = null;
      ema50Ref.current = null;
      ema200Ref.current = null;
      overlayRef.current = null;
      orbOverlayRef.current = null;
      lastBarCountRef.current = 0;
    };
  }, []);

  useEffect(() => {
    if (!candleRef.current || !volumeRef.current || bars.length === 0) return;

    if (bars.length === lastBarCountRef.current) return;
    const deduped = new Map<number, BacktestBar>();
    for (const b of bars) deduped.set(b.time, b);
    const sorted = Array.from(deduped.values()).sort((a, b) => a.time - b.time);

    candleRef.current.setData(sorted.map((b) => ({ time: b.time as Time, open: b.open, high: b.high, low: b.low, close: b.close })));
    volumeRef.current.setData(sorted.map((b) => ({ time: b.time as Time, value: b.volume, color: b.close >= b.open ? "rgba(16, 185, 129, 0.15)" : "rgba(239, 68, 68, 0.15)" })));

    const ema9Data = sorted.filter((b) => b.ema9 && b.ema9 > 0).map((b) => ({ time: b.time as Time, value: b.ema9! }));
    const ema21Data = sorted.filter((b) => b.ema21 && b.ema21 > 0).map((b) => ({ time: b.time as Time, value: b.ema21! }));
    const ema50Data = sorted.filter((b) => b.ema50 && b.ema50 > 0).map((b) => ({ time: b.time as Time, value: b.ema50! }));
    const ema200Data = sorted.filter((b) => b.ema200 && b.ema200 > 0).map((b) => ({ time: b.time as Time, value: b.ema200! }));

    if (ema9Ref.current) ema9Ref.current.setData(ema9Data);
    if (ema21Ref.current) ema21Ref.current.setData(ema21Data);
    if (ema50Ref.current) ema50Ref.current.setData(ema50Data);
    if (ema200Ref.current) ema200Ref.current.setData(ema200Data);

    lastBarCountRef.current = bars.length;

    // ORB range boxes — shaded rectangles for each day's opening range
    if (orbOverlayRef.current) {
      const ranges = computeORBRanges(sorted, orbWindowMinutes);
      orbOverlayRef.current.setRanges(ranges);
    }

    // Force chart to match container size — fixes squeeze on initial load
    // when grid layout changes during streaming.
    if (containerRef.current && chartRef.current) {
      const rect = containerRef.current.getBoundingClientRect();
      if (rect.width > 0 && rect.height > 0) {
        chartRef.current.applyOptions({ width: rect.width, height: rect.height });
      }
    }

    const ts = chartRef.current?.timeScale();
    if (!ts) return;

    const visibleCandles = 120;
    const dataLen = sorted.length;
    const from = Math.max(0, dataLen - visibleCandles);
    const to = dataLen - 1 + 5;
    ts.setVisibleLogicalRange({ from, to });
  }, [bars]);

  useEffect(() => {
    if (!overlayRef.current) return;
    if (trades.length === 0) {
      overlayRef.current.setSignals([]);
      return;
    }

    const barTimesArr = bars.map((b) => b.time).sort((a, b) => a - b);
    const barTimesSet = new Set(barTimesArr);
    const barMap = new Map(bars.map((b) => [b.time, b]));

    const findClosestBarTime = (unixSec: number): number => {
      if (barTimesSet.has(unixSec)) return unixSec;
      let closest = barTimesArr[0] ?? unixSec;
      for (const bt of barTimesArr) {
        if (Math.abs(bt - unixSec) < Math.abs(closest - unixSec)) closest = bt;
        if (bt > unixSec) break;
      }
      return closest;
    };

    const markerData: SignalMarkerData[] = trades
      .map((t) => {
        const filledAt = t.filled_at ? new Date(t.filled_at) : null;
        if (!filledAt) return null;
        const filledUnix = Math.floor(filledAt.getTime() / 1000);
        const matchedTime = findClosestBarTime(filledUnix);
        const bar = barMap.get(matchedTime);
        const dir = t.direction ?? "";
        const isEntry = dir === "LONG" || dir === "SHORT";
        const isLongSide = dir === "LONG" || (!dir && t.side?.toLowerCase() === "buy");
        const label = dir === "LONG" ? "BUY" : dir === "SHORT" ? "SHORT" : dir === "CLOSE_LONG" ? "SELL" : dir === "CLOSE_SHORT" ? "COVER" : (t.side?.toLowerCase() === "buy" ? "BUY" : "SELL");
        return {
          time: matchedTime as Time,
          price: bar ? (isEntry ? bar.low * 0.999 : bar.high * 1.001) : t.price,
          side: isLongSide ? "buy" : "sell",
          kind: isEntry ? "entry" : "exit",
          executed: true,
          label: `${label} ${t.quantity?.toFixed?.(0) ?? ""} @ $${t.price?.toFixed?.(2) ?? ""}`,
        } as SignalMarkerData;
      })
      .filter((m): m is SignalMarkerData => m !== null);

    overlayRef.current.setSignals(markerData);
  }, [trades, bars]);

  const tradeCount = trades.length;
  const hasActivity = tradeCount > 0;

  const emaLegend = [
    { label: "EMA 9", color: "rgba(251, 191, 36, 0.7)" },
    { label: "EMA 21", color: "rgba(139, 92, 246, 0.7)" },
    { label: "EMA 50", color: "rgba(236, 72, 153, 0.6)" },
    { label: "EMA 200", color: "rgba(249, 115, 22, 0.5)" },
  ];

  return (
    <div className={`rounded-lg border bg-card overflow-hidden flex flex-col h-full ${hasActivity ? "border-emerald-500/30" : "border-border"}`}>
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-border/50">
        <div className="flex items-center gap-3">
          <span className="text-xs font-mono font-semibold text-foreground">{symbol}</span>
          <div className="flex items-center gap-2">
            {emaLegend.map((e) => (
              <div key={e.label} className="flex items-center gap-1">
                <span className="w-2.5 h-[2px] rounded-full" style={{ backgroundColor: e.color }} />
                <span className="text-[9px] font-mono text-muted-foreground">{e.label}</span>
              </div>
            ))}
          </div>
        </div>
        {tradeCount > 0 && (
          <span className="text-[10px] font-mono text-emerald-400">{tradeCount} fill{tradeCount !== 1 ? "s" : ""}</span>
        )}
      </div>
      <div ref={containerRef} className="flex-1 min-h-0" />
    </div>
  );
}

interface Position {
  symbol: string;
  strategy: string;
  direction: string; // "LONG" or "SHORT"
  entry: BacktestTrade;
  exit: BacktestTrade | null;
  qty: number;
  entryPrice: number;
  exitPrice: number | null;
  pnl: number | null;
  pnlPct: number | null;
  entryTime: string;
  exitTime: string | null;
  exitReason: string | null;
  regime: string | null;
  vixBucket: string | null;
  marketContext: string | null;
}

function groupPositions(trades: BacktestTrade[]): Position[] {
  // Pair entries (LONG/SHORT) with their CLOSE exits sequentially per symbol.
  const openBySymbol = new Map<string, BacktestTrade>();
  const positions: Position[] = [];

  for (const t of trades) {
    const dir = t.direction ?? "";
    const isEntry = dir === "LONG" || dir === "SHORT" || (!dir && t.side === "buy");
    const isExit = dir === "CLOSE" || dir === "CLOSE_LONG" || dir === "CLOSE_SHORT" || (!dir && !isEntry && t.side === "sell");
    const key = `${t.symbol}:${t.strategy ?? ""}`;

    if (isEntry) {
      openBySymbol.set(key, t);
    } else if (isExit) {
      const entry = openBySymbol.get(key);
      if (entry) {
        openBySymbol.delete(key);
        const isShort = (entry.direction ?? "") === "SHORT";
        const qty = entry.quantity ?? 0;
        const entryPx = entry.price ?? 0;
        const exitPx = t.price ?? 0;
        const pnl = isShort ? (entryPx - exitPx) * qty : (exitPx - entryPx) * qty;
        const pnlPct = entryPx > 0 ? (pnl / (entryPx * qty)) * 100 : 0;
        positions.push({
          symbol: t.symbol,
          strategy: t.strategy ?? "",
          direction: isShort ? "SHORT" : "LONG",
          entry,
          exit: t,
          qty,
          entryPrice: entryPx,
          exitPrice: exitPx,
          pnl,
          pnlPct,
          entryTime: entry.filled_at ?? "",
          exitTime: t.filled_at ?? "",
          exitReason: parseExitReason(t.rationale),
          regime: entry.regime ?? null,
          vixBucket: entry.vix_bucket ?? null,
          marketContext: entry.market_context ?? null,
        });
      }
    }
  }

  for (const [, entry] of openBySymbol) {
    positions.push({
      symbol: entry.symbol,
      strategy: entry.strategy ?? "",
      direction: (entry.direction ?? "") === "SHORT" ? "SHORT" : "LONG",
      entry,
      exit: null,
      qty: entry.quantity ?? 0,
      entryPrice: entry.price ?? 0,
      exitPrice: null,
      pnl: null,
      pnlPct: null,
      entryTime: entry.filled_at ?? "",
      exitTime: null,
      exitReason: null,
      regime: entry.regime ?? null,
      vixBucket: entry.vix_bucket ?? null,
      marketContext: entry.market_context ?? null,
    });
  }

  return positions;
}

export interface TradeLogHandle {
  scrollToTrade: (trade: BacktestTrade) => void;
}

const TradeLogInline = forwardRef<TradeLogHandle, { trades: BacktestTrade[]; onScrollToTime?: (symbol: string, isoTime: string) => void }>(function TradeLogInline({ trades, onScrollToTime }, ref) {
   const scrollRef = useRef<HTMLDivElement>(null);
   const rowRefs = useRef<Map<number, HTMLTableRowElement>>(new Map());
   const [highlightIdx, setHighlightIdx] = useState<number | null>(null);
   useEffect(() => { if (scrollRef.current) { scrollRef.current.scrollTop = scrollRef.current.scrollHeight; } }, [trades.length]);

  const positions = useMemo(() => groupPositions(trades), [trades]);

  useImperativeHandle(ref, () => ({
    scrollToTrade(trade: BacktestTrade) {
      const idx = findPositionForTrade(positions, trade);
      if (idx < 0) return;
      setHighlightIdx(idx);
      const row = rowRefs.current.get(idx);
      if (row) row.scrollIntoView({ behavior: "smooth", block: "center" });
      // Clear highlight after animation
      setTimeout(() => setHighlightIdx(null), 2000);
    },
  }), [positions]);

  if (positions.length === 0 && trades.length === 0) {
    return <div className="flex items-center justify-center h-full text-xs text-muted-foreground">No trades yet</div>;
  }

  const fmtTime = (s: string | null) => {
    if (!s) return "—";
    return new Date(s).toLocaleString("en-US", { timeZone: "America/New_York", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  };

  return (
    <div ref={scrollRef} className="h-full overflow-y-auto">
      <table className="w-full text-xs font-mono">
        <thead className="sticky top-0 bg-card">
          <tr className="text-[10px] text-muted-foreground uppercase">
            <th className="text-left px-4 py-1.5">#</th>
            <th className="text-left px-2 py-1.5">Symbol</th>
            <th className="text-left px-2 py-1.5">Side</th>
            <th className="text-left px-2 py-1.5">Strategy</th>
            <th className="text-right px-2 py-1.5">Qty</th>
            <th className="text-right px-2 py-1.5">Entry</th>
            <th className="text-left px-2 py-1.5">Entry Time</th>
            <th className="text-right px-2 py-1.5">Exit</th>
            <th className="text-left px-2 py-1.5">Exit Time</th>
            <th className="text-right px-4 py-1.5">P&L</th>
            <th className="text-left px-2 py-1.5">Exit Reason</th>
            <th className="text-left px-2 py-1.5 cursor-help" title="EMA-based regime from EMA21/EMA50 divergence (0.3% threshold) + RSI/Stochastic on the strategy timeframe. TREND = EMAs diverging >0.3%, BALANCE = EMAs converging, REVERSAL = RSI overbought/oversold with stochastic crossover.">EMA Regime</th>
            <th className="text-left px-2 py-1.5 cursor-help" title="VIX volatility bucket derived from VIXY ETF price. LOW_VOL (VIX<15) = calm, standard params. NORMAL (15-25) = elevated, stops widened 1.5x. HIGH_VOL (VIX>25) = stress, ORB entries blocked.">VIX</th>
            <th className="text-left px-2 py-1.5 cursor-help" title="Composite market context at entry. Shows VIX bucket + NR7 (prior day narrowest range in 7 = compression day) + VWAP alignment (VWAP+ = price on correct side of VWAP, VWAP- = against VWAP).">Context</th>
          </tr>
        </thead>
        <tbody>
          {positions.map((p, i) => {
            const isWin = p.pnl !== null && p.pnl > 0;
            const isLoss = p.pnl !== null && p.pnl < 0;
            return (
              <tr key={i} ref={(el) => { if (el) rowRefs.current.set(i, el); else rowRefs.current.delete(i); }} className={`border-t border-border/30 transition-colors duration-500 ${highlightIdx === i ? "!bg-blue-500/20" : isWin ? "bg-emerald-500/[0.03]" : isLoss ? "bg-red-500/[0.03]" : ""}`}>
                <td className="px-4 py-1 text-muted-foreground">{i + 1}</td>
                <td className="px-2 py-1 text-foreground">{p.symbol}</td>
                <td className="px-2 py-1">
                  <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-bold ${
                    p.direction === "SHORT" ? "bg-red-500/20 text-red-400" : "bg-emerald-500/20 text-emerald-400"
                  }`}>
                    {p.direction}
                  </span>
                </td>
                <td className="px-2 py-1 text-muted-foreground">{p.strategy}</td>
                <td className="px-2 py-1 text-right text-foreground">{p.qty.toFixed(0)}</td>
                <td className="px-2 py-1 text-right text-emerald-400">${p.entryPrice.toFixed(2)}</td>
                <td className="px-2 py-1">
                  {p.entryTime ? (
                    <button
                      className="text-muted-foreground hover:text-blue-400 hover:underline cursor-pointer transition-colors"
                      onClick={() => onScrollToTime?.(p.symbol, p.entryTime)}
                    >
                      {fmtTime(p.entryTime)}
                    </button>
                  ) : "—"}
                </td>
                <td className="px-2 py-1 text-right text-red-400">{p.exitPrice !== null ? `$${p.exitPrice.toFixed(2)}` : "—"}</td>
                <td className="px-2 py-1">
                  {p.exitTime ? (
                    <button
                      className="text-muted-foreground hover:text-blue-400 hover:underline cursor-pointer transition-colors"
                      onClick={() => onScrollToTime?.(p.symbol, p.exitTime!)}
                    >
                      {fmtTime(p.exitTime)}
                    </button>
                  ) : "—"}
                </td>
                <td className={`px-4 py-1 text-right font-medium ${isWin ? "text-emerald-400" : isLoss ? "text-red-400" : "text-muted-foreground"}`}>
                  {p.pnl !== null ? (
                    <span>{p.pnl >= 0 ? "+" : ""}{formatCurrency(p.pnl)} <span className="text-[10px]">({p.pnlPct! >= 0 ? "+" : ""}{p.pnlPct!.toFixed(2)}%)</span></span>
                  ) : (
                    <span className="text-amber-400">open</span>
                  )}
                </td>
                <td className="px-2 py-1 text-[10px] text-muted-foreground">
                  {p.exitReason ?? ""}
                </td>
                <td className="px-2 py-1 text-[10px]">
                  {p.regime ? (
                    <span className={`inline-block px-1 py-0.5 rounded text-[9px] font-medium ${
                      p.regime === "TREND" ? "bg-blue-500/20 text-blue-400" :
                      p.regime === "BALANCE" ? "bg-amber-500/20 text-amber-400" :
                      p.regime === "REVERSAL" ? "bg-purple-500/20 text-purple-400" :
                      "bg-gray-500/20 text-gray-400"
                    }`}>{p.regime}</span>
                  ) : ""}
                </td>
                <td className="px-2 py-1 text-[10px]">
                  {p.vixBucket ? (
                    <span className={`inline-block px-1 py-0.5 rounded text-[9px] font-medium ${
                      p.vixBucket === "LOW_VOL" ? "bg-emerald-500/20 text-emerald-400" :
                      p.vixBucket === "NORMAL" ? "bg-amber-500/20 text-amber-400" :
                      p.vixBucket === "HIGH_VOL" ? "bg-red-500/20 text-red-400" :
                      "bg-gray-500/20 text-gray-400"
                    }`}>{p.vixBucket}</span>
                  ) : ""}
                </td>
                <td className="px-2 py-1 text-[10px] text-muted-foreground whitespace-nowrap">
                  {p.marketContext ?? ""}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
});

function MetricsPanelInline({
  metrics, result, initialEquity,
}: {
  metrics: BacktestMetrics | null; result: BacktestResult | null; initialEquity: number;
}) {
  const m = result ?? metrics;
  if (!m) {
    return <div className="flex items-center justify-center h-full text-xs text-muted-foreground">Run a backtest to see results</div>;
  }

  const equity = result?.final_equity ?? metrics?.equity ?? initialEquity;
  const pnl = result?.total_pnl ?? metrics?.total_pnl ?? 0;
  const returnPct = result?.total_return_pct ?? metrics?.total_return ?? 0;
  const tradeCount = result?.trade_count ?? metrics?.trades ?? 0;
  const winRate = result?.win_rate_pct ?? metrics?.win_rate ?? 0;
  const drawdown = result?.max_drawdown_pct ?? metrics?.max_drawdown ?? 0;
  const sharpe = result?.sharpe_ratio ?? metrics?.sharpe ?? 0;
  const profitFactor = result?.profit_factor ?? metrics?.profit_factor ?? 0;
  const avgWin = result?.avg_win ?? 0;
  const avgLoss = result?.avg_loss ?? 0;

  const stats = [
    { label: "Equity", value: formatCurrency(equity), color: "" },
    { label: "P&L", value: formatCurrency(pnl), color: pnl >= 0 ? "text-emerald-400" : "text-red-400" },
    { label: "Return", value: formatPct(returnPct), color: returnPct >= 0 ? "text-emerald-400" : "text-red-400" },
    { label: "Trades", value: String(tradeCount), color: "" },
    { label: "Win Rate", value: `${winRate.toFixed(1)}%`, color: winRate >= 50 ? "text-emerald-400" : "text-red-400" },
    { label: "Max Drawdown", value: `${drawdown.toFixed(2)}%`, color: "text-red-400" },
    { label: "Sharpe Ratio", value: sharpe.toFixed(3), color: sharpe > 0 ? "text-emerald-400" : "text-red-400" },
    { label: "Profit Factor", value: profitFactor.toFixed(2), color: profitFactor >= 1 ? "text-emerald-400" : "text-red-400" },
    { label: "Avg Win", value: formatCurrency(avgWin), color: "text-emerald-400" },
    { label: "Avg Loss", value: formatCurrency(avgLoss), color: "text-red-400" },
  ];

  return (
    <div className="p-4 h-full overflow-y-auto">
      <div className="grid grid-cols-5 gap-x-6 gap-y-3">
        {stats.map((s) => (
          <div key={s.label}>
            <div className="text-[10px] text-muted-foreground uppercase tracking-wider">{s.label}</div>
            <div className={`text-sm font-mono font-medium ${s.color || "text-foreground"}`}>{s.value}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function EquityCurveInline({ data }: { data: { time: number; value: number }[] }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Line", Time> | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = createChart(containerRef.current, {
      width: containerRef.current.clientWidth, height: containerRef.current.clientHeight || 100,
      layout: { background: { type: ColorType.Solid, color: "transparent" }, textColor: "rgba(148, 163, 184, 0.6)", fontFamily: "var(--font-geist-mono, monospace)", fontSize: 10 },
      grid: { vertLines: { visible: false }, horzLines: { color: "rgba(148, 163, 184, 0.05)" } },
      rightPriceScale: { borderVisible: false },
      timeScale: { borderVisible: false, timeVisible: true, fixLeftEdge: true, fixRightEdge: true },
      crosshair: { mode: CrosshairMode.Normal },
    });
    chartRef.current = chart;
    const series = chart.addSeries(LineSeries, { color: "#10b981", lineWidth: 2, priceLineVisible: false, lastValueVisible: true });
    seriesRef.current = series;
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) chart.applyOptions({ width: entry.contentRect.width, height: entry.contentRect.height });
      chart.timeScale().fitContent();
    });
    observer.observe(containerRef.current);
    return () => { observer.disconnect(); chart.remove(); chartRef.current = null; seriesRef.current = null; };
  }, []);

  useEffect(() => {
    if (!seriesRef.current || data.length === 0) return;
    const sorted = [...data].sort((a, b) => a.time - b.time);
    seriesRef.current.setData(sorted.map((d) => ({ time: d.time as Time, value: d.value })));
    chartRef.current?.timeScale().fitContent();
  }, [data]);

  if (data.length === 0) {
    return <div className="flex items-center justify-center h-full text-xs text-muted-foreground">No equity data yet</div>;
  }

  return <div ref={containerRef} className="w-full h-full" />;
}
