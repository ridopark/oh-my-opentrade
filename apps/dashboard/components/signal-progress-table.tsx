"use client";

import { useMemo, useState, useRef, useCallback, useEffect } from "react";
import { createPortal } from "react-dom";
import { X, GripHorizontal } from "lucide-react";
import type { EntryGatedPayload, EntryCheckResult, BarSnapshot } from "@/lib/types";
import { LiveChart, avwapAnchorColor, avwapAnchorLabel } from "@/components/live-chart";
import { useChartData } from "@/lib/use-chart-data";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

interface SignalProgressTableProps {
  avwapProgress: Map<string, EntryGatedPayload>;
  macdProgress: Map<string, EntryGatedPayload>;
}

// ---------------------------------------------------------------------------
// Unified row model
// ---------------------------------------------------------------------------

interface UnifiedRow {
  symbol: string;
  avwap: EntryGatedPayload | undefined;
  macd: EntryGatedPayload | undefined;
  bar: BarSnapshot | undefined;
  compositeScore: number;
}

// ---------------------------------------------------------------------------
// Left border color by composite score
// ---------------------------------------------------------------------------

function borderColor(score: number): string {
  if (score >= 7) return "border-l-emerald-500";
  if (score >= 5) return "border-l-yellow-500";
  if (score >= 3) return "border-l-red-500";
  return "border-l-zinc-700";
}

// ---------------------------------------------------------------------------
// EntryChecksPanel — per-entry-type failure reasons
// ---------------------------------------------------------------------------

