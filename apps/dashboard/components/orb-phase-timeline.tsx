"use client";

import type { ORBPhaseUpdatePayload } from "@/lib/types";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

interface ORBPhaseTimelineProps {
  data: Map<string, ORBPhaseUpdatePayload>;
}

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

// Sort: most progressed phases first (AWAITING_RETEST/BREAKOUT_SEEN = 3 first, etc.)
function phaseSortValue(phase: string): number {
  const step = phaseStep(phase);
  if (step === -1) return 100; // INVALID goes last
  return -step; // negate so higher steps come first
}

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

function confidenceColor(c: number): string {
  if (c >= 0.75) return "bg-emerald-500";
  if (c >= 0.5) return "bg-yellow-500";
  return "bg-red-500";
}

export function ORBPhaseTimeline({ data }: ORBPhaseTimelineProps) {
  if (data.size === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-muted-foreground text-sm">
        Waiting for ORB phase data...
      </div>
    );
  }

  const rows = Array.from(data.values()).sort(
    (a, b) => phaseSortValue(a.phase) - phaseSortValue(b.phase),
  );

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900 overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow className="border-zinc-800 hover:bg-transparent">
            <TableHead className="text-zinc-400 text-xs">Symbol</TableHead>
            <TableHead className="text-zinc-400 text-xs">Phase</TableHead>
            <TableHead className="text-zinc-400 text-xs">Range</TableHead>
            <TableHead className="text-zinc-400 text-xs">Breakout</TableHead>
            <TableHead className="text-zinc-400 text-xs">Retest</TableHead>
            <TableHead className="text-zinc-400 text-xs">Confidence</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => {
            const hasBreakout = !!row.breakout.direction;

            return (
              <TableRow key={row.symbol} className="border-zinc-800">
                {/* Symbol */}
                <TableCell className="py-2 font-bold font-mono text-zinc-100">
                  {row.symbol}
                </TableCell>

                {/* Phase */}
                <TableCell className="py-2">
                  <PhaseIndicator phase={row.phase} />
                </TableCell>

                {/* Range */}
                <TableCell className="py-2">
                  <div className="flex items-center gap-1.5">
                    <span
                      className={`h-2 w-2 rounded-full ${
                        row.range.valid ? "bg-emerald-500" : "bg-red-500"
                      }`}
                    />
                    <span className="text-zinc-200 text-xs">
                      {row.range.high.toFixed(2)} - {row.range.low.toFixed(2)}
                    </span>
                  </div>
                  <span className="text-zinc-500 text-xs">
                    ({row.range.barCount}/{row.range.expectedBars} bars)
                  </span>
                </TableCell>

                {/* Breakout */}
                <TableCell className="py-2">
                  {hasBreakout ? (
                    <div>
                      <span
                        className={`text-xs font-medium ${
                          row.breakout.direction === "LONG"
                            ? "text-emerald-400"
                            : "text-red-400"
                        }`}
                      >
                        {row.breakout.direction} @ {row.breakout.breakClose.toFixed(2)}
                      </span>
                      <span className="text-zinc-500 text-xs ml-1">
                        (RVOL {row.breakout.rvol.toFixed(1)}x)
                      </span>
                    </div>
                  ) : (
                    <span className="text-zinc-500 text-xs">Watching...</span>
                  )}
                </TableCell>

                {/* Retest */}
                <TableCell className="py-2">
                  {!hasBreakout ? (
                    <span className="text-zinc-600">{"\u2014"}</span>
                  ) : row.retest.holdConfirmed ? (
                    <span className="text-emerald-400 text-xs font-medium">
                      Confirmed
                    </span>
                  ) : row.retest.touched ? (
                    <span className="text-yellow-400 text-xs">
                      Touched @ {row.retest.touchPrice.toFixed(2)}
                    </span>
                  ) : (
                    <div className="flex flex-col gap-1">
                      <span className="text-zinc-400 text-xs">
                        Pending ({row.retest.barsSinceBreak}/{row.retest.maxRetestBars})
                      </span>
                      <div className="h-1 w-16 rounded-full bg-zinc-800 overflow-hidden">
                        <div
                          className="h-full rounded-full bg-zinc-500"
                          style={{
                            width: `${
                              row.retest.maxRetestBars > 0
                                ? (row.retest.barsSinceBreak / row.retest.maxRetestBars) * 100
                                : 0
                            }%`,
                          }}
                        />
                      </div>
                    </div>
                  )}
                </TableCell>

                {/* Confidence */}
                <TableCell className="py-2">
                  <div className="flex items-center gap-2">
                    <div className="h-2 w-16 rounded-full bg-zinc-800 overflow-hidden">
                      <div
                        className={`h-full rounded-full ${confidenceColor(row.confidence)}`}
                        style={{ width: `${row.confidence * 100}%` }}
                      />
                    </div>
                    <span className="text-zinc-400 text-xs">
                      {(row.confidence * 100).toFixed(0)}%
                    </span>
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
