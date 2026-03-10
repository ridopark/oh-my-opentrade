"use client";

import { useMemo } from "react";
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { BarChart3 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { CHART_COLORS } from "@/lib/chart-theme";
import type { SymbolAttribution } from "@/lib/types";
import { cn } from "@/lib/utils";

interface SymbolAttributionChartProps {
  data: SymbolAttribution[] | undefined;
}

const formatCurrency = (value: number) => {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(value);
};

export function SymbolAttributionChart({ data }: SymbolAttributionChartProps) {
  const chartData = useMemo(() => {
    if (!data) return [];
    // Sort by absolute P&L to show most impactful first, or keep existing order? Let's keep existing or sort by P&L
    return [...data].sort((a, b) => b.realizedPnl - a.realizedPnl);
  }, [data]);

  if (!data || data.length === 0) {
    return (
      <Card className="col-span-1 lg:col-span-2">
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-sm font-medium">Symbol Attribution</CardTitle>
          <BarChart3 className="h-4 w-4 text-muted-foreground" />
        </CardHeader>
        <CardContent>
          <div className="flex h-[200px] items-center justify-center text-sm text-slate-500">
            No symbol data for this period
          </div>
        </CardContent>
      </Card>
    );
  }

  // Calculate dynamic height based on number of symbols to prevent squishing
  const chartHeight = Math.max(300, chartData.length * 40 + 60);

  return (
    <Card className="col-span-1 lg:col-span-2">
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <CardTitle className="text-sm font-medium">Symbol Attribution</CardTitle>
        <BarChart3 className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-8 mt-4">
          <div style={{ height: chartHeight }} className="w-full">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart
                data={chartData}
                layout="vertical"
                margin={{ top: 5, right: 20, left: 40, bottom: 5 }}
              >
                <CartesianGrid strokeDasharray="3 3" horizontal={true} vertical={false} stroke={CHART_COLORS.grid} />
                <XAxis 
                  type="number"
                  tickFormatter={(value) => formatCurrency(value)}
                  tick={{ fill: CHART_COLORS.text, fontSize: 12 }}
                  tickLine={false}
                  axisLine={false}
                />
                <YAxis 
                  type="category" 
                  dataKey="symbol"
                  tick={{ fill: CHART_COLORS.text, fontSize: 12, fontWeight: 500 }}
                  tickLine={false}
                  axisLine={false}
                />
                <Tooltip 
                  cursor={{ fill: "rgba(148, 163, 184, 0.1)" }}
                  contentStyle={{ 
                    backgroundColor: CHART_COLORS.tooltipBg,
                    borderColor: CHART_COLORS.grid,
                    color: CHART_COLORS.tooltipText 
                  }}
                  formatter={(value: unknown) => [formatCurrency(Number(value)), "P&L"]}
                  labelStyle={{ color: CHART_COLORS.text, marginBottom: 8 }}
                />
                <Bar dataKey="realizedPnl" radius={[0, 4, 4, 0]} barSize={20}>
                  {chartData.map((entry, index) => (
                    <Cell 
                      key={`cell-${index}`} 
                      fill={entry.realizedPnl >= 0 ? CHART_COLORS.positive : CHART_COLORS.negative} 
                    />
                  ))}
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          </div>

          <div>
            <div className="rounded-md border border-slate-800 h-full max-h-[400px] overflow-y-auto">
              <Table>
                <TableHeader className="sticky top-0 bg-background/95 backdrop-blur z-10">
                  <TableRow className="border-slate-800 hover:bg-transparent">
                    <TableHead>Symbol</TableHead>
                    <TableHead className="text-right">P&L</TableHead>
                    <TableHead className="text-right">Trades</TableHead>
                    <TableHead className="text-right">Win Rate</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {chartData.map((row) => {
                    const winRate = row.tradeCount > 0 
                      ? (row.winCount / row.tradeCount) * 100 
                      : 0;
                    
                    return (
                      <TableRow key={row.symbol} className="border-slate-800">
                        <TableCell className="font-medium">{row.symbol}</TableCell>
                        <TableCell className={cn(
                          "text-right font-medium",
                          row.realizedPnl >= 0 ? "text-emerald-500" : "text-rose-500"
                        )}>
                          {formatCurrency(row.realizedPnl)}
                        </TableCell>
                        <TableCell className="text-right">{row.tradeCount}</TableCell>
                        <TableCell className="text-right">
                          {row.tradeCount > 0 ? `${winRate.toFixed(1)}%` : "—"}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