function EntryChecksPanel({ checks }: { checks: EntryCheckResult[] }) {
  return (
    <div className="mt-2 rounded border border-zinc-800 bg-zinc-900/50 px-3 py-2">
      <span className="text-[10px] text-zinc-500 uppercase mb-1 block">Entry Checks</span>
      <div className="grid grid-cols-1 gap-x-6 gap-y-0.5 sm:grid-cols-2">
        {checks.map((c) => {
          const disabled = c.reason === "disabled";
          const showBar = !c.passed && !disabled;
          const prox = Math.max(0, Math.min(1, c.proximity ?? 0));
          const barColor = prox >= 0.75 ? "bg-emerald-500" : prox >= 0.5 ? "bg-yellow-500" : "bg-zinc-600";
          return (
            <div key={c.name} className="flex items-center gap-1.5">
              <span className={c.passed ? "text-emerald-400" : "text-zinc-600"}>
                {c.passed ? "\u2713" : "\u2717"}
              </span>
              <span className="font-mono text-[11px] text-zinc-300 min-w-[80px]">{c.name}</span>
              {showBar && (
                <div
                  className="h-1 w-12 shrink-0 rounded-full bg-zinc-800 overflow-hidden"
                  title={`${(prox * 100).toFixed(0)}% to entry`}
                >
                  <div
                    className={`h-full rounded-full transition-all ${barColor}`}
                    style={{ width: `${Math.max(prox * 100, 2)}%` }}
                  />
                </div>
              )}
              <span className="text-[11px] text-zinc-500 truncate">{c.reason}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// AVWAPDetail (popover content)
// ---------------------------------------------------------------------------

function AVWAPDetail({ avwap }: { avwap: EntryGatedPayload }) {
  const c = avwap.confluence;
  const ind = avwap.indicators;

  const factors = [
    { label: "Fib", pts: 3, active: c.fib, detail: c.fibDetail },
    { label: "Key", pts: 3, active: c.keyLevel, detail: c.keyLevelDetail },
    { label: "Cndl", pts: 2, active: c.candle, detail: c.candleDetail },
    { label: "Band", pts: 2, active: c.band, detail: c.band ? "yes" : "" },
  ];

  const biasOK = !!ind.avwapBias;
  const slopeOK = Math.abs(ind.slopeBPS) >= 0.3;
  const confOK = c.maxScore > 0 && c.score >= c.maxScore;
  const confPct = c.maxScore > 0 ? Math.min(100, (c.score / c.maxScore) * 100) : 0;
  const confFillColor = confPct >= 100 ? "bg-emerald-500" : confPct >= 50 ? "bg-yellow-500" : confPct > 0 ? "bg-orange-500" : "bg-zinc-700";
  const allPreGatesOK = biasOK && slopeOK && confOK;
  const entryBlocking = allPreGatesOK && avwap.blockingGate === "entry_specific";

  const pill = (passed: boolean) =>
    passed
      ? "bg-emerald-500/20 border border-emerald-500/40 text-emerald-300"
      : "bg-yellow-500/15 border border-yellow-500/50 text-yellow-200 animate-pulse";

  return (
    <div className="space-y-2">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">AVWAP Readiness</h4>

      {/* All gates evaluated in parallel — green (done) or yellow pulse (working) */}
      <div className="flex items-center gap-1.5 w-full">
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(biasOK)}`}>
          <span className="text-[11px] font-medium">Bias</span>
          <span className="text-[10px]">{biasOK ? ind.avwapBias : "\u2014"}</span>
        </div>
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(confOK)}`}>
          <span className="text-[11px] font-medium">Conf {c.score}/{c.maxScore}</span>
          <div className="w-full mt-0.5 space-y-0.5">
            <div className="h-1.5 w-full rounded-full bg-zinc-800/60 overflow-hidden">
              <div className={`h-full rounded-full transition-all duration-300 ${confFillColor}`} style={{ width: `${Math.max(confPct, 2)}%` }} />
            </div>
            <span className="text-[9px] opacity-80 block truncate text-center">
              {factors.filter(f => f.active).map(f => f.detail || f.label).join(", ") || "none"}
            </span>
          </div>
        </div>
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(slopeOK)}`}>
          <span className="text-[11px] font-medium">Slope</span>
          <span className="text-[10px]">{ind.slopeBPS.toFixed(1)} bps</span>
        </div>
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(entryBlocking ? false : allPreGatesOK)}`}>
          <span className="text-[11px] font-medium">Entry</span>
          <span className="text-[10px]">{entryBlocking ? "0 fired" : allPreGatesOK ? "ready" : "\u2014"}</span>
        </div>
      </div>

      {/* Entry checks detail (when all pre-gates pass) */}
      {entryBlocking && avwap.entryChecks && avwap.entryChecks.length > 0 && (
        <EntryChecksPanel checks={avwap.entryChecks} />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// MACDDetail (popover content)
// ---------------------------------------------------------------------------

function MACDDetail({ macd }: { macd: EntryGatedPayload }) {
  const c = macd.confluence;
  const warmupOK = macd.blockingGate !== "warmup";
  const regimeOK = macd.blockingGate !== "regime" && warmupOK;
  const crossoverOK = macd.blockingGate !== "crossover" && regimeOK && warmupOK;
  const filtersOK = !macd.blockingGate;

  const pill = (passed: boolean) =>
    passed
      ? "bg-emerald-500/20 border border-emerald-500/40 text-emerald-300"
      : "bg-yellow-500/15 border border-yellow-500/50 text-yellow-200 animate-pulse";
  const confPct = c.maxScore > 0 ? Math.min(100, (c.score / c.maxScore) * 100) : 0;
  const confFillColor = confPct >= 100 ? "bg-emerald-500" : confPct >= 50 ? "bg-yellow-500" : confPct > 0 ? "bg-orange-500" : "bg-zinc-700";

  return (
    <div className="space-y-2">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">MACD Readiness</h4>
      <div className="flex items-center gap-1.5 w-full">
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(warmupOK)}`}>
          <span className="text-[11px] font-medium">Warmup</span>
        </div>
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(regimeOK)}`}>
          <span className="text-[11px] font-medium">Regime</span>
          <span className="text-[10px]">EMA9</span>
        </div>
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(crossoverOK)}`}>
          <span className="text-[11px] font-medium">Cross</span>
          <span className="text-[10px]">MACD</span>
        </div>
        <div className={`flex flex-col items-center flex-1 min-w-0 rounded-xl px-2 py-1.5 ${pill(filtersOK)}`}>
          <span className="text-[11px] font-medium">Filters</span>
          {c.maxScore > 0 && (
            <div className="w-full mt-0.5 space-y-0.5">
              <div className="flex items-center gap-1">
                <div className="h-1.5 flex-1 rounded-full bg-zinc-800/60 overflow-hidden">
                  <div className={`h-full rounded-full transition-all duration-300 ${confFillColor}`} style={{ width: `${Math.max(confPct, 2)}%` }} />
                </div>
                <span className="text-[9px] font-mono">{c.score}/{c.maxScore}</span>
              </div>
            </div>
          )}
        </div>
      </div>
      {macd.blockingDetail && (
        <span className="text-[10px] text-zinc-500 block">{macd.blockingDetail}</span>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Segmented Gate Bar — compact readiness indicator
// ---------------------------------------------------------------------------

interface GateBarSegment {
  label: string;
  status: "passed" | "active" | "pending";
}

// Operational gates that block before the 4 conceptual gates are evaluated
const AVWAP_OPERATIONAL_GATES = ["cooldown", "max_trades", "hours", "position", "regime"];

function avwapSegments(avwap: EntryGatedPayload): GateBarSegment[] {
  // Operational gates (cooldown, hours, etc.) → show pre-gate as active, rest gray
  if (AVWAP_OPERATIONAL_GATES.includes(avwap.blockingGate)) {
    return [
      { label: avwap.blockingGate.charAt(0).toUpperCase() + avwap.blockingGate.slice(1).replace(/_/g, " "), status: "active" as const },
      { label: "Bias", status: "pending" as const },
      { label: "Conf", status: "pending" as const },
      { label: "Slope", status: "pending" as const },
      { label: "Entry", status: "pending" as const },
    ];
  }
  // Each gate is independently evaluated
  const biasOK = !!avwap.indicators.avwapBias;
  const slopeOK = Math.abs(avwap.indicators.slopeBPS) >= 0.3;
  const confOK = avwap.confluence.maxScore > 0 && avwap.confluence.score >= avwap.confluence.maxScore;
  const allOK = biasOK && slopeOK && confOK;
  const entryBlocking = allOK && avwap.blockingGate === "entry_specific";
  return [
    { label: "Bias", status: biasOK ? "passed" as const : "active" as const },
    { label: "Conf", status: confOK ? "passed" as const : "active" as const },
    { label: "Slope", status: slopeOK ? "passed" as const : "active" as const },
    { label: "Entry", status: entryBlocking ? "active" as const : allOK ? "passed" as const : "active" as const },
  ];
}

const MACD_GATE_ORDER = ["warmup", "regime", "crossover", "filters"] as const;

function macdSegments(macd: EntryGatedPayload): GateBarSegment[] {
  const blockIdx = macd.blockingGate
    ? MACD_GATE_ORDER.indexOf(macd.blockingGate as typeof MACD_GATE_ORDER[number])
    : MACD_GATE_ORDER.length;
  return MACD_GATE_ORDER.map((gate, i) => ({
    label: gate,
    status: i < blockIdx ? "passed" as const
      : i === blockIdx && blockIdx < MACD_GATE_ORDER.length ? "active" as const
      : blockIdx >= MACD_GATE_ORDER.length ? "passed" as const
      : "active" as const,
  }));
}

function PillGateBar({
  segments,
  onClick,
}: {
  segments: GateBarSegment[];
  onClick: (e: React.MouseEvent) => void;
}) {
  return (
    <div className="flex gap-0.5 cursor-pointer" onClick={onClick}>
      {segments.map((seg, i) => (
        <div
          key={i}
          className={`h-4 px-1.5 rounded text-[8px] font-medium flex items-center ${
            seg.status === "passed"
              ? "bg-emerald-500/25 text-emerald-400 border border-emerald-500/40"
              : seg.status === "active"
                ? "bg-yellow-500/20 text-yellow-300 border border-yellow-500/40 animate-pulse"
                : "bg-zinc-500/10 text-zinc-600 border border-zinc-700/40"
          }`}
          title={seg.label}
        >
          {seg.label}
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Draggable detail panel (portal)
// ---------------------------------------------------------------------------

function DetailPanel({
  symbol, bar, avwap, macd, anchorRect, onClose,
}: {
  symbol: string;
  bar: BarSnapshot | undefined;
  avwap: EntryGatedPayload | undefined;
  macd: EntryGatedPayload | undefined;
  anchorRect: DOMRect;
  onClose: () => void;
}) {
  const priceUp = bar ? bar.close >= bar.open : true;
  const panelRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ x: number; y: number } | null>(null);
  const dragging = useRef(false);
  const dragOffset = useRef({ x: 0, y: 0 });

  // Chart data with timeframe switching
  const TIMEFRAMES = ["1m", "5m", "15m", "1h", "1d"] as const;
  const [timeframe, setTimeframe] = useState<(typeof TIMEFRAMES)[number]>("5m");
  const chartSymbols = useMemo(() => [symbol], [symbol]);
  const { barsBySymbol } = useChartData(timeframe, "/api/events", chartSymbols);
  const chartBars = barsBySymbol[symbol] ?? [];

  // Legend toggle state
  const [hiddenSeries, setHiddenSeries] = useState<Set<string>>(new Set());
  const toggleSeries = useCallback((key: string) => {
    setHiddenSeries((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  }, []);
  const LEGEND_ITEMS: { key: string; label: string; color: string }[] = useMemo(() => [
    { key: "EMA 9", label: "EMA 9", color: "rgba(251, 191, 36, 0.7)" },
    { key: "EMA 21", label: "EMA 21", color: "rgba(139, 92, 246, 0.7)" },
    { key: "EMA 50", label: "EMA 50", color: "rgba(236, 72, 153, 0.6)" },
    { key: "EMA 200", label: "EMA 200", color: "rgba(249, 115, 22, 0.5)" },
    { key: "session_open", label: avwapAnchorLabel("session_open"), color: avwapAnchorColor("session_open") },
    { key: "pd_high", label: avwapAnchorLabel("pd_high"), color: avwapAnchorColor("pd_high") },
    { key: "pd_low", label: avwapAnchorLabel("pd_low"), color: avwapAnchorColor("pd_low") },
  ], []);

  // Position panel to the left of the anchor button on mount
  useEffect(() => {
    if (!panelRef.current) return;
    const w = panelRef.current.offsetWidth;
    let x = anchorRect.left - w - 8;
    let y = anchorRect.top;
    // If too far left, flip to right
    if (x < 8) x = anchorRect.right + 8;
    // Keep within viewport vertically
    const maxY = window.innerHeight - panelRef.current.offsetHeight - 8;
    if (y > maxY) y = maxY;
    if (y < 8) y = 8;
    setPos({ x, y });
  }, [anchorRect]);

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    dragging.current = true;
    dragOffset.current = { x: e.clientX - (pos?.x ?? 0), y: e.clientY - (pos?.y ?? 0) };
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  }, [pos]);

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    if (!dragging.current) return;
    setPos({ x: e.clientX - dragOffset.current.x, y: e.clientY - dragOffset.current.y });
  }, []);

  const onPointerUp = useCallback(() => {
    dragging.current = false;
  }, []);

  return createPortal(
    <div
      ref={panelRef}
      className="fixed z-50 w-[480px] rounded-lg border border-zinc-700 bg-zinc-950 shadow-2xl"
      style={pos ? { left: pos.x, top: pos.y } : { opacity: 0, left: anchorRect.left - 488, top: anchorRect.top }}
    >
      {/* Drag handle + close button */}
      <div
        className="flex items-center justify-between px-3 py-1.5 border-b border-zinc-800 cursor-grab active:cursor-grabbing select-none"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
      >
        <div className="flex items-center gap-2">
          <GripHorizontal className="h-3.5 w-3.5 text-zinc-600" />
          <span className="font-mono font-bold text-sm text-zinc-100">{symbol}</span>
          {bar && (
            <span className={`font-mono text-xs ${priceUp ? "text-emerald-400" : "text-red-400"}`}>
              {bar.close.toFixed(2)} (H{bar.high.toFixed(2)} L{bar.low.toFixed(2)})
            </span>
          )}
        </div>
        <button
          onClick={onClose}
          className="inline-flex items-center justify-center h-5 w-5 rounded hover:bg-zinc-700 text-zinc-500 hover:text-zinc-300 transition-colors"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      {/* Timeframe pills + Chart */}
      <div className="px-3 pt-2">
        <div className="flex items-center gap-1 mb-1">
          {TIMEFRAMES.map((tf) => (
            <button
              key={tf}
              onClick={() => setTimeframe(tf)}
              className={`px-2 py-0.5 text-[10px] font-mono rounded transition-colors ${
                timeframe === tf
                  ? "bg-white/10 text-zinc-100"
                  : "text-zinc-500 hover:bg-white/5 hover:text-zinc-300"
              }`}
            >
              {tf}
            </button>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 mb-1">
          {LEGEND_ITEMS.map((item) => {
            const hidden = hiddenSeries.has(item.key);
            return (
              <button
                key={item.key}
                onClick={() => toggleSeries(item.key)}
                className={`flex items-center gap-1 text-[10px] font-mono transition-opacity ${hidden ? "opacity-30" : "opacity-100"}`}
              >
                <span className="inline-block h-2 w-3 rounded-sm" style={{ backgroundColor: item.color }} />
                {item.label}
              </button>
            );
          })}
        </div>
        <div style={{ height: 200 }}>
          {chartBars.length > 0 ? (
            <LiveChart
              key={`detail-${symbol}-${timeframe}`}
              symbol={symbol}
              bars={chartBars}
              showLabels={false}
              hiddenSeries={hiddenSeries}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-xs text-zinc-600">
              Loading chart...
            </div>
          )}
        </div>
      </div>

      {/* Detail content */}
      <div className="p-4 space-y-4">
        {avwap && <AVWAPDetail avwap={avwap} />}
        {macd && <MACDDetail macd={macd} />}
      </div>
    </div>,
    document.body,
  );
}

// ---------------------------------------------------------------------------
// Dense table row with detail popover
// ---------------------------------------------------------------------------

function SignalRow({ row }: { row: UnifiedRow }) {
  const { symbol, avwap, macd, bar } = row;
  const priceUp = bar ? bar.close >= bar.open : true;

  const [open, setOpen] = useState(false);
  const [anchorRect, setAnchorRect] = useState<DOMRect | null>(null);
  const avwapBarRef = useRef<HTMLDivElement>(null);
  const macdBarRef = useRef<HTMLDivElement>(null);

  const handleOpen = useCallback((ref: React.RefObject<HTMLDivElement | null>) => (e: React.MouseEvent) => {
    e.stopPropagation();
    if (open) {
      setOpen(false);
    } else {
      setAnchorRect(ref.current?.getBoundingClientRect() ?? null);
      setOpen(true);
    }
  }, [open]);

  // Strategy segments
  const avwapSegs = avwap ? avwapSegments(avwap) : null;
  const macdSegs = macd ? macdSegments(macd) : null;

  return (
    <tr className="border-b border-zinc-800/60 hover:bg-zinc-800/40 cursor-default h-8 text-[11px]">
      {/* Symbol */}
      <td className={`pl-2 pr-1 font-mono font-bold text-zinc-100 border-l-3 ${borderColor(row.compositeScore)}`}>
        {symbol}
      </td>

      {/* Price */}
      <td className="px-2 text-right font-mono">
        {bar ? (
          <span className={priceUp ? "text-emerald-400" : "text-red-400"}>
            {bar.close.toFixed(2)}
          </span>
        ) : (
          <span className="text-zinc-600">{"\u2014"}</span>
        )}
      </td>

      {/* AVWAP Readiness */}
      <td className="px-2 text-center">
        {avwapSegs ? (
          <div ref={avwapBarRef} className="inline-block">
            <PillGateBar
              segments={avwapSegs}
              onClick={handleOpen(avwapBarRef)}
            />
          </div>
        ) : (
          <span className="text-zinc-700 text-[10px]">{"\u2014"}</span>
        )}
      </td>

      {/* MACD Readiness */}
      <td className="px-2 text-center">
        {macdSegs ? (
          <div ref={macdBarRef} className="inline-block">
            <PillGateBar
              segments={macdSegs}
              onClick={handleOpen(macdBarRef)}
            />
          </div>
        ) : (
          <span className="text-zinc-700 text-[10px]">{"\u2014"}</span>
        )}
        {open && anchorRect && (
          <DetailPanel
            symbol={symbol}
            bar={bar}
            avwap={avwap}
            macd={macd}
            anchorRect={anchorRect}
            onClose={() => setOpen(false)}
          />
        )}
      </td>
    </tr>
  );
}

// ---------------------------------------------------------------------------
// Column header labels
// ---------------------------------------------------------------------------

const COL_GROUPS = [
  { label: "", span: 2 },
  { label: "AVWAP", span: 1 },
  { label: "MACD", span: 1 },
] as const;

const COLUMNS = ["Sym", "Price", "Readiness", "Readiness"] as const;

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function SignalProgressTable({ avwapProgress, macdProgress }: SignalProgressTableProps) {
  const rows: UnifiedRow[] = useMemo(() => {
    const symbols = new Set<string>();
    for (const key of avwapProgress.keys()) symbols.add(key);
    for (const key of macdProgress.keys()) symbols.add(key);

    const result: UnifiedRow[] = [];
    for (const symbol of symbols) {
      const avwap = avwapProgress.get(symbol);
      const macd = macdProgress.get(symbol);
      const bar = avwap?.bar ?? macd?.bar;
      const avwapScore = avwap ? avwap.confluence.score : 0;
      const macdScore = macd ? macd.confluence.score : 0;
      const compositeScore = Math.max(avwapScore, macdScore);
      result.push({ symbol, avwap, macd, bar, compositeScore });
    }

    result.sort((a, b) => b.compositeScore - a.compositeScore);
    return result;
  }, [avwapProgress, macdProgress]);

  if (rows.length === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-muted-foreground text-sm">
        Waiting for signal data...
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900 overflow-x-auto">
      <table className="w-full text-xs border-collapse">
        <thead>
          {/* Group header row */}
          <tr className="border-b border-zinc-700">
            {COL_GROUPS.map((g, i) => (
              <th
                key={i}
                colSpan={g.span}
                className={`py-1 text-[10px] font-semibold text-zinc-500 uppercase tracking-wider ${
                  i > 0 ? "border-l border-zinc-700" : ""
                }`}
              >
                {g.label}
              </th>
            ))}
          </tr>
          {/* Column header row */}
          <tr className="border-b border-zinc-700">
            {COLUMNS.map((col, i) => (
              <th
                key={i}
                className={`px-1 py-1 text-[10px] font-medium text-zinc-500 ${
                  i === 0 ? "text-left" : i === 1 ? "text-right" : "text-center"
                } ${i === 0 ? "pl-2" : ""} ${i === COLUMNS.length - 1 ? "pr-2" : ""}`}
              >
                {col}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <SignalRow key={row.symbol} row={row} />
          ))}
        </tbody>
      </table>
    </div>
  );
}
