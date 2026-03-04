"use client";

import { useEventStream } from "@/lib/event-stream";
import { MultiSymbolChart } from "@/components/multi-symbol-chart";
import { isMarketOpen, relativeTime, formatNumber } from "@/lib/format";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Activity,
  Container,
  Radio,
  TrendingUp,
  Clock,
  Zap,
} from "lucide-react";
import { useMemo, useEffect, useState } from "react";

interface ServiceStatus {
  name: string;
  healthy: boolean;
  detail?: string;
}

interface HealthResponse {
  healthy: boolean;
  services: ServiceStatus[];
}

function useServiceHealth() {
  const [health, setHealth] = useState<HealthResponse | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function poll() {
      try {
        const res = await fetch("/api/health/services");
        if (!res.ok) throw new Error(`status ${res.status}`);
        const data: HealthResponse = await res.json();
        if (!cancelled) setHealth(data);
      } catch {
        if (!cancelled) setHealth({ healthy: false, services: [] });
      }
    }
    poll();
    const id = setInterval(poll, 10_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  return health;
}

function EventPayloadSummary({
  type,
  payload,
}: {
  type: string;
  payload: unknown;
}) {
  const p = payload as Record<string, unknown>;

  if (type === "MarketBarSanitized") {
    const fmt = (v: unknown) => (typeof v === "number" ? v.toFixed(2) : "—");
    return (
      <span className="flex flex-wrap items-center gap-x-2 font-mono text-xs">
        <span className="font-medium text-foreground">{String(p.Symbol ?? "—")}</span>
        <span className="text-muted-foreground">{String(p.Timeframe ?? "")}</span>
        <span>
          <span className="text-muted-foreground">O:</span>
          <span className="text-foreground">{fmt(p.Open)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">H:</span>
          <span className="text-foreground">{fmt(p.High)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">L:</span>
          <span className="text-foreground">{fmt(p.Low)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">C:</span>
          <span className="text-foreground">{fmt(p.Close)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">Vol:</span>
          <span className="text-foreground">{String(p.Volume ?? "—")}</span>
        </span>
        {p.Suspect === true && (
          <Badge variant="destructive" className="text-[10px] px-1 py-0">⚠ suspect</Badge>
        )}
      </span>
    );
  }

  if (type === "StateUpdated") {
    const f1 = (v: unknown) => (typeof v === "number" ? v.toFixed(1) : "—");
    const f2 = (v: unknown) => (typeof v === "number" ? v.toFixed(2) : "—");
    return (
      <span className="flex flex-wrap items-center gap-x-2 font-mono text-xs">
        <span className="font-medium text-foreground">{String(p.Symbol ?? "—")}</span>
        <span className="text-muted-foreground">{String(p.Timeframe ?? "")}</span>
        <span>
          <span className="text-muted-foreground">RSI:</span>
          <span className="text-foreground">{f1(p.RSI)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">K:</span>
          <span className="text-foreground">{f1(p.StochK)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">D:</span>
          <span className="text-foreground">{f1(p.StochD)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">EMA9:</span>
          <span className="text-foreground">{f2(p.EMA9)}</span>
        </span>
        <span>
          <span className="text-muted-foreground">EMA21:</span>
          <span className="text-foreground">{f2(p.EMA21)}</span>
        </span>
      </span>
    );
  }

  if (
    type === "OrderIntentCreated" ||
    type === "OrderIntentValidated" ||
    type === "OrderIntentRejected" ||
    type === "OrderSubmitted"
  ) {
    const confidence = typeof p.confidence === "number" ? (p.confidence * 100).toFixed(0) + "%" : "—";
    const limit = typeof p.limitPrice === "number" ? p.limitPrice.toFixed(2) : "—";
    const stop = typeof p.stopLoss === "number" ? p.stopLoss.toFixed(2) : "—";
    return (
      <span className="flex flex-wrap items-center gap-x-2 font-mono text-xs">
        <span className="font-medium text-foreground">{String(p.symbol ?? "—")}</span>
        <span className="text-foreground">{String(p.direction ?? "—")}</span>
        <span>
          <span className="text-muted-foreground">Lim:</span>
          <span className="text-foreground">{limit}</span>
        </span>
        <span>
          <span className="text-muted-foreground">SL:</span>
          <span className="text-foreground">{stop}</span>
        </span>
        <span>
          <span className="text-muted-foreground">Qty:</span>
          <span className="text-foreground">{String(p.quantity ?? "—")}</span>
        </span>
        <span className="text-foreground">{confidence}</span>
      </span>
    );
  }

  if (type === "FillReceived") {
    const price = typeof p.price === "number" ? p.price.toFixed(2) : "—";
    return (
      <span className="flex flex-wrap items-center gap-x-2 font-mono text-xs">
        <span className="font-medium text-foreground">{String(p.symbol ?? "—")}</span>
        <span className="text-foreground">{String(p.side ?? "—")}</span>
        <span className="text-foreground">{String(p.quantity ?? "—")}</span>
        <span className="text-muted-foreground">@</span>
        <span className="text-foreground">{price}</span>
      </span>
    );
  }

  if (type === "DebateCompleted") {
    const decision = p.decision as Record<string, unknown> | undefined;
    const conf = typeof decision?.confidence === "number"
      ? (decision.confidence * 100).toFixed(0) + "%"
      : "—";
    return (
      <span className="flex flex-wrap items-center gap-x-2 font-mono text-xs">
        <span className="font-medium text-foreground">{String(p.symbol ?? "—")}</span>
        <span className="text-foreground">{String(decision?.direction ?? "—")}</span>
        <span className="text-foreground">{conf}</span>
      </span>
    );
  }

  // Fallback
  const raw = JSON.stringify(payload);
  return (
    <span className="break-all text-muted-foreground">
      {raw.length > 120 ? raw.slice(0, 120) + "…" : raw}
    </span>
  );
}

export default function DashboardPage() {
  const { events, connected } = useEventStream({ maxEvents: 200 });
  const serviceHealth = useServiceHealth();

  const stats = useMemo(() => {
    const marketOpen = isMarketOpen();
    const debates = events.filter((e) => e.type === "DebateCompleted").length;
    const orders = events.filter(
      (e) =>
        e.type === "OrderIntentCreated" ||
        e.type === "OrderIntentValidated" ||
        e.type === "OrderIntentRejected" ||
        e.type === "OrderSubmitted"
    ).length;
    const stateUpdates = events.filter(
      (e) => e.type === "StateUpdated"
    ).length;
    const lastEvent = events[0]?.occurredAt ?? null;

    const eventTypes = new Set(events.map((e) => e.type));

    return {
      marketOpen,
      debates,
      orders,
      stateUpdates,
      totalEvents: events.length,
      eventTypes: eventTypes.size,
      lastEvent,
    };
  }, [events]);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-foreground">
            System Dashboard
          </h1>
          <p className="text-sm text-muted-foreground">
            Real-time system health and activity monitoring
          </p>
        </div>
        <Badge
          variant={connected ? "default" : "destructive"}
          className="gap-1"
        >
          <div
            className={`h-2 w-2 rounded-full ${connected ? "bg-emerald-400 animate-pulse" : "bg-red-400"}`}
          />
          {connected ? "Connected" : "Disconnected"}
        </Badge>
      </div>

      {/* Stats Grid */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {/* Market Status */}
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <TrendingUp className="h-4 w-4" />
              Market Status
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex items-center gap-2">
              <div
                className={`h-3 w-3 rounded-full ${stats.marketOpen ? "bg-emerald-500 animate-pulse" : "bg-red-500"}`}
              />
              <CardTitle className="text-2xl">
                {stats.marketOpen ? "OPEN" : "CLOSED"}
              </CardTitle>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              NYSE / NASDAQ — ET hours
            </p>
          </CardContent>
        </Card>

        {/* Event Bus */}
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <Radio className="h-4 w-4" />
              Event Bus
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CardTitle className="text-2xl">
              {formatNumber(stats.totalEvents)}
            </CardTitle>
            <p className="mt-1 text-xs text-muted-foreground">
              {stats.eventTypes} event types active
            </p>
          </CardContent>
        </Card>

        {/* Services / health */}
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <Container className="h-4 w-4" />
              Services
            </CardDescription>
          </CardHeader>
          <CardContent>
            {serviceHealth === null ? (
              <CardTitle className="text-2xl text-muted-foreground">—</CardTitle>
            ) : (
              <CardTitle
                className={`text-2xl ${ serviceHealth.healthy ? "" : "text-destructive" }`}
              >
                {serviceHealth.services.filter((s) => s.healthy).length} /{" "}
                {serviceHealth.services.length}
              </CardTitle>
            )}
            <p className="mt-1 text-xs text-muted-foreground">
              {serviceHealth === null
                ? "Loading…"
                : serviceHealth.healthy
                ? "All services healthy"
                : serviceHealth.services
                    .filter((s) => !s.healthy)
                    .map((s) => s.name)
                    .join(", ") + " degraded"}
            </p>
          </CardContent>
        </Card>

        {/* Last Event */}
        <Card>
          <CardHeader className="pb-2">
            <CardDescription className="flex items-center gap-2">
              <Clock className="h-4 w-4" />
              Last Event
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CardTitle className="text-2xl">
              {stats.lastEvent ? relativeTime(stats.lastEvent) : "—"}
            </CardTitle>
            <p className="mt-1 text-xs text-muted-foreground">
              SSE stream latency
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Multi-Symbol Percent Chart */}
      <MultiSymbolChart />

      {/* Activity Breakdown */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Zap className="h-4 w-4 text-yellow-500" />
              Debates Completed
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-4xl font-bold tabular-nums">
              {stats.debates}
            </div>
            <div className="mt-4 space-y-2">
              {events
                .filter((e) => e.type === "DebateCompleted")
                .slice(0, 3)
                .map((e) => {
                  const payload = e.payload as {
                    symbol: string;
                    decision: { direction: string; confidence: number };
                  };
                  return (
                    <div
                      key={e.id}
                      className="flex items-center justify-between rounded-md bg-muted/50 px-3 py-2 text-sm"
                    >
                      <span className="font-mono font-medium">
                        {payload.symbol}
                      </span>
                      <div className="flex items-center gap-2">
                        <Badge
                          variant={
                            payload.decision.direction === "LONG"
                              ? "default"
                              : "destructive"
                          }
                          className={
                            payload.decision.direction === "LONG"
                              ? "bg-emerald-500/20 text-emerald-400 hover:bg-emerald-500/30"
                              : "bg-red-500/20 text-red-400 hover:bg-red-500/30"
                          }
                        >
                          {payload.decision.direction}
                        </Badge>
                        <span className="text-muted-foreground">
                          {(payload.decision.confidence * 100).toFixed(0)}%
                        </span>
                      </div>
                    </div>
                  );
                })}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Activity className="h-4 w-4 text-blue-500" />
              Order Intents
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-4xl font-bold tabular-nums">
              {stats.orders}
            </div>
            <div className="mt-4 space-y-2">
              {events
                .filter(
                  (e) =>
                    e.type === "OrderIntentCreated" ||
                    e.type === "OrderIntentValidated" ||
                    e.type === "OrderIntentRejected" ||
                    e.type === "OrderSubmitted"
                )
                .slice(0, 3)
                .map((e) => {
                  const payload = e.payload as {
                    symbol: string;
                    direction: string;
                    status: string;
                  };
                  return (
                    <div
                      key={e.id}
                      className="flex items-center justify-between rounded-md bg-muted/50 px-3 py-2 text-sm"
                    >
                      <span className="font-mono font-medium">
                        {payload.symbol}
                      </span>
                      <div className="flex items-center gap-2">
                        <Badge
                          variant={
                            payload.direction === "LONG"
                              ? "default"
                              : "destructive"
                          }
                          className={
                            payload.direction === "LONG"
                              ? "bg-emerald-500/20 text-emerald-400 hover:bg-emerald-500/30"
                              : "bg-red-500/20 text-red-400 hover:bg-red-500/30"
                          }
                        >
                          {payload.direction}
                        </Badge>
                        <Badge variant="outline" className="text-xs">
                          {payload.status}
                        </Badge>
                      </div>
                    </div>
                  );
                })}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Radio className="h-4 w-4 text-purple-500" />
              State Updates
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-4xl font-bold tabular-nums">
              {stats.stateUpdates}
            </div>
            <div className="mt-4 space-y-2">
              {events
                .filter((e) => e.type === "StateUpdated")
                .slice(0, 3)
                .map((e) => {
                  const payload = e.payload as {
                    Symbol?: string;
                    RSI?: number;
                    StochK?: number;
                    EMA9?: number;
                    EMA21?: number;
                  };
                  const rsi = payload.RSI;
                  const rsiColor = rsi == null ? '' : rsi < 30 ? 'text-red-400' : rsi > 70 ? 'text-yellow-400' : 'text-emerald-400';
                  return (
                    <div
                      key={e.id}
                      className="flex items-center justify-between rounded-md bg-muted/50 px-3 py-2 text-sm"
                    >
                      <span className="font-mono font-medium">
                        {payload.Symbol ?? "—"}
                      </span>
                      <div className="flex items-center gap-2 font-mono text-xs tabular-nums">
                        <span className="text-muted-foreground">RSI</span>
                        <span className={rsiColor}>{rsi != null ? rsi.toFixed(1) : "—"}</span>
                        <span className="text-muted-foreground">K</span>
                        <span>{payload.StochK != null ? payload.StochK.toFixed(1) : "—"}</span>
                        <span className="text-muted-foreground">EMA9</span>
                        <span>{payload.EMA9 != null ? payload.EMA9.toFixed(2) : "—"}</span>
                      </div>
                    </div>
                  );
                })}
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Event Stream */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Live Event Stream</CardTitle>
          <CardDescription>
            Most recent events across all channels
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="max-h-64 space-y-1 overflow-y-auto font-mono text-xs">
            {events.slice(0, 20).map((e) => (
              <div
                key={e.id}
                className="flex items-center gap-3 rounded px-2 py-1.5 hover:bg-muted/50"
              >
                <span className="text-muted-foreground">
                  {relativeTime(e.occurredAt)}
                </span>
                <Badge variant="outline" className="text-[10px] font-normal">
                  {e.type}
                </Badge>
                <EventPayloadSummary type={e.type} payload={e.payload} />
              </div>
            ))}
            {events.length === 0 && (
              <p className="py-4 text-center text-muted-foreground">
                Waiting for events...
              </p>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
