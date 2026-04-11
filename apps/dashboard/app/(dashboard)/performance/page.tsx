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
  ChevronDown,
  ChevronUp,
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

// ─── Formatters ──────────────────────────────────────────

const formatCurrency = (val: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(val);

const formatPercent = (val: number) =>
  new Intl.NumberFormat("en-US", { style: "percent", minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(val / 100);

const formatNumber = (val: number) =>
  new Intl.NumberFormat("en-US").format(val);

// ─── URL ↔ Filter Sync ──────────────────────────────────

const VALID_RANGES = new Set(["1d", "7d", "30d", "90d", "all"]);

function filtersFromParams(params: URLSearchParams): PerformanceFilters {
  const from = params.get("from") ?? undefined;
  const to = params.get("to") ?? undefined;
  const range = params.get("range") ?? undefined;
  const strategy = params.get("strategy") ?? undefined;
  const symbol = params.get("symbol") ?? undefined;

  if (from && to) return { from, to, strategy, symbol };
  return { range: range && VALID_RANGES.has(range) ? range : "30d", strategy, symbol };
}

function filtersToParams(filters: PerformanceFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.from && filters.to) {
    params.set("from", filters.from);
    params.set("to", filters.to);
  } else if (filters.range && filters.range !== "30d") {
    params.set("range", filters.range);
  }
  if (filters.strategy) params.set("strategy", filters.strategy);
  if (filters.symbol) params.set("symbol", filters.symbol);
  return params;
}

// ─── Page Wrapper ────────────────────────────────────────

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

// ─── Stat Card ───────────────────────────────────────────

function StatCard({
  title,
  value,
  icon: Icon,
  className,
  subtitle,
  large,
}: {
  title: string;
  value: string;
  icon: LucideIcon;
  trend?: "up" | "down";
  className?: string;
  subtitle?: string;
  large?: boolean;
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-1 pt-4 px-4">
        <CardTitle className="text-xs font-medium text-muted-foreground">{title}</CardTitle>
        <Icon className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent className="px-4 pb-4">
        <div className={cn(large ? "text-2xl font-bold" : "text-lg font-semibold", className)}>{value}</div>
        {subtitle && <p className="text-[11px] text-muted-foreground mt-0.5">{subtitle}</p>}
      </CardContent>
    </Card>
  );
}

// ─── Main Content ────────────────────────────────────────

function PerformanceContent() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const [showDetails, setShowDetails] = useState(false);

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
  const symbolNames = symbolData?.map((s) => s.symbol) || [];

  const trades = tradesData?.pages.flatMap((page) => page.items) ?? [];

  const chartContainerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);

  // Equity Chart
  useEffect(() => {
    if (!chartContainerRef.current || !dashboardData || !dashboardData.equity?.length) return;

    const chart = createChart(chartContainerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "rgba(148, 163, 184, 1)",
      },
      grid: {
        vertLines: { color: "rgba(148, 163, 184, 0.1)" },
        horzLines: { color: "rgba(148, 163, 184, 0.1)" },
      },
      width: chartContainerRef.current.clientWidth,
      height: 320,
      timeScale: { timeVisible: true, secondsVisible: false },
    });

    const equitySeries = chart.addSeries(LineSeries, {
      color: "#10b981",
      lineWidth: 2,
    });

    const data = dashboardData.equity.map((pt) => ({
      time: (new Date(pt.time).getTime() / 1000) as Time,
      value: pt.equity,
    }));

    equitySeries.setData(data);
    chart.timeScale().fitContent();
    chartRef.current = chart;

    const resizeObserver = new ResizeObserver(() => {
      if (chartContainerRef.current && chartRef.current) {
        chartRef.current.applyOptions({ width: chartContainerRef.current.clientWidth });
      }
    });
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
    <div className="space-y-6">
      {/* Header + Filters */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Performance</h1>
          <p className="text-sm text-muted-foreground">
            {summary.num_trades} trades &middot; {summary.winning_days}W / {summary.losing_days}L days
          </p>
        </div>
        <PerformanceFilterBar
          filters={filters}
          onFiltersChange={setFilters}
          strategies={strategyNames}
          symbols={symbolNames}
        />
      </div>

      {/* Hero Metrics — 5 cards */}
      <div className="grid gap-3 grid-cols-2 sm:grid-cols-3 lg:grid-cols-5">
        <StatCard
          title="Total P&L"
          value={formatCurrency(summary.total_pnl)}
          icon={DollarSign}
          large
          className={summary.total_pnl >= 0 ? "text-emerald-400" : "text-red-400"}
          subtitle={`${formatCurrency(summary.gross_profit)} profit / ${formatCurrency(summary.gross_loss)} loss`}
        />
        <StatCard
          title="Win Rate"
          value={summary.win_rate !== null ? formatPercent(summary.win_rate * 100) : "\u2014"}
          icon={Percent}
          large
          className={summary.win_rate && summary.win_rate > 0.5 ? "text-emerald-400" : undefined}
        />
        <StatCard
          title="Profit Factor"
          value={summary.profit_factor?.toFixed(2) ?? "\u2014"}
          icon={Scale}
          large
          className={summary.profit_factor && summary.profit_factor > 1 ? "text-emerald-400" : "text-red-400"}
        />
        <StatCard
          title="Max Drawdown"
          value={formatPercent(summary.max_drawdown_pct)}
          icon={TrendingDown}
          large
          className="text-red-400"
        />
        <StatCard
          title="Total Trades"
          value={formatNumber(summary.num_trades)}
          icon={Hash}
          large
        />
      </div>

      {/* Secondary Metrics — collapsible */}
      <div>
        <button
          onClick={() => setShowDetails(!showDetails)}
          className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          {showDetails ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          {showDetails ? "Hide" : "Show"} detailed metrics
        </button>
        {showDetails && (
          <div className="grid gap-3 grid-cols-2 sm:grid-cols-4 mt-3">
            <StatCard title="Sharpe Ratio" value={summary.sharpe?.toFixed(2) ?? "\u2014"} icon={Activity} />
            <StatCard title="Sortino Ratio" value={summary.sortino?.toFixed(2) ?? "\u2014"} icon={Activity} />
            <StatCard title="Expectancy" value={summary.expectancy ? formatCurrency(summary.expectancy) : "\u2014"} icon={DollarSign} />
            <StatCard title="CAGR" value={summary.cagr !== null ? formatPercent(summary.cagr * 100) : "\u2014"} icon={TrendingUp} />
          </div>
        )}
      </div>

      {/* Equity Curve — centerpiece */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-base">
            <TrendingUp className="h-4 w-4 text-emerald-500" />
            Equity Curve
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div ref={chartContainerRef} className="h-[320px] w-full" />
        </CardContent>
      </Card>

      {/* Drawdown + Daily P&L side by side on large screens */}
      <div className="grid gap-4 lg:grid-cols-2">
        <DrawdownChart data={drawdown} />
        <DailyPnlChart data={daily_pnl} />
      </div>

      {/* Strategy + Symbol breakdowns side by side */}
      <div className="grid gap-4 lg:grid-cols-2">
        <StrategyComparisonTable data={strategies} />
        <SymbolAttributionChart data={symbolData} />
      </div>

      {/* Trade Log */}
      <TradeLogTable
        trades={trades}
        hasNextPage={hasNextPage}
        isFetchingNextPage={isFetchingNextPage}
        onLoadMore={() => fetchNextPage()}
      />
    </div>
  );
}
