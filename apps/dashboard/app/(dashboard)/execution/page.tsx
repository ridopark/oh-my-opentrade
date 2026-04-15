"use client";

import { useExecutionEvents } from "@/lib/event-stream";
import { relativeTime, formatPrice, formatQty } from "@/lib/format";
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
  ChevronDown,
  ArrowRight,
} from "lucide-react";
import { useState, useEffect, useCallback, useMemo } from "react";

// ─── Types ───────────────────────────────────────────────

interface MarketQuote {
  bid: number;
  ask: number;
  mid: number;
  fetchedAt: number;
}

interface DisplayOrder {
  id: string;
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
  filledAt?: string;
  filledPrice?: number;
  filledQty?: number;
  debate?: {
    bullArgument: string;
    bearArgument: string;
    judgeReasoning: string;
  };
}

type TabFilter = "active" | "filled" | "rejected";

// ─── Helpers ─────────────────────────────────────────────

function StatusDot({ status }: { status: string }) {
  const colors: Record<string, string> = {
    created: "bg-blue-400",
    validated: "bg-blue-400",
    submitted: "bg-amber-400 animate-pulse",
    filled: "bg-emerald-400",
    rejected: "bg-red-400",
    canceled: "bg-muted-foreground",
  };
  return (
    <span
      className={`inline-block h-2.5 w-2.5 rounded-full ${colors[status] ?? "bg-muted-foreground"}`}
      title={status}
    />
  );
}

function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    submitted: "pending fill",
    canceled: "canceled",
  };
  return labels[status] ?? status;
}

function isActive(status: string): boolean {
  return status === "created" || status === "validated" || status === "submitted";
}

function isFilled(status: string): boolean {
  return status === "filled";
}

function isRejected(status: string): boolean {
  return status === "rejected" || status === "canceled";
}

