"use client";

import { Fragment, useEffect, useState, useCallback, useMemo } from "react";
import { relativeTime, formatQty } from "@/lib/format";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Wallet,
  DollarSign,
  TrendingUp,
  TrendingDown,
  X,
  RefreshCw,
  AlertTriangle,
  ChevronDown,
  ChevronRight,
} from "lucide-react";
import { EquityCurveChart, type EquityPoint } from "@/components/charts/equity-curve-chart";

type DriftStatus = "SYNCED" | "ORPHAN_BROKER" | "ORPHAN_OMO" | "QTY_DRIFT" | "SIDE_DRIFT";

interface Position {
  symbol: string;
  side: string;
  quantity: number;
  avg_entry_price: number;
  current_price: number;
  market_value: number;
  unrealized_pnl: number;
  unrealized_pnl_pct: number;
  instrument_type?: string;
  underlying?: string;
  strike?: number;
  option_right?: string;
  expiry?: string;
  dte?: number;
  closing?: boolean;
  opened_at?: string;
  // Drift annotations populated post-fetch by the merge step. broker-only
  // rows leave omo undefined; ORPHAN_OMO rows are synthesized from the
  // monitored response so the existing group/render path needs no rework.
  status?: DriftStatus;
  omo?: MonitoredPosition;
}

interface MonitoredPosition {
  symbol: string;
  strategy: string;
  side: string;
  quantity: number;
  entry_price: number;
  high_water_mark: number;
  low_water_mark: number;
  entry_time: string;
  exit_rules: string[];
  instrument_type: string;
  underlying: string;
  strike?: number;
  option_right?: string;
  expiry?: string;
  dte?: number;
  iv_at_entry?: number;
  asset_class: string;
  est_max_loss_usd?: number;
}

interface Account {
  equity: number;
  buying_power: number;
  daily_pnl: number;
  daily_pnl_pct: number;
}

interface PositionGroup {
  underlying: string;
  positions: Position[];
  totalPnl: number;
  totalValue: number;
}

