"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { useEventStream } from "@/lib/event-stream";
import { cn } from "@/lib/utils";
import type {
  DomainEvent,
  EventType,
  StrategyEvaluationPayload,
  StrategySignalEvent,
} from "@/lib/types";

// Phase 3: compact, expandable activity tail for a single strategy detail
// page. Intentionally client-side-filtered so we reuse the existing SSE
// subscription instead of adding a strategy-scoped endpoint — O(events)
// over a small max-50 buffer is trivial and keeps the backend surface thin.

const FEED_EVENT_TYPES: EventType[] = [
  "StrategyEvaluation",
  "StrategySignalLifecycle",
  "FillReceived",
];

const MAX_ENTRIES = 50;

interface ActivityFeedProps {
  strategyID: string;
  className?: string;
}

interface FeedEntry {
  key: string;       // stable React key (event id + timestamp)
  type: EventType;
  at: string;        // RFC3339 — what we actually render
  symbol: string;
  summary: string;
}

// ---- per-event summary extractors -----------------------------------------
//
// The server-sent payloads for these three event types have diverged
// conventions (camelCase for Phase-2 telemetry, PascalCase for the legacy
// perf signal log). Keeping the extraction logic here, not in a shared lib,
// avoids polluting domain types with UI-only formatting concerns.

function pickString(v: unknown): string | null {
  return typeof v === "string" && v.length > 0 ? v : null;
}

function extractStrategy(evt: DomainEvent): string | null {
  const p = (evt.payload ?? {}) as Record<string, unknown>;
  return (
    pickString(p.strategy) ??
    pickString(p.Strategy) ??
    pickString((p as { strategyID?: unknown }).strategyID) ??
    null
  );
}

function extractSymbol(evt: DomainEvent): string {
  const p = (evt.payload ?? {}) as Record<string, unknown>;
  return (
    pickString(p.symbol) ??
    pickString(p.Symbol) ??
    "—"
  );
}

function extractAt(evt: DomainEvent): string {
  const p = (evt.payload ?? {}) as Record<string, unknown>;
  return (
    pickString(p.at) ??
    pickString(p.TS) ??
    pickString((p as { time?: unknown }).time) ??
    evt.occurredAt ??
    new Date().toISOString()
  );
}

function summarize(evt: DomainEvent): string {
  const p = (evt.payload ?? {}) as Record<string, unknown>;
  switch (evt.type) {
    case "StrategyEvaluation": {
      const payload = evt.payload as StrategyEvaluationPayload | undefined;
      const dec = payload?.lastDecision;
      const outcome = dec?.outcome ?? "eval";
      const summary = dec?.summary ? ` — ${dec.summary}` : "";
      const evalCount = payload?.evalCount !== undefined && payload?.evalCount !== null
        ? ` #${payload.evalCount}`
        : "";
      return `${outcome}${summary}${evalCount}`;
    }
    case "StrategySignalLifecycle": {
      const sig = evt.payload as StrategySignalEvent | undefined;
      const side = sig?.Side ?? "";
      const kind = sig?.Kind ?? "signal";
      const status = sig?.Status ?? "";
      const reason = sig?.Reason ? ` — ${sig.Reason}` : "";
      return `${status} ${side} ${kind}${reason}`.trim();
    }
    case "FillReceived": {
      // Fill payload shape isn't tightly typed on the frontend yet — pull
      // the common fields defensively and fall back to a short placeholder.
      const side = pickString(p.side) ?? pickString(p.Side) ?? "";
      const qty = (p.quantity as number | undefined) ?? (p.Quantity as number | undefined);
      const price = (p.price as number | undefined) ?? (p.Price as number | undefined);
      const qtyStr = typeof qty === "number" ? qty.toString() : "";
      const priceStr = typeof price === "number" ? `@ ${price.toFixed(2)}` : "";
      const pieces = [side, qtyStr, priceStr].filter(Boolean);
      return pieces.length > 0 ? `FILL ${pieces.join(" ")}` : "FILL";
    }
    default:
      return "";
  }
}

