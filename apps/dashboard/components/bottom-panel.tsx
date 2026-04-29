"use client";

import { useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Activity, Zap, ChevronUp, ChevronDown, TrendingDown } from "lucide-react";
import type { StrategySignalEvent, RegimeType, EntryGatedPayload } from "@/lib/types";
import { useRollingDecay, useComponentAttribution } from "@/hooks/use-decay";
import { useStrategyList } from "@/hooks/queries";
import { RollingPfChart } from "@/components/decay/rolling-pf-chart";
import { AttributionChart } from "@/components/decay/attribution-chart";
import { useResizableHeight, SIGNALS_BOTTOM_KEY } from "@/lib/use-resizable-height";
import { ResizeHandle } from "@/components/resize-handle";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface BarLogEntry {
  receivedAt: number;
  eventType: "bar" | "forming";
  symbol: string;
  timeframe: string;
  time: string;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

export type BottomTab = "signals" | "market" | "bars" | "strategy" | "decay";


// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatTimeET(isoString: string): string {
  try {
    return new Intl.DateTimeFormat("en-US", {
      timeZone: "America/New_York",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    }).format(new Date(isoString));
  } catch {
    return "";
  }
}

export function sideBadge(sig: StrategySignalEvent) {
  const isBuy = sig.Side?.toLowerCase() === "buy";
  const isEntry = sig.Kind?.toLowerCase() !== "exit";
  let text = "";
  let cls = "";
  if (isBuy && isEntry) { text = "LONG"; cls = "bg-emerald-500 text-white"; }
  else if (!isBuy && !isEntry) { text = "EXIT"; cls = "bg-orange-500 text-white"; }
  else if (!isBuy && isEntry) { text = "SHORT"; cls = "bg-rose-600 text-white"; }
  else { text = "COVER"; cls = "bg-sky-500 text-white"; }
  return { text, cls };
}

export function regimeBadge(regime: RegimeType) {
  return regime === "TREND" || regime === "TREND_UP" || regime === "TREND_DOWN"
    ? "bg-emerald-500/15 text-emerald-500 border-emerald-500/30"
    : regime === "REVERSAL"
      ? "bg-red-500/15 text-red-500 border-red-500/30"
      : "bg-amber-500/15 text-amber-500 border-amber-500/30";
}

