"use client";

import { useMemo } from "react";
import { format } from "date-fns";
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { BarChart3 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CHART_COLORS } from "@/lib/chart-theme";
import type { DailyPnlEntry } from "@/lib/types";

interface DailyPnlChartProps {
  data: DailyPnlEntry[] | undefined;
}

const formatCurrency = (value: number) => {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(value);
};

export function DailyPnlChart({ data }: DailyPnlChartProps) {
  const chartData = useMemo(() => {
    if (!data) return [];
    return data.map((d) => ({
      ...d,
      formattedDate: format(new Date(d.date), "MM/dd"),
      total_pnl: d.realized_pnl + d.unrealized_pnl
    }));
  }, [data]);

  if (!data || data.length === 0) {
    return (
      <Card className="col-span-1 lg:col-span-2">
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-sm font-medium">Daily P&L</CardTitle>
          <BarChart3 className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          <div className="flex h-[200px] items-center justify-center text-sm text-slate-500">
            No daily P&L data
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="col-span-1 lg:col-span-2">
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <CardTitle className="text-sm font-medium">Daily P&L</CardTitle>
        <BarChart3 className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="h-[200px] w-full mt-4">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={chartData} margin={{ top: 10, right: 10, left: 10, bottom: 0 }}>
              <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={CHART_COLORS.grid} />
              <XAxis 
                dataKey="formattedDate" 
                tick={{ fill: CHART_COLORS.text, fontSize: 12 }}
                tickLine={false}
                axisLine={false}
              />
              <YAxis 
                tickFormatter={(value) => formatCurrency(value)}
                tick={{ fill: CHART_COLORS.text, fontSize: 12 }}
                tickLine={false}
                axisLine={false}
                width={80}
              />
              <Tooltip 
                cursor={{ fill: "rgba(148, 163, 184, 0.1)" }}
                contentStyle={{ 
                  backgroundColor: CHART_COLORS.tooltipBg,
                  borderColor: CHART_COLORS.grid,
                  color: CHART_COLORS.tooltipText 
                }}
                labelStyle={{ color: CHART_COLORS.text, marginBottom: 8 }}
                content={({ active, payload, label }) => {
                  if (active && payload && payload.length) {
                    const data = payload[0].payload as typeof chartData[0];
                    return (
                      <div className="rounded-lg border bg-popover p-3 text-popover-foreground shadow-sm">
                        <p className="font-medium text-sm mb-2">{label}</p>
                        <div className="space-y-1 text-sm">
                          <div className="flex justify-between gap-4">
                            <span className="text-slate-400">P&L</span>
                            <span className={data.total_pnl >= 0 ? "text-emerald-500 font-medium" : "text-rose-500 font-medium"}>
                              {formatCurrency(data.total_pnl)}
                            </span>
                          </div>
                          <div className="flex justify-between gap-4">
                            <span className="text-slate-400">Trades</span>
                            <span className="font-medium">{data.trade_count}</span>
                          </div>
                        </div>
                      </div>
                    );
                  }
                  return null;
                }}
              />
              <Bar dataKey="total_pnl" radius={[4, 4, 0, 0]}>
                {chartData.map((entry, index) => (
                  <Cell 
                    key={`cell-${index}`} 
                    fill={entry.total_pnl >= 0 ? CHART_COLORS.positive : CHART_COLORS.negative} 
                  />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      </CardContent>
    </Card>
  );
}
