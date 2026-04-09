"use client";

import { useMemo, useState, useRef, useCallback, useEffect } from "react";
import { createPortal } from "react-dom";
import { X, GripHorizontal } from "lucide-react";
import type { EntryGatedPayload, EntryCheckResult, ORBPhaseUpdatePayload, BarSnapshot } from "@/lib/types";
import { LiveChart, avwapAnchorColor, avwapAnchorLabel } from "@/components/live-chart";
import { useChartData } from "@/lib/use-chart-data";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

interface SignalProgressTableProps {
  avwapProgress: Map<string, EntryGatedPayload>;
  macdProgress: Map<string, EntryGatedPayload>;
  orbProgress: Map<string, ORBPhaseUpdatePayload>;
}

// ---------------------------------------------------------------------------
// Unified row model
// ---------------------------------------------------------------------------

interface UnifiedRow {
  symbol: string;
  avwap: EntryGatedPayload | undefined;
  macd: EntryGatedPayload | undefined;
  orb: ORBPhaseUpdatePayload | undefined;
  bar: BarSnapshot | undefined;
  compositeScore: number;
}

// ---------------------------------------------------------------------------
// AVWAP helper functions
// ---------------------------------------------------------------------------

function biasColor(bias: string): string {
  if (bias === "LONG") return "text-emerald-400";
  if (bias === "SHORT") return "text-red-400";
  return "text-zinc-500";
}

function slopeColor(bps: number): string {
  return Math.abs(bps) >= 0.3 ? "text-emerald-400" : "text-red-400";
}

function blockingGateColor(gate: string): string {
  switch (gate) {
    case "confluence":
      return "bg-yellow-500/20 text-yellow-400 border-yellow-500/30";
    case "entry_specific":
      return "bg-blue-500/20 text-blue-400 border-blue-500/30";
    case "slope":
      return "bg-red-500/20 text-red-400 border-red-500/30";
    case "bias":
      return "bg-orange-500/20 text-orange-400 border-orange-500/30";
    default:
      return "bg-zinc-500/20 text-zinc-400 border-zinc-500/30";
  }
}

// ---------------------------------------------------------------------------
// ORB helper functions
// ---------------------------------------------------------------------------

const PHASE_ORDER: Record<string, number> = {
  PRE_OPEN: 0,
  FORMING_RANGE: 1,
  RANGE_SET: 2,
  BREAKOUT_SEEN: 3,
  AWAITING_RETEST: 3,
  RETEST_CONFIRMED: 4,
  SIGNAL_FIRED: 5,
  DONE_FOR_SESSION: 5,
  INVALID: -1,
};

function phaseStep(phase: string): number {
  return PHASE_ORDER[phase] ?? -1;
}

function confidenceColor(c: number): string {
  if (c >= 0.75) return "bg-emerald-500";
  if (c >= 0.5) return "bg-yellow-500";
  return "bg-red-500";
}

