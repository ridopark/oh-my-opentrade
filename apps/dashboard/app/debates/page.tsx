"use client";

import { useDebateEvents } from "@/lib/event-stream";
import { relativeTime } from "@/lib/format";
import type { DebateEvent } from "@/lib/types";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Swords, TrendingUp, TrendingDown, Scale } from "lucide-react";

function ConfidenceBar({ value }: { value: number }) {
  const pct = Math.round(value * 100);
  return (
    <div className="flex items-center gap-2">
      <div className="h-2 flex-1 rounded-full bg-muted">
        <div
          className={`h-2 rounded-full transition-all ${
            pct >= 80
              ? "bg-emerald-500"
              : pct >= 60
                ? "bg-yellow-500"
                : "bg-red-500"
          }`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="w-10 text-right text-xs font-mono tabular-nums text-muted-foreground">
        {pct}%
      </span>
    </div>
  );
}

function DebateCard({
  debate,
  occurredAt,
}: {
  debate: DebateEvent;
  occurredAt: string;
}) {
  const { decision, symbol, timeframe } = debate;
  const isLong = decision.direction === "LONG";

  return (
    <Card className="overflow-hidden">
      {/* Header bar */}
      <div
        className={`h-1 ${isLong ? "bg-emerald-500" : "bg-red-500"}`}
      />
      <CardHeader>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <CardTitle className="text-lg font-mono">{symbol}</CardTitle>
            <Badge variant="outline" className="text-xs">
              {timeframe}
            </Badge>
          </div>
          <div className="flex items-center gap-2">
            <Badge
              className={
                isLong
                  ? "bg-emerald-500/20 text-emerald-400 hover:bg-emerald-500/30"
                  : "bg-red-500/20 text-red-400 hover:bg-red-500/30"
              }
            >
              {isLong ? (
                <TrendingUp className="mr-1 h-3 w-3" />
              ) : (
                <TrendingDown className="mr-1 h-3 w-3" />
              )}
              {decision.direction}
            </Badge>
            <span className="text-xs text-muted-foreground">
              {relativeTime(occurredAt)}
            </span>
          </div>
        </div>
        <CardDescription>{decision.rationale}</CardDescription>
      </CardHeader>

      <CardContent className="space-y-4">
        {/* Confidence */}
        <div>
          <p className="mb-1 text-xs font-medium text-muted-foreground">
            Confidence
          </p>
          <ConfidenceBar value={decision.confidence} />
        </div>

        {/* Arguments */}
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div className="rounded-md border border-emerald-500/20 bg-emerald-500/5 p-3">
            <div className="mb-1 flex items-center gap-1 text-xs font-semibold text-emerald-400">
              <TrendingUp className="h-3 w-3" />
              Bull Case
            </div>
            <p className="text-xs leading-relaxed text-muted-foreground">
              {decision.bullArgument}
            </p>
          </div>
          <div className="rounded-md border border-red-500/20 bg-red-500/5 p-3">
            <div className="mb-1 flex items-center gap-1 text-xs font-semibold text-red-400">
              <TrendingDown className="h-3 w-3" />
              Bear Case
            </div>
            <p className="text-xs leading-relaxed text-muted-foreground">
              {decision.bearArgument}
            </p>
          </div>
        </div>

        {/* Judge */}
        <div className="rounded-md border border-border bg-muted/30 p-3">
          <div className="mb-1 flex items-center gap-1 text-xs font-semibold text-foreground">
            <Scale className="h-3 w-3" />
            Judge Reasoning
          </div>
          <p className="text-xs leading-relaxed text-muted-foreground">
            {decision.judgeReasoning}
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

export default function DebatesPage() {
  const { debates, connected } = useDebateEvents(50);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold text-foreground">
            <Swords className="h-6 w-6" />
            Debate Feed
          </h1>
          <p className="text-sm text-muted-foreground">
            Live Bull vs Bear adversarial debates — AI-powered trade analysis
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Badge variant="outline" className="tabular-nums">
            {debates.length} debates
          </Badge>
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

      {/* Debate Cards */}
      <div className="space-y-4">
        {debates.map((event) => {
          const payload = event.payload as DebateEvent;
          return (
            <DebateCard
              key={event.id}
              debate={payload}
              occurredAt={event.occurredAt}
            />
          );
        })}
        {debates.length === 0 && (
          <Card>
            <CardContent className="flex flex-col items-center justify-center py-12">
              <Swords className="mb-3 h-10 w-10 text-muted-foreground" />
              <p className="text-muted-foreground">
                Waiting for debate events...
              </p>
              <p className="text-xs text-muted-foreground">
                Debates will appear here in real-time via SSE
              </p>
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
