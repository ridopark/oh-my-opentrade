"use client";

import { useStateEvents } from "@/lib/event-stream";
import { formatPercent } from "@/lib/format";
import type { StrategyDNAEvent, StrategyDNA } from "@/lib/types";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Dna, ArrowRight, TrendingUp, TrendingDown, Minus } from "lucide-react";
import { useMemo } from "react";
import { useCurrentStrategy } from "@/hooks/queries";

function isStrategyDNAPayload(payload: unknown): payload is StrategyDNAEvent {
  if (!payload || typeof payload !== "object") return false;
  const p = payload as Record<string, unknown>;
  return (
    "current" in p &&
    typeof p.current === "object" &&
    p.current !== null &&
    "version" in (p.current as Record<string, unknown>) &&
    "parameters" in (p.current as Record<string, unknown>)
  );
}

function ParamCard({
  label,
  value,
  previousValue,
}: {
  label: string;
  value: string | number | boolean;
  previousValue?: string | number | boolean;
}) {
  const changed = previousValue !== undefined && previousValue !== value;
  const numericDiff =
    changed && typeof value === "number" && typeof previousValue === "number"
      ? value - previousValue
      : null;

  return (
    <div
      className={`rounded-md border p-3 ${changed ? "border-yellow-500/30 bg-yellow-500/5" : "border-border"}`}
    >
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="mt-1 flex items-center gap-2">
        <span className="font-mono text-sm font-medium">{String(value)}</span>
        {changed && previousValue !== undefined && (
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
            <ArrowRight className="h-3 w-3" />
            <span className="line-through">{String(previousValue)}</span>
            {numericDiff !== null && (
              <span
                className={
                  numericDiff > 0
                    ? "text-emerald-400"
                    : numericDiff < 0
                      ? "text-red-400"
                      : ""
                }
              >
                ({numericDiff > 0 ? "+" : ""}
                {numericDiff.toFixed(2)})
              </span>
            )}
          </span>
        )}
      </div>
    </div>
  );
}

function MetricCard({
  label,
  value,
  previousValue,
  format = "number",
}: {
  label: string;
  value: number;
  previousValue?: number;
  format?: "number" | "percent";
}) {
  const displayValue =
    format === "percent" ? formatPercent(value) : value.toFixed(2);
  const diff =
    previousValue !== undefined ? value - previousValue : null;

  return (
    <div className="rounded-md border border-border p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="mt-1 flex items-center gap-2">
        <span className="font-mono text-lg font-bold">{displayValue}</span>
        {diff !== null && (
          <span className="flex items-center gap-0.5 text-xs">
            {diff > 0.001 ? (
              <TrendingUp className="h-3 w-3 text-emerald-400" />
            ) : diff < -0.001 ? (
              <TrendingDown className="h-3 w-3 text-red-400" />
            ) : (
              <Minus className="h-3 w-3 text-muted-foreground" />
            )}
            <span
              className={
                diff > 0.001
                  ? "text-emerald-400"
                  : diff < -0.001
                    ? "text-red-400"
                    : "text-muted-foreground"
              }
            >
              {diff > 0 ? "+" : ""}
              {format === "percent"
                ? formatPercent(diff)
                : diff.toFixed(2)}
            </span>
          </span>
        )}
      </div>
    </div>
  );
}