function formatTime(ts: string): string {
  const d = new Date(ts);
  if (!Number.isFinite(d.getTime())) return "--:--:--";
  return d.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

const TYPE_STYLES: Record<string, string> = {
  StrategyEvaluation: "text-muted-foreground",
  StrategySignalLifecycle: "text-blue-400",
  FillReceived: "text-emerald-500",
};

const TYPE_LABELS: Record<string, string> = {
  StrategyEvaluation: "EVAL",
  StrategySignalLifecycle: "SIG ",
  FillReceived: "FILL",
};

export function ActivityFeed({ strategyID, className }: ActivityFeedProps) {
  const [open, setOpen] = useState(false);
  const { events, connected } = useEventStream({
    eventTypes: FEED_EVENT_TYPES,
    maxEvents: 200,
  });

  // Accumulate filtered entries in a ref so we retain history even when the
  // SSE window evicts older envelopes. Max 50 in-memory per plan §4.
  const entriesRef = useRef<FeedEntry[]>([]);
  const [version, setVersion] = useState(0);

  useEffect(() => {
    if (!events || events.length === 0) return;
    // Events arrive newest-first; walk oldest→newest so ordering stays stable.
    const next = entriesRef.current.slice();
    let dirty = false;
    for (let i = events.length - 1; i >= 0; i--) {
      const evt = events[i];
      if (!FEED_EVENT_TYPES.includes(evt.type)) continue;
      const strategy = extractStrategy(evt);
      if (strategy && strategy !== strategyID) continue;
      // For FillReceived events, `strategy` may be missing — without a
      // strategy label we can't attribute the fill, so filter it out rather
      // than show cross-strategy noise on a strategy-scoped page.
      if (!strategy) continue;

      const key = `${evt.id ?? ""}:${evt.type}`;
      if (next.some((e) => e.key === key)) continue;
      next.unshift({
        key,
        type: evt.type,
        at: extractAt(evt),
        symbol: extractSymbol(evt),
        summary: summarize(evt),
      });
      dirty = true;
    }
    if (dirty) {
      entriesRef.current = next.slice(0, MAX_ENTRIES);
      setVersion((v) => v + 1);
    }
  }, [events, strategyID]);

  const entries = useMemo(
    () => entriesRef.current,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [version],
  );

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className={cn("rounded-md border bg-card", className)}
    >
      <CollapsibleTrigger
        className="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-sm hover:bg-accent/40"
        aria-label={open ? "Collapse activity feed" : "Expand activity feed"}
      >
        <span className="flex items-center gap-2">
          {open ? (
            <ChevronDown className="h-4 w-4 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-4 w-4 text-muted-foreground" />
          )}
          <span className="font-medium">Activity</span>
          <span className="text-xs text-muted-foreground">
            {entries.length > 0
              ? `${entries.length} recent event${entries.length === 1 ? "" : "s"}`
              : connected
                ? "waiting for events…"
                : "disconnected"}
          </span>
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="max-h-80 overflow-auto border-t">
          {entries.length === 0 ? (
            <div className="px-3 py-4 text-xs text-muted-foreground">
              No activity captured yet. New events will stream in live.
            </div>
          ) : (
            <ul className="divide-y divide-border/60">
              {entries.map((e) => (
                <li
                  key={e.key}
                  className="flex items-start gap-3 px-3 py-1.5 font-mono text-xs leading-snug"
                >
                  <span className="tabular-nums text-muted-foreground">
                    {formatTime(e.at)}
                  </span>
                  <span
                    className={cn(
                      "w-12 shrink-0 font-semibold",
                      TYPE_STYLES[e.type] ?? "text-foreground",
                    )}
                  >
                    {TYPE_LABELS[e.type] ?? e.type.slice(0, 4).toUpperCase()}
                  </span>
                  <span className="w-16 shrink-0 font-medium text-foreground">
                    {e.symbol}
                  </span>
                  <span className="flex-1 truncate text-muted-foreground">
                    {e.summary}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

export default ActivityFeed;