function abbreviatePhase(phase: string): string {
  switch (phase) {
    case "PRE_OPEN": return "PRE";
    case "FORMING_RANGE": return "FORMING";
    case "RANGE_SET": return "RANGE";
    case "BREAKOUT_SEEN": return "BREAK";
    case "AWAITING_RETEST": return "RETEST";
    case "RETEST_CONFIRMED": return "CONFIRMED";
    case "SIGNAL_FIRED": return "SIGNAL";
    case "DONE_FOR_SESSION": return "DONE";
    case "INVALID": return "INVALID";
    default: return phase;
  }
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
// PhaseIndicator (expanded ORB detail)
// ---------------------------------------------------------------------------

function PhaseIndicator({ phase }: { phase: string }) {
  const step = phaseStep(phase);
  const isInvalid = phase === "INVALID";
  const totalSteps = 6;

  return (
    <div className="flex flex-col items-start gap-1">
      <div className="flex items-center">
        {Array.from({ length: totalSteps }).map((_, i) => {
          let dotClass: string;
          if (isInvalid) {
            dotClass = "bg-red-500";
          } else if (i < step) {
            dotClass = "bg-emerald-500";
          } else if (i === step) {
            dotClass = "bg-yellow-500 animate-pulse";
          } else {
            dotClass = "bg-zinc-700";
          }

          return (
            <div key={i} className="flex items-center">
              {i > 0 && (
                <div
                  className={`w-4 h-0.5 ${
                    isInvalid
                      ? "bg-red-500/40"
                      : i <= step
                        ? "bg-emerald-500/40"
                        : "bg-zinc-700"
                  }`}
                />
              )}
              <div className={`w-2 h-2 rounded-full ${dotClass}`} />
            </div>
          );
        })}
      </div>
      <span className="text-xs text-zinc-500">{phase.replace(/_/g, " ")}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// EntryChecksPanel — per-entry-type failure reasons
// ---------------------------------------------------------------------------

function EntryChecksPanel({ checks }: { checks: EntryCheckResult[] }) {
  return (
    <div className="mt-2 rounded border border-zinc-800 bg-zinc-900/50 px-3 py-2">
      <span className="text-[10px] text-zinc-500 uppercase mb-1 block">Entry Checks</span>
      <div className="grid grid-cols-1 gap-x-6 gap-y-0.5 sm:grid-cols-2">
        {checks.map((c) => (
          <div key={c.name} className="flex items-center gap-1.5">
            <span className={c.passed ? "text-emerald-400" : "text-zinc-600"}>
              {c.passed ? "\u2713" : "\u2717"}
            </span>
            <span className="font-mono text-[11px] text-zinc-300 min-w-[80px]">{c.name}</span>
            <span className="text-[11px] text-zinc-500 truncate">{c.reason}</span>
          </div>
        ))}
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

  return (
    <div className="space-y-3">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">AVWAP Confluence</h4>
      {/* Factor segments bar — same style as readiness bars */}
      <div className="flex items-center gap-2">
        <div className="flex h-2.5 w-32 rounded-full bg-zinc-800 overflow-hidden gap-px">
          {factors.map((f, i) => (
            <div
              key={i}
              className={`flex-1 ${f.active ? "bg-emerald-500" : "bg-zinc-700"} ${i === 0 ? "rounded-l-full" : ""} ${i === factors.length - 1 ? "rounded-r-full" : ""}`}
              title={`${f.label}(+${f.pts}): ${f.active ? f.detail || "yes" : "no"}`}
            />
          ))}
        </div>
        <span className={`text-xs font-mono font-medium ${c.score >= c.maxScore ? "text-emerald-400" : "text-zinc-300"}`}>
          {c.score}/{c.maxScore}
        </span>
        <span className="text-[10px] text-zinc-500">
          {factors.filter(f => f.active).map(f => f.detail || f.label).join(", ") || "none"}
        </span>
      </div>
      {/* Indicators and gate */}
      <div className="flex flex-wrap items-start gap-4">
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Bias</span>
          <span className={`text-xs font-medium ${biasColor(ind.avwapBias)}`}>
            {ind.avwapBias || "\u2014"}
          </span>
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Slope</span>
          <span className={`text-xs ${slopeColor(ind.slopeBPS)}`}>
            {ind.slopeBPS.toFixed(1)} bps
          </span>
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Blocking Gate</span>
          {avwap.blockingGate ? (
            <span
              className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium ${blockingGateColor(avwap.blockingGate)}`}
            >
              {avwap.blockingGate}
            </span>
          ) : (
            <span className="text-emerald-400 text-xs">All passed</span>
          )}
        </div>
      </div>
      {avwap.entryChecks && avwap.entryChecks.length > 0 && (
        <EntryChecksPanel checks={avwap.entryChecks} />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// ORBDetail (popover content)
// ---------------------------------------------------------------------------

function ORBDetail({ orb }: { orb: ORBPhaseUpdatePayload }) {
  const hasBreakout = !!orb.breakout.direction;

  return (
    <div className="space-y-3">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">ORB Phase</h4>
      <PhaseIndicator phase={orb.phase} />
      <div className="flex flex-wrap items-start gap-4">
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Range</span>
          <div className="flex items-center gap-1.5">
            <span
              className={`h-2 w-2 rounded-full ${orb.range.valid ? "bg-emerald-500" : "bg-red-500"}`}
            />
            <span className="text-zinc-200 text-xs">
              {orb.range.high.toFixed(2)} - {orb.range.low.toFixed(2)}
            </span>
          </div>
          <span className="text-zinc-500 text-xs">
            ({orb.range.barCount}/{orb.range.expectedBars} bars)
          </span>
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Breakout</span>
          {hasBreakout ? (
            <div>
              <span
                className={`text-xs font-medium ${
                  orb.breakout.direction === "LONG" ? "text-emerald-400" : "text-red-400"
                }`}
              >
                {orb.breakout.direction} @ {orb.breakout.breakClose.toFixed(2)}
              </span>
              <span className="text-zinc-500 text-xs ml-1">
                (RVOL {orb.breakout.rvol.toFixed(1)}x)
              </span>
            </div>
          ) : (
            <span className="text-zinc-500 text-xs">Watching...</span>
          )}
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Retest</span>
          {!hasBreakout ? (
            <span className="text-zinc-600 text-xs">{"\u2014"}</span>
          ) : orb.retest.holdConfirmed ? (
            <span className="text-emerald-400 text-xs font-medium">Confirmed</span>
          ) : orb.retest.touched ? (
            <span className="text-yellow-400 text-xs">
              Touched @ {orb.retest.touchPrice.toFixed(2)}
            </span>
          ) : (
            <div className="flex flex-col gap-1">
              <span className="text-zinc-400 text-xs">
                Pending ({orb.retest.barsSinceBreak}/{orb.retest.maxRetestBars})
              </span>
              <div className="h-1 w-16 rounded-full bg-zinc-800 overflow-hidden">
                <div
                  className="h-full rounded-full bg-zinc-500"
                  style={{
                    width: `${
                      orb.retest.maxRetestBars > 0
                        ? (orb.retest.barsSinceBreak / orb.retest.maxRetestBars) * 100
                        : 0
                    }%`,
                  }}
                />
              </div>
            </div>
          )}
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Confidence</span>
          <div className="flex items-center gap-2">
            <div className="h-2 w-16 rounded-full bg-zinc-800 overflow-hidden">
              <div
                className={`h-full rounded-full ${confidenceColor(orb.confidence)}`}
                style={{ width: `${orb.confidence * 100}%` }}
              />
            </div>
            <span className="text-zinc-400 text-xs">
              {(orb.confidence * 100).toFixed(0)}%
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Heatmap cell background by normalized value (0-1)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Segmented Gate Bar — compact readiness indicator
// ---------------------------------------------------------------------------

interface GateBarSegment {
  label: string;
  status: "passed" | "active" | "pending";
}

const AVWAP_GATE_ORDER = ["bias", "slope", "confluence", "entry_specific"] as const;
// Operational gates that block before the 4 conceptual gates are evaluated
const AVWAP_OPERATIONAL_GATES = ["cooldown", "max_trades", "hours", "position", "regime"];

function avwapSegments(avwap: EntryGatedPayload): GateBarSegment[] {
  // Operational gates (cooldown, hours, etc.) → all segments pending
  if (AVWAP_OPERATIONAL_GATES.includes(avwap.blockingGate)) {
    return AVWAP_GATE_ORDER.map((gate) => ({
      label: gate,
      status: "pending" as const,
    }));
  }
  const blockIdx = avwap.blockingGate
    ? AVWAP_GATE_ORDER.indexOf(avwap.blockingGate as typeof AVWAP_GATE_ORDER[number])
    : AVWAP_GATE_ORDER.length;
  return AVWAP_GATE_ORDER.map((gate, i) => ({
    label: gate,
    status: i < blockIdx ? "passed" as const
      : i === blockIdx && blockIdx < AVWAP_GATE_ORDER.length ? "active" as const
      : blockIdx >= AVWAP_GATE_ORDER.length ? "passed" as const
      : "pending" as const,
  }));
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
      : "pending" as const,
  }));
}

function orbSegments(orb: ORBPhaseUpdatePayload): GateBarSegment[] {
  const LABELS = ["Range", "Breakout", "Retest", "Signal"] as const;
  const THRESHOLDS = [2, 3, 4, 5];
  const step = phaseStep(orb.phase);

  if (orb.phase === "INVALID") {
    return LABELS.map((label) => ({ label, status: "pending" as const }));
  }

  return LABELS.map((label, i) => {
    if (step >= THRESHOLDS[i]) return { label, status: "passed" as const };
    const prevPassed = i === 0 ? true : step >= THRESHOLDS[i - 1];
    if (prevPassed && step < THRESHOLDS[i]) return { label, status: "active" as const };
    return { label, status: "pending" as const };
  });
}

function SegmentedGateBar({
  segments,
  summary,
  summaryColor,
  onClick,
}: {
  segments: GateBarSegment[];
  summary: string;
  summaryColor: string;
  onClick: (e: React.MouseEvent) => void;
}) {
  return (
    <div className="flex items-center gap-2 cursor-pointer" onClick={onClick}>
      <div className="flex h-2 w-24 rounded-full bg-zinc-800 overflow-hidden gap-px">
        {segments.map((seg, i) => (
          <div
            key={i}
            className={`flex-1 ${
              seg.status === "passed"
                ? "bg-emerald-500"
                : seg.status === "active"
                  ? "bg-yellow-500 animate-pulse"
                  : "bg-zinc-700"
            } ${i === 0 ? "rounded-l-full" : ""} ${i === segments.length - 1 ? "rounded-r-full" : ""}`}
            title={seg.label}
          />
        ))}
      </div>
      <span className={`text-[10px] font-mono whitespace-nowrap ${summaryColor}`}>{summary}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Draggable detail panel (portal)
// ---------------------------------------------------------------------------

function DetailPanel({
  symbol, bar, avwap, orb, anchorRect, onClose,
}: {
  symbol: string;
  bar: BarSnapshot | undefined;
  avwap: EntryGatedPayload | undefined;
  orb: ORBPhaseUpdatePayload | undefined;
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
    { key: "ORB", label: "ORB", color: "rgba(59, 130, 246, 0.5)" },
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
              orbWindowMinutes={orb?.range.windowMinutes}
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
        {avwap ? <AVWAPDetail avwap={avwap} /> : (
          <div className="space-y-2">
            <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">AVWAP Confluence</h4>
            <span className="text-xs text-zinc-600">No data</span>
          </div>
        )}
        {orb ? <ORBDetail orb={orb} /> : (
          <div className="space-y-2">
            <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">ORB Phase</h4>
            <span className="text-xs text-zinc-600">No data</span>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

// ---------------------------------------------------------------------------
// Dense table row with detail popover
// ---------------------------------------------------------------------------

function SignalRow({ row }: { row: UnifiedRow }) {
  const { symbol, avwap, macd, orb, bar } = row;
  const priceUp = bar ? bar.close >= bar.open : true;

  const [open, setOpen] = useState(false);
  const [anchorRect, setAnchorRect] = useState<DOMRect | null>(null);
  const avwapBarRef = useRef<HTMLDivElement>(null);
  const macdBarRef = useRef<HTMLDivElement>(null);
  const orbBarRef = useRef<HTMLDivElement>(null);

  const handleOpen = useCallback((ref: React.RefObject<HTMLDivElement | null>) => (e: React.MouseEvent) => {
    e.stopPropagation();
    if (open) {
      setOpen(false);
    } else {
      setAnchorRect(ref.current?.getBoundingClientRect() ?? null);
      setOpen(true);
    }
  }, [open]);

  // AVWAP bar data
  const avwapSegs = avwap ? avwapSegments(avwap) : null;
  const avwapPassed = avwapSegs ? avwapSegs.filter(s => s.status === "passed").length : 0;
  const avwapSummary = avwap
    ? `${avwapPassed}/4 ${avwap.blockingGate || "ready"}${avwap.blockingGate === "confluence" ? ` (${avwap.confluence.score}/${avwap.confluence.maxScore})` : ""}`
    : "";
  const avwapSummaryColor = avwap
    ? (avwap.blockingGate ? "text-zinc-400" : "text-emerald-400")
    : "text-zinc-700";

  // MACD bar data
  const macdSegs = macd ? macdSegments(macd) : null;
  const macdPassed = macdSegs ? macdSegs.filter(s => s.status === "passed").length : 0;
  const macdSummary = macd
    ? `${macdPassed}/4 ${macd.blockingGate || "ready"}${macd.blockingGate === "filters" && macd.confluence.maxScore > 0 ? ` (${macd.confluence.score}/${macd.confluence.maxScore})` : ""}`
    : "";
  const macdSummaryColor = macd
    ? (macd.blockingGate ? "text-zinc-400" : "text-emerald-400")
    : "text-zinc-700";

  // ORB bar data
  const orbSegs = orb ? orbSegments(orb) : null;
  const orbPassed = orbSegs ? orbSegs.filter(s => s.status === "passed").length : 0;
  const orbSummary = orb ? `${orbPassed}/4 ${abbreviatePhase(orb.phase)}` : "";
  const orbSummaryColor = orb
    ? (orb.phase === "SIGNAL_FIRED" || orb.phase === "RETEST_CONFIRMED" ? "text-emerald-400"
      : orb.phase === "INVALID" ? "text-red-400"
      : "text-zinc-400")
    : "text-zinc-700";

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
            <SegmentedGateBar
              segments={avwapSegs}
              summary={avwapSummary}
              summaryColor={avwapSummaryColor}
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
            <SegmentedGateBar
              segments={macdSegs}
              summary={macdSummary}
              summaryColor={macdSummaryColor}
              onClick={handleOpen(macdBarRef)}
            />
          </div>
        ) : (
          <span className="text-zinc-700 text-[10px]">{"\u2014"}</span>
        )}
      </td>

      {/* ORB Readiness */}
      <td className="px-2 text-center">
        {orbSegs ? (
          <div ref={orbBarRef} className="inline-block">
            <SegmentedGateBar
              segments={orbSegs}
              summary={orbSummary}
              summaryColor={orbSummaryColor}
              onClick={handleOpen(orbBarRef)}
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
            orb={orb}
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
  { label: "ORB", span: 1 },
] as const;

const COLUMNS = ["Sym", "Price", "Readiness", "Readiness", "Readiness"] as const;

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function SignalProgressTable({ avwapProgress, macdProgress, orbProgress }: SignalProgressTableProps) {
  const rows: UnifiedRow[] = useMemo(() => {
    const symbols = new Set<string>();
    for (const key of avwapProgress.keys()) symbols.add(key);
    for (const key of macdProgress.keys()) symbols.add(key);
    for (const key of orbProgress.keys()) symbols.add(key);

    const result: UnifiedRow[] = [];
    for (const symbol of symbols) {
      const avwap = avwapProgress.get(symbol);
      const macd = macdProgress.get(symbol);
      const orb = orbProgress.get(symbol);
      const bar = avwap?.bar ?? macd?.bar ?? orb?.bar;
      const avwapScore = avwap ? avwap.confluence.score : 0;
      const macdScore = macd ? macd.confluence.score : 0;
      const orbScore = orb ? orb.confidence * 10 : 0;
      const compositeScore = Math.max(avwapScore, macdScore, orbScore);
      result.push({ symbol, avwap, macd, orb, bar, compositeScore });
    }

    result.sort((a, b) => b.compositeScore - a.compositeScore);
    return result;
  }, [avwapProgress, macdProgress, orbProgress]);

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