function statusBadgeColor(status: string): string {
  switch (status) {
    case "executed":        return "bg-emerald-500/15 text-emerald-400 border-emerald-500/30";
    case "validated":       return "bg-blue-500/15 text-blue-400 border-blue-500/30";
    case "generated":       return "bg-zinc-500/15 text-zinc-400 border-zinc-500/30";
    case "blocked":         return "bg-orange-500/15 text-orange-400 border-orange-500/30";
    case "rejected":        return "bg-red-500/15 text-red-400 border-red-500/30";
    case "canceled":        return "bg-rose-500/15 text-rose-400 border-rose-500/30";
    case "suppressed":      return "bg-amber-500/15 text-amber-400 border-amber-500/30";
    case "debate_override": return "bg-purple-500/15 text-purple-400 border-purple-500/30";
    default:                return "bg-zinc-500/15 text-zinc-400 border-zinc-500/30";
  }
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

interface BottomPanelProps {
  bottomTab: BottomTab;
  setBottomTab: (tab: BottomTab) => void;
  recentSignalEvents: StrategySignalEvent[];
  regimeBySymbol: Record<string, { regime: RegimeType; strength: number; rsi: number }>;
  onSymbolClick: (sym: string) => void;
  barLog: BarLogEntry[];
  avwapProgress: Map<string, EntryGatedPayload>;
  macdProgress: Map<string, EntryGatedPayload>;
  onLoadOlderSignals?: () => void;
  hasMoreSignals?: boolean;
  loadingMoreSignals?: boolean;
  hideBlocked?: boolean;
  onToggleHideBlocked?: () => void;
}

export function BottomPanel({
  bottomTab,
  setBottomTab,
  recentSignalEvents,
  regimeBySymbol,
  onSymbolClick,
  barLog,
  avwapProgress,
  macdProgress,
  onLoadOlderSignals,
  hasMoreSignals,
  loadingMoreSignals,
  hideBlocked = false,
  onToggleHideBlocked,
}: BottomPanelProps) {
  const symbolsWithRegime = useMemo(() => Object.keys(regimeBySymbol).sort(), [regimeBySymbol]);
  const [expanded, setExpanded] = useState(false);
  // When filter is OFF, recentSignalEvents still includes blocked rows and we
  // can show an accurate count on the "Hide blocked" pill. When the filter is
  // ON, the server already excluded them, so we hide the count.
  const blockedCount = useMemo(
    () => (hideBlocked ? 0 : recentSignalEvents.reduce((n, s) => (s.Status === "blocked" ? n + 1 : n), 0)),
    [recentSignalEvents, hideBlocked],
  );
  const { height, handleProps } = useResizableHeight(SIGNALS_BOTTOM_KEY, 350, { min: 140, max: 700 });
  const strategyList = useStrategyList();
  const strategyIds = useMemo(() => [...new Set((strategyList.data ?? []).map((s) => s.id))], [strategyList.data]);
  const [decayStrategy, setDecayStrategy] = useState<string>("");
  const activeDecayStrategy = decayStrategy || strategyIds[0] || "";
  const rollingDecay = useRollingDecay(bottomTab === "decay" ? activeDecayStrategy : "");
  const componentAttribution = useComponentAttribution(bottomTab === "decay" ? activeDecayStrategy : "");

  const strategySummary = useMemo(() => {
    const avwapCount = avwapProgress.size;
    const avwapGateBreakdown: Record<string, number> = {};
    let avwapReady = 0;
    for (const [, p] of avwapProgress) {
      if (!p.blockingGate) { avwapReady++; continue; }
      avwapGateBreakdown[p.blockingGate] = (avwapGateBreakdown[p.blockingGate] ?? 0) + 1;
    }

    const macdCount = macdProgress.size;
    const macdGateBreakdown: Record<string, number> = {};
    let macdReady = 0;
    for (const [, p] of macdProgress) {
      if (!p.blockingGate) { macdReady++; continue; }
      macdGateBreakdown[p.blockingGate] = (macdGateBreakdown[p.blockingGate] ?? 0) + 1;
    }

    return {
      avwapCount, avwapReady, avwapGateBreakdown,
      macdCount, macdReady, macdGateBreakdown,
    };
  }, [avwapProgress, macdProgress]);

  return (
    <div
      className="mt-1 rounded-t-lg border border-border bg-card flex flex-col shrink-0"
      style={expanded ? { height: `${height}px` } : undefined}
    >
      {expanded && <ResizeHandle {...handleProps} />}
      {/* Tab bar */}
      <div className="flex items-center gap-0 border-b border-border shrink-0">
        {(["signals", "market", "bars", "strategy", "decay"] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => { setBottomTab(tab); if (!expanded) setExpanded(true); }}
            className={`px-4 py-2 text-xs font-mono transition-colors relative ${
              bottomTab === tab ? "text-foreground" : "text-muted-foreground hover:text-foreground"
            }`}
          >
            {tab === "signals" ? (
              <span className="flex items-center gap-1.5">
                <Zap className="w-3 h-3" />
                Signals ({recentSignalEvents.length}{hideBlocked ? ", no blocked" : ""})
              </span>
            ) : tab === "bars" ? (
              <span className="flex items-center gap-1.5">
                <Activity className="w-3 h-3" />
                Bars ({barLog.length})
              </span>
            ) : tab === "strategy" ? (
              <span className="flex items-center gap-1.5">
                <Activity className="w-3 h-3" />
                Strategy
              </span>
            ) : tab === "decay" ? (
              <span className="flex items-center gap-1.5">
                <TrendingDown className="w-3 h-3" />
                Decay
              </span>
            ) : (
              <span className="flex items-center gap-1.5">
                <Activity className="w-3 h-3" />
                Market Info
              </span>
            )}
            {bottomTab === tab && (
              <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-emerald-500" />
            )}
          </button>
        ))}
        <button
          onClick={() => setExpanded((prev) => !prev)}
          className="ml-auto px-2 py-2 text-muted-foreground hover:text-foreground transition-colors"
        >
          {expanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronUp className="w-3.5 h-3.5" />}
        </button>
      </div>

      {/* Tab content */}
      {expanded && <div className="flex-1 min-h-0 overflow-auto">
        {bottomTab === "signals" && (
          <div className="p-2">
            {onToggleHideBlocked && (blockedCount > 0 || hideBlocked) && (
              <div className="flex items-center justify-end pb-1.5">
                <button
                  onClick={onToggleHideBlocked}
                  className="text-[10px] font-mono px-2 py-0.5 rounded border border-border bg-card hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
                >
                  {hideBlocked ? "Show blocked" : `Hide blocked (${blockedCount})`}
                </button>
              </div>
            )}
            {recentSignalEvents.length === 0 ? (
              <p className="text-xs text-muted-foreground text-center py-8">
                {hideBlocked
                  ? "No non-blocked signals in range. Toggle \"Show blocked\" to view blocked rows, or click Load older."
                  : "No signals yet. Signals appear when strategies generate buy/sell decisions."}
              </p>
            ) : (
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-[10px] text-muted-foreground uppercase border-b border-border/50">
                    <th className="text-left py-1 px-2 font-medium">Time (ET)</th>
                    <th className="text-left py-1 px-2 font-medium">Symbol</th>
                    <th className="text-left py-1 px-2 font-medium">Strategy</th>
                    <th className="text-left py-1 px-2 font-medium">Side</th>
                    <th className="text-left py-1 px-2 font-medium">Kind</th>
                    <th className="text-left py-1 px-2 font-medium">Status</th>
                    <th className="text-left py-1 px-2 font-medium">Confidence</th>
                    <th className="text-left py-1 px-2 font-medium">Reason</th>
                  </tr>
                </thead>
                <tbody>
                  {recentSignalEvents.map((sig, idx) => {
                    const badge = sideBadge(sig);
                    return (
                      <tr
                        key={`${sig.SignalID}-${idx}`}
                        className="border-b border-border/30 hover:bg-muted/30 cursor-pointer transition-colors"
                        onClick={() => onSymbolClick(sig.Symbol)}
                      >
                        <td className="py-1.5 px-2 font-mono text-muted-foreground">{formatTimeET(sig.TS)}</td>
                        <td className="py-1.5 px-2 font-bold">{sig.Symbol}</td>
                        <td className="py-1.5 px-2 font-mono text-muted-foreground">{sig.Strategy}</td>
                        <td className="py-1.5 px-2">
                          <Badge className={`text-[9px] px-1.5 py-0 ${badge.cls}`}>{badge.text}</Badge>
                        </td>
                        <td className="py-1.5 px-2 text-muted-foreground">{sig.Kind}</td>
                        <td className="py-1.5 px-2">
                          <Badge variant="outline" className={`text-[9px] px-1.5 py-0 ${statusBadgeColor(sig.Status)}`}>
                            {sig.Status}
                          </Badge>
                        </td>
                        <td className="py-1.5 px-2">
                          {sig.Confidence > 0 && (
                            <div className="flex items-center gap-1.5">
                              <div className="w-16 h-1.5 bg-secondary rounded-full overflow-hidden">
                                <div
                                  className="h-full bg-emerald-500 rounded-full"
                                  style={{ width: `${sig.Confidence * 100}%` }}
                                />
                              </div>
                              <span className="text-[10px] font-mono text-muted-foreground">
                                {(sig.Confidence * 100).toFixed(0)}%
                              </span>
                            </div>
                          )}
                        </td>
                        <td className="py-1.5 px-2 text-muted-foreground text-[11px] max-w-[320px] truncate" title={sig.Reason}>
                          {sig.Reason}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
            {onLoadOlderSignals && hasMoreSignals && (
              <div className="flex justify-center py-2">
                <button
                  onClick={onLoadOlderSignals}
                  disabled={loadingMoreSignals}
                  className="text-xs font-mono px-3 py-1 rounded border border-border bg-card hover:bg-muted disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
                >
                  {loadingMoreSignals ? "Loading..." : "Load older"}
                </button>
              </div>
            )}
          </div>
        )}

        {bottomTab === "market" && (
          <div className="p-3 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
            {symbolsWithRegime.length === 0 ? (
              <p className="text-xs text-muted-foreground col-span-full text-center py-8">
                No regime data yet. Waiting for StateUpdated events...
              </p>
            ) : (
              symbolsWithRegime.map((sym) => {
                const r = regimeBySymbol[sym];
                return (
                  <div
                    key={sym}
                    className="rounded-lg border border-border/50 bg-muted/20 p-3 cursor-pointer hover:bg-muted/40 transition-colors"
                    onClick={() => onSymbolClick(sym)}
                  >
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-sm font-bold">{sym}</span>
                      <Badge variant="outline" className={regimeBadge(r.regime)}>
                        {r.regime}
                      </Badge>
                    </div>
                    <div className="flex items-center gap-2 mb-1.5">
                      <span className="text-[10px] text-muted-foreground w-14">Strength</span>
                      <div className="flex-1 h-1.5 bg-secondary rounded-full overflow-hidden">
                        <div
                          className={`h-full rounded-full transition-all duration-500 ${
                            r.regime === "TREND" || r.regime === "TREND_UP" || r.regime === "TREND_DOWN"
                              ? "bg-emerald-500"
                              : r.regime === "REVERSAL"
                                ? "bg-red-500"
                                : "bg-amber-500"
                          }`}
                          style={{ width: `${Math.min(r.strength * 100, 100)}%` }}
                        />
                      </div>
                      <span className="text-[10px] font-mono text-muted-foreground w-8 text-right">
                        {(r.strength * 100).toFixed(0)}%
                      </span>
                    </div>
                    {r.rsi > 0 && (
                      <div className="flex items-center gap-2">
                        <span className="text-[10px] text-muted-foreground w-14">RSI</span>
                        <span className={`text-xs font-mono font-medium ${
                          r.rsi > 70 ? "text-red-500" : r.rsi < 30 ? "text-emerald-500" : "text-foreground"
                        }`}>
                          {r.rsi.toFixed(1)}
                        </span>
                      </div>
                    )}
                  </div>
                );
              })
            )}
          </div>
        )}

        {bottomTab === "strategy" && (
          <div className="p-3 grid grid-cols-1 md:grid-cols-3 gap-3">
            <div className="rounded-lg border border-border/50 bg-muted/20 p-3">
              <div className="flex items-center justify-between mb-2">
                <span className="text-sm font-bold">AVWAP Confluence</span>
                <Badge variant="outline" className="text-[9px]">5m</Badge>
              </div>
              <div className="flex items-center gap-2 mb-1.5">
                <span className="text-[10px] text-muted-foreground w-14">Active</span>
                <span className="text-xs font-mono font-medium">{strategySummary.avwapCount} symbols</span>
              </div>
              {strategySummary.avwapReady > 0 && (
                <div className="flex items-center gap-2 mb-1.5">
                  <span className="text-[10px] text-muted-foreground w-14">Ready</span>
                  <span className="text-xs font-mono font-medium text-emerald-400">{strategySummary.avwapReady} (all gates passed)</span>
                </div>
              )}
              <p className="text-xs text-muted-foreground leading-relaxed">
                {Object.keys(strategySummary.avwapGateBreakdown).length === 0 && strategySummary.avwapCount === 0
                  ? "No symbols being tracked."
                  : Object.entries(strategySummary.avwapGateBreakdown)
                      .sort(([, a], [, b]) => b - a)
                      .map(([gate, count]) => `${count} blocked by ${gate}`)
                      .join(", ") || "All symbols passed gates."}
              </p>
            </div>
            <div className="rounded-lg border border-border/50 bg-muted/20 p-3">
              <div className="flex items-center justify-between mb-2">
                <span className="text-sm font-bold">MACD (Bollinger + MACD)</span>
                <Badge variant="outline" className="text-[9px]">15m</Badge>
              </div>
              <div className="flex items-center gap-2 mb-1.5">
                <span className="text-[10px] text-muted-foreground w-14">Active</span>
                <span className="text-xs font-mono font-medium">{strategySummary.macdCount} symbols</span>
              </div>
              {strategySummary.macdReady > 0 && (
                <div className="flex items-center gap-2 mb-1.5">
                  <span className="text-[10px] text-muted-foreground w-14">Ready</span>
                  <span className="text-xs font-mono font-medium text-emerald-400">{strategySummary.macdReady} (all gates passed)</span>
                </div>
              )}
              <p className="text-xs text-muted-foreground leading-relaxed">
                {Object.keys(strategySummary.macdGateBreakdown).length === 0 && strategySummary.macdCount === 0
                  ? "No symbols being tracked."
                  : Object.entries(strategySummary.macdGateBreakdown)
                      .sort(([, a], [, b]) => b - a)
                      .map(([gate, count]) => `${count} blocked by ${gate}`)
                      .join(", ") || "All symbols passed gates."}
              </p>
            </div>
          </div>
        )}

        {bottomTab === "bars" && (
          <div className="p-2">
            {barLog.length === 0 ? (
              <p className="text-xs text-muted-foreground text-center py-8">
                No bars received yet. Waiting for market data...
              </p>
            ) : (
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-[10px] text-muted-foreground uppercase border-b border-border/50">
                    <th className="text-left py-1 px-2 font-medium">Received</th>
                    <th className="text-left py-1 px-2 font-medium">Type</th>
                    <th className="text-left py-1 px-2 font-medium">Symbol</th>
                    <th className="text-left py-1 px-2 font-medium">TF</th>
                    <th className="text-left py-1 px-2 font-medium">Bar Time</th>
                    <th className="text-right py-1 px-2 font-medium">Open</th>
                    <th className="text-right py-1 px-2 font-medium">High</th>
                    <th className="text-right py-1 px-2 font-medium">Low</th>
                    <th className="text-right py-1 px-2 font-medium">Close</th>
                    <th className="text-right py-1 px-2 font-medium">Volume</th>
                  </tr>
                </thead>
                <tbody>
                  {barLog.map((entry, idx) => {
                    const isGreen = entry.close >= entry.open;
                    return (
                      <tr
                        key={`${entry.receivedAt}-${idx}`}
                        className="border-b border-border/30 hover:bg-muted/30 cursor-pointer transition-colors"
                        onClick={() => onSymbolClick(entry.symbol)}
                      >
                        <td className="py-1 px-2 font-mono text-muted-foreground">
                          {formatTimeET(new Date(entry.receivedAt).toISOString())}
                        </td>
                        <td className="py-1 px-2">
                          <span className={`text-[9px] px-1.5 py-0.5 rounded font-mono ${
                            entry.eventType === "forming"
                              ? "bg-amber-500/15 text-amber-400"
                              : "bg-blue-500/15 text-blue-400"
                          }`}>
                            {entry.eventType === "forming" ? "FORMING" : "BAR"}
                          </span>
                        </td>
                        <td className="py-1 px-2 font-bold">{entry.symbol}</td>
                        <td className="py-1 px-2 font-mono text-muted-foreground">{entry.timeframe}</td>
                        <td className="py-1 px-2 font-mono text-muted-foreground">
                          {formatTimeET(entry.time)}
                        </td>
                        <td className="py-1 px-2 font-mono text-right">{entry.open.toFixed(2)}</td>
                        <td className="py-1 px-2 font-mono text-right text-emerald-400">{entry.high.toFixed(2)}</td>
                        <td className="py-1 px-2 font-mono text-right text-red-400">{entry.low.toFixed(2)}</td>
                        <td className={`py-1 px-2 font-mono text-right font-medium ${isGreen ? "text-emerald-400" : "text-red-400"}`}>
                          {entry.close.toFixed(2)}
                        </td>
                        <td className="py-1 px-2 font-mono text-right text-muted-foreground">
                          {entry.volume >= 1000 ? `${(entry.volume / 1000).toFixed(1)}K` : entry.volume.toFixed(0)}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        )}

        {bottomTab === "decay" && (
          <div className="p-2 h-[300px] flex flex-col">
            {/* Strategy selector */}
            <div className="flex items-center gap-2 mb-2 shrink-0">
              <label htmlFor="decay-strategy" className="text-[10px] text-muted-foreground uppercase font-medium">
                Strategy
              </label>
              <select
                id="decay-strategy"
                value={activeDecayStrategy}
                onChange={(e) => setDecayStrategy(e.target.value)}
                className="rounded border border-border bg-muted/30 px-2 py-1 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-emerald-500"
              >
                {strategyIds.map((id) => (
                  <option key={id} value={id}>{id}</option>
                ))}
              </select>
            </div>
            {/* Charts row */}
            <div className="flex gap-2 flex-1 min-h-0">
              <div className="w-[60%] h-full">
                <RollingPfChart data={rollingDecay.data} isLoading={rollingDecay.isLoading} />
              </div>
              <div className="w-[40%] h-full">
                <AttributionChart data={componentAttribution.data} isLoading={componentAttribution.isLoading} />
              </div>
            </div>
          </div>
        )}
      </div>}
    </div>
  );
}
