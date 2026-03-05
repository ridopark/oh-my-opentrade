"use client";

import { useExecutionEvents } from "@/lib/event-stream";
import { relativeTime, formatPrice } from "@/lib/format";
import type {
  OrderIntentEvent,
  OrderIntentStatus,
  HistoricalOrder,
  HistoricalOrdersResponse,
} from "@/lib/types";
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
  Database,
  Radio,
} from "lucide-react";
import { useState, useEffect, useCallback } from "react";

// Unified order type for display (works for both live SSE + historical)
interface DisplayOrder {
  id: string; // unique key for React
  intentId: string;
  symbol: string;
  direction: string;
  limitPrice: number;
  stopLoss: number;
  maxSlippageBPS: number;
  quantity: number;
  strategy: string;
  rationale: string;
  confidence: number;
  status: string;
  reason?: string;
  occurredAt: string;
  source: "live" | "historical";
  // Fill data (historical only)
  filledAt?: string;
  filledPrice?: number;
  filledQty?: number;
  // Debate data
  debate?: {
    bullArgument: string;
    bearArgument: string;
    judgeReasoning: string;
  };
}

function StatusBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    created: "bg-blue-500/20 text-blue-400",
    validated: "bg-emerald-500/20 text-emerald-400",
    rejected: "bg-red-500/20 text-red-400",
    submitted: "bg-yellow-500/20 text-yellow-400",
    filled: "bg-emerald-500/20 text-emerald-400",
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

function SourceBadge({ source }: { source: "live" | "historical" }) {
  if (source === "live") {
    return (
      <Badge className="bg-emerald-500/10 text-emerald-400 text-[10px] px-1.5 py-0">
        <Radio className="mr-0.5 h-2.5 w-2.5" />
        Live
      </Badge>
    );
  }
  return (
    <Badge className="bg-muted text-muted-foreground text-[10px] px-1.5 py-0">
      <Database className="mr-0.5 h-2.5 w-2.5" />
      DB
    </Badge>
  );
}

