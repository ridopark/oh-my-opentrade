"use client";

import { useEffect, useRef, useState, useMemo, use } from "react";
import {
  createChart,
  ColorType,
  type IChartApi,
  type Time,
  LineSeries,
} from "lightweight-charts";
import {
  TrendingUp,
  TrendingDown,
  BarChart3,
  DollarSign,
  Activity,
  Percent,
  Hash,
  Scale,
  ArrowLeft,
  Zap,
  Eye,
  Dna,
  type LucideIcon,
} from "lucide-react";

import {
  useStrategyDashboard,
  useStrategyState,
  useStrategySignals,
  useAllStrategiesDNA,
} from "@/hooks/queries";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import Link from "next/link";

// Helper for formatting currency
const formatCurrency = (val: number) =>
  new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(val);

// Helper for formatting percentage
const formatPercent = (val: number) =>
  new Intl.NumberFormat("en-US", {
    style: "percent",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(val / 100);

// Helper for formatting large numbers
const formatNumber = (val: number) =>
  new Intl.NumberFormat("en-US").format(val);

type RangeOption = "7d" | "30d" | "90d" | "all";

export default function StrategyDetailPage({
  params,
}: {
  params: Promise<{ strategyID: string }>;
}) {
  const { strategyID } = use(params);
  const [range, setRange] = useState<RangeOption>("30d");

  // 1. Dashboard Data
  const {
    data: dashboardData,
    isLoading: loadingDashboard,
    error: dashboardError,
    refetch: refetchDashboard,
  } = useStrategyDashboard(strategyID, range);

  // 2. State Snapshots (Live)
  const { data: stateData, isLoading: loadingState } =
    useStrategyState(strategyID);

  // 3. Signals Log (Infinite)
  const {
    data: signalsData,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useStrategySignals(strategyID);

  // 4. Strategy DNA
  const { data: allDNAs } = useAllStrategiesDNA();
  const strategyDNA = useMemo(
    () => allDNAs?.find((d) => d.id === strategyID) ?? null,
    [allDNAs, strategyID]
  );

  const [activeTab, setActiveTab] = useState<"performance" | "dna">("performance");

  const signals = signalsData?.pages.flatMap((p) => p.items) ?? [];

  const chartContainerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);

  // Equity Chart Effect
  useEffect(() => {
    if (!chartContainerRef.current || !dashboardData?.equityCurve?.length) return;

    const chart = createChart(chartContainerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "rgba(148, 163, 184, 1)", // slate-400
      },
      grid: {
        vertLines: { color: "rgba(148, 163, 184, 0.15)" },
        horzLines: { color: "rgba(148, 163, 184, 0.15)" },
      },
      width: chartContainerRef.current.clientWidth,
      height: 300,
      timeScale: {
        timeVisible: true,
        secondsVisible: false,
      },
    });

    const equitySeries = chart.addSeries(LineSeries, {
      color: "#10b981", // emerald-500
      lineWidth: 2,
    });

    // Transform data (PascalCase from StrategyEquityPointEntry)
    const data = dashboardData.equityCurve.map((pt) => ({
      time: (new Date(pt.Time).getTime() / 1000) as Time,
      value: pt.Equity,
    }));

    equitySeries.setData(data);
    chart.timeScale().fitContent();

    chartRef.current = chart;

    // Responsive resize
    const handleResize = () => {
      if (chartContainerRef.current && chartRef.current) {
        chartRef.current.applyOptions({
          width: chartContainerRef.current.clientWidth,
        });
      }
    };

    const resizeObserver = new ResizeObserver(handleResize);
    resizeObserver.observe(chartContainerRef.current);

    return () => {
      resizeObserver.disconnect();
      chart.remove();
      chartRef.current = null;
    };
  }, [dashboardData]);

  // Performance data loading/error state (used inside performance tab only)
  const perfLoading = loadingDashboard;
  const perfError = dashboardError;


  return (
    <div className="space-y-8">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <Link
              href="/strategies"
              className="text-muted-foreground hover:text-foreground transition-colors"
            >
              <ArrowLeft className="h-4 w-4" />
            </Link>
            <h1 className="text-3xl font-bold tracking-tight">{strategyID}</h1>
            <Badge variant="outline" className="ml-2">
              Active
            </Badge>
            <Link href={`/strategies/${strategyID}/config`}>
              <Button variant="outline" size="sm" className="ml-2 text-xs">
                Edit Config
              </Button>
            </Link>
            <Link href={`/strategies/${strategyID}/sweep`}>
              <Button variant="outline" size="sm" className="ml-1 text-xs">
                Sweep
              </Button>
            </Link>
          </div>
          <p className="text-muted-foreground">
            Strategy performance and live state monitoring.
          </p>
        </div>

        <div className="flex items-center gap-3">
          {/* Tab Switcher */}
          <div className="flex items-center rounded-lg border bg-card p-1">
            {([
              { key: "performance" as const, label: "Performance", icon: TrendingUp },
              { key: "dna" as const, label: "DNA", icon: Dna },
            ]).map((tab) => (
              <button
                key={tab.key}
                onClick={() => setActiveTab(tab.key)}
                className={cn(
                  "flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
                  activeTab === tab.key
                    ? "bg-primary text-primary-foreground shadow-sm"
                    : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
                )}
              >
                <tab.icon className="h-3.5 w-3.5" />
                {tab.label}
              </button>
            ))}
          </div>

          {/* Range Selector (only for performance tab) */}
          {activeTab === "performance" && (
            <div className="flex items-center rounded-lg border bg-card p-1">
              {(["7d", "30d", "90d", "all"] as RangeOption[]).map((opt) => (
                <button
                  key={opt}
                  onClick={() => setRange(opt)}
                  className={cn(
                    "rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
                    range === opt
                      ? "bg-primary text-primary-foreground shadow-sm"
                      : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
                  )}
                >
                  {opt.toUpperCase()}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {activeTab === "performance" && perfLoading && (
        <div className="flex h-96 items-center justify-center">
          <div className="h-8 w-8 animate-spin rounded-full border-b-2 border-emerald-500" />
        </div>
      )}

      {activeTab === "performance" && perfError && (
        <div className="flex h-96 flex-col items-center justify-center gap-4">
          <p className="text-destructive">Failed to load strategy data</p>
          <Button onClick={() => refetchDashboard()}>Retry</Button>
        </div>
      )}

      {activeTab === "performance" && dashboardData && !dashboardData.summary && (
        <div className="flex h-96 flex-col items-center justify-center gap-4 text-muted-foreground">
          <p>No performance data available for this strategy yet.</p>
          <Button variant="outline" onClick={() => refetchDashboard()}>Refresh</Button>
        </div>
      )}

      {activeTab === "performance" && dashboardData && dashboardData.summary && (
        <>
          {/* Overview Stat Cards */}
          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
            <StatCard
              title="Total P&L"
              value={formatCurrency(dashboardData.summary.totalRealizedPnl)}
              icon={DollarSign}
              trend={dashboardData.summary.totalRealizedPnl >= 0 ? "up" : "down"}
              className={
                dashboardData.summary.totalRealizedPnl >= 0 ? "text-emerald-500" : "text-rose-500"
              }
            />
            <StatCard
              title="Win Rate"
              value={formatPercent(dashboardData.summary.winRate * 100)}
              icon={Percent}
            />
            <StatCard
              title="Profit Factor"
              value={dashboardData.summary.profitFactor.toFixed(2)}
              icon={Scale}
            />
            <StatCard
              title="Sharpe Ratio"
              value={dashboardData.summary.sharpe?.toFixed(2) ?? "\u2014"}
              icon={Activity}
            />
            <StatCard
              title="Max Drawdown"
              value={formatPercent(dashboardData.summary.maxDrawdown ?? 0)}
              icon={TrendingDown}
              className="text-rose-500"
            />
            <StatCard
              title="Total Trades"
              value={formatNumber(dashboardData.summary.totalTrades)}
              icon={Hash}
            />
          </div>

          {/* Charts Section */}
          <div className="grid gap-4 lg:grid-cols-1">
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <TrendingUp className="h-5 w-5 text-emerald-500" />
                  Equity Curve
                </CardTitle>
              </CardHeader>
              <CardContent>
                <div ref={chartContainerRef} className="h-[300px] w-full" />
              </CardContent>
            </Card>
          </div>

          {/* Daily P&L Bars */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <BarChart3 className="h-5 w-5 text-blue-500" />
                Daily P&L
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="h-[200px] w-full overflow-x-auto">
                <div className="flex h-full items-end gap-1 pb-6 pt-2 min-w-max px-2">
                  {!dashboardData.dailyPnl || dashboardData.dailyPnl.length === 0 ? (
                    <div className="flex h-full w-full items-center justify-center text-muted-foreground">
                      No data for this period
                    </div>
                  ) : (
                    dashboardData.dailyPnl.map((day) => {
                      const isPositive = day.RealizedPnL >= 0;
                      const maxAbs = Math.max(
                        ...dashboardData.dailyPnl.map((d) => Math.abs(d.RealizedPnL))
                      );
                      const heightPct =
                        maxAbs > 0
                          ? (Math.abs(day.RealizedPnL) / maxAbs) * 100
                          : 0;

                      return (
                        <div
                          key={day.Day}
                          className="group relative flex flex-col items-center justify-end h-full w-3 sm:w-4"
                        >
                          <div
                            style={{ height: `${Math.max(heightPct, 2)}%` }}
                            className={cn(
                              "w-full rounded-t-sm transition-all hover:opacity-80",
                              isPositive ? "bg-emerald-500" : "bg-rose-500"
                            )}
                          />
                          {/* Tooltip */}
                          <div className="absolute bottom-full mb-2 hidden w-max rounded bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md group-hover:block z-50">
                            <div className="font-bold">{day.Day}</div>
                            <div
                              className={
                                isPositive ? "text-emerald-500" : "text-rose-500"
                              }
                            >
                              {formatCurrency(day.RealizedPnL)}
                            </div>
                            <div className="text-muted-foreground">
                              {day.TradeCount} trades
                            </div>
                          </div>
                        </div>
                      );
                    })
                  )}
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Symbol Attribution Table */}
          <Card>
            <CardHeader>
              <CardTitle>Symbol Attribution</CardTitle>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Symbol</TableHead>
                    <TableHead className="text-right">Realized P&L</TableHead>
                    <TableHead className="text-right">Trades</TableHead>
                    <TableHead className="text-right">Wins</TableHead>
                    <TableHead className="text-right">Losses</TableHead>
                    <TableHead className="text-right">Win Rate</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(dashboardData.bySymbol ?? []).map((s) => (
                    <TableRow key={s.symbol}>
                      <TableCell className="font-medium">{s.symbol}</TableCell>
                      <TableCell
                        className={cn(
                          "text-right font-mono",
                          s.realizedPnl >= 0 ? "text-emerald-500" : "text-rose-500"
                        )}
                      >
                        {formatCurrency(s.realizedPnl)}
                      </TableCell>
                      <TableCell className="text-right">{s.tradeCount}</TableCell>
                      <TableCell className="text-right text-emerald-500">
                        {s.winCount}
                      </TableCell>
                      <TableCell className="text-right text-rose-500">
                        {s.lossCount}
                      </TableCell>
                      <TableCell className="text-right">
                        {s.tradeCount > 0
                          ? formatPercent((s.winCount / s.tradeCount) * 100)
                          : "\u2014"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>

          {/* Live State Snapshots */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Zap className="h-5 w-5 text-amber-500" />
                Live State
              </CardTitle>
            </CardHeader>
            <CardContent>
              {loadingState ? (
                <div className="flex h-32 items-center justify-center text-muted-foreground">
                  Loading state...
                </div>
              ) : !stateData || stateData.length === 0 ? (
                <div className="flex h-32 items-center justify-center text-muted-foreground">
                  No active state data available
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Symbol</TableHead>
                      <TableHead>Kind</TableHead>
                      <TableHead>As Of</TableHead>
                      <TableHead>Payload</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {stateData.map((state, i) => (
                      <TableRow key={`${state.symbol}-${i}`}>
                        <TableCell className="font-medium">
                          {state.symbol}
                        </TableCell>
                        <TableCell>{state.kind}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {new Date(state.asOf).toLocaleTimeString()}
                        </TableCell>
                        <TableCell>
                          <div className="max-h-32 w-[300px] overflow-auto rounded bg-muted/50 p-2 text-xs font-mono sm:w-[500px]">
                            <pre>{JSON.stringify(state.payload, null, 2)}</pre>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>

          {/* Signal Lifecycle Log */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Eye className="h-5 w-5 text-blue-400" />
                Signal Log
              </CardTitle>
            </CardHeader>
            <CardContent>
              {signals.length === 0 ? (
                <div className="flex h-32 items-center justify-center text-muted-foreground">
                  No signals recorded
                </div>
              ) : (
                <div className="space-y-4">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Time</TableHead>
                        <TableHead>Signal ID</TableHead>
                        <TableHead>Symbol</TableHead>
                        <TableHead>Kind</TableHead>
                        <TableHead>Side</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead className="text-right">Conf</TableHead>
                        <TableHead>Reason</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {signals.filter((sig) => sig !== null).map((sig) => (
                        <TableRow key={`${sig.SignalID}-${sig.TS}`}>
                          <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                            {new Date(sig.TS).toLocaleString()}
                          </TableCell>
                          <TableCell className="font-mono text-xs">
                            {sig.SignalID.substring(0, 8)}...
                          </TableCell>
                          <TableCell className="font-medium">
                            {sig.Symbol}
                          </TableCell>
                          <TableCell>{sig.Kind}</TableCell>
                          <TableCell>
                            <span
                              className={cn(
                                "font-medium",
                                sig.Side === "BUY"
                                  ? "text-emerald-500"
                                  : "text-rose-500"
                              )}
                            >
                              {sig.Side}
                            </span>
                          </TableCell>
                          <TableCell>
                            <Badge
                              variant={
                                sig.Status === "generated" ? "outline" : "default"
                              }
                              className={cn(
                                sig.Status === "validated" && "bg-blue-500",
                                sig.Status === "executed" && "bg-emerald-500",
                                sig.Status === "rejected" && "bg-rose-500",
                                sig.Status === "suppressed" && "bg-amber-500"
                              )}
                            >
                              {sig.Status}
                            </Badge>
                          </TableCell>
                          <TableCell className="text-right">
                            {formatPercent(sig.Confidence * 100)}
                          </TableCell>
                          <TableCell className="max-w-[200px] truncate text-xs text-muted-foreground">
                            {sig.Reason}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                  {hasNextPage && (
                    <div className="flex justify-center pt-4">
                      <Button
                        variant="outline"
                        onClick={() => fetchNextPage()}
                        disabled={isFetchingNextPage}
                      >
                        {isFetchingNextPage ? "Loading..." : "Load More"}
                      </Button>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </>
      )}

      {activeTab === "dna" && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Dna className="h-5 w-5 text-purple-500" />
              Strategy Parameters
              {strategyDNA && (
                <Badge variant="outline" className="ml-2 font-mono text-xs">
                  v{strategyDNA.version}
                </Badge>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent>
            {!strategyDNA ? (
              <div className="flex h-32 items-center justify-center text-muted-foreground">
                No DNA configuration found for this strategy.
              </div>
            ) : Object.keys(strategyDNA.parameters).length === 0 ? (
              <div className="flex h-32 items-center justify-center text-muted-foreground">
                No parameters defined.
              </div>
            ) : (
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
                {Object.entries(strategyDNA.parameters).map(([key, value]) => (
                  <div key={key} className="rounded-md border border-border p-3">
                    <p className="text-xs text-muted-foreground">
                      {key
                        .replace(/_/g, " ")
                        .replace(/([A-Z])/g, " $1")
                        .replace(/^./, (s) => s.toUpperCase())}
                    </p>
                    <p className="mt-1 font-mono text-sm font-medium">
                      {String(value)}
                    </p>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function StatCard({
  title,
  value,
  icon: Icon,
  className,
}: {
  title: string;
  value: string;
  icon: LucideIcon;
  trend?: "up" | "down";
  className?: string;
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium text-muted-foreground">
          {title}
        </CardTitle>
        <Icon className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className={cn("text-2xl font-bold", className)}>{value}</div>
      </CardContent>
    </Card>
  );
}
