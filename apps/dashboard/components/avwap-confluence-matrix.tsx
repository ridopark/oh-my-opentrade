"use client";

import type { EntryGatedPayload } from "@/lib/types";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

interface AVWAPConfluenceMatrixProps {
  data: Map<string, EntryGatedPayload>;
}

function biasColor(bias: string): string {
  if (bias === "LONG") return "text-emerald-400";
  if (bias === "SHORT") return "text-red-400";
  return "text-zinc-500";
}

function biasLabel(bias: string): string {
  if (bias === "LONG") return "LONG";
  if (bias === "SHORT") return "SHORT";
  return "\u2014";
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

function FactorCell({ active, detail }: { active: boolean; detail: string }) {
  if (!active) {
    return (
      <TableCell className="py-2 text-zinc-600">{"\u2014"}</TableCell>
    );
  }
  return (
    <TableCell className="py-2 bg-emerald-500/10">
      <span className="text-emerald-300 text-xs">{detail || "yes"}</span>
    </TableCell>
  );
}

export function AVWAPConfluenceMatrix({ data }: AVWAPConfluenceMatrixProps) {
  if (data.size === 0) {
    return (
      <div className="flex items-center justify-center py-12 text-muted-foreground text-sm">
        Waiting for AVWAP signal data...
      </div>
    );
  }

  const rows = Array.from(data.values()).sort(
    (a, b) => b.confluence.score - a.confluence.score,
  );

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900 overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow className="border-zinc-800 hover:bg-transparent">
            <TableHead className="text-zinc-400 text-xs">Symbol</TableHead>
            <TableHead className="text-zinc-400 text-xs">Bias</TableHead>
            <TableHead className="text-zinc-400 text-xs">Vol</TableHead>
            <TableHead className="text-zinc-400 text-xs">Slope</TableHead>
            <TableHead className="text-zinc-400 text-xs">Fib(+3)</TableHead>
            <TableHead className="text-zinc-400 text-xs">Key Lvl(+3)</TableHead>
            <TableHead className="text-zinc-400 text-xs">Candle(+2)</TableHead>
            <TableHead className="text-zinc-400 text-xs">Band(+2)</TableHead>
            <TableHead className="text-zinc-400 text-xs">Score</TableHead>
            <TableHead className="text-zinc-400 text-xs">Blocking Gate</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => {
            const c = row.confluence;
            const ind = row.indicators;
            const pct = c.maxScore > 0 ? (c.score / c.maxScore) * 100 : 0;

            return (
              <TableRow key={row.symbol} className="border-zinc-800">
                <TableCell className="py-2 font-bold font-mono text-zinc-100">
                  {row.symbol}
                </TableCell>
                <TableCell className={`py-2 font-medium ${biasColor(ind.avwapBias)}`}>
                  {biasLabel(ind.avwapBias)}
                </TableCell>
                <TableCell className={`py-2 ${volColor(ind.volumeRatio)}`}>
                  {ind.volumeRatio.toFixed(1)}x
                </TableCell>
                <TableCell className={`py-2 ${slopeColor(ind.slopeBPS)}`}>
                  {ind.slopeBPS.toFixed(1)}
                </TableCell>
                <FactorCell active={c.fib} detail={c.fibDetail} />
                <FactorCell active={c.keyLevel} detail={c.keyLevelDetail} />
                <FactorCell active={c.candle} detail={c.candleDetail} />
                <FactorCell active={c.band} detail={c.band ? "yes" : ""} />
                <TableCell className="py-2">
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
                </TableCell>
                <TableCell className="py-2">
                  {row.blockingGate ? (
                    <span
                      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium ${blockingGateColor(row.blockingGate)}`}
                    >
                      {row.blockingGate}
                    </span>
                  ) : (
                    <span className="text-emerald-400 text-xs">All passed</span>
                  )}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
