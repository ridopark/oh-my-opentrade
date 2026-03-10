"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams, useRouter } from "next/navigation";
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
  usePerformanceSymbols,
  PerformanceFilters,
} from "@/hooks/queries";
import { DrawdownChart } from "@/components/charts/drawdown-chart";
import { DailyPnlChart } from "@/components/charts/daily-pnl-chart";
import { SymbolAttributionChart } from "@/components/charts/symbol-attribution-chart";
import { StrategyComparisonTable } from "@/components/strategy-comparison-table";
import { TradeLogTable } from "@/components/trade-log-table";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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

// ---------------------------------------------------------------------------
// URL ↔ filter sync helpers
// ---------------------------------------------------------------------------

const VALID_RANGES = new Set(["7d", "30d", "90d", "all"]);

function filtersFromParams(params: URLSearchParams): PerformanceFilters {
  const from = params.get("from") ?? undefined;
  const to = params.get("to") ?? undefined;
  const range = params.get("range") ?? undefined;
  const strategy = params.get("strategy") ?? undefined;
  const symbol = params.get("symbol") ?? undefined;

  // Custom date range takes precedence over range preset
  if (from && to) {
    return { from, to, strategy, symbol };
  }
  return {
    range: range && VALID_RANGES.has(range) ? range : "30d",
    strategy,
    symbol,
  };
}

function filtersToParams(filters: PerformanceFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.from && filters.to) {
    params.set("from", filters.from);
    params.set("to", filters.to);
  } else if (filters.range && filters.range !== "30d") {
    // Omit range=30d (the default) to keep URLs clean
    params.set("range", filters.range);
  }
  if (filters.strategy) params.set("strategy", filters.strategy);
  if (filters.symbol) params.set("symbol", filters.symbol);
  return params;
}

// ---------------------------------------------------------------------------
// Page wrapper (Suspense required for useSearchParams)
// ---------------------------------------------------------------------------

export default function PerformancePage() {
  return (
    <Suspense
      fallback={
        <div className="flex h-96 items-center justify-center">
          <div className="h-8 w-8 animate-spin rounded-full border-b-2 border-emerald-500" />
        </div>
      }
    >
      <PerformanceContent />
    </Suspense>
  );
}

function PerformanceContent() {
  const searchParams = useSearchParams();
  const router = useRouter();

  const [filters, setFiltersState] = useState<PerformanceFilters>(() =>
    filtersFromParams(searchParams),
  );

  const setFilters = useCallback(
    (next: PerformanceFilters) => {
      setFiltersState(next);
      const qs = filtersToParams(next).toString();
      router.replace(qs ? `?${qs}` : "/performance", { scroll: false });
    },
    [router],
  );

  const {
    data: dashboardData,
    isLoading: loadingDashboard,
    error: dashboardError,
    refetch: refetchDashboard,
  } = usePerformanceDashboard(filters);

  const {
    data: tradesData,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = usePerformanceTrades(filters);

  const { data: strategies } = usePerformanceStrategies(filters);
  const strategyNames = strategies?.map((s) => s.strategy) || [];

  const { data: symbolData } = usePerformanceSymbols(filters);

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

  const { summary, daily_pnl, drawdown } = dashboardData!;

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
        <DrawdownChart data={drawdown} />
      </div>

      <DailyPnlChart data={daily_pnl} />

      <StrategyComparisonTable data={strategies} />

      <SymbolAttributionChart data={symbolData} />

      <TradeLogTable
        trades={trades}
        hasNextPage={hasNextPage}
        isFetchingNextPage={isFetchingNextPage}
        onLoadMore={() => fetchNextPage()}
      />
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
