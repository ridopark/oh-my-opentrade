"use client";

import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { SymbolLiveness } from "@/lib/types";

// Thresholds (seconds)
const GREEN_MAX_S = 15;
const AMBER_MAX_S = 60;

type PillStatus = "green" | "amber" | "red" | "grey";

interface LivenessPillProps {
  // Either pass a single tick timestamp + health, or a list of symbols
  // (card-level pill uses max-of-symbols = most recent tick, worst-case health).
  lastTickAt?: string | null;
  feedHealthy?: boolean;
  symbols?: SymbolLiveness[];
  className?: string;
}

function parseTime(ts: string | null | undefined): number | null {
  if (!ts) return null;
  const t = new Date(ts).getTime();
  return Number.isFinite(t) && t > 0 ? t : null;
}

function deriveStatus(
  mostRecentTickMs: number | null,
  nowMs: number,
  feedHealthy: boolean,
): { status: PillStatus; ageS: number | null } {
  if (mostRecentTickMs === null) return { status: "grey", ageS: null };
  const ageS = Math.max(0, Math.floor((nowMs - mostRecentTickMs) / 1000));
  if (!feedHealthy) return { status: "red", ageS };
  if (ageS < GREEN_MAX_S) return { status: "green", ageS };
  if (ageS <= AMBER_MAX_S) return { status: "amber", ageS };
  return { status: "red", ageS };
}

function formatAge(ageS: number | null): string {
  if (ageS === null) return "never";
  if (ageS < 60) return `${ageS}s ago`;
  const m = Math.floor(ageS / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ago`;
}

const STATUS_CLASSES: Record<PillStatus, string> = {
  green:
    "border-emerald-500/60 bg-emerald-500/10 text-emerald-500",
  amber:
    "border-amber-500/60 bg-amber-500/10 text-amber-500",
  red: "border-rose-500/60 bg-rose-500/10 text-rose-500",
  grey: "border-border bg-muted/40 text-muted-foreground",
};

const DOT_CLASSES: Record<PillStatus, string> = {
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-rose-500",
  grey: "bg-muted-foreground/50",
};

export function LivenessPill({
  lastTickAt,
  feedHealthy,
  symbols,
  className,
}: LivenessPillProps) {
  // Re-derive every second without refetching.
  const [nowMs, setNowMs] = useState<number>(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  // Card-level (symbols-array) takes max-of-symbols for lastTick
  // and "unhealthy if any symbol feed unhealthy" for feedHealthy.
  let mostRecentTickMs: number | null = null;
  let healthy = true;

  if (symbols && symbols.length > 0) {
    let hasAny = false;
    let allHealthy = true;
    for (const s of symbols) {
      const ms = parseTime(s.lastTickAt);
      if (ms !== null) {
        hasAny = true;
        if (mostRecentTickMs === null || ms > mostRecentTickMs) {
          mostRecentTickMs = ms;
        }
      }
      if (!s.feedHealthy) allHealthy = false;
    }
    healthy = hasAny ? allHealthy : true;
  } else {
    mostRecentTickMs = parseTime(lastTickAt);
    healthy = feedHealthy !== false; // undefined → assume healthy
  }

  const { status, ageS } = deriveStatus(mostRecentTickMs, nowMs, healthy);

  return (
    <Badge
      variant="outline"
      className={cn(
        "gap-1.5 font-medium tabular-nums",
        STATUS_CLASSES[status],
        className,
      )}
      aria-label={`Liveness: ${status}, last tick ${formatAge(ageS)}`}
    >
      <span
        className={cn("h-1.5 w-1.5 rounded-full", DOT_CLASSES[status])}
        aria-hidden
      />
      {formatAge(ageS)}
    </Badge>
  );
}

export default LivenessPill;
