"use client";

import { useMemo } from "react";
import { format } from "date-fns";
import { Area, AreaChart, ResponsiveContainer, Tooltip, XAxis, YAxis, CartesianGrid } from "recharts";
import { TrendingDown } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CHART_COLORS } from "@/lib/chart-theme";
import type { DrawdownPoint } from "@/lib/types";

interface DrawdownChartProps {
  data: DrawdownPoint[] | undefined;
}

export function DrawdownChart({ data }: DrawdownChartProps) {
  const chartData = useMemo(() => {
    if (!data) return [];
    return data.map((d) => ({
      ...d,
      formattedTime: format(new Date(d.time), "MMM dd"),
    }));
  }, [data]);

  if (!data || data.length === 0) {
    return (
      <Card>
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-sm font-medium">Drawdown</CardTitle>
          <TrendingDown className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          <div className="flex h-[200px] items-center justify-center text-sm text-slate-500">
            No drawdown data
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <CardTitle className="text-sm font-medium">Drawdown</CardTitle>
        <TrendingDown className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="h-[200px] w-full mt-4">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={chartData} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id="colorDrawdown" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor={CHART_COLORS.negative} stopOpacity={0.3} />
                  <stop offset="95%" stopColor={CHART_COLORS.negative} stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={CHART_COLORS.grid} />
              <XAxis 
                dataKey="formattedTime" 
                tick={{ fill: CHART_COLORS.text, fontSize: 12 }}
                tickLine={false}
                axisLine={false}
                minTickGap={30}
              />
              <YAxis 
                tickFormatter={(value) => `${value}%`}
                tick={{ fill: CHART_COLORS.text, fontSize: 12 }}
                tickLine={false}
                axisLine={false}
                width={50}
              />
              <Tooltip 
                contentStyle={{ 
                  backgroundColor: CHART_COLORS.tooltipBg,
                  borderColor: CHART_COLORS.grid,
                  color: CHART_COLORS.tooltipText 
                }}
                itemStyle={{ color: CHART_COLORS.negative }}
                formatter={(value: unknown) => [`${Number(value).toFixed(2)}%`, "Drawdown"]}
                labelStyle={{ color: CHART_COLORS.text, marginBottom: 4 }}
              />
              <Area 
                type="monotone" 
                dataKey="drawdown_pct" 
                stroke={CHART_COLORS.negative} 
                fillOpacity={1} 
                fill="url(#colorDrawdown)" 
              />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </CardContent>
    </Card>
  );
}