function formatCurrency(v: number) {
  return v.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

function formatPct(v: number) {
  return `${v >= 0 ? "+" : ""}${v.toFixed(2)}%`;
}

function PnlText({ value, pct }: { value: number; pct?: number }) {
  const color = value >= 0 ? "text-emerald-400" : "text-red-400";
  return (
    <span className={color}>
      {formatCurrency(value)}
      {pct !== undefined && (
        <span className="ml-1 text-xs opacity-75">({formatPct(pct)})</span>
      )}
    </span>
  );
}

function ContractLabel({ pos }: { pos: Position }) {
  if (pos.instrument_type !== "OPTION") {
    return <span className="font-mono font-medium">{pos.symbol}</span>;
  }
  const right = pos.option_right === "CALL" ? "C" : "P";
  const rightColor = pos.option_right === "CALL" ? "text-emerald-400" : "text-red-400";
  const strike = pos.strike ? `$${pos.strike}` : "";
  const expiry = pos.expiry
    ? new Date(pos.expiry + "T00:00:00").toLocaleDateString("en-US", { month: "short", day: "numeric" })
    : "";

  return (
    <div className="flex items-center gap-2">
      <span className={`font-mono font-bold text-xs ${rightColor}`}>{right}</span>
      <span className="font-mono text-sm">{strike}</span>
      <span className="text-xs text-muted-foreground">{expiry}</span>
    </div>
  );
}

// Broker emits "long"/"short"; OMO emits "BUY"/"SELL". Collapse to one axis.
function normalizeSide(s: string): "BUY" | "SELL" {
  const upper = s.toUpperCase();
  return upper === "SHORT" || upper === "SELL" ? "SELL" : "BUY";
}

function computeStatus(broker: Position, omo: MonitoredPosition | undefined): DriftStatus {
  if (!omo) return "ORPHAN_BROKER";
  if (broker.quantity !== omo.quantity) return "QTY_DRIFT";
  if (normalizeSide(broker.side) !== normalizeSide(omo.side)) return "SIDE_DRIFT";
  return "SYNCED";
}

function synthesizeOrphanOmo(omo: MonitoredPosition): Position {
  return {
    symbol: omo.symbol,
    side: normalizeSide(omo.side) === "SELL" ? "short" : "long",
    quantity: omo.quantity,
    avg_entry_price: omo.entry_price,
    current_price: 0,
    market_value: 0,
    unrealized_pnl: 0,
    unrealized_pnl_pct: 0,
    instrument_type: omo.instrument_type || undefined,
    underlying: omo.underlying,
    strike: omo.strike,
    option_right: omo.option_right,
    expiry: omo.expiry,
    dte: omo.dte,
    opened_at: omo.entry_time,
    status: "ORPHAN_OMO",
    omo,
  };
}

const STATUS_META: Record<DriftStatus, { cls: string; label: string }> = {
  SYNCED:        { cls: "bg-emerald-500/15 text-emerald-400 border-emerald-500/30", label: "Synced" },
  ORPHAN_BROKER: { cls: "bg-red-500/15 text-red-400 border-red-500/30",             label: "Not monitored" },
  ORPHAN_OMO:    { cls: "bg-amber-500/15 text-amber-400 border-amber-500/30",       label: "Broker missing" },
  QTY_DRIFT:     { cls: "bg-amber-500/15 text-amber-400 border-amber-500/30",       label: "Qty drift" },
  SIDE_DRIFT:    { cls: "bg-red-500/15 text-red-400 border-red-500/30",             label: "Side drift" },
};

function StatusBadge({ status }: { status: DriftStatus }) {
  const { cls, label } = STATUS_META[status];
  return (
    <Badge className={`text-[10px] px-1.5 py-0 border ${cls}`} title={status}>
      {label}
    </Badge>
  );
}

function DteBadge({ dte }: { dte?: number }) {
  if (dte === undefined) return null;
  const color =
    dte <= 7 ? "bg-red-500/20 text-red-400" :
    dte <= 14 ? "bg-amber-500/20 text-amber-400" :
    "bg-muted text-muted-foreground";
  return <Badge className={`text-[10px] px-1.5 py-0 ${color}`}>{dte}d</Badge>;
}

export default function PortfolioPage() {
  const [positions, setPositions] = useState<Position[]>([]);
  const [account, setAccount] = useState<Account | null>(null);
  const [bootstrapComplete, setBootstrapComplete] = useState(true);
  const [loading, setLoading] = useState(true);
  const [closing, setClosing] = useState<string | null>(null);
  const [confirmClose, setConfirmClose] = useState<string | null>(null);
  const [pendingClose, setPendingClose] = useState<Set<string>>(new Set());
  const [closingAll, setClosingAll] = useState(false);
  const [confirmCloseAll, setConfirmCloseAll] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [equity, setEquity] = useState<EquityPoint[] | undefined>(undefined);
  const [equityRange, setEquityRange] = useState<"1d" | "7d" | "30d" | "90d">("30d");
  const [equityLoading, setEquityLoading] = useState(true);

  useEffect(() => {
    let canceled = false;
    setEquityLoading(true);
    fetch(`/api/performance/dashboard?range=${equityRange}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (canceled) return;
        setEquity(Array.isArray(data?.equity) ? data.equity : []);
      })
      .catch(() => {
        if (!canceled) setEquity([]);
      })
      .finally(() => {
        if (!canceled) setEquityLoading(false);
      });
    return () => {
      canceled = true;
    };
  }, [equityRange]);

  const fetchData = useCallback(async () => {
    try {
      const [posRes, accRes, monRes] = await Promise.all([
        fetch("/api/portfolio/positions"),
        fetch("/api/portfolio/account"),
        fetch("/api/portfolio/monitored"),
      ]);

      const brokerPositions: Position[] = posRes.ok
        ? ((await posRes.json()).positions || [])
        : [];

      let monitored: MonitoredPosition[] = [];
      let bootstrap = true;
      if (monRes.ok) {
        const data = await monRes.json();
        monitored = data.monitored || [];
        bootstrap = data.bootstrap_complete !== false;
      }
      // 503 is the "monitor not wired" path (backtest binary) — we leave
      // monitored empty and bootstrap=true so no spurious banner appears.
      setBootstrapComplete((prev) => (prev === bootstrap ? prev : bootstrap));

      const omoBySym = new Map(monitored.map((m) => [m.symbol, m]));
      const merged: Position[] = brokerPositions.map((p) => {
        const omo = omoBySym.get(p.symbol);
        const status = computeStatus(p, omo);
        omoBySym.delete(p.symbol);
        return { ...p, omo, status };
      });
      // Synthesize ORPHAN_OMO rows so the existing grouped renderer
      // surfaces them without special-casing.
      for (const omo of omoBySym.values()) {
        merged.push(synthesizeOrphanOmo(omo));
      }

      setPositions(merged);
      if (accRes.ok) {
        const data = await accRes.json();
        setAccount(data);
      }
      setError(null);
    } catch {
      setError("Failed to fetch portfolio data");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
    const interval = setInterval(fetchData, 5000);
    return () => clearInterval(interval);
  }, [fetchData]);

  const closePosition = async (symbol: string) => {
    setClosing(symbol);
    setConfirmClose(null);
    try {
      const res = await fetch(`/api/portfolio/positions/${encodeURIComponent(symbol)}`, {
        method: "DELETE",
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        setError(data.error || `Failed to close ${symbol}`);
      } else {
        setPendingClose((prev) => new Set(prev).add(symbol));
      }
      await fetchData();
    } catch {
      setError(`Failed to close ${symbol}`);
    } finally {
      setClosing(null);
    }
  };

  // Clear pending close when position disappears from broker
  useEffect(() => {
    if (pendingClose.size === 0) return;
    const currentSymbols = new Set(positions.map((p) => p.symbol));
    setPendingClose((prev) => {
      const next = new Set(prev);
      for (const sym of prev) {
        if (!currentSymbols.has(sym)) next.delete(sym);
      }
      return next.size === prev.size ? prev : next;
    });
  }, [positions, pendingClose]);

  const closeAllPositions = async () => {
    setClosingAll(true);
    setConfirmCloseAll(false);
    try {
      const res = await fetch("/api/portfolio/positions", { method: "DELETE" });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        setError(data.error || "Failed to close all positions");
      }
      await fetchData();
    } catch {
      setError("Failed to close all positions");
    } finally {
      setClosingAll(false);
    }
  };

  // Group positions by underlying
  const groups: PositionGroup[] = useMemo(() => {
    const map = new Map<string, Position[]>();
    for (const p of positions) {
      const key = p.underlying || p.symbol;
      const list = map.get(key) || [];
      list.push(p);
      map.set(key, list);
    }
    return Array.from(map.entries())
      .map(([underlying, pos]) => ({
        underlying,
        positions: pos,
        totalPnl: pos.reduce((s, p) => s + p.unrealized_pnl, 0),
        totalValue: pos.reduce((s, p) => s + p.market_value, 0),
      }))
      .sort((a, b) => a.underlying.localeCompare(b.underlying));
  }, [positions]);

  // Broker-only view drives counts and destructive actions; ORPHAN_OMO rows
  // are visualized but not real broker positions and must not be tallied
  // into the "Close All" confirm count.
  const brokerPositions = positions.filter((p) => p.status !== "ORPHAN_OMO");
  const totalUnrealizedPnl = brokerPositions.reduce((sum, p) => sum + p.unrealized_pnl, 0);
  const totalMarketValue = brokerPositions.reduce((sum, p) => sum + p.market_value, 0);
  const driftRows = positions.filter((p) => p.status && p.status !== "SYNCED");
  const showDriftBanner = bootstrapComplete && driftRows.length > 0;
  const driftSummary = driftRows
    .slice(0, 3)
    .map((p) => `${p.underlying || p.symbol}${p.status ? ` (${p.status})` : ""}`)
    .join(", ") + (driftRows.length > 3 ? `, +${driftRows.length - 3} more` : "");

  const toggleGroup = (underlying: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(underlying)) next.delete(underlying);
      else next.add(underlying);
      return next;
    });
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Portfolio</h1>
          <p className="text-sm text-muted-foreground">
            {brokerPositions.length} position{brokerPositions.length !== 1 ? "s" : ""} across {groups.length} underlying{groups.length !== 1 ? "s" : ""}
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={fetchData} disabled={loading}>
            <RefreshCw className={`h-4 w-4 mr-1 ${loading ? "animate-spin" : ""}`} />
            Refresh
          </Button>
          {brokerPositions.length > 0 && !confirmCloseAll && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setConfirmCloseAll(true)}
              disabled={closingAll}
            >
              <X className="h-4 w-4 mr-1" />
              Close All
            </Button>
          )}
          {confirmCloseAll && (
            <div className="flex gap-1">
              <Button variant="destructive" size="sm" onClick={closeAllPositions}>
                Confirm Close All ({brokerPositions.length})
              </Button>
              <Button variant="outline" size="sm" onClick={() => setConfirmCloseAll(false)}>
                Cancel
              </Button>
            </div>
          )}
        </div>
      </div>

      {/* Error banner */}
      {error && (
        <div className="flex items-center gap-2 p-3 rounded-md bg-red-500/10 border border-red-500/20 text-red-400 text-sm">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          {error}
          <button onClick={() => setError(null)} className="ml-auto">
            <X className="h-3 w-3" />
          </button>
        </div>
      )}

      {/* Account Summary Cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground flex items-center gap-1">
              <Wallet className="h-4 w-4" /> Equity
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {account ? formatCurrency(account.equity) : "\u2014"}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground flex items-center gap-1">
              <DollarSign className="h-4 w-4" /> Buying Power
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {account ? formatCurrency(account.buying_power) : "\u2014"}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground flex items-center gap-1">
              {(account?.daily_pnl ?? 0) >= 0 ? (
                <TrendingUp className="h-4 w-4 text-emerald-400" />
              ) : (
                <TrendingDown className="h-4 w-4 text-red-400" />
              )}
              Daily P&L
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {account ? (
                <PnlText value={account.daily_pnl} pct={account.daily_pnl_pct} />
              ) : "\u2014"}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground flex items-center gap-1">
              {totalUnrealizedPnl >= 0 ? (
                <TrendingUp className="h-4 w-4 text-emerald-400" />
              ) : (
                <TrendingDown className="h-4 w-4 text-red-400" />
              )}
              Unrealized P&L
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              <PnlText value={totalUnrealizedPnl} />
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              {formatCurrency(totalMarketValue)} total value
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Equity curve */}
      <div className="grid grid-cols-1 gap-4">
        <div className="flex items-center justify-end gap-1">
          {(["1d", "7d", "30d", "90d"] as const).map((r) => (
            <Button
              key={r}
              size="sm"
              variant={equityRange === r ? "default" : "outline"}
              onClick={() => setEquityRange(r)}
            >
              {r}
            </Button>
          ))}
        </div>
        <EquityCurveChart data={equity} loading={equityLoading} />
      </div>

      {/* Drift banner — broker vs OMO position-monitor divergence */}
      {showDriftBanner && (
        <div className="flex items-center gap-2 p-3 rounded-md bg-amber-500/10 border border-amber-500/30 text-amber-400 text-sm">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span>
            <span className="font-semibold">{driftRows.length}</span> position{driftRows.length === 1 ? "" : "s"} drifting between broker and OMO: {driftSummary}
          </span>
        </div>
      )}

      {/* Positions Table — grouped by underlying */}
      <Card>
        <CardHeader>
          <CardTitle>Open Positions</CardTitle>
        </CardHeader>
        <CardContent>
          {positions.length === 0 ? (
            <div className="text-center py-12 text-muted-foreground">
              <Wallet className="h-10 w-10 mx-auto mb-3 opacity-30" />
              <p className="text-lg">No open positions</p>
              <p className="text-sm">Positions will appear here when strategies open trades</p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8"></TableHead>
                  <TableHead>Contract</TableHead>
                  <TableHead>Side</TableHead>
                  <TableHead className="text-right">Qty</TableHead>
                  <TableHead className="text-right">Avg Entry</TableHead>
                  <TableHead className="text-right">Current</TableHead>
                  <TableHead className="text-center">DTE</TableHead>
                  <TableHead className="text-right">P&L Open</TableHead>
                  <TableHead className="text-right">Max Loss</TableHead>
                  <TableHead className="text-right">Opened</TableHead>
                  <TableHead className="text-center">Monitored</TableHead>
                  <TableHead className="text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((group) => {
                  const isCollapsed = collapsed.has(group.underlying);
                  const multiLeg = group.positions.length > 1;
                  return (
                    <Fragment key={group.underlying}>
                      {/* Group header row */}
                      <TableRow
                        className="bg-muted/30 hover:bg-muted/50 cursor-pointer border-t"
                        onClick={() => multiLeg && toggleGroup(group.underlying)}
                      >
                        <TableCell className="px-2">
                          {multiLeg ? (
                            isCollapsed ? <ChevronRight className="h-4 w-4 text-muted-foreground" /> : <ChevronDown className="h-4 w-4 text-muted-foreground" />
                          ) : <div className="w-4" />}
                        </TableCell>
                        <TableCell colSpan={2}>
                          <div className="flex items-center gap-2">
                            <span className="font-mono font-bold text-base">{group.underlying}</span>
                            {multiLeg && (
                              <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                                {group.positions.length} legs
                              </Badge>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className="text-right" />
                        <TableCell className="text-right" />
                        <TableCell className="text-right" />
                        <TableCell className="text-center" />
                        <TableCell className="text-right font-mono font-medium">
                          <PnlText value={group.totalPnl} />
                        </TableCell>
                        <TableCell />
                        <TableCell />
                        <TableCell />
                        <TableCell />
                      </TableRow>

                      {/* Individual position rows */}
                      {!isCollapsed && group.positions.map((pos, i) => {
                        const status = pos.status;
                        const driftBorder =
                          status === "ORPHAN_BROKER" || status === "SIDE_DRIFT" ? "border-l-2 border-l-red-500/60" :
                          status === "ORPHAN_OMO" || status === "QTY_DRIFT" ? "border-l-2 border-l-amber-500/60" :
                          "";
                        const isOrphanOmo = status === "ORPHAN_OMO";
                        return (
                        <TableRow key={`${pos.symbol}-${i}`} className={`hover:bg-muted/20 ${driftBorder}`}>
                          <TableCell />
                          <TableCell className="pl-8">
                            <ContractLabel pos={pos} />
                          </TableCell>
                          <TableCell>
                            <Badge
                              className={`text-xs ${
                                pos.side === "long"
                                  ? "bg-emerald-500/20 text-emerald-400"
                                  : "bg-red-500/20 text-red-400"
                              }`}
                            >
                              {pos.side.toUpperCase()}
                            </Badge>
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            {formatQty(pos.quantity)}
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            {formatCurrency(pos.avg_entry_price)}
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            {isOrphanOmo ? "—" : formatCurrency(pos.current_price)}
                          </TableCell>
                          <TableCell className="text-center">
                            <DteBadge dte={pos.dte} />
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            {isOrphanOmo ? <span className="text-muted-foreground">—</span> : <PnlText value={pos.unrealized_pnl} pct={pos.unrealized_pnl_pct} />}
                          </TableCell>
                          <TableCell
                            className="text-right font-mono tabular-nums text-xs"
                            title="Clean-trigger estimate from PREMIUM_STOP only. Excludes slippage, gap-through, and time-based exits (STAGNATION_EXIT, EOD_FLATTEN). Catastrophic worst case = full premium paid."
                          >
                            {pos.omo?.est_max_loss_usd !== undefined ? (
                              <span className="text-red-400">{formatCurrency(pos.omo.est_max_loss_usd)}</span>
                            ) : (
                              <span className="text-muted-foreground">—</span>
                            )}
                          </TableCell>
                          <TableCell className="text-right text-xs text-muted-foreground whitespace-nowrap">
                            {pos.opened_at ? relativeTime(pos.opened_at) : "—"}
                          </TableCell>
                          <TableCell className="text-center">
                            {status ? (
                              <div className="flex flex-col items-center gap-0.5">
                                <StatusBadge status={status} />
                                {pos.omo && (
                                  <span
                                    className="text-[10px] text-muted-foreground"
                                    title={`Strategy: ${pos.omo.strategy}\nExit rules: ${pos.omo.exit_rules.join(", ") || "(none)"}`}
                                  >
                                    {pos.omo.strategy} · {pos.omo.exit_rules.length} rule{pos.omo.exit_rules.length === 1 ? "" : "s"}
                                  </span>
                                )}
                                {status === "QTY_DRIFT" && pos.omo && (
                                  <span className="text-[10px] text-amber-400/80">b={formatQty(pos.quantity)} o={formatQty(pos.omo.quantity)}</span>
                                )}
                              </div>
                            ) : (
                              <span className="text-xs text-muted-foreground">—</span>
                            )}
                          </TableCell>
                          <TableCell className="text-right">
                            {isOrphanOmo ? (
                              <span className="text-xs text-muted-foreground">—</span>
                            ) : (pos.closing || pendingClose.has(pos.symbol)) ? (
                              <span className="flex items-center gap-1 text-xs text-amber-400">
                                <RefreshCw className="h-3 w-3 animate-spin" />
                                Closing...
                              </span>
                            ) : confirmClose === pos.symbol ? (
                              <div className="flex gap-1 justify-end">
                                <Button
                                  variant="destructive"
                                  size="sm"
                                  onClick={(e) => { e.stopPropagation(); closePosition(pos.symbol); }}
                                  disabled={closing === pos.symbol}
                                >
                                  {closing === pos.symbol && <RefreshCw className="h-3 w-3 animate-spin mr-1" />}
                                  Confirm
                                </Button>
                                <Button variant="outline" size="sm" onClick={(e) => { e.stopPropagation(); setConfirmClose(null); }}>
                                  Cancel
                                </Button>
                              </div>
                            ) : (
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={(e) => { e.stopPropagation(); setConfirmClose(pos.symbol); }}
                                disabled={closingAll}
                                className="text-red-400 hover:text-red-300 hover:bg-red-500/10"
                              >
                                <X className="h-3 w-3 mr-1" />
                                Close
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                        );
                      })}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