function DNAView({ dna, previous }: { dna: StrategyDNA; previous: StrategyDNA | null }) {
  const paramKeys = Object.keys(dna.parameters);
  const metricKeys = Object.keys(dna.performanceMetrics);

  const percentMetrics = new Set([
    "winRate",
    "maxDrawdown",
    "avgWin",
    "avgLoss",
  ]);

  return (
    <div className="space-y-6">
      {/* Parameters */}
      <div>
        <h3 className="mb-3 text-sm font-semibold text-foreground">
          Strategy Parameters
        </h3>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
          {paramKeys.map((key) => (
            <ParamCard
              key={key}
              label={key
                .replace(/_/g, " ")
                .replace(/([A-Z])/g, " $1")
                .replace(/^./, (s) => s.toUpperCase())}
              value={dna.parameters[key]}
              previousValue={previous?.parameters[key]}
            />
          ))}
        </div>
      </div>

      {metricKeys.length > 0 && (
        <div>
          <h3 className="mb-3 text-sm font-semibold text-foreground">
            Performance Metrics
          </h3>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
            {metricKeys.map((key) => (
              <MetricCard
                key={key}
                  label={key
                    .replace(/_/g, " ")
                    .replace(/([A-Z])/g, " $1")
                    .replace(/^./, (s) => s.toUpperCase())}
                value={dna.performanceMetrics[key]}
                previousValue={previous?.performanceMetrics[key]}
                format={percentMetrics.has(key) ? "percent" : "number"}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

export default function DNAPage() {
  const { states, connected } = useStateEvents(50);
  const { data: fetchedDNA } = useCurrentStrategy();

  // Find the latest DNA event from SSE stream
  const latestDNA = useMemo(() => {
    for (const event of states) {
      if (isStrategyDNAPayload(event.payload)) {
        return event.payload;
      }
    }
    return null;
  }, [states]);

  // Priority: SSE event > REST fetch > hardcoded fallback
  const currentDNA = latestDNA?.current ?? fetchedDNA ?? {
    id: "orb_break_retest",
    version: 1,
    description: "Opening Range Breakout — Break & Retest",
    parameters: {
      orb_window_minutes: 30,
      min_rvol: 1.5,
      max_risk_bps: 200,
      limit_offset_bps: 5,
      stop_bps_below_low: 10,
      min_confidence: 0.65,
      breakout_confirm_bps: 2,
      touch_tolerance_bps: 2,
      hold_confirm_bps: 0,
      max_retest_bars: 15,
      allow_missing_range_bars: 1,
      max_signals_per_session: 1,
    },
    performanceMetrics: {},
  };
  const previousDNA = latestDNA?.previous ?? null;
  const strategyName = currentDNA.id || "orb_break_retest";
  const strategyDescription = currentDNA.description || "Active strategy DNA — Paper trading mode";

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold text-foreground">
            <Dna className="h-6 w-6" />
            Strategy DNA
          </h1>
          <p className="text-sm text-muted-foreground">
            Current strategy configuration and performance metrics
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="outline" className="font-mono">
            v{currentDNA.version}
          </Badge>
          {previousDNA && (
            <Badge
              variant="outline"
              className="bg-yellow-500/10 text-yellow-400"
            >
              Changed from v{previousDNA.version}
            </Badge>
          )}
          <Badge
            variant={connected ? "default" : "destructive"}
            className="gap-1"
          >
            <div
              className={`h-2 w-2 rounded-full ${connected ? "bg-emerald-400 animate-pulse" : "bg-red-400"}`}
            />
            {connected ? "Live" : "Offline"}
          </Badge>
        </div>
      </div>

      {/* DNA Card */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div>
              <CardTitle>
                {strategyName}{" "}
                <Badge variant="outline" className="ml-2 font-mono text-xs">
                  v{currentDNA.version}
                </Badge>
              </CardTitle>
              <CardDescription>
                {strategyDescription}
              </CardDescription>
            </div>
            {!latestDNA && !fetchedDNA && (
              <Badge variant="outline" className="text-xs text-muted-foreground">
                Default config (awaiting backend)
              </Badge>
            )}
          </div>
        </CardHeader>
        <CardContent>
          <DNAView dna={currentDNA} previous={previousDNA} />
        </CardContent>
      </Card>

      {/* DNA Diff */}
      {previousDNA && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">
              Version Diff: v{previousDNA.version} → v{currentDNA.version}
            </CardTitle>
            <CardDescription>
              Parameters that changed between versions
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              {Object.keys(currentDNA.parameters)
                .filter(
                  (key) =>
                    previousDNA.parameters[key] !== currentDNA.parameters[key]
                )
                .map((key) => (
                  <div
                    key={key}
                    className="flex items-center justify-between rounded-md bg-muted/50 px-4 py-2 text-sm"
                  >
                    <span className="font-medium">
                      {key
                        .replace(/_/g, " ")
                        .replace(/([A-Z])/g, " $1")
                        .replace(/^./, (s) => s.toUpperCase())}
                    </span>
                    <div className="flex items-center gap-3 font-mono text-sm">
                      <span className="text-red-400 line-through">
                        {String(previousDNA.parameters[key])}
                      </span>
                      <ArrowRight className="h-3 w-3 text-muted-foreground" />
                      <span className="text-emerald-400">
                        {String(currentDNA.parameters[key])}
                      </span>
                    </div>
                  </div>
                ))}
              {Object.keys(currentDNA.parameters).filter(
                (key) =>
                  previousDNA.parameters[key] !== currentDNA.parameters[key]
              ).length === 0 && (
                <p className="py-4 text-center text-sm text-muted-foreground">
                  No parameter changes between versions
                </p>
              )}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
