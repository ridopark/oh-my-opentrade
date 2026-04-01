"use client";

import { useMemo, useState, useRef, useCallback, useEffect } from "react";
import { createPortal } from "react-dom";
import { Info, X, GripHorizontal } from "lucide-react";
import type { EntryGatedPayload, ORBPhaseUpdatePayload, BarSnapshot } from "@/lib/types";
import { LiveChart } from "@/components/live-chart";
import { useChartData } from "@/lib/use-chart-data";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

interface SignalProgressTableProps {
  avwapProgress: Map<string, EntryGatedPayload>;
  orbProgress: Map<string, ORBPhaseUpdatePayload>;
}

// ---------------------------------------------------------------------------
// Unified row model
// ---------------------------------------------------------------------------

interface UnifiedRow {
  symbol: string;
  avwap: EntryGatedPayload | undefined;
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

function volColor(ratio: number): string {
  if (ratio >= 2.0) return "text-emerald-400";
  if (ratio >= 1.0) return "text-yellow-400";
  return "text-red-400";
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

function orbChipColor(phase: string): string {
  switch (phase) {
    case "RETEST_CONFIRMED":
    case "SIGNAL_FIRED":
    case "DONE_FOR_SESSION":
      return "border-emerald-500/40 bg-emerald-500/10 text-emerald-300";
    case "AWAITING_RETEST":
    case "BREAKOUT_SEEN":
      return "border-blue-500/40 bg-blue-500/10 text-blue-300";
    case "RANGE_SET":
    case "FORMING_RANGE":
      return "border-yellow-500/40 bg-yellow-500/10 text-yellow-300";
    case "INVALID":
      return "border-red-500/40 bg-red-500/10 text-red-300";
    case "PRE_OPEN":
    default:
      return "border-zinc-500/40 bg-zinc-500/10 text-zinc-400";
  }
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
// FactorValue (expanded AVWAP detail)
// ---------------------------------------------------------------------------

function FactorValue({ label, active, detail }: { label: string; active: boolean; detail: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[10px] text-zinc-500 uppercase">{label}</span>
      {active ? (
        <span className="text-xs text-emerald-300">{detail || "yes"}</span>
      ) : (
        <span className="text-xs text-zinc-600">{"\u2014"}</span>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// AVWAPDetail (popover content)
// ---------------------------------------------------------------------------

function AVWAPDetail({ avwap }: { avwap: EntryGatedPayload }) {
  const c = avwap.confluence;
  const ind = avwap.indicators;

  return (
    <div className="space-y-3">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">AVWAP Confluence</h4>
      <div className="flex flex-wrap items-start gap-4">
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Bias</span>
          <span className={`text-xs font-medium ${biasColor(ind.avwapBias)}`}>
            {ind.avwapBias || "\u2014"}
          </span>
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Vol</span>
          <span className={`text-xs ${volColor(ind.volumeRatio)}`}>
            {ind.volumeRatio.toFixed(1)}x
          </span>
        </div>
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Slope</span>
          <span className={`text-xs ${slopeColor(ind.slopeBPS)}`}>
            {ind.slopeBPS.toFixed(1)}
          </span>
        </div>
        <FactorValue label="Fib(+3)" active={c.fib} detail={c.fibDetail} />
        <FactorValue label="Key Lvl(+3)" active={c.keyLevel} detail={c.keyLevelDetail} />
        <FactorValue label="Candle(+2)" active={c.candle} detail={c.candleDetail} />
        <FactorValue label="Band(+2)" active={c.band} detail={c.band ? "yes" : ""} />
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

function heatBg(value: number, max: number): string {
  const ratio = max > 0 ? value / max : 0;
  if (ratio >= 0.7) return "bg-emerald-500/20";
  if (ratio >= 0.5) return "bg-yellow-500/15";
  if (ratio >= 0.3) return "bg-zinc-500/10";
  return "";
}

// ---------------------------------------------------------------------------
// Dot indicator for boolean factors
// ---------------------------------------------------------------------------

function Dot({ active }: { active: boolean }) {
  return (
    <span className={`inline-block h-2 w-2 rounded-full ${active ? "bg-emerald-400" : "bg-zinc-700"}`} />
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
        <div style={{ height: 200 }}>
          {chartBars.length > 0 ? (
            <LiveChart
              key={`detail-${symbol}-${timeframe}`}
              symbol={symbol}
              bars={chartBars}
              showLabels={false}
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
  const { symbol, avwap, orb, bar } = row;
  const priceUp = bar ? bar.close >= bar.open : true;

  const c = avwap?.confluence;
  const ind = avwap?.indicators;
  const hasBreakout = orb ? !!orb.breakout.direction : false;

  const [open, setOpen] = useState(false);
  const [anchorRect, setAnchorRect] = useState<DOMRect | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);

  return (
    <tr className="border-b border-zinc-800/60 hover:bg-zinc-800/40 cursor-default h-7 text-[11px]">
      {/* Symbol */}
      <td className={`pl-2 pr-1 font-mono font-bold text-zinc-100 border-l-3 ${borderColor(row.compositeScore)}`}>
        {symbol}
      </td>

      {/* Price */}
      <td className="px-1 text-right font-mono">
        {bar ? (
          <span className={priceUp ? "text-emerald-400" : "text-red-400"}>
            {bar.close.toFixed(2)}
          </span>
        ) : (
          <span className="text-zinc-600">{"\u2014"}</span>
        )}
      </td>

      {/* AVWAP Bias */}
      <td className="px-1 text-center font-mono font-bold">
        {ind ? (
          <span className={biasColor(ind.avwapBias)}>
            {ind.avwapBias === "LONG" ? "L" : ind.avwapBias === "SHORT" ? "S" : "-"}
          </span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* AVWAP Score — heatmap bg */}
      <td className={`px-1 text-center font-mono ${c ? heatBg(c.score, c.maxScore) : ""}`}>
        {c ? (
          <span className="text-zinc-200">{c.score}/{c.maxScore}</span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* Vol — heatmap bg */}
      <td className={`px-1 text-center font-mono ${ind ? heatBg(ind.volumeRatio, 3) : ""}`}>
        {ind ? (
          <span className={volColor(ind.volumeRatio)}>{ind.volumeRatio.toFixed(1)}</span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* Slope */}
      <td className="px-1 text-center font-mono">
        {ind ? (
          <span className={slopeColor(ind.slopeBPS)}>{ind.slopeBPS.toFixed(1)}</span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* Fib / Key / Candle / Band — dots */}
      <td className="px-1 text-center"><Dot active={!!c?.fib} /></td>
      <td className="px-1 text-center"><Dot active={!!c?.keyLevel} /></td>
      <td className="px-1 text-center"><Dot active={!!c?.candle} /></td>
      <td className="px-1 text-center"><Dot active={!!c?.band} /></td>

      {/* Gate */}
      <td className="px-1 text-center">
        {avwap ? (
          avwap.blockingGate ? (
            <span className={`rounded-full border px-1 text-[10px] leading-4 ${blockingGateColor(avwap.blockingGate)}`}>
              {avwap.blockingGate}
            </span>
          ) : (
            <span className="text-emerald-400/60">OK</span>
          )
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* ORB Phase */}
      <td className="px-1 text-center">
        {orb ? (
          <span className={`rounded-full border px-1.5 text-[10px] leading-4 ${orbChipColor(orb.phase)}`}>
            {abbreviatePhase(orb.phase)}
          </span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* ORB Dir */}
      <td className="px-1 text-center font-mono font-medium">
        {hasBreakout && orb ? (
          <span className={orb.breakout.direction === "LONG" ? "text-emerald-400" : "text-red-400"}>
            {orb.breakout.direction === "LONG" ? "L" : "S"}
          </span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* ORB RVOL — heatmap bg */}
      <td className={`px-1 text-center font-mono ${hasBreakout && orb ? heatBg(orb.breakout.rvol, 3) : ""}`}>
        {hasBreakout && orb ? (
          <span className={volColor(orb.breakout.rvol)}>{orb.breakout.rvol.toFixed(1)}</span>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* Retest */}
      <td className="px-1 text-center font-mono">
        {orb && hasBreakout ? (
          orb.retest.holdConfirmed ? (
            <span className="text-emerald-400 font-medium">Y</span>
          ) : orb.retest.touched ? (
            <span className="text-yellow-400">T</span>
          ) : (
            <span className="text-zinc-400">{orb.retest.barsSinceBreak}/{orb.retest.maxRetestBars}</span>
          )
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* Confidence — inline bar */}
      <td className="px-1">
        {orb ? (
          <div className="flex items-center gap-1">
            <div className="h-1.5 w-8 rounded-full bg-zinc-800 overflow-hidden">
              <div
                className={`h-full rounded-full ${confidenceColor(orb.confidence)}`}
                style={{ width: `${orb.confidence * 100}%` }}
              />
            </div>
            <span className="text-zinc-500 text-[10px]">{(orb.confidence * 100).toFixed(0)}%</span>
          </div>
        ) : (
          <span className="text-zinc-700">{"\u2014"}</span>
        )}
      </td>

      {/* Detail panel button */}
      <td className="px-1 pr-2 text-center">
        <button
          ref={btnRef}
          className="inline-flex items-center justify-center h-5 w-5 rounded hover:bg-zinc-700 text-zinc-500 hover:text-zinc-300 transition-colors"
          onClick={(e) => {
            e.stopPropagation();
            if (open) {
              setOpen(false);
            } else {
              setAnchorRect(btnRef.current?.getBoundingClientRect() ?? null);
              setOpen(true);
            }
          }}
        >
          <Info className="h-3.5 w-3.5" />
        </button>
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
  { label: "", span: 2 },                    // Symbol + Price
  { label: "AVWAP", span: 9 },               // Bias, Score, Vol, Slope, Fib, Key, Cndl, Band, Gate
  { label: "ORB", span: 5 },                 // Phase, Dir, RVOL, Retest, Conf
  { label: "", span: 1 },                    // Detail button
] as const;

const COLUMNS = [
  "Sym", "Price",
  "Bias", "Score", "Vol", "Slope", "Fib", "Key", "Cndl", "Band", "Gate",
  "Phase", "Dir", "RVOL", "Retest", "Conf",
  "",
] as const;

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function SignalProgressTable({ avwapProgress, orbProgress }: SignalProgressTableProps) {
  const rows: UnifiedRow[] = useMemo(() => {
    const symbols = new Set<string>();
    for (const key of avwapProgress.keys()) symbols.add(key);
    for (const key of orbProgress.keys()) symbols.add(key);

    const result: UnifiedRow[] = [];
    for (const symbol of symbols) {
      const avwap = avwapProgress.get(symbol);
      const orb = orbProgress.get(symbol);
      const bar = avwap?.bar ?? orb?.bar;
      const avwapScore = avwap ? avwap.confluence.score : 0;
      const orbScore = orb ? orb.confidence * 10 : 0;
      const compositeScore = Math.max(avwapScore, orbScore);
      result.push({ symbol, avwap, orb, bar, compositeScore });
    }

    result.sort((a, b) => b.compositeScore - a.compositeScore);
    return result;
  }, [avwapProgress, orbProgress]);

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
                key={col}
                className={`px-1 py-1 text-[10px] font-medium text-zinc-500 ${
                  i <= 1 ? "text-left" : "text-center"
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
