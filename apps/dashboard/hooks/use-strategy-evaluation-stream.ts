"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useEventStream } from "@/lib/event-stream";
import { useStrategyLiveness } from "@/hooks/queries";
import type {
  DomainEvent,
  StrategyEvaluationPayload,
  StrategyLiveness,
  SymbolLiveness,
} from "@/lib/types";

// Phase 2: merges polled liveness snapshot with live SSE StrategyEvaluation
// deltas. The polled endpoint seeds counters and feed health; SSE keeps
// lastEvalAt / evalCount / barsToday / lastDecision fresh between polls so
// pill pulses and "Xs ago" labels feel real-time rather than 2s-bucketed.
//
// Gracefully degrades: if SSE never delivers StrategyEvaluation (backend not
// yet shipped), the merge is a no-op and the polled data flows through as-is.

type EvalDelta = Pick<
  SymbolLiveness,
  "lastEvalAt" | "evalCount" | "barsToday" | "lastDecision"
>;

function keyFor(strategy: string, symbol: string): string {
  return `${strategy}:${symbol}`;
}

/**
 * Subscribes to SSE StrategyEvaluation events and exposes a keyed delta map.
 * Exported for consumers that want to combine with their own polled data.
 */
export function useStrategyEvaluationStream(): Map<string, EvalDelta> {
  const { events } = useEventStream({ eventTypes: ["StrategyEvaluation"] });

  // Accumulate deltas into a ref-backed Map so we retain older keys even if
  // the event window evicts the corresponding envelope.
  const mapRef = useRef<Map<string, EvalDelta>>(new Map());
  const [version, setVersion] = useState(0);

  useEffect(() => {
    if (!events || events.length === 0) return;
    let dirty = false;
    // Events come newest-first; walk in reverse so older arrivals don't
    // overwrite newer ones for the same (strategy, symbol) pair.
    for (let i = events.length - 1; i >= 0; i--) {
      const evt = events[i] as DomainEvent<StrategyEvaluationPayload>;
      if (evt.type !== "StrategyEvaluation") continue;
      const p = evt.payload;
      if (!p || !p.strategy || !p.symbol) continue;
      const k = keyFor(p.strategy, p.symbol);
      const prev = mapRef.current.get(k);
      // Skip stale arrivals (monotonic `at` guard).
      if (prev?.lastEvalAt && p.at && new Date(p.at).getTime() <= new Date(prev.lastEvalAt).getTime()) {
        continue;
      }
      mapRef.current.set(k, {
        lastEvalAt: p.at ?? prev?.lastEvalAt ?? null,
        evalCount: p.evalCount,
        barsToday: p.barsToday,
        lastDecision: p.lastDecision ?? prev?.lastDecision ?? null,
      });
      dirty = true;
    }
    if (dirty) setVersion((v) => v + 1);
  }, [events]);

  // Returning a fresh Map instance per version lets downstream memoisers
  // detect changes via referential identity.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => new Map(mapRef.current), [version]);
}

/**
 * Combines the polled `useStrategyLiveness` query with the SSE stream.
 * - Polled result seeds every field (feed health, counts, symbol list).
 * - SSE deltas override lastEvalAt / evalCount / barsToday / lastDecision
 *   for matching (strategy, symbol) keys.
 */
export function useStrategyLivenessLive(strategyID: string) {
  const polled = useStrategyLiveness(strategyID);
  const deltas = useStrategyEvaluationStream();

  const data = useMemo<StrategyLiveness | null>(() => {
    if (!polled.data) return polled.data ?? null;
    const base = polled.data;
    const merged = base.symbols.map((s) => {
      const d = deltas.get(keyFor(base.strategy, s.symbol));
      if (!d) return s;
      // Only adopt SSE delta if it is newer than the polled snapshot for this symbol.
      const polledMs = s.lastEvalAt ? new Date(s.lastEvalAt).getTime() : 0;
      const deltaMs = d.lastEvalAt ? new Date(d.lastEvalAt).getTime() : 0;
      if (deltaMs <= polledMs) return s;
      return {
        ...s,
        lastEvalAt: d.lastEvalAt ?? s.lastEvalAt,
        evalCount: d.evalCount ?? s.evalCount,
        barsToday: d.barsToday ?? s.barsToday,
        lastDecision: d.lastDecision ?? s.lastDecision,
      };
    });
    return { ...base, symbols: merged };
  }, [polled.data, deltas]);

  return { ...polled, data };
}
