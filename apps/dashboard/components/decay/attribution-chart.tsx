"use client";

import { useMemo } from "react";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Cell,
} from "recharts";
import { CHART_COLORS } from "@/lib/chart-theme";
import type { ComponentAttribution } from "@/lib/decay-types";

interface AttributionChartProps {
  data: ComponentAttribution[] | undefined;
  isLoading: boolean;
}

const MIN_SAMPLE = 50;

function barColor(item: ComponentAttribution): string {
  if (item.nFired < MIN_SAMPLE) return "rgba(148, 163, 184, 0.4)"; // gray — insufficient data
  if (item.marginal !== null && item.marginal > 0) return "#10b981"; // emerald-500
  return "#f43f5e"; // rose-500
}

export function AttributionChart({ data, isLoading }: AttributionChartProps) {
  const sorted = useMemo(() => {
    if (!data || data.length === 0) return [];
    return [...data].sort((a, b) => {
      const absA = Math.abs(a.marginal ?? 0);
      const absB = Math.abs(b.marginal ?? 0);
      return absB - absA;
    });
  }, [data]);

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        Loading attribution data...
      </div>
    );
  }

  if (!data || data.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        No component attribution data available.
      </div>
    );
  }

  return (
    <ResponsiveContainer width="100%" height="100%">
      <BarChart
        data={sorted}
        layout="vertical"
        margin={{ top: 10, right: 10, left: 0, bottom: 0 }}
      >
        <CartesianGrid strokeDasharray="3 3" horizontal={false} stroke={CHART_COLORS.grid} />

        <XAxis
          type="number"
          tick={{ fill: CHART_COLORS.text, fontSize: 10 }}
          tickLine={false}
          axisLine={false}
          label={{ value: "Marginal PF", position: "insideBottom", offset: -2, fill: CHART_COLORS.text, fontSize: 10 }}
        />

        <YAxis
          type="category"
          dataKey="component"
          tick={{ fill: CHART_COLORS.text, fontSize: 9 }}
          tickLine={false}
          axisLine={false}
          width={110}
        />

        <Tooltip
          cursor={{ fill: "rgba(148, 163, 184, 0.08)" }}
          content={({ active, payload }) => {
            if (!active || !payload || payload.length === 0) return null;
            const item = payload[0].payload as ComponentAttribution;
            return (
              <div className="rounded-lg border bg-popover p-2 text-popover-foreground shadow-sm text-xs">
                <p className="font-medium mb-1">{item.component}</p>
                <div className="space-y-0.5">
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">Group</span>
                    <span>{item.group}</span>
                  </div>
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">PF (fired)</span>
                    <span>{item.pfFired !== null ? item.pfFired.toFixed(2) : "n/a"}</span>
                  </div>
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">PF (not fired)</span>
                    <span>{item.pfNotFired !== null ? item.pfNotFired.toFixed(2) : "n/a"}</span>
                  </div>
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">Marginal</span>
                    <span className={
                      item.marginal !== null && item.marginal > 0
                        ? "text-emerald-500 font-medium"
                        : "text-rose-500 font-medium"
                    }>
                      {item.marginal !== null ? item.marginal.toFixed(3) : "n/a"}
                    </span>
                  </div>
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">n (fired)</span>
                    <span>{item.nFired}</span>
                  </div>
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">n (not fired)</span>
                    <span>{item.nNotFired}</span>
                  </div>
                  {item.nFired < MIN_SAMPLE && (
                    <p className="text-amber-400 mt-1">Insufficient data (n &lt; {MIN_SAMPLE})</p>
                  )}
                </div>
              </div>
            );
          }}
        />

        <Bar dataKey="marginal" radius={[0, 4, 4, 0]}>
          {sorted.map((item, index) => (
            <Cell key={`cell-${index}`} fill={barColor(item)} />
          ))}
        </Bar>
      </BarChart>
    </ResponsiveContainer>
  );
}
