"use client";

import { useEffect, useState, useCallback, useMemo } from "react";
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
  const [loading, setLoading] = useState(true);
  const [closing, setClosing] = useState<string | null>(null);
  const [confirmClose, setConfirmClose] = useState<string | null>(null);
  const [closingAll, setClosingAll] = useState(false);
  const [confirmCloseAll, setConfirmCloseAll] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  const fetchData = useCallback(async () => {
    try {
      const [posRes, accRes] = await Promise.all([
        fetch("/api/portfolio/positions"),
        fetch("/api/portfolio/account"),
      ]);
      if (posRes.ok) {
        const data = await posRes.json();
        setPositions(data.positions || []);
      }
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
      }
      await fetchData();
    } catch {
      setError(`Failed to close ${symbol}`);
    } finally {
      setClosing(null);
    }
  };

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

  const totalUnrealizedPnl = positions.reduce((sum, p) => sum + p.unrealized_pnl, 0);
  const totalMarketValue = positions.reduce((sum, p) => sum + p.market_value, 0);

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
            {positions.length} position{positions.length !== 1 ? "s" : ""} across {groups.length} underlying{groups.length !== 1 ? "s" : ""}
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={fetchData} disabled={loading}>
            <RefreshCw className={`h-4 w-4 mr-1 ${loading ? "animate-spin" : ""}`} />
            Refresh
          </Button>
          {positions.length > 0 && !confirmCloseAll && (
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
                Confirm Close All ({positions.length})
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
                  <TableHead className="text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((group) => {
                  const isCollapsed = collapsed.has(group.underlying);
                  const multiLeg = group.positions.length > 1;
                  return (
                    <>{/* eslint-disable-next-line react/jsx-key */}
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
                      </TableRow>

                      {/* Individual position rows */}
                      {!isCollapsed && group.positions.map((pos, i) => (
                        <TableRow key={`${pos.symbol}-${i}`} className="hover:bg-muted/20">
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
                            {pos.quantity}
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            {formatCurrency(pos.avg_entry_price)}
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            {formatCurrency(pos.current_price)}
                          </TableCell>
                          <TableCell className="text-center">
                            <DteBadge dte={pos.dte} />
                          </TableCell>
                          <TableCell className="text-right font-mono tabular-nums">
                            <PnlText value={pos.unrealized_pnl} pct={pos.unrealized_pnl_pct} />
                          </TableCell>
                          <TableCell className="text-right">
                            {confirmClose === pos.symbol ? (
                              <div className="flex gap-1 justify-end">
                                <Button
                                  variant="destructive"
                                  size="sm"
                                  onClick={() => closePosition(pos.symbol)}
                                  disabled={closing === pos.symbol}
                                >
                                  {closing === pos.symbol && <RefreshCw className="h-3 w-3 animate-spin mr-1" />}
                                  Confirm
                                </Button>
                                <Button variant="outline" size="sm" onClick={() => setConfirmClose(null)}>
                                  Cancel
                                </Button>
                              </div>
                            ) : (
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => setConfirmClose(pos.symbol)}
                                disabled={closingAll}
                                className="text-red-400 hover:text-red-300 hover:bg-red-500/10"
                              >
                                <X className="h-3 w-3 mr-1" />
                                Close
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </>
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