interface OrderDetailSheetProps {
  order: DisplayOrder | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function OrderDetailSheet({
  order,
  open,
  onOpenChange,
}: OrderDetailSheetProps) {
  if (!order) return null;

  const isLong = order.direction === "LONG";
  const hasDebate = order.debate;

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
            <span className="flex items-center gap-1.5">
              Intent: {order.intentId.slice(0, 8)}
              <SourceBadge source={order.source} />
            </span>
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
            {order.status === "rejected" && order.reason && (
              <div className="rounded-md border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-400">
                <span className="font-semibold">Rejection Reason:</span>{" "}
                {order.reason}
              </div>
            )}
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

          {/* Fill Data (historical orders that were filled) */}
          {order.filledAt && (
            <div className="grid grid-cols-2 gap-4 rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4">
              <div>
                <p className="text-xs text-emerald-400">Filled Price</p>
                <p className="font-mono text-lg font-medium text-emerald-400">
                  {formatPrice(order.filledPrice ?? 0)}
                </p>
              </div>
              <div>
                <p className="text-xs text-emerald-400">Filled Qty</p>
                <p className="font-mono text-lg font-medium text-emerald-400">
                  {order.filledQty}
                </p>
              </div>
              <div className="col-span-2">
                <p className="text-xs text-muted-foreground">Filled At</p>
                <p className="font-mono text-sm">
                  {relativeTime(order.filledAt)}
                </p>
              </div>
            </div>
          )}

          {/* Debate Analysis (if applicable) */}
          {hasDebate && (
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
                  {order.debate!.bullArgument}
                </p>
              </div>

              {/* Bear Case */}
              <div className="rounded-md border-l-2 border-l-red-500 bg-red-500/5 p-3">
                <div className="mb-1 flex items-center gap-2 text-xs font-semibold text-red-400">
                  <TrendingDown className="h-3 w-3" />
                  Bear Case
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {order.debate!.bearArgument}
                </p>
              </div>

              {/* Judge Reasoning */}
              <div className="rounded-md border bg-muted/30 p-3">
                <div className="mb-1 flex items-center gap-2 text-xs font-semibold text-foreground">
                  <Scale className="h-3 w-3" />
                  Judge Verdict
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {order.debate!.judgeReasoning}
                </p>
              </div>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function historicalToDisplay(h: HistoricalOrder): DisplayOrder {
  const directionMap: Record<string, string> = {
    BUY: "LONG",
    SELL: "SHORT",
    buy: "LONG",
    sell: "SHORT",
  };
  return {
    id: `hist-${h.intent_id}`,
    intentId: h.intent_id,
    symbol: h.symbol,
    direction: directionMap[h.side] ?? h.side,
    limitPrice: h.limit_price,
    stopLoss: h.stop_loss,
    maxSlippageBPS: 0,
    quantity: h.quantity,
    strategy: h.strategy,
    rationale: h.rationale,
    confidence: h.confidence,
    status: h.status,
    occurredAt: h.time,
    source: "historical",
    filledAt: h.filled_at,
    filledPrice: h.filled_price,
    filledQty: h.filled_qty,
    debate: h.thought_log
      ? {
          bullArgument: h.thought_log.bull_argument,
          bearArgument: h.thought_log.bear_argument,
          judgeReasoning: h.thought_log.judge_reasoning,
        }
      : undefined,
  };
}

export default function ExecutionPage() {
  const { orders, debates, connected } = useExecutionEvents(100);
  const [historicalOrders, setHistoricalOrders] = useState<DisplayOrder[]>([]);
  const [histLoading, setHistLoading] = useState(true);
  const [selectedOrder, setSelectedOrder] = useState<DisplayOrder | null>(null);

  // Fetch historical orders on mount
  const fetchHistorical = useCallback(async () => {
    try {
      const res = await fetch("/api/orders?range=30d&limit=200");
      if (!res.ok) return;
      const data: HistoricalOrdersResponse = await res.json();
      setHistoricalOrders(data.items.map(historicalToDisplay));
    } catch {
      // silently fail — historical data is supplemental
    } finally {
      setHistLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchHistorical();
  }, [fetchHistorical]);

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

  // Convert live SSE orders to DisplayOrder
  const liveOrders: DisplayOrder[] = orders.map((e) => {
    const p = e.payload as OrderIntentEvent;
    const status = (p.status ?? statusFromType(e.type)) as string;
    const debate = debates.get(p.symbol);
    return {
      id: `live-${e.id}`,
      intentId: p.id,
      symbol: p.symbol,
      direction: p.direction,
      limitPrice: p.limitPrice,
      stopLoss: p.stopLoss,
      maxSlippageBPS: p.maxSlippageBPS,
      quantity: p.quantity,
      strategy: p.strategy,
      rationale: p.rationale,
      confidence: p.confidence,
      status: status ?? "unknown",
      reason: p.reason,
      occurredAt: e.occurredAt,
      source: "live" as const,
      debate:
        p.strategy === "debate" && debate
          ? {
              bullArgument: debate.decision.bullArgument,
              bearArgument: debate.decision.bearArgument,
              judgeReasoning: debate.decision.judgeReasoning,
            }
          : undefined,
    };
  });

  // Merge: live SSE first, then historical (deduplicated by intentId)
  const liveIntentIds = new Set(liveOrders.map((o) => o.intentId));
  const deduplicatedHistorical = historicalOrders.filter(
    (o) => !liveIntentIds.has(o.intentId)
  );
  const allOrders = [...liveOrders, ...deduplicatedHistorical];

  // Stats (over merged set)
  const validated = allOrders.filter((o) => o.status === "validated").length;
  const rejected = allOrders.filter((o) => o.status === "rejected").length;
  const submitted = allOrders.filter((o) => o.status === "submitted").length;
  const filled = allOrders.filter((o) => o.status === "filled").length;

  // Strategy Stats
  const strategies = allOrders.reduce(
    (acc, curr) => {
      const s = curr.strategy || "unknown";
      acc[s] = (acc[s] || 0) + 1;
      return acc;
    },
    {} as Record<string, number>
  );

  return (
    <div className="space-y-6">
      <OrderDetailSheet
        order={selectedOrder}
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
            Order intents — live stream + historical from database
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
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-5">
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-muted-foreground">Total</p>
            <p className="text-2xl font-bold tabular-nums">
              {allOrders.length}
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
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-emerald-400">Filled</p>
            <p className="text-2xl font-bold tabular-nums text-emerald-400">
              {filled}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Order Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Recent Order Intents</CardTitle>
          <CardDescription>
            {liveOrders.length > 0 && (
              <span className="mr-2">
                <Radio className="mr-1 inline h-3 w-3 text-emerald-400" />
                {liveOrders.length} live
              </span>
            )}
            {deduplicatedHistorical.length > 0 && (
              <span>
                <Database className="mr-1 inline h-3 w-3 text-muted-foreground" />
                {deduplicatedHistorical.length} from database
              </span>
            )}
            {histLoading && (
              <span className="text-muted-foreground">
                Loading historical orders...
              </span>
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8"></TableHead>
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
                {allOrders.map((order) => (
                  <TableRow
                    key={order.id}
                    className="cursor-pointer hover:bg-muted/50"
                    onClick={() => setSelectedOrder(order)}
                  >
                    <TableCell className="px-1">
                      <SourceBadge source={order.source} />
                    </TableCell>
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
                      <div>
                        <StatusBadge status={order.status ?? "unknown"} />
                        {order.status === "rejected" && order.reason && (
                          <p className="mt-1 text-[11px] leading-tight text-red-400/80 max-w-[200px] truncate" title={order.reason}>
                            {order.reason}
                          </p>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {order.quantity}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {relativeTime(order.occurredAt)}
                    </TableCell>
                  </TableRow>
                ))}
                {allOrders.length === 0 && !histLoading && (
                  <TableRow>
                    <TableCell
                      colSpan={10}
                      className="py-8 text-center text-muted-foreground"
                    >
                      No order intents found. Waiting for live events...
                    </TableCell>
                  </TableRow>
                )}
                {allOrders.length === 0 && histLoading && (
                  <TableRow>
                    <TableCell
                      colSpan={10}
                      className="py-8 text-center text-muted-foreground"
                    >
                      Loading historical orders...
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
