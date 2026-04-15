"use client";

import { cn } from "@/lib/utils";
import type { SymbolLiveness } from "@/lib/types";

interface LivenessCountersProps {
  // Pass either explicit counts (per-symbol row) OR a symbols list (card-level sum).
  ticks?: number | null;
  bars?: number | null;
  signals?: number | null;
  fills?: number | null;
  symbols?: SymbolLiveness[];
  className?: string;
  compact?: boolean;
}

const numFmt = new Intl.NumberFormat("en-US");

function fmt(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "\u2014";
  return numFmt.format(v);
}

function sumField(
  symbols: SymbolLiveness[],
  key: keyof Pick<
    SymbolLiveness,
    "evalCount" | "barsToday" | "signalCount" | "fillCount"
  >,
): number {
  let total = 0;
  for (const s of symbols) total += s[key] ?? 0;
  return total;
}

export function LivenessCounters({
  ticks,
  bars,
  signals,
  fills,
  symbols,
  className,
  compact = false,
}: LivenessCountersProps) {
  // Card-level uses sum-across-symbols.
  // Note: "Ticks" = evalCount (runner evaluations driven by tick/bar arrivals);
  // we intentionally use evalCount as the ticks counter per spec (no per-tick
  // integer is tracked in Phase 1 — evalCount is the closest proxy).
  const t =
    symbols && symbols.length > 0 ? sumField(symbols, "evalCount") : ticks ?? null;
  const b =
    symbols && symbols.length > 0 ? sumField(symbols, "barsToday") : bars ?? null;
  const sg =
    symbols && symbols.length > 0
      ? sumField(symbols, "signalCount")
      : signals ?? null;
  const f =
    symbols && symbols.length > 0 ? sumField(symbols, "fillCount") : fills ?? null;

  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-4 gap-y-1 text-xs tabular-nums",
        compact ? "text-[11px]" : "",
        className,
      )}
    >
      <Stat label="Ticks" value={fmt(t)} />
      <Stat label="Bars" value={fmt(b)} />
      <Stat label="Signals" value={fmt(sg)} />
      <Stat label="Fills" value={fmt(f)} />
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline gap-1">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono font-medium text-foreground">{value}</span>
    </div>
  );
}

export default LivenessCounters;
