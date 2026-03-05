"use client";

import { useExecutionEvents } from "@/lib/event-stream";
import { relativeTime, formatPrice } from "@/lib/format";
import type { OrderIntentEvent, OrderIntentStatus, DebateEvent } from "@/lib/types";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import {
  ListOrdered,
  Swords,
  TrendingUp,
  TrendingDown,
  Scale,
} from "lucide-react";
import { useState } from "react";

function StatusBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    created: "bg-blue-500/20 text-blue-400",
    validated: "bg-emerald-500/20 text-emerald-400",
    rejected: "bg-red-500/20 text-red-400",
    submitted: "bg-yellow-500/20 text-yellow-400",
  };

  return (
    <Badge className={styles[status] ?? "bg-muted text-muted-foreground"}>
      {status}
    </Badge>
  );
}

function StrategyBadge({ strategy }: { strategy: string }) {
  const styles: Record<string, string> = {
    debate: "bg-violet-500/20 text-violet-400",
    orb_break_retest: "bg-blue-500/20 text-blue-400",
    avwap: "bg-cyan-500/20 text-cyan-400",
    ai_scalping: "bg-amber-500/20 text-amber-400",
  };
  const defaultStyle = "bg-muted text-muted-foreground";

  const formattedName = (strategy || "unknown")
    .replace(/_/g, " ")
    .replace(/\b\w/g, (l) => l.toUpperCase());

  return (
    <Badge className={styles[strategy] || defaultStyle}>
      {strategy === "debate" && <Swords className="mr-1 h-3 w-3" />}
      {formattedName}
    </Badge>
  );
}

