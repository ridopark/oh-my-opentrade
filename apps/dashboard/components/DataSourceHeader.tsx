"use client";

import { useEffect, useState } from "react";
import { useDataSourceHealth } from "@/hooks/queries";
import { cn } from "@/lib/utils";
import type { DataSource } from "@/lib/types";

// Phase 3: dashboard-wide health strip. Designed to be subtle — one row of
// small dots + labels, no icons, no card chrome — so it sits above every
// page without competing for attention. When the backend endpoint isn't
// ready the four expected sources are rendered as grey "unknown" dots so
// the strip never collapses and never hides operational risk.

const EXPECTED_SOURCES: Array<{ id: string; label: string }> = [
  { id: "ibkr", label: "IBKR" },
  { id: "alpaca", label: "Alpaca" },
  { id: "omo-data", label: "omo-data" },
  { id: "database", label: "Database" },
];

function mergeSources(remote: DataSource[] | undefined): DataSource[] {
  const byId = new Map<string, DataSource>();
  for (const s of remote ?? []) byId.set(s.id, s);
  const merged: DataSource[] = [];
  // Preserve the expected order first so the strip is stable across reloads.
  for (const e of EXPECTED_SOURCES) {
    const found = byId.get(e.id);
    merged.push(
      found ?? {
        id: e.id,
        label: e.label,
        healthy: false,
        lastEventAt: null,
        detail: "unknown",
      },
    );
    byId.delete(e.id);
  }
  // Append any extra sources the backend returned we didn't expect.
  for (const s of byId.values()) merged.push(s);
  return merged;
}

function dotColor(source: DataSource, unknown: boolean): string {
  if (unknown) return "bg-muted-foreground/40";
  if (!source.healthy) return "bg-rose-500";
  if (source.lastEventAt === null) return "bg-muted-foreground/50";
  return "bg-emerald-500";
}

function formatAge(ts: string | null, nowMs: number): string {
  if (!ts) return "never";
  const t = new Date(ts).getTime();
  if (!Number.isFinite(t)) return "unknown";
  const ageS = Math.max(0, Math.floor((nowMs - t) / 1000));
  if (ageS < 60) return `${ageS}s ago`;
  const m = Math.floor(ageS / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ago`;
}

export function DataSourceHeader({ className }: { className?: string }) {
  const { data, isError, isLoading } = useDataSourceHealth();

  // Tick every second so "Xs ago" stays fresh without a refetch.
  const [nowMs, setNowMs] = useState<number>(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  const endpointUnknown = !data || isError;
  const sources = mergeSources(data?.sources);

  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-border/60 bg-muted/20 px-4 py-1.5 text-xs text-muted-foreground",
        className,
      )}
      aria-label="Data source health"
    >
      <span className="font-medium uppercase tracking-wide text-muted-foreground/70">
        Data sources
      </span>
      {sources.map((s) => {
        const unknown = endpointUnknown;
        const tooltip = unknown
          ? `${s.label}: unknown (endpoint unavailable)`
          : `${s.label}: ${s.healthy ? "healthy" : "unhealthy"} · last event ${formatAge(s.lastEventAt, nowMs)}${s.detail ? ` · ${s.detail}` : ""}`;
        return (
          <span
            key={s.id}
            className="flex items-center gap-1.5"
            title={tooltip}
          >
            <span
              aria-hidden
              className={cn(
                "inline-block h-2 w-2 rounded-full",
                dotColor(s, unknown),
              )}
            />
            <span className="text-foreground/80">{s.label}</span>
            <span className="tabular-nums text-muted-foreground">
              {unknown ? "unknown" : formatAge(s.lastEventAt, nowMs)}
            </span>
          </span>
        );
      })}
      {isLoading && !data && (
        <span className="text-muted-foreground/60">loading…</span>
      )}
    </div>
  );
}

export default DataSourceHeader;
