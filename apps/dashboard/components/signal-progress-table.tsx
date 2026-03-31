"use client";

import { useState, useMemo } from "react";
import { ChevronRight } from "lucide-react";
import type { EntryGatedPayload, ORBPhaseUpdatePayload, BarSnapshot } from "@/lib/types";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

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
// AVWAP helper functions (ported from avwap-confluence-matrix.tsx)
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

function scoreBarColor(score: number): string {
  if (score >= 7) return "bg-emerald-500";
  if (score >= 5) return "bg-yellow-500";
  return "bg-red-500";
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
// ORB helper functions (ported from orb-phase-timeline.tsx)
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

// ---------------------------------------------------------------------------
// Collapsed row chips
// ---------------------------------------------------------------------------

function avwapChipColor(score: number): string {
  if (score >= 7) return "border-emerald-500/40 bg-emerald-500/10 text-emerald-300";
  if (score >= 5) return "border-yellow-500/40 bg-yellow-500/10 text-yellow-300";
  return "border-red-500/40 bg-red-500/10 text-red-300";
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

function topConfluenceFactors(avwap: EntryGatedPayload): string {
  const c = avwap.confluence;
  const parts: string[] = [];
  if (c.fib) parts.push("fib");
  if (c.keyLevel) parts.push("key");
  if (c.candle) parts.push("candle");
  if (c.band) parts.push("band");
  if (parts.length === 0) return "";
  return parts.slice(0, 2).join("+");
}

// ---------------------------------------------------------------------------
// AVWAPChip (collapsed summary)
// ---------------------------------------------------------------------------

function AVWAPChip({ avwap }: { avwap: EntryGatedPayload }) {
  const score = avwap.confluence.score;
  const factors = topConfluenceFactors(avwap);
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs ${avwapChipColor(score)}`}
    >
      <span className={`font-medium ${biasColor(avwap.indicators.avwapBias)}`}>
        {avwap.indicators.avwapBias === "LONG" ? "L" : avwap.indicators.avwapBias === "SHORT" ? "S" : "-"}
      </span>
      <span>{score}/{avwap.confluence.maxScore}</span>
      {factors && <span className="text-zinc-500">{factors}</span>}
    </span>
  );
}

// ---------------------------------------------------------------------------
// ORBChip (collapsed summary)
// ---------------------------------------------------------------------------

function ORBChip({ orb }: { orb: ORBPhaseUpdatePayload }) {
  const hasBreakout = !!orb.breakout.direction;
  const phase = orb.phase;

  let detail = abbreviatePhase(phase);
  if (hasBreakout && (phase === "BREAKOUT_SEEN" || phase === "AWAITING_RETEST" || phase === "RETEST_CONFIRMED" || phase === "SIGNAL_FIRED")) {
    const dir = orb.breakout.direction === "LONG" ? "L" : "S";
    detail = `${abbreviatePhase(phase)} ${dir} ${orb.breakout.rvol.toFixed(1)}x`;
  }
  if (phase === "AWAITING_RETEST") {
    detail = `RETEST ${orb.retest.barsSinceBreak}/${orb.retest.maxRetestBars}`;
  }

  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs ${orbChipColor(phase)}`}
    >
      {detail}
    </span>
  );
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
// FactorCell (expanded AVWAP detail)
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
// AVWAPDetail (expanded panel, left side)
// ---------------------------------------------------------------------------

function AVWAPDetail({ avwap }: { avwap: EntryGatedPayload }) {
  const c = avwap.confluence;
  const ind = avwap.indicators;
  const pct = c.maxScore > 0 ? (c.score / c.maxScore) * 100 : 0;

  return (
    <div className="space-y-3">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">AVWAP Confluence</h4>
      <div className="flex flex-wrap items-start gap-4">
        {/* Bias */}
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Bias</span>
          <span className={`text-xs font-medium ${biasColor(ind.avwapBias)}`}>
            {ind.avwapBias || "\u2014"}
          </span>
        </div>

        {/* Vol */}
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Vol</span>
          <span className={`text-xs ${volColor(ind.volumeRatio)}`}>
            {ind.volumeRatio.toFixed(1)}x
          </span>
        </div>

        {/* Slope */}
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Slope</span>
          <span className={`text-xs ${slopeColor(ind.slopeBPS)}`}>
            {ind.slopeBPS.toFixed(1)}
          </span>
        </div>

        {/* Factors */}
        <FactorValue label="Fib(+3)" active={c.fib} detail={c.fibDetail} />
        <FactorValue label="Key Lvl(+3)" active={c.keyLevel} detail={c.keyLevelDetail} />
        <FactorValue label="Candle(+2)" active={c.candle} detail={c.candleDetail} />
        <FactorValue label="Band(+2)" active={c.band} detail={c.band ? "yes" : ""} />

        {/* Score */}
        <div className="flex flex-col gap-0.5">
          <span className="text-[10px] text-zinc-500 uppercase">Score</span>
          <div className="flex items-center gap-2">
            <div className="relative h-4 w-20 rounded-full bg-zinc-800 overflow-hidden">
              <div
                className={`absolute inset-y-0 left-0 rounded-full ${scoreBarColor(c.score)}`}
                style={{ width: `${pct}%` }}
              />
              <span className="absolute inset-0 flex items-center justify-center text-[10px] font-medium text-zinc-100">
                {c.score}/{c.maxScore}
              </span>
            </div>
          </div>
        </div>

        {/* Blocking Gate */}
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
// ORBDetail (expanded panel, right side)
// ---------------------------------------------------------------------------

function ORBDetail({ orb }: { orb: ORBPhaseUpdatePayload }) {
  const hasBreakout = !!orb.breakout.direction;

  return (
    <div className="space-y-3">
      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">ORB Phase</h4>

      {/* Phase dots */}
      <PhaseIndicator phase={orb.phase} />

      <div className="flex flex-wrap items-start gap-4">
        {/* Range */}
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

        {/* Breakout */}
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

        {/* Retest */}
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

        {/* Confidence */}
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
// ScoreBar (mini progress bar for composite score)
// ---------------------------------------------------------------------------

function ScoreBar({ score, max }: { score: number; max: number }) {
  const pct = max > 0 ? (score / max) * 100 : 0;
  return (
    <div className="flex items-center gap-2">
      <div className="relative h-4 w-20 rounded-full bg-zinc-800 overflow-hidden">
        <div
          className={`absolute inset-y-0 left-0 rounded-full ${scoreBarColor(score)}`}
          style={{ width: `${pct}%` }}
        />
        <span className="absolute inset-0 flex items-center justify-center text-[10px] font-medium text-zinc-100">
          {score.toFixed(1)}/10
        </span>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function SignalProgressTable({ avwapProgress, orbProgress }: SignalProgressTableProps) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  const toggleExpand = (symbol: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(symbol)) {
        next.delete(symbol);
      } else {
        next.add(symbol);
      }
      return next;
    });
  };

  // Build unified rows sorted by composite score descending
  const rows: UnifiedRow[] = useMemo(() => {
    const symbols = new Set<string>();
    for (const key of avwapProgress.keys()) symbols.add(key);
    for (const key of orbProgress.keys()) symbols.add(key);

    const result: UnifiedRow[] = [];
    for (const symbol of symbols) {
      const avwap = avwapProgress.get(symbol);
      const orb = orbProgress.get(symbol);

      // Pick bar from whichever source is available
      const bar = avwap?.bar ?? orb?.bar;

      // Composite score: max of AVWAP score (0-10) and ORB confidence * 10
      const avwapScore = avwap ? avwap.confluence.score : 0;
      const orbScore = orb ? orb.confidence * 10 : 0;
      const compositeScore = Math.max(avwapScore, orbScore);

      result.push({ symbol, avwap, orb, bar, compositeScore });
    }

    result.sort((a, b) => b.compositeScore - a.compositeScore);
    return result;
  }, [avwapProgress, orbProgress]);

  // Empty state
  if (rows.length === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-muted-foreground text-sm">
        Waiting for signal data...
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900 overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow className="border-zinc-800 hover:bg-transparent">
            <TableHead className="text-zinc-400 text-xs w-8" />
            <TableHead className="text-zinc-400 text-xs">Symbol</TableHead>
            <TableHead className="text-zinc-400 text-xs text-right">Price</TableHead>
            <TableHead className="text-zinc-400 text-xs">AVWAP</TableHead>
            <TableHead className="text-zinc-400 text-xs">ORB</TableHead>
            <TableHead className="text-zinc-400 text-xs">Score</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row, idx) => {
            const isExpanded = !collapsed.has(row.symbol);

            return (
              <CollapsibleRow
                key={row.symbol}
                row={row}
                isExpanded={isExpanded}
                onToggle={() => toggleExpand(row.symbol)}
                even={idx % 2 === 0}
              />
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// CollapsibleRow — renders the collapsed + expanded rows as adjacent <tr>s
// ---------------------------------------------------------------------------

function CollapsibleRow({
  row,
  isExpanded,
  onToggle,
  even,
}: {
  row: UnifiedRow;
  isExpanded: boolean;
  onToggle: () => void;
  even: boolean;
}) {
  const { symbol, avwap, orb, bar } = row;

  const priceUp = bar ? bar.close >= bar.open : true;
  const rowBg = even ? "bg-zinc-950/80" : "bg-zinc-900/60";

  return (
    <>
      {/* Collapsed row */}
      <TableRow
        className={`border-zinc-800 cursor-pointer ${rowBg}`}
        onClick={onToggle}
      >
        {/* Chevron */}
        <TableCell className="py-2 w-8 pr-0">
          <ChevronRight
            className={`h-4 w-4 text-zinc-500 transition-transform duration-150 ${
              isExpanded ? "rotate-90" : ""
            }`}
          />
        </TableCell>

        {/* Symbol */}
        <TableCell className="py-2 font-bold font-mono text-zinc-100">
          {symbol}
        </TableCell>

        {/* Price */}
        <TableCell className="py-2 text-right">
          {bar?.close ? (
            <>
              <span
                className={`font-mono text-xs ${priceUp ? "text-emerald-400" : "text-red-400"}`}
              >
                {bar.close.toFixed(2)}
              </span>
              <span className="text-zinc-600 text-[10px] ml-1">
                H{bar.high.toFixed(2)} L{bar.low.toFixed(2)}
              </span>
            </>
          ) : (
            <span className="text-zinc-600 text-xs">{"\u2014"}</span>
          )}
        </TableCell>

        {/* AVWAP chip */}
        <TableCell className="py-2">
          {avwap ? <AVWAPChip avwap={avwap} /> : <span className="text-zinc-600 text-xs">{"\u2014"}</span>}
        </TableCell>

        {/* ORB chip */}
        <TableCell className="py-2">
          {orb ? <ORBChip orb={orb} /> : <span className="text-zinc-600 text-xs">{"\u2014"}</span>}
        </TableCell>

        {/* Composite score */}
        <TableCell className="py-2">
          <ScoreBar score={row.compositeScore} max={10} />
        </TableCell>
      </TableRow>

      {/* Expanded detail panel */}
      {isExpanded && (
        <TableRow className={`border-zinc-800 hover:bg-transparent ${rowBg}`}>
          <TableCell colSpan={6} className="p-0">
            <div className="border-l-2 border-zinc-700 p-4 ml-4">
              <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
                {/* AVWAP detail */}
                <div>
                  {avwap ? (
                    <AVWAPDetail avwap={avwap} />
                  ) : (
                    <div className="space-y-3">
                      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">AVWAP Confluence</h4>
                      <span className="text-xs text-zinc-600">No data</span>
                    </div>
                  )}
                </div>

                {/* ORB detail */}
                <div>
                  {orb ? (
                    <ORBDetail orb={orb} />
                  ) : (
                    <div className="space-y-3">
                      <h4 className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">ORB Phase</h4>
                      <span className="text-xs text-zinc-600">No data</span>
                    </div>
                  )}
                </div>
              </div>
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}
