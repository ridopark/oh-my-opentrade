"use client";

import { useEffect, useRef, useState } from "react";
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
  type LucideIcon,
} from "lucide-react";

import {
  usePerformanceDashboard,
  usePerformanceTrades,
  usePerformanceStrategies,
  PerformanceFilters,
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
import { cn } from "@/lib/utils";
import { PerformanceFilterBar } from "@/components/performance-filter-bar";

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

export default function PerformancePage() {
  const [filters, setFilters] = useState<PerformanceFilters>({ range: "30d" });

  const {
    data: dashboardData,
    isLoading: loadingDashboard,
    error: dashboardError,
    refetch: refetchDashboard,
  } = usePerformanceDashboard(filters);

  const {
    data: tradesData,
    isLoading: loadingTrades,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = usePerformanceTrades(filters);

  const { data: strategies } = usePerformanceStrategies(filters);
  const strategyNames = strategies?.map((s) => s.strategy) || [];

  // Flatten all pages into a single trades array
  const trades = tradesData?.pages.flatMap((page) => page.items) ?? [];

  const chartContainerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);

  // Equity Chart
  useEffect(() => {
    if (!chartContainerRef.current || !dashboardData) return;


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


    // Transform data
    const data = dashboardData.equity.map((pt) => ({
      time: (new Date(pt.time).getTime() / 1000) as Time,
      value: pt.equity,
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

  if (loadingDashboard) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-b-2 border-emerald-500" />
      </div>
    );
  }

  if (dashboardError) {
    return (
      <div className="flex h-96 flex-col items-center justify-center gap-4">
        <p className="text-destructive">Failed to load performance data</p>
        <Button onClick={() => refetchDashboard()}>Retry</Button>
      </div>
    );
  }

  const { summary, daily_pnl } = dashboardData!;

  return (
    <div className="space-y-8">
      {/* Header & Range Selector */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Performance</h1>
          <p className="text-muted-foreground">
            Track your trading performance and metrics.
          </p>
        </div>
        <PerformanceFilterBar
          filters={filters}
          onFiltersChange={setFilters}
          strategies={strategyNames}
        />
      </div>

      {/* Summary Stats Grid */}
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        <StatCard
          title="Total P&L"
          value={formatCurrency(summary.total_pnl)}
          icon={DollarSign}
          trend={summary.total_pnl >= 0 ? "up" : "down"}
          className={
            summary.total_pnl >= 0 ? "text-emerald-500" : "text-rose-500"
          }
        />
        <StatCard
          title="Sharpe Ratio"
          value={summary.sharpe?.toFixed(2) ?? "—"}
          icon={Activity}
        />
        <StatCard
          title="Max Drawdown"
          value={formatPercent(summary.max_drawdown_pct)}
          icon={TrendingDown}
          className="text-rose-500"
        />
        <StatCard
          title="Win Rate"
          value={
            summary.win_rate !== null
              ? formatPercent(summary.win_rate * 100)
              : "—"
          }
          icon={Percent}
        />
        <StatCard
          title="Profit Factor"
          value={summary.profit_factor?.toFixed(2) ?? "—"}
          icon={Scale}
        />
        <StatCard
          title="Sortino Ratio"
          value={summary.sortino?.toFixed(2) ?? "—"}
          icon={Activity}
        />
        <StatCard
          title="Expectancy"
          value={summary.expectancy ? formatCurrency(summary.expectancy) : "—"}
          icon={DollarSign}
        />
        <StatCard
          title="CAGR"
          value={summary.cagr !== null ? formatPercent(summary.cagr * 100) : "—"}
          icon={TrendingUp}
        />
        <StatCard
          title="Total Trades"
          value={formatNumber(summary.num_trades)}
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
              {daily_pnl.length === 0 ? (
                <div className="flex h-full w-full items-center justify-center text-muted-foreground">
                  No data for this period
                </div>
              ) : (
                daily_pnl.map((day) => {
                  const isPositive = day.realized_pnl >= 0;
                  const maxAbs = Math.max(
                    ...daily_pnl.map((d) => Math.abs(d.realized_pnl))
                  );
                  // Min height 4px so 0 is visible as a line
                  const heightPct =
                    maxAbs > 0
                      ? (Math.abs(day.realized_pnl) / maxAbs) * 100
                      : 0;

                  return (
                    <div
                      key={day.date}
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
                        <div className="font-bold">{day.date}</div>
                        <div
                          className={
                            isPositive ? "text-emerald-500" : "text-rose-500"
                          }
                        >
                          {formatCurrency(day.realized_pnl)}
                        </div>
                        <div className="text-muted-foreground">
                          {day.trade_count} trades
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

      {/* Trades Table */}
      <Card>
        <CardHeader>
          <CardTitle>Trades</CardTitle>
        </CardHeader>
        <CardContent>
          {trades.length === 0 && !loadingTrades ? (
            <div className="text-center text-muted-foreground py-8">
              No trades in this period
            </div>
          ) : (
            <div className="space-y-4">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Time</TableHead>
                    <TableHead>Symbol</TableHead>
                    <TableHead>Side</TableHead>
                    <TableHead className="text-right">Qty</TableHead>
                    <TableHead className="text-right">Price</TableHead>
                    <TableHead className="text-right">Comm</TableHead>
                    <TableHead>Status</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {trades.map((trade) => (
                    <TableRow key={trade.trade_id}>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {new Date(trade.time).toLocaleString()}
                      </TableCell>
                      <TableCell className="font-medium">
                        {trade.symbol}
                      </TableCell>
                      <TableCell>
                        <span
                          className={cn(
                            "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
                            trade.side === "BUY"
                              ? "bg-emerald-500/10 text-emerald-500"
                              : "bg-rose-500/10 text-rose-500"
                          )}
                        >
                          {trade.side}
                        </span>
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        {trade.quantity}
                      </TableCell>
                      <TableCell className="text-right font-mono">
                        {formatCurrency(trade.price)}
                      </TableCell>
                      <TableCell className="text-right font-mono text-muted-foreground">
                        {formatCurrency(trade.commission)}
                      </TableCell>
                      <TableCell>
                        <span className="text-xs text-muted-foreground uppercase">
                          {trade.status}
                        </span>
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
