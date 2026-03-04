"use client";

import { useOrderIntentEvents } from "@/lib/event-stream";
import { relativeTime, formatPrice } from "@/lib/format";
import type { OrderIntentEvent } from "@/lib/types";
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
import { ListOrdered } from "lucide-react";

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

function ConfidenceCell({ value }: { value: number }) {
  const pct = Math.round(value * 100);
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-16 rounded-full bg-muted">
        <div
          className={`h-1.5 rounded-full ${
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

export default function ExecutionPage() {
  const { orders, connected } = useOrderIntentEvents(100);

  const statusFromType = (type: string): string => {
    switch (type) {
      case "OrderIntentCreated":   return "created";
      case "OrderIntentValidated": return "validated";
      case "OrderIntentRejected":  return "rejected";
      case "OrderSubmitted":       return "submitted";
      default:                     return "unknown";
    }
  };

  const orderPayloads = orders.map((e) => {
    const p = e.payload as OrderIntentEvent;
    return {
      ...p,
      status: (p.status ?? statusFromType(e.type)) as string,
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

  return (
    <div className="space-y-6">
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
                  <TableRow key={order.eventId}>
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
                      <StatusBadge status={order.status} />
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
                      colSpan={8}
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
