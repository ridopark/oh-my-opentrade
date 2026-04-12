"use client";

import { useMemo } from "react";
import {
  ComposedChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceLine,
  ReferenceArea,
} from "recharts";
import { CHART_COLORS } from "@/lib/chart-theme";
import type { RollingDecayPoint } from "@/lib/decay-types";

interface RollingPfChartProps {
  data: RollingDecayPoint[] | undefined;
  isLoading: boolean;
}

const COLORS = {
  pf20: "#3b82f6",   // blue-500
  pf60: "#10b981",   // emerald-500
  pf120: "#a855f7",  // purple-500
  wr60: "#f97316",   // orange-500
} as const;

export function RollingPfChart({ data, isLoading }: RollingPfChartProps) {
  // Compute red/amber zones
  const redZones = useMemo(() => {
    if (!data || data.length === 0) return [];
    const zones: { x1: number; x2: number }[] = [];
    let start: number | null = null;
    for (const pt of data) {
      const pf = pt.rollingPf60 ?? pt.rollingPf20;
      if (pf !== null && pf < 1.0) {
        if (start === null) start = pt.tradeSeq;
      } else {
        if (start !== null) {
          zones.push({ x1: start, x2: pt.tradeSeq });
          start = null;
        }
      }
    }
    if (start !== null) {
      zones.push({ x1: start, x2: data[data.length - 1].tradeSeq });
    }
    return zones;
  }, [data]);

  const amberZones = useMemo(() => {
    if (!data || data.length === 0) return [];
    const zones: { x1: number; x2: number }[] = [];
    let start: number | null = null;
    for (const pt of data) {
      if (
        pt.rollingPf20 !== null &&
        pt.rollingPf120 !== null &&
        pt.rollingPf20 < pt.rollingPf120
      ) {
        if (start === null) start = pt.tradeSeq;
      } else {
        if (start !== null) {
          zones.push({ x1: start, x2: pt.tradeSeq });
          start = null;
        }
      }
    }
    if (start !== null) {
      zones.push({ x1: start, x2: data[data.length - 1].tradeSeq });
    }
    return zones;
  }, [data]);

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        Loading rolling PF data...
      </div>
    );
  }

  if (!data || data.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        No rolling decay data available.
      </div>
    );
  }

  return (
    <ResponsiveContainer width="100%" height="100%">
      <ComposedChart data={data} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
        <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={CHART_COLORS.grid} />

        {/* Red zones: PF < 1.0 */}
        {redZones.map((z, i) => (
          <ReferenceArea
            key={`red-${i}`}
            x1={z.x1}
            x2={z.x2}
            fill="rgba(244, 63, 94, 0.08)"
            strokeOpacity={0}
          />
        ))}

        {/* Amber zones: PF20 < PF120 */}
        {amberZones.map((z, i) => (
          <ReferenceArea
            key={`amber-${i}`}
            x1={z.x1}
            x2={z.x2}
            fill="rgba(245, 158, 11, 0.06)"
            strokeOpacity={0}
          />
        ))}

        <XAxis
          dataKey="tradeSeq"
          tick={{ fill: CHART_COLORS.text, fontSize: 10 }}
          tickLine={false}
          axisLine={false}
          label={{ value: "Trade #", position: "insideBottom", offset: -2, fill: CHART_COLORS.text, fontSize: 10 }}
        />

        {/* Left Y: Profit Factor */}
        <YAxis
          yAxisId="pf"
          tick={{ fill: CHART_COLORS.text, fontSize: 10 }}
          tickLine={false}
          axisLine={false}
          width={40}
          domain={[0, "auto"]}
          label={{ value: "PF", angle: -90, position: "insideLeft", fill: CHART_COLORS.text, fontSize: 10 }}
        />

        {/* Right Y: Win Rate */}
        <YAxis
          yAxisId="wr"
          orientation="right"
          tick={{ fill: CHART_COLORS.text, fontSize: 10 }}
          tickLine={false}
          axisLine={false}
          width={40}
          domain={[0, 1]}
          tickFormatter={(v: number) => `${(v * 100).toFixed(0)}%`}
          label={{ value: "WR", angle: 90, position: "insideRight", fill: CHART_COLORS.text, fontSize: 10 }}
        />

        <ReferenceLine yAxisId="pf" y={1.0} stroke="rgba(148, 163, 184, 0.4)" strokeDasharray="4 4" />

        <Tooltip
          contentStyle={{
            backgroundColor: CHART_COLORS.tooltipBg,
            borderColor: CHART_COLORS.grid,
            color: CHART_COLORS.tooltipText,
          }}
          content={({ active, payload }) => {
            if (!active || !payload || payload.length === 0) return null;
            const pt = payload[0].payload as RollingDecayPoint;
            return (
              <div className="rounded-lg border bg-popover p-2 text-popover-foreground shadow-sm text-xs">
                <p className="font-medium mb-1">Trade #{pt.tradeSeq}</p>
                <div className="space-y-0.5">
                  <div className="flex justify-between gap-3">
                    <span className="text-muted-foreground">PnL</span>
                    <span className={pt.pnl >= 0 ? "text-emerald-500" : "text-rose-500"}>
                      ${pt.pnl.toFixed(2)}
                    </span>
                  </div>
                  {pt.rollingPf20 !== null && (
                    <div className="flex justify-between gap-3">
                      <span style={{ color: COLORS.pf20 }}>PF-20</span>
                      <span>{pt.rollingPf20.toFixed(2)}</span>
                    </div>
                  )}
                  {pt.rollingPf60 !== null && (
                    <div className="flex justify-between gap-3">
                      <span style={{ color: COLORS.pf60 }}>PF-60</span>
                      <span>{pt.rollingPf60.toFixed(2)}</span>
                    </div>
                  )}
                  {pt.rollingPf120 !== null && (
                    <div className="flex justify-between gap-3">
                      <span style={{ color: COLORS.pf120 }}>PF-120</span>
                      <span>{pt.rollingPf120.toFixed(2)}</span>
                    </div>
                  )}
                  {pt.rollingWr60 !== null && (
                    <div className="flex justify-between gap-3">
                      <span style={{ color: COLORS.wr60 }}>WR-60</span>
                      <span>{(pt.rollingWr60 * 100).toFixed(1)}%</span>
                    </div>
                  )}
                </div>
              </div>
            );
          }}
        />

        <Line
          yAxisId="pf"
          type="monotone"
          dataKey="rollingPf20"
          stroke={COLORS.pf20}
          dot={false}
          strokeWidth={1.5}
          name="PF-20"
          connectNulls
        />
        <Line
          yAxisId="pf"
          type="monotone"
          dataKey="rollingPf60"
          stroke={COLORS.pf60}
          dot={false}
          strokeWidth={2}
          name="PF-60"
          connectNulls
        />
        <Line
          yAxisId="pf"
          type="monotone"
          dataKey="rollingPf120"
          stroke={COLORS.pf120}
          dot={false}
          strokeWidth={2}
          name="PF-120"
          connectNulls
        />
        <Line
          yAxisId="wr"
          type="monotone"
          dataKey="rollingWr60"
          stroke={COLORS.wr60}
          dot={false}
          strokeWidth={1.5}
          strokeDasharray="6 3"
          name="WR-60"
          connectNulls
        />
      </ComposedChart>
    </ResponsiveContainer>
  );
}
