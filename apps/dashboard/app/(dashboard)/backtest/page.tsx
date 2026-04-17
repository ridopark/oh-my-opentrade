"use client";

import React, { useState, useRef, useEffect, useMemo, useImperativeHandle, forwardRef } from "react";
import { formatQty } from "@/lib/format";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  createChart,
  ColorType,
  AreaSeries,
  CrosshairMode,
  type IChartApi,
  type ISeriesApi,
  type Time,
} from "lightweight-charts";
import {
  useBacktest,
  type BacktestConfig,
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

/** Extract entry setup type from the rationale string.
 *  e.g. "passthrough (no-ai): entry buy strength=0.90 setup:avwap_breakout" → "BREAKOUT" */
function parseEntryReason(rationale?: string): string | null {
  if (!rationale) return null;
  const m = rationale.match(/setup:(\S+)/);
  if (!m) return null;
  // Strip strategy prefix (e.g. "avwap_breakout" → "BREAKOUT", "orb_break_retest" → "BREAK RETEST")
  const raw = m[1];
  const stripped = raw.replace(/^(avwap|orb)_/, "");
  return stripped.toUpperCase().replace(/_/g, " ");
}

/** Extract confluence score and detail from the rationale string.
 *  e.g. "confluence:5(fib_38.2+strength_candle)" → { score: 5, detail: "fib_38.2+strength_candle" } */
function parseConfluence(rationale?: string): { score: number; detail: string } | null {
  if (!rationale) return null;
  const m = rationale.match(/confluence:(\d+)\(([^)]*)\)/);
  if (!m) return null;
  return { score: parseInt(m[1], 10), detail: m[2] };
}

const SPEED_OPTIONS = ["1x", "2x", "5x", "10x", "max"] as const;

interface StrategyMeta {
  id: string;
  name: string;
  state: string;
  symbols?: string[];
  timeframes?: string[];
}

