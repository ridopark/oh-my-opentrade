"use client";

import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";
import type { DecisionReason, DecisionOutcome } from "@/lib/types";

interface LastDecisionProps {
  decision?: DecisionReason | null;
  // Compact form hides the outcome word — used on the strategies list cards
  // where the pill already conveys liveness status and vertical space is tight.
  compact?: boolean;
  className?: string;
}

// Outcome → colour hint. HOLD is the common case so we keep it muted; ENTRY
// is the only reason a trader cares about acting right now, hence the
// emerald highlight.
const OUTCOME_CLASSES: Record<string, string> = {
  HOLD: "text-muted-foreground",
  ENTRY: "text-emerald-500",
  EXIT: "text-amber-500",
  SUPPRESSED: "text-rose-500",
};

function formatAge(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ago`;
}

export function LastDecision({ decision, compact, className }: LastDecisionProps) {
  // A 1-second ticker keeps the "Xs ago" label live without refetching.
  const [nowMs, setNowMs] = useState<number>(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  if (!decision) {
    return (
      <span
        className={cn("text-xs italic text-muted-foreground", className)}
        aria-label="Awaiting evaluation"
      >
        Awaiting evaluation…
      </span>
    );
  }

  const atMs = decision.at ? new Date(decision.at).getTime() : 0;
  const age = Number.isFinite(atMs) && atMs > 0 ? nowMs - atMs : null;
  const outcome: DecisionOutcome = decision.outcome ?? "HOLD";
  const colourClass = OUTCOME_CLASSES[outcome] ?? "text-muted-foreground";

  return (
    <span
      className={cn("text-xs tabular-nums", className)}
      title={`${outcome} — ${decision.summary}`}
    >
      {age !== null && (
        <span className="text-muted-foreground">{formatAge(age)} · </span>
      )}
      {!compact && (
        <span className={cn("font-medium", colourClass)}>{outcome}</span>
      )}
      {!compact && decision.summary && (
        <span className="text-muted-foreground"> — {decision.summary}</span>
      )}
      {compact && (
        <span className={cn(colourClass)}>{decision.summary || outcome}</span>
      )}
    </span>
  );
}

export default LastDecision;