function ConfidenceCell({
  value,
  className,
}: {
  value: number;
  className?: string;
}) {
  const pct = Math.round(value * 100);
  return (
    <div className={`flex items-center gap-2 ${className}`}>
      <div className="flex-1 rounded-full bg-muted">
        <div
          className={`h-1.5 rounded-full transition-all ${
            pct >= 80
              ? "bg-emerald-500"
              : pct >= 60
                ? "bg-yellow-500"
                : "bg-red-500"
          }`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-xs font-mono tabular-nums">{pct}%</span>
    </div>
  );
}

interface OrderDetailSheetProps {
  order: (OrderIntentEvent & { eventId: string; occurredAt: string }) | null;
  debate: DebateEvent | undefined;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function OrderDetailSheet({
  order,
  debate,
  open,
  onOpenChange,
}: OrderDetailSheetProps) {
  if (!order) return null;

  const isLong = order.direction === "LONG";
  const hasDebate = order.strategy === "debate" && debate;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="overflow-y-auto sm:max-w-lg">
        <SheetHeader className="mb-6 space-y-4">
          <div className="flex items-center justify-between">
            <SheetTitle className="font-mono text-3xl font-bold">
              {order.symbol}
            </SheetTitle>
            <div className="flex items-center gap-2">
              <Badge
                className={
                  isLong
                    ? "bg-emerald-500/20 text-emerald-400"
                    : "bg-red-500/20 text-red-400"
                }
              >
                {isLong ? (
                  <TrendingUp className="mr-1 h-3 w-3" />
                ) : (
                  <TrendingDown className="mr-1 h-3 w-3" />
                )}
                {order.direction}
              </Badge>
              <StatusBadge status={order.status || "unknown"} />
            </div>
          </div>
          <SheetDescription className="flex items-center justify-between text-xs">
            <span>Event ID: {order.eventId.slice(0, 8)}</span>
            <span>{relativeTime(order.occurredAt)}</span>
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-6">
          {/* Strategy & Rationale */}
          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <StrategyBadge strategy={order.strategy} />
              <div className="w-32">
                <ConfidenceCell value={order.confidence} />
              </div>
            </div>
            <div className="rounded-md bg-muted/50 p-3 text-sm text-muted-foreground">
              {order.rationale || "No rationale provided."}
            </div>
          </div>

          {/* Order Details Grid */}
          <div className="grid grid-cols-2 gap-4 rounded-lg border bg-card p-4">
            <div>
              <p className="text-xs text-muted-foreground">Limit Price</p>
              <p className="font-mono text-lg font-medium">
                {formatPrice(order.limitPrice)}
              </p>
            </div>
            <div>
              <p className="text-xs text-muted-foreground">Stop Loss</p>
              <p className="font-mono text-lg font-medium">
                {formatPrice(order.stopLoss)}
              </p>
            </div>
            <div>
              <p className="text-xs text-muted-foreground">Quantity</p>
              <p className="font-mono text-lg font-medium">{order.quantity}</p>
            </div>
            <div>
              <p className="text-xs text-muted-foreground">Max Slippage</p>
              <p className="font-mono text-lg font-medium">
                {order.maxSlippageBPS} bps
              </p>
            </div>
          </div>

          {/* Debate Analysis (if applicable) */}
          {hasDebate && debate && (
            <div className="space-y-4 pt-4">
              <div className="flex items-center gap-2 border-t pt-4">
                <Swords className="h-4 w-4 text-violet-400" />
                <h3 className="font-semibold text-foreground">
                  Debate Analysis
                </h3>
              </div>

              {/* Bull Case */}
              <div className="rounded-md border-l-2 border-l-emerald-500 bg-emerald-500/5 p-3">
                <div className="mb-1 flex items-center gap-2 text-xs font-semibold text-emerald-400">
                  <TrendingUp className="h-3 w-3" />
                  Bull Case
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {debate.decision.bullArgument}
                </p>
              </div>

              {/* Bear Case */}
              <div className="rounded-md border-l-2 border-l-red-500 bg-red-500/5 p-3">
                <div className="mb-1 flex items-center gap-2 text-xs font-semibold text-red-400">
                  <TrendingDown className="h-3 w-3" />
                  Bear Case
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {debate.decision.bearArgument}
                </p>
              </div>

              {/* Judge Reasoning */}
              <div className="rounded-md border bg-muted/30 p-3">
                <div className="mb-1 flex items-center gap-2 text-xs font-semibold text-foreground">
                  <Scale className="h-3 w-3" />
                  Judge Verdict
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {debate.decision.judgeReasoning}
                </p>
              </div>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

export default function ExecutionPage() {
  const { orders, debates, connected } = useExecutionEvents(100);
  const [selectedOrder, setSelectedOrder] =
    useState<(OrderIntentEvent & { eventId: string; occurredAt: string }) | null>(
      null
    );

  const statusFromType = (type: string): OrderIntentStatus | undefined => {
    switch (type) {
      case "OrderIntentCreated":
        return "created";
      case "OrderIntentValidated":
        return "validated";
      case "OrderIntentRejected":
        return "rejected";
      case "OrderSubmitted":
        return "submitted";
      default:
        return undefined;
    }
  };

  const orderPayloads = orders.map((e) => {
    const p = e.payload as OrderIntentEvent;
    return {
      ...p,
      status: (p.status ?? statusFromType(e.type)) as OrderIntentEvent["status"],
      occurredAt: e.occurredAt,
      eventId: e.id,
    };
  });

  // Stats
  const validated = orderPayloads.filter(
    (o) => o.status === "validated"
  ).length;
  const rejected = orderPayloads.filter((o) => o.status === "rejected").length;
  const submitted = orderPayloads.filter(
    (o) => o.status === "submitted"
  ).length;

  // Strategy Stats
  const strategies = orderPayloads.reduce((acc, curr) => {
    const s = curr.strategy || "unknown";
    acc[s] = (acc[s] || 0) + 1;
    return acc;
  }, {} as Record<string, number>);

  return (
    <div className="space-y-6">
      <OrderDetailSheet
        order={selectedOrder}
        debate={selectedOrder ? debates.get(selectedOrder.symbol) : undefined}
        open={!!selectedOrder}
        onOpenChange={(open) => !open && setSelectedOrder(null)}
      />

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold text-foreground">
            <ListOrdered className="h-6 w-6" />
            Execution Monitor
          </h1>
          <p className="text-sm text-muted-foreground">
            Order intents — created, validated, rejected, and submitted
          </p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex gap-2">
            {Object.entries(strategies).map(([strat, count]) => (
              <Badge key={strat} variant="outline" className="text-xs">
                {strat}: {count}
              </Badge>
            ))}
          </div>
          <Badge
            variant={connected ? "default" : "destructive"}
            className="gap-1"
          >
            <div
              className={`h-2 w-2 rounded-full ${
                connected ? "bg-emerald-400 animate-pulse" : "bg-red-400"
              }`}
            />
            {connected ? "Live" : "Offline"}
          </Badge>
        </div>
      </div>

      {/* Summary Stats */}
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-muted-foreground">Total</p>
            <p className="text-2xl font-bold tabular-nums">
              {orderPayloads.length}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-emerald-400">Validated</p>
            <p className="text-2xl font-bold tabular-nums text-emerald-400">
              {validated}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-red-400">Rejected</p>
            <p className="text-2xl font-bold tabular-nums text-red-400">
              {rejected}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-yellow-400">Submitted</p>
            <p className="text-2xl font-bold tabular-nums text-yellow-400">
              {submitted}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Order Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Recent Order Intents</CardTitle>
          <CardDescription>
            Real-time order flow from the execution pipeline
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Symbol</TableHead>
                  <TableHead>Direction</TableHead>
                  <TableHead>Strategy</TableHead>
                  <TableHead className="text-right">Limit Price</TableHead>
                  <TableHead className="text-right">Stop Loss</TableHead>
                  <TableHead>Confidence</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Qty</TableHead>
                  <TableHead>Time</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {orderPayloads.map((order) => (
                  <TableRow
                    key={order.eventId}
                    className="cursor-pointer hover:bg-muted/50"
                    onClick={() => setSelectedOrder(order)}
                  >
                    <TableCell className="font-mono font-medium">
                      {order.symbol}
                    </TableCell>
                    <TableCell>
                      <Badge
                        className={
                          order.direction === "LONG"
                            ? "bg-emerald-500/20 text-emerald-400 hover:bg-emerald-500/30"
                            : "bg-red-500/20 text-red-400 hover:bg-red-500/30"
                        }
                      >
                        {order.direction}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <StrategyBadge strategy={order.strategy} />
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {formatPrice(order.limitPrice)}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {formatPrice(order.stopLoss)}
                    </TableCell>
                    <TableCell>
                      <ConfidenceCell value={order.confidence} />
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={order.status ?? "unknown"} />
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {order.quantity}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {relativeTime(order.occurredAt)}
                    </TableCell>
                  </TableRow>
                ))}
                {orderPayloads.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={9}
                      className="py-8 text-center text-muted-foreground"
                    >
                      Waiting for order intent events...
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