function formatCurrency(v: number) {
  return v.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

function formatPct(v: number) {
  return `${v >= 0 ? "+" : ""}${v.toFixed(2)}%`;
}

export default function BacktestPage() {
  const bt = useBacktest();
  const [availableStrategies, setAvailableStrategies] = useState<StrategyMeta[]>([]);

  useEffect(() => {
    fetch("/api/backtest/strategies")
      .then((r) => r.json())
      .then((data) => {
        if (!Array.isArray(data)) return;
        // Hide retired/deactivated strategies from the dropdown. Backtests
        // against them still work if explicitly requested by ID, but they
        // don't belong in the default selection surface (crypto_revert_v1
        // retired 2026-04-17, orb_break_retest retired 2026-04-12).
        const active = (data as StrategyMeta[]).filter(
          (s) => s.state?.toLowerCase() !== "deactivated",
        );
        setAvailableStrategies(active);
      })
      .catch(() => {});
  }, []);

  const defaults: BacktestConfig = useMemo(() => ({
    symbols: [],
    from: new Date(Date.now() - 7 * 86400000).toISOString().split("T")[0],
    to: new Date().toISOString().split("T")[0],
    timeframe: "5m",
    initialEquity: 100000,
    slippageBps: 5,
    speed: "max",
    noAi: true,
    strategies: [],
    useDailyScreener: false,
    screenerTopN: 5,
  }), []);

  const [config, setConfig] = useState<BacktestConfig>(defaults);
  const [selectedStrategies, setSelectedStrategies] = useState<string[]>([]);
  const [hydrated, setHydrated] = useState(false);

  const toggleStrategy = (id: string) => {
    setSelectedStrategies((prev) =>
      prev.includes(id) ? prev.filter((s) => s !== id) : [...prev, id]
    );
  };

  useEffect(() => {
    try {
      const saved = localStorage.getItem("backtest-config");
      if (saved) {
        const parsed = JSON.parse(saved);
        // `symbols` is derived from the strategy TOML — never restore it from
        // localStorage. Otherwise a stale cached list can override the current
        // strategy's routing and silently change backtest results.
        const { symbols: _ignored, ...rest } = parsed;
        setConfig((prev) => ({ ...prev, ...rest }));
        if (parsed.strategies?.length) setSelectedStrategies(parsed.strategies);
      }
    } catch {}
    setHydrated(true);
  }, []);

  // When selected strategies change, update config.strategies (symbols come from each strategy's TOML).
  useEffect(() => {
    if (selectedStrategies.length === 0 || availableStrategies.length === 0) return;
    const first = availableStrategies.find((s) => s.id === selectedStrategies[0]);
    setConfig((prev) => ({
      ...prev,
      strategies: selectedStrategies,
      symbols: first?.symbols ?? prev.symbols,
      timeframe: first?.timeframes?.[0] ?? prev.timeframe,
    }));
  }, [selectedStrategies, availableStrategies]);

  // Auto-select first strategy on load if none selected.
  useEffect(() => {
    if (!hydrated || availableStrategies.length === 0 || selectedStrategies.length > 0) return;
    setSelectedStrategies([availableStrategies[0].id]);
  }, [hydrated, availableStrategies, selectedStrategies]);

  useEffect(() => {
    if (hydrated) {
      try {
        // Don't persist `symbols` — it's derived from the strategy TOML
        // and must always come fresh from the API to reflect TOML edits.
        const { symbols: _ignored, ...toSave } = config;
        localStorage.setItem("backtest-config", JSON.stringify(toSave));
      } catch {}
    }
  }, [config, hydrated]);

  const handleRun = async () => {
    await bt.run(config);
  };

  const updateConfig = <K extends keyof BacktestConfig>(key: K, value: BacktestConfig[K]) => {
    setConfig((prev) => ({ ...prev, [key]: value }));
  };

  const isRunning = bt.status === "running" || bt.status === "paused";
  const selectedStrats = availableStrategies.filter((s) => selectedStrategies.includes(s.id));

  const [bottomTab, setBottomTab] = useState<"trades" | "results">("trades");

  return (
    <div className="flex flex-col min-h-[calc(100vh-3rem)]">
      <TopBar
        config={config}
        updateConfig={updateConfig}
        selectedStrategies={selectedStrategies}
        onToggleStrategy={toggleStrategy}
        selectedStrats={selectedStrats}
        onRun={handleRun}
        isRunning={isRunning}
        status={bt.status}
        progress={bt.progress}
        setupStage={bt.setupStage}
        availableStrategies={availableStrategies}
        onPause={bt.pause}
        onResume={bt.resume}
        onSetSpeed={async (s) => { updateConfig("speed", s); await bt.setSpeed(s); }}
        onCancel={bt.cancel}
        backtestId={bt.backtestId}
      />

      <div className="mt-2 flex-1 min-h-0 flex flex-col rounded-lg border border-border bg-card overflow-hidden">
        <EquityCurveMain data={bt.equityCurve} />
      </div>

      <div className="h-[350px] mt-1 rounded-t-lg border border-border bg-card flex flex-col">
        <div className="flex items-center gap-0 border-b border-border shrink-0">
          {(["trades", "results"] as const).map((tab) => (
            <button
              key={tab}
              onClick={() => setBottomTab(tab)}
              className={`px-4 py-2 text-xs font-mono transition-colors relative ${
                bottomTab === tab
                  ? "text-foreground"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {tab === "trades" ? `Positions (${Math.floor((bt.trades?.length ?? 0) / 2)})` : "Results"}
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
          {bottomTab === "trades" && <TradeLogInline trades={bt.trades ?? []} />}
          {bottomTab === "results" && <MetricsPanelInline metrics={bt.metrics} result={bt.result} initialEquity={config.initialEquity} />}
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
  config, updateConfig, selectedStrategies, onToggleStrategy, selectedStrats, onRun, isRunning, status, progress, setupStage, availableStrategies, onPause, onResume, onSetSpeed, onCancel, backtestId,
}: {
  config: BacktestConfig;
  updateConfig: <K extends keyof BacktestConfig>(key: K, val: BacktestConfig[K]) => void;
  selectedStrategies: string[];
  onToggleStrategy: (id: string) => void;
  selectedStrats: StrategyMeta[];
  onRun: () => void;
  isRunning: boolean;
  status: string;
  progress: BacktestProgress | null;
  setupStage: string | null;
  availableStrategies: StrategyMeta[];
  onPause: () => void;
  onResume: () => void;
  onSetSpeed: (s: string) => void;
  onCancel: () => void;
  backtestId: string | null;
}) {
  const [strategiesOpen, setStrategiesOpen] = useState(false);
  const stratDropdownRef = useRef<HTMLDivElement>(null);
  const pct = progress?.pct ?? 0;
  const inputCls = "bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500";
  const pillCls = (active: boolean) => `px-2 py-1 text-[10px] font-mono rounded transition-colors ${active ? "bg-white/10 text-foreground" : "text-muted-foreground hover:bg-white/5"}`;

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (stratDropdownRef.current && !stratDropdownRef.current.contains(e.target as Node)) setStrategiesOpen(false);
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  return (
    <div className="relative z-40 rounded-lg border border-border bg-card px-4 py-2.5 flex items-center gap-4 flex-wrap">
      <h1 className="text-sm font-semibold text-foreground shrink-0">Backtest</h1>

      <div className="flex items-center gap-1.5" ref={stratDropdownRef}>
        <span className="text-[10px] text-muted-foreground uppercase">Strategy</span>
        <div className="relative">
          <button
            onClick={() => setStrategiesOpen(!strategiesOpen)}
            className={`${inputCls} w-52 text-left flex items-center justify-between`}
          >
            <span className="truncate">
              {selectedStrategies.length === 0
                ? "Select..."
                : selectedStrategies.length === 1
                  ? selectedStrategies[0]
                  : `${selectedStrategies.length} strategies`}
            </span>
            <span className="text-muted-foreground ml-1">{strategiesOpen ? "\u25B2" : "\u25BC"}</span>
          </button>
          {strategiesOpen && (
            <div className="absolute top-full left-0 mt-1 z-50 w-72 max-h-64 overflow-y-auto rounded-lg border border-border bg-card shadow-xl">
              {availableStrategies.map((strat) => {
                const checked = selectedStrategies.includes(strat.id);
                return (
                  <button
                    key={strat.id}
                    onClick={() => onToggleStrategy(strat.id)}
                    className={`w-full px-3 py-2 text-xs text-left flex items-center gap-2 hover:bg-white/5 transition-colors ${checked ? "text-emerald-400 bg-white/5" : "text-muted-foreground"}`}
                  >
                    <span className={`w-3.5 h-3.5 rounded border flex-shrink-0 flex items-center justify-center text-[9px] ${checked ? "border-emerald-500 bg-emerald-500/20 text-emerald-400" : "border-border"}`}>
                      {checked ? "\u2713" : ""}
                    </span>
                    <span className="font-mono">{strat.id}</span>
                    <span className="text-[10px] text-muted-foreground/50 truncate">{strat.symbols?.length ?? 0} symbols</span>
                  </button>
                );
              })}
            </div>
          )}
        </div>
        {selectedStrats.length > 0 && (
          <span className="text-[10px] text-muted-foreground font-mono">
            {selectedStrats.length === 1
              ? `${selectedStrats[0].symbols?.length ?? 0} symbols · ${selectedStrats[0].timeframes?.[0] ?? "5m"}`
              : `${selectedStrats.length} strategies`}
          </span>
        )}
      </div>

      <div className="flex items-center gap-1.5">
        <span className="text-[10px] text-muted-foreground uppercase">From</span>
        <input type="date" value={config.from} onChange={(e) => updateConfig("from", e.target.value)} className={`${inputCls} w-28`} />
        <span className="text-[10px] text-muted-foreground uppercase">To</span>
        <input type="date" value={config.to} onChange={(e) => updateConfig("to", e.target.value)} className={`${inputCls} w-28`} />
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
        <label className="flex items-center gap-1 cursor-pointer ml-1">
          <input
            type="checkbox"
            checked={!config.noAi}
            onChange={(e) => updateConfig("noAi", !e.target.checked)}
            className="h-3 w-3 rounded border-border accent-emerald-500"
          />
          <span className="text-[10px] text-muted-foreground uppercase">AI</span>
        </label>
      </div>

      <div className="flex items-center gap-2 ml-auto shrink-0">
        {isRunning && !progress && (
          <>
            <div className="h-4 w-4 animate-spin rounded-full border-b-2 border-emerald-500" />
            <span className="text-[10px] font-mono text-muted-foreground">{setupStage ?? "Starting\u2026"}</span>
            <button onClick={onCancel} className="px-1.5 py-0.5 text-[10px] font-mono rounded text-red-400 hover:bg-red-500/10 transition-colors">Cancel</button>
          </>
        )}

        {isRunning && progress && (
          <>
            <button onClick={status === "paused" ? onResume : onPause}
              className="px-2 py-1 text-xs font-mono rounded bg-white/10 text-foreground hover:bg-white/15 transition-colors">
              {status === "paused" ? "\u25B6" : "\u23F8"}
            </button>
            <div className="w-20">
              <div className="h-1 rounded-full bg-white/5 overflow-hidden">
                <div className="h-full rounded-full bg-emerald-500 transition-all duration-300" style={{ width: `${pct}%` }} />
              </div>
            </div>
            <span className="text-[10px] font-mono text-muted-foreground w-8 text-right">{pct.toFixed(0)}%</span>
            <button onClick={onCancel} className="px-1.5 py-0.5 text-[10px] font-mono rounded text-red-400 hover:bg-red-500/10 transition-colors">Cancel</button>
          </>
        )}

        {!isRunning && (
          <Button onClick={onRun} disabled={selectedStrategies.length === 0} size="sm" className="h-7 text-xs px-4">
            {status === "completed" ? "Run Again" : "Run"}
          </Button>
        )}

        {status !== "idle" && <StatusBadge status={status} />}
        {backtestId && (
          <span className="text-[10px] font-mono text-muted-foreground/60 truncate max-w-32" title={backtestId}>
            {backtestId.replace("bt-", "").slice(0, 12)}
          </span>
        )}
      </div>
    </div>
  );
}

function EquityCurveMain({ data }: { data: { time: number; value: number }[] }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Area", Time> | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = createChart(containerRef.current, {
      width: containerRef.current.clientWidth,
      height: containerRef.current.clientHeight || 300,
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "rgba(148, 163, 184, 0.6)",
        fontFamily: "var(--font-geist-mono, monospace)",
        fontSize: 11,
      },
      grid: {
        vertLines: { visible: false },
        horzLines: { color: "rgba(148, 163, 184, 0.06)" },
      },
      rightPriceScale: { borderVisible: false },
      timeScale: {
        borderVisible: false,
        timeVisible: true,
        fixLeftEdge: true,
        fixRightEdge: true,
      },
      crosshair: { mode: CrosshairMode.Normal },
    });
    chartRef.current = chart;
    const series = chart.addSeries(AreaSeries, {
      lineColor: "#10b981",
      lineWidth: 2,
      topColor: "rgba(16, 185, 129, 0.28)",
      bottomColor: "rgba(16, 185, 129, 0.02)",
      priceLineVisible: false,
      lastValueVisible: true,
    });
    seriesRef.current = series;
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        chart.applyOptions({
          width: entry.contentRect.width,
          height: entry.contentRect.height,
        });
      }
      chart.timeScale().fitContent();
    });
    observer.observe(containerRef.current);
    return () => {
      observer.disconnect();
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (!seriesRef.current || data.length === 0) return;
    const sorted = [...data].sort((a, b) => a.time - b.time);
    seriesRef.current.setData(
      sorted.map((d) => ({ time: d.time as Time, value: d.value }))
    );
    chartRef.current?.timeScale().fitContent();
  }, [data]);

  return (
    <div className="relative w-full h-full">
      <div ref={containerRef} className="w-full h-full" />
      {data.length === 0 && (
        <div className="absolute inset-0 flex items-center justify-center text-sm text-muted-foreground">
          Run a backtest to see the equity curve
        </div>
      )}
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
  entryReason: string | null;
  confluence: { score: number; detail: string } | null;
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
        // For options: determine underlying direction from OCC symbol (C=Call=LONG, P=Put=SHORT)
        const isPut = /P\d{8}$/.test(entry.symbol);
        const isOption = (t.instrument_type === "OPTION" || entry.instrument_type === "OPTION");
        // Equity short vs options: bought puts represent bearish direction
        const isEquityShort = (entry.direction ?? "") === "SHORT" && !isOption;
        const isBearish = isEquityShort || isPut;
        const qty = entry.quantity ?? 0;
        const entryPx = entry.price ?? 0;
        const exitPx = t.price ?? 0;
        // Use collector-computed P&L if available (handles options 100x multiplier).
        // Fall back to manual calculation with multiplier for options.
        const multiplier = isOption ? 100 : 1;
        // For options (both calls and puts): P&L = (exit - entry) * qty * multiplier
        // For equity shorts: P&L = (entry - exit) * qty
        const pnl = t.pnl !== undefined && t.pnl !== 0
          ? t.pnl
          : (isEquityShort ? (entryPx - exitPx) * qty * multiplier : (exitPx - entryPx) * qty * multiplier);
        // P&L% based on premium/price change — works for both equity and options
        const pnlPct = entryPx > 0 ? ((exitPx - entryPx) / entryPx * (isEquityShort ? -1 : 1)) * 100 : 0;
        positions.push({
          symbol: t.symbol,
          strategy: t.strategy ?? "",
          direction: isBearish ? "SHORT" : "LONG",
          entry,
          exit: t,
          qty,
          entryPrice: entryPx,
          exitPrice: exitPx,
          pnl,
          pnlPct,
          entryTime: entry.filled_at ?? "",
          exitTime: t.filled_at ?? "",
          entryReason: parseEntryReason(entry.rationale),
          confluence: parseConfluence(entry.rationale),
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
      direction: ((entry.direction ?? "") === "SHORT" || /P\d{8}$/.test(entry.symbol)) ? "SHORT" : "LONG",
      entry,
      exit: null,
      qty: entry.quantity ?? 0,
      entryPrice: entry.price ?? 0,
      exitPrice: null,
      pnl: null,
      pnlPct: null,
      entryTime: entry.filled_at ?? "",
      exitTime: null,
      entryReason: parseEntryReason(entry.rationale),
      confluence: parseConfluence(entry.rationale),
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

const ROW_HEIGHT = 28;

const TradeLogInline = forwardRef<TradeLogHandle, { trades: BacktestTrade[]; onScrollToTime?: (symbol: string, isoTime: string) => void }>(function TradeLogInline({ trades, onScrollToTime }, ref) {
   const scrollRef = useRef<HTMLDivElement>(null);
   const [highlightIdx, setHighlightIdx] = useState<number | null>(null);

  const positions = useMemo(() => groupPositions(trades), [trades]);

  const virtualizer = useVirtualizer({
    count: positions.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 20,
  });

  // Auto-scroll to bottom when new trades arrive
  useEffect(() => {
    if (positions.length > 0) {
      virtualizer.scrollToIndex(positions.length - 1);
    }
  }, [positions.length, virtualizer]);

  useImperativeHandle(ref, () => ({
    scrollToTrade(trade: BacktestTrade) {
      const tradeTime = trade.filled_at ?? "";
      let idx = -1;
      for (let i = 0; i < positions.length; i++) {
        const p = positions[i];
        if (p.entryTime === tradeTime || p.exitTime === tradeTime) { idx = i; break; }
      }
      if (idx < 0) return;
      setHighlightIdx(idx);
      virtualizer.scrollToIndex(idx, { align: "center" });
      setTimeout(() => setHighlightIdx(null), 2000);
    },
  }), [positions, virtualizer]);

  if (positions.length === 0 && trades.length === 0) {
    return <div className="flex items-center justify-center h-full text-xs text-muted-foreground">No trades yet</div>;
  }

  const fmtTime = (s: string | null) => {
    if (!s) return "\u2014";
    return new Date(s).toLocaleString("en-US", { timeZone: "America/New_York", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
  };

  return (
    <div ref={scrollRef} className="h-full overflow-y-auto">
      <table className="w-full text-xs font-mono">
        <thead className="sticky top-0 bg-card z-10">
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
            <th className="text-left px-2 py-1.5">Entry Reason</th>
            <th className="text-center px-2 py-1.5 cursor-help" title="Confluence score (0-10): Fib +3, Key Level +3, Candle Pattern +2, Band Zone +2">Conf</th>
            <th className="text-left px-2 py-1.5">Exit Reason</th>
            <th className="text-left px-2 py-1.5 cursor-help" title="EMA-based regime from EMA21/EMA50 divergence (0.3% threshold) + RSI/Stochastic on the strategy timeframe. TREND = EMAs diverging >0.3%, BALANCE = EMAs converging, REVERSAL = RSI overbought/oversold with stochastic crossover.">EMA Regime</th>
            <th className="text-left px-2 py-1.5 cursor-help" title="Market context at entry. LOW_VOL/NORMAL/HIGH_VOL = SPY 20-day realized vol bucket (<15/15-25/>25). ATR% = this symbol's 14-day daily ATR as % of price — determines stop distance and position size (higher ATR = wider stop, smaller position). NR7 = prior day had narrowest range in 7 sessions (compression → expansion). VWAP+ = price on correct side of VWAP at entry, VWAP- = against institutional flow.">Context</th>
          </tr>
        </thead>
        <tbody>
          {virtualizer.getVirtualItems().length > 0 && (
            <tr><td colSpan={15} style={{ height: virtualizer.getVirtualItems()[0].start, padding: 0, border: 'none', lineHeight: 0 }} /></tr>
          )}
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const i = virtualRow.index;
            const p = positions[i];
            const isWin = p.pnl !== null && p.pnl > 0;
            const isLoss = p.pnl !== null && p.pnl < 0;
            return (
              <tr key={i} data-index={i} ref={virtualizer.measureElement} style={{ height: ROW_HEIGHT }} className={`border-t border-border/30 transition-colors duration-500 ${highlightIdx === i ? "!bg-blue-500/20" : isWin ? "bg-emerald-500/[0.03]" : isLoss ? "bg-red-500/[0.03]" : ""}`}>
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
                <td className="px-2 py-1 text-right text-foreground">{formatQty(p.qty)}</td>
                <td className="px-2 py-1 text-right text-emerald-400">${p.entryPrice.toFixed(2)}</td>
                <td className="px-2 py-1">
                  {p.entryTime ? (
                    <button
                      className="text-muted-foreground hover:text-blue-400 hover:underline cursor-pointer transition-colors"
                      onClick={() => onScrollToTime?.(p.symbol, p.entryTime)}
                    >
                      {fmtTime(p.entryTime)}
                    </button>
                  ) : "\u2014"}
                </td>
                <td className="px-2 py-1 text-right text-red-400">{p.exitPrice !== null ? `$${p.exitPrice.toFixed(2)}` : "\u2014"}</td>
                <td className="px-2 py-1">
                  {p.exitTime ? (
                    <button
                      className="text-muted-foreground hover:text-blue-400 hover:underline cursor-pointer transition-colors"
                      onClick={() => onScrollToTime?.(p.symbol, p.exitTime!)}
                    >
                      {fmtTime(p.exitTime)}
                    </button>
                  ) : "\u2014"}
                </td>
                <td className={`px-4 py-1 text-right font-medium ${isWin ? "text-emerald-400" : isLoss ? "text-red-400" : "text-muted-foreground"}`}>
                  {p.pnl !== null ? (
                    <span>{p.pnl >= 0 ? "+" : ""}{formatCurrency(p.pnl)} <span className="text-[10px]">({p.pnlPct! >= 0 ? "+" : ""}{p.pnlPct!.toFixed(2)}%)</span></span>
                  ) : (
                    <span className="text-amber-400">open</span>
                  )}
                </td>
                <td className="px-2 py-1 text-[10px] text-muted-foreground">
                  {p.entryReason ?? ""}
                </td>
                <td className="px-2 py-1 text-center" title={p.confluence?.detail ?? ""}>
                  {p.confluence ? (
                    <span className={`inline-block px-1.5 py-0.5 rounded text-[9px] font-mono font-bold ${
                      p.confluence.score >= 5 ? "bg-emerald-500/20 text-emerald-400" :
                      p.confluence.score >= 3 ? "bg-blue-500/20 text-blue-400" :
                      "bg-gray-500/20 text-gray-400"
                    }`}>{p.confluence.score}</span>
                  ) : ""}
                </td>
                <td className="px-2 py-1 text-[10px] text-muted-foreground">
                  {p.exitReason ?? ""}
                </td>
                <td className="px-2 py-1 text-[10px]">
                  {p.regime ? (
                    <span className={`inline-block px-1 py-0.5 rounded text-[9px] font-medium ${
                      p.regime === "TREND" || p.regime === "TREND_UP" ? "bg-emerald-500/20 text-emerald-400" :
                      p.regime === "TREND_DOWN" ? "bg-rose-500/20 text-rose-400" :
                      p.regime === "BALANCE" ? "bg-amber-500/20 text-amber-400" :
                      p.regime === "REVERSAL" ? "bg-purple-500/20 text-purple-400" :
                      "bg-gray-500/20 text-gray-400"
                    }`}>{p.regime}</span>
                  ) : ""}
                </td>
                <td className="px-2 py-1 text-[10px] text-muted-foreground whitespace-nowrap">
                  {p.marketContext ?? ""}
                </td>
              </tr>
            );
          })}
          {virtualizer.getVirtualItems().length > 0 && (() => {
            const items = virtualizer.getVirtualItems();
            const bottomPad = virtualizer.getTotalSize() - items[items.length - 1].end;
            return bottomPad > 0 ? <tr><td colSpan={15} style={{ height: bottomPad, padding: 0, border: 'none', lineHeight: 0 }} /></tr> : null;
          })()}
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
