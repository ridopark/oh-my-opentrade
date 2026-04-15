"use client";

import { useMemo } from "react";
import { format } from "date-fns";
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { TrendingUp } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CHART_COLORS } from "@/lib/chart-theme";

export interface EquityPoint {
  time: string;
  equity: number;
  cash?: number;
  drawdown_pct?: number;
}

interface EquityCurveChartProps {
  data: EquityPoint[] | undefined;
  loading?: boolean;
}

const formatCurrency = (value: number) =>
  new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: 0,
  }).format(value);

export function EquityCurveChart({ data, loading }: EquityCurveChartProps) {
  const chartData = useMemo(() => {
    if (!data) return [];
    return data.map((d) => ({
      ...d,
      ts: new Date(d.time).getTime(),
      formatted: format(new Date(d.time), "MM/dd HH:mm"),
    }));
  }, [data]);

  const first = chartData[0]?.equity ?? 0;
  const last = chartData[chartData.length - 1]?.equity ?? 0;
  const delta = last - first;
  const deltaPct = first > 0 ? (delta / first) * 100 : 0;
  const positive = delta >= 0;

  const domain = useMemo(() => {
    if (chartData.length === 0) return ["auto", "auto"] as const;
    const values = chartData.map((d) => d.equity);
    const min = Math.min(...values);
    const max = Math.max(...values);
    const pad = (max - min) * 0.08 || max * 0.01;
    return [min - pad, max + pad] as const;
  }, [chartData]);

  if (loading) {
    return (
      <Card className="col-span-1 lg:col-span-2">
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-sm font-medium">Equity Curve</CardTitle>
          <TrendingUp className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          <div className="flex h-[260px] items-center justify-center text-sm text-slate-500">
            Loading equity curve...
          </div>
        </CardContent>
      </Card>
    );
  }

  if (!data || data.length === 0) {
    return (
      <Card className="col-span-1 lg:col-span-2">
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-sm font-medium">Equity Curve</CardTitle>
          <TrendingUp className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          <div className="flex h-[260px] items-center justify-center text-sm text-slate-500">
            No equity history yet
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="col-span-1 lg:col-span-2">
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <div className="flex items-baseline gap-3">
          <CardTitle className="text-sm font-medium">Equity Curve</CardTitle>
          <span className={positive ? "text-emerald-500 text-xs font-medium" : "text-rose-500 text-xs font-medium"}>
            {positive ? "+" : ""}
            {formatCurrency(delta)} ({positive ? "+" : ""}
            {deltaPct.toFixed(2)}%)
          </span>
        </div>
        <TrendingUp className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="h-[260px] w-full mt-4">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={chartData} margin={{ top: 10, right: 10, left: 10, bottom: 0 }}>
              <defs>
                <linearGradient id="equityGradient" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={positive ? CHART_COLORS.positive : CHART_COLORS.negative} stopOpacity={0.3} />
                  <stop offset="100%" stopColor={positive ? CHART_COLORS.positive : CHART_COLORS.negative} stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={CHART_COLORS.grid} />
              <XAxis
                dataKey="formatted"
                tick={{ fill: CHART_COLORS.text, fontSize: 11 }}
                tickLine={false}
                axisLine={false}
                minTickGap={40}
              />
              <YAxis
                domain={domain}
                tickFormatter={(v: number) => formatCurrency(v)}
                tick={{ fill: CHART_COLORS.text, fontSize: 11 }}
                tickLine={false}
                axisLine={false}
                width={80}
              />
              <Tooltip
                contentStyle={{
                  backgroundColor: CHART_COLORS.tooltipBg,
                  borderColor: CHART_COLORS.grid,
                  color: CHART_COLORS.tooltipText,
                }}
                labelStyle={{ color: CHART_COLORS.text, marginBottom: 6 }}
                content={({ active, payload }) => {
                  if (!active || !payload || !payload.length) return null;
                  const p = payload[0].payload as (typeof chartData)[number];
                  return (
                    <div className="rounded-lg border bg-popover p-3 text-popover-foreground shadow-sm">
                      <p className="font-medium text-sm mb-2">{p.formatted}</p>
                      <div className="space-y-1 text-sm">
                        <div className="flex justify-between gap-4">
                          <span className="text-slate-400">Equity</span>
                          <span className="font-medium">{formatCurrency(p.equity)}</span>
                        </div>
                        {p.cash !== undefined && (
                          <div className="flex justify-between gap-4">
                            <span className="text-slate-400">Cash</span>
                            <span className="font-medium">{formatCurrency(p.cash)}</span>
                          </div>
                        )}
                        {p.drawdown_pct !== undefined && p.drawdown_pct > 0 && (
                          <div className="flex justify-between gap-4">
                            <span className="text-slate-400">Drawdown</span>
                            <span className="text-rose-500 font-medium">-{(p.drawdown_pct * 100).toFixed(2)}%</span>
                          </div>
                        )}
                      </div>
                    </div>
                  );
                }}
              />
              <Area
                type="monotone"
                dataKey="equity"
                stroke={positive ? CHART_COLORS.positive : CHART_COLORS.negative}
                strokeWidth={2}
                fill="url(#equityGradient)"
              />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </CardContent>
    </Card>
  );
}