function ConfidenceBar({ value }: { value: number }) {
  const pct = Math.round(value * 100);
  return (
    <div className="flex items-center gap-1.5">
      <div className="w-12 h-1.5 rounded-full bg-muted overflow-hidden">
        <div
          className={`h-full rounded-full ${pct >= 80 ? "bg-emerald-500" : pct >= 60 ? "bg-yellow-500" : "bg-red-500"}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-[11px] font-mono tabular-nums text-muted-foreground">{pct}%</span>
    </div>
  );
}

function PriceCell({ order, quote, onFetch }: { order: DisplayOrder; quote?: MarketQuote; onFetch: () => void }) {
  if (order.status === "filled" && order.filledPrice) {
    return (
      <div className="flex items-center gap-1 font-mono tabular-nums">
        <span className="text-muted-foreground line-through text-xs">{formatPrice(order.limitPrice)}</span>
        <ArrowRight className="h-3 w-3 text-muted-foreground" />
        <span className="text-emerald-400">{formatPrice(order.filledPrice)}</span>
      </div>
    );
  }
  if (!isActive(order.status)) {
    return <span className="font-mono tabular-nums">{formatPrice(order.limitPrice)}</span>;
  }
  if (!quote) {
    return (
      <div className="flex items-center gap-2">
        <span className="font-mono tabular-nums">{formatPrice(order.limitPrice)}</span>
        <button onClick={(e) => { e.stopPropagation(); onFetch(); }} className="text-[10px] text-blue-400 hover:underline">mkt</button>
      </div>
    );
  }
  const likely = quote.ask <= order.limitPrice;
  const gap = order.limitPrice > 0 ? ((quote.ask - order.limitPrice) / order.limitPrice) * 100 : 0;
  return (
    <div className="flex items-center gap-2 font-mono tabular-nums">
      <span>{formatPrice(order.limitPrice)}</span>
      <span className={`text-[10px] ${likely ? "text-emerald-400" : "text-red-400"}`}>
        ({formatPrice(quote.mid)}{!likely && ` +${gap.toFixed(1)}%`})
      </span>
    </div>
  );
}

// ─── Detail Sheet ────────────────────────────────────────

function OrderDetailSheet({ order, open, onOpenChange }: { order: DisplayOrder | null; open: boolean; onOpenChange: (o: boolean) => void }) {
  if (!order) return null;
  const isLong = order.direction === "LONG";

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="overflow-y-auto sm:max-w-lg">
        <SheetHeader className="mb-6 space-y-3">
          <div className="flex items-center justify-between">
            <SheetTitle className="font-mono text-2xl font-bold">{order.symbol}</SheetTitle>
            <div className="flex items-center gap-2">
              <Badge className={isLong ? "bg-emerald-500/20 text-emerald-400" : "bg-red-500/20 text-red-400"}>
                {isLong ? <TrendingUp className="mr-1 h-3 w-3" /> : <TrendingDown className="mr-1 h-3 w-3" />}
                {order.direction}
              </Badge>
              <StatusDot status={order.status} />
              <span className="text-xs text-muted-foreground">{statusLabel(order.status)}</span>
            </div>
          </div>
          <SheetDescription className="text-xs">
            {order.strategy.replace(/_/g, " ")} &middot; {relativeTime(order.occurredAt)} &middot; ID: {order.intentId.slice(0, 8)}
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-5">
          {/* Order grid */}
          <div className="grid grid-cols-2 gap-3 rounded-lg border bg-card p-4">
            <div>
              <p className="text-[11px] text-muted-foreground">Limit Price</p>
              <p className="font-mono text-lg font-medium">{formatPrice(order.limitPrice)}</p>
            </div>
            <div>
              <p className="text-[11px] text-muted-foreground">Stop Loss</p>
              <p className="font-mono text-lg font-medium">{formatPrice(order.stopLoss)}</p>
            </div>
            <div>
              <p className="text-[11px] text-muted-foreground">Quantity</p>
              <p className="font-mono text-lg font-medium">{formatQty(order.quantity)}</p>
            </div>
            <div>
              <p className="text-[11px] text-muted-foreground">Confidence</p>
              <ConfidenceBar value={order.confidence} />
            </div>
          </div>

          {/* Fill info */}
          {order.filledAt && (
            <div className="grid grid-cols-2 gap-3 rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4">
              <div>
                <p className="text-[11px] text-emerald-400">Fill Price</p>
                <p className="font-mono text-lg font-medium text-emerald-400">{formatPrice(order.filledPrice ?? 0)}</p>
              </div>
              <div>
                <p className="text-[11px] text-emerald-400">Fill Qty</p>
                <p className="font-mono text-lg font-medium text-emerald-400">{formatQty(order.filledQty ?? 0)}</p>
              </div>
            </div>
          )}

          {/* Rejection reason */}
          {order.status === "rejected" && order.reason && (
            <div className="rounded-md border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-400">
              <span className="font-semibold">Rejected:</span> {order.reason}
            </div>
          )}

          {/* Rationale */}
          {order.rationale && (
            <div className="rounded-md bg-muted/50 p-3 text-sm text-muted-foreground">
              {order.rationale}
            </div>
          )}

          {/* Debate */}
          {order.debate && (
            <div className="space-y-3 pt-3 border-t">
              <div className="flex items-center gap-2">
                <Swords className="h-4 w-4 text-violet-400" />
                <h3 className="font-semibold text-sm">Debate Analysis</h3>
              </div>
              <div className="rounded-md border-l-2 border-l-emerald-500 bg-emerald-500/5 p-3">
                <div className="mb-1 text-[11px] font-semibold text-emerald-400 flex items-center gap-1"><TrendingUp className="h-3 w-3" />Bull</div>
                <p className="text-xs text-muted-foreground">{order.debate.bullArgument}</p>
              </div>
              <div className="rounded-md border-l-2 border-l-red-500 bg-red-500/5 p-3">
                <div className="mb-1 text-[11px] font-semibold text-red-400 flex items-center gap-1"><TrendingDown className="h-3 w-3" />Bear</div>
                <p className="text-xs text-muted-foreground">{order.debate.bearArgument}</p>
              </div>
              <div className="rounded-md border bg-muted/30 p-3">
                <div className="mb-1 text-[11px] font-semibold flex items-center gap-1"><Scale className="h-3 w-3" />Verdict</div>
                <p className="text-xs text-muted-foreground">{order.debate.judgeReasoning}</p>
              </div>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─── Data Conversion ─────────────────────────────────────

function historicalToDisplay(h: HistoricalOrder): DisplayOrder {
  const dirMap: Record<string, string> = { BUY: "LONG", SELL: "SHORT", buy: "LONG", sell: "SHORT" };
  return {
    id: `hist-${h.intent_id}`,
    intentId: h.intent_id,
    symbol: h.symbol,
    direction: dirMap[h.side] ?? h.side,
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
    debate: h.thought_log ? {
      bullArgument: h.thought_log.bull_argument,
      bearArgument: h.thought_log.bear_argument,
      judgeReasoning: h.thought_log.judge_reasoning,
    } : undefined,
  };
}

// ─── Main Page ───────────────────────────────────────────

export default function ExecutionPage() {
  const { orders, debates, connected } = useExecutionEvents(100);
  const [historicalOrders, setHistoricalOrders] = useState<DisplayOrder[]>([]);
  const [histLoading, setHistLoading] = useState(true);
  const [selectedOrder, setSelectedOrder] = useState<DisplayOrder | null>(null);
  const [quotes, setQuotes] = useState<Record<string, MarketQuote>>({});
  const [activeTab, setActiveTab] = useState<TabFilter>("active");

  const fetchHistorical = useCallback(async () => {
    try {
      const res = await fetch("/api/orders?range=30d&limit=200");
      if (!res.ok) return;
      const data: HistoricalOrdersResponse = await res.json();
      setHistoricalOrders(data.items.map(historicalToDisplay));
    } catch { /* supplemental */ } finally {
      setHistLoading(false);
    }
  }, []);

  useEffect(() => { fetchHistorical(); }, [fetchHistorical]);

  const fetchQuote = useCallback(async (symbol: string) => {
    try {
      const res = await fetch(`/api/portfolio/quote/${encodeURIComponent(symbol)}`);
      if (!res.ok) return;
      const data = await res.json();
      setQuotes((prev) => ({ ...prev, [symbol]: { bid: data.bid, ask: data.ask, mid: data.mid, fetchedAt: Date.now() } }));
    } catch { /* supplemental */ }
  }, []);

  const statusFromType = (type: string): OrderIntentStatus | undefined => {
    switch (type) {
      case "OrderIntentCreated": return "created";
      case "OrderIntentValidated": return "validated";
      case "OrderIntentRejected": return "rejected";
      case "OrderSubmitted": return "submitted";
      default: return undefined;
    }
  };

  // Deduplicate live SSE events by intentId, keeping highest-priority status
  const statusPriority: Record<string, number> = {
    created: 0, validated: 1, submitted: 2, filled: 3, rejected: 3, canceled: 3,
  };
  const liveByIntent = new Map<string, DisplayOrder>();
  for (const e of orders) {
    const p = e.payload as OrderIntentEvent;
    const status = (p.status ?? statusFromType(e.type)) as string;
    const debate = debates.get(p.symbol);
    const order: DisplayOrder = {
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
      source: "live",
      debate: p.strategy === "debate" && debate ? {
        bullArgument: debate.decision.bullArgument,
        bearArgument: debate.decision.bearArgument,
        judgeReasoning: debate.decision.judgeReasoning,
      } : undefined,
    };
    const existing = liveByIntent.get(p.id);
    if (!existing || (statusPriority[status] ?? 0) >= (statusPriority[existing.status] ?? 0)) {
      liveByIntent.set(p.id, order);
    }
  }
  const liveOrders = Array.from(liveByIntent.values());

  // Merge live + historical (deduplicated)
  const liveIntentIds = new Set(liveOrders.map((o) => o.intentId));
  const allOrders = [...liveOrders, ...historicalOrders.filter((o) => !liveIntentIds.has(o.intentId))];

  // Auto-fetch quotes for active orders
  const activeSymbols = useMemo(() => [...new Set(
    allOrders.filter((o) => isActive(o.status)).map((o) => o.symbol)
  )], [allOrders]);

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (activeSymbols.length === 0) return;
    activeSymbols.forEach((s) => fetchQuote(s));
    const iv = setInterval(() => activeSymbols.forEach((s) => fetchQuote(s)), 30_000);
    return () => clearInterval(iv);
  }, [activeSymbols.join(","), fetchQuote]);

  // Hide the position_monitor exit-retry loop from the Rejected tab.
  // It generated ~1.2k canceled/expired rows for a single stuck BA position
  // on 2026-04-13/14. The rows are real IBKR submissions (not accounting
  // phantoms) and stay in the DB for forensics, but they drown out genuine
  // rejections in the UI.
  const isReconcileNoise = (o: { strategy: string; status: string }) =>
    o.strategy === "reconciliation" && (o.status === "canceled" || o.status === "expired");

  // Filter by tab
  const filtered = allOrders.filter((o) => {
    if (activeTab === "active") return isActive(o.status);
    if (activeTab === "filled") return isFilled(o.status);
    return isRejected(o.status) && !isReconcileNoise(o);
  });

  // Counts
  const activeCount = allOrders.filter((o) => isActive(o.status)).length;
  const filledCount = allOrders.filter((o) => isFilled(o.status)).length;
  const rejectedCount = allOrders.filter((o) => isRejected(o.status) && !isReconcileNoise(o)).length;

  const tabs: { key: TabFilter; label: string; count: number }[] = [
    { key: "active", label: "Active", count: activeCount },
    { key: "filled", label: "Filled", count: filledCount },
    { key: "rejected", label: "Rejected", count: rejectedCount },
  ];

  return (
    <div className="space-y-6">
      <OrderDetailSheet order={selectedOrder} open={!!selectedOrder} onOpenChange={(o) => !o && setSelectedOrder(null)} />

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold text-foreground">
            <ListOrdered className="h-6 w-6" />
            Execution Monitor
          </h1>
          <p className="text-sm text-muted-foreground">
            {allOrders.length} orders today
          </p>
        </div>
        <Badge variant={connected ? "default" : "destructive"} className="gap-1">
          <div className={`h-2 w-2 rounded-full ${connected ? "bg-emerald-400 animate-pulse" : "bg-red-400"}`} />
          {connected ? "Live" : "Offline"}
        </Badge>
      </div>

      {/* Summary cards */}
      <div className="grid grid-cols-3 gap-4">
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-amber-400">Active</p>
            <p className="text-2xl font-bold tabular-nums text-amber-400">{activeCount}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-emerald-400">Filled</p>
            <p className="text-2xl font-bold tabular-nums text-emerald-400">{filledCount}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-6">
            <p className="text-xs text-red-400">Rejected</p>
            <p className="text-2xl font-bold tabular-nums text-red-400">{rejectedCount}</p>
          </CardContent>
        </Card>
      </div>

      {/* Tab filters */}
      <div className="flex gap-1 border-b">
        {tabs.map((tab) => (
          <button
            key={tab.key}
            onClick={() => setActiveTab(tab.key)}
            className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
              activeTab === tab.key
                ? "border-foreground text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {tab.label}
            {tab.count > 0 && (
              <span className="ml-1.5 text-xs bg-muted px-1.5 py-0.5 rounded-full tabular-nums">{tab.count}</span>
            )}
          </button>
        ))}
      </div>

      {/* Orders table */}
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{tabs.find((t) => t.key === activeTab)?.label} Orders</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-6"></TableHead>
                  <TableHead>Symbol</TableHead>
                  <TableHead>Side</TableHead>
                  <TableHead className="text-right">Qty</TableHead>
                  <TableHead className="text-right">Price</TableHead>
                  <TableHead>Strategy</TableHead>
                  <TableHead>Confidence</TableHead>
                  <TableHead className="text-right">Time</TableHead>
                  <TableHead className="w-6"></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((order) => (
                  <TableRow
                    key={order.id}
                    className={`cursor-pointer hover:bg-muted/50 ${isRejected(order.status) ? "opacity-50" : ""}`}
                    onClick={() => setSelectedOrder(order)}
                  >
                    <TableCell className="px-2">
                      <StatusDot status={order.status} />
                    </TableCell>
                    <TableCell className="font-mono font-medium text-sm">
                      {order.symbol}
                    </TableCell>
                    <TableCell>
                      <span className={`text-xs font-medium ${order.direction === "LONG" ? "text-emerald-400" : "text-red-400"}`}>
                        {order.direction === "LONG" ? "BUY" : "SELL"} {formatQty(order.quantity)}
                      </span>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums text-xs">
                      {formatQty(order.quantity)}
                    </TableCell>
                    <TableCell className="text-right">
                      <PriceCell order={order} quote={quotes[order.symbol]} onFetch={() => fetchQuote(order.symbol)} />
                    </TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {(order.strategy || "unknown").replace(/_/g, " ")}
                      </span>
                    </TableCell>
                    <TableCell>
                      <ConfidenceBar value={order.confidence} />
                    </TableCell>
                    <TableCell className="text-right text-xs text-muted-foreground whitespace-nowrap">
                      {relativeTime(order.occurredAt)}
                    </TableCell>
                    <TableCell className="px-2">
                      <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                    </TableCell>
                  </TableRow>
                ))}
                {filtered.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={9} className="py-8 text-center text-muted-foreground">
                      {histLoading ? "Loading..." : `No ${activeTab} orders`}
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
