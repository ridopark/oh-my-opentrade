"use client";

import { useEffect, useState, useCallback } from "react";
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
}

interface Account {
  equity: number;
  buying_power: number;
  daily_pnl: number;
  daily_pnl_pct: number;
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

export default function PortfolioPage() {
  const [positions, setPositions] = useState<Position[]>([]);
  const [account, setAccount] = useState<Account | null>(null);
  const [loading, setLoading] = useState(true);
  const [closing, setClosing] = useState<string | null>(null);
  const [closingAll, setClosingAll] = useState(false);
  const [confirmCloseAll, setConfirmCloseAll] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
    } catch (err) {
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

  const totalUnrealizedPnl = positions.reduce((sum, p) => sum + p.unrealized_pnl, 0);
  const totalMarketValue = positions.reduce((sum, p) => sum + p.market_value, 0);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Portfolio</h1>
          <p className="text-sm text-muted-foreground">
            Open positions and account overview
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
              {account ? formatCurrency(account.equity) : "—"}
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
              {account ? formatCurrency(account.buying_power) : "—"}
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
              ) : "—"}
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
              {positions.length} position{positions.length !== 1 ? "s" : ""} · {formatCurrency(totalMarketValue)} value
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Positions Table */}
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
                  <TableHead>Symbol</TableHead>
                  <TableHead>Side</TableHead>
                  <TableHead className="text-right">Qty</TableHead>
                  <TableHead className="text-right">Avg Entry</TableHead>
                  <TableHead className="text-right">Current</TableHead>
                  <TableHead className="text-right">Market Value</TableHead>
                  <TableHead className="text-right">Unrealized P&L</TableHead>
                  <TableHead className="text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {positions.map((pos) => (
                  <TableRow key={pos.symbol}>
                    <TableCell className="font-medium">{pos.symbol}</TableCell>
                    <TableCell>
                      <Badge
                        variant={pos.side === "long" ? "default" : "destructive"}
                        className="text-xs"
                      >
                        {pos.side.toUpperCase()}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-right font-mono">
                      {pos.quantity.toLocaleString(undefined, { maximumFractionDigits: 4 })}
                    </TableCell>
                    <TableCell className="text-right font-mono">
                      {formatCurrency(pos.avg_entry_price)}
                    </TableCell>
                    <TableCell className="text-right font-mono">
                      {formatCurrency(pos.current_price)}
                    </TableCell>
                    <TableCell className="text-right font-mono">
                      {formatCurrency(pos.market_value)}
                    </TableCell>
                    <TableCell className="text-right font-mono">
                      <PnlText value={pos.unrealized_pnl} pct={pos.unrealized_pnl_pct} />
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => closePosition(pos.symbol)}
                        disabled={closing === pos.symbol || closingAll}
                        className="text-red-400 hover:text-red-300 hover:bg-red-500/10"
                      >
                        {closing === pos.symbol ? (
                          <RefreshCw className="h-3 w-3 animate-spin" />
                        ) : (
                          <X className="h-3 w-3" />
                        )}
                        <span className="ml-1">Close</span>
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
