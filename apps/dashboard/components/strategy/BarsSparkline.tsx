"use client";

import { useMemo } from "react";
import { cn } from "@/lib/utils";

// Inline-SVG 60-point sparkline for bars-per-minute over the last hour.
// Deliberately no charting lib: this renders hundreds of times in the
// per-symbol table and a dependency would dominate the component tree cost.
//
// Graceful degradation: when `data` is missing or empty the column still
// occupies its slot — we render a faint baseline so the table doesn't jump
// when the backend adds the field later.

interface BarsSparklineProps {
  data?: number[] | null;
  width?: number;
  height?: number;
  className?: string;
}

const SLOT_COUNT = 60;

export function BarsSparkline({
  data,
  width = 120,
  height = 24,
  className,
}: BarsSparklineProps) {
  const { points, max, total, last, hasData } = useMemo(() => {
    const arr = Array.isArray(data) ? data : [];
    const padded = arr.length >= SLOT_COUNT
      ? arr.slice(arr.length - SLOT_COUNT)
      : [...new Array(SLOT_COUNT - arr.length).fill(0), ...arr];
    let m = 0;
    let sum = 0;
    for (const v of padded) {
      const n = Number.isFinite(v) ? Math.max(0, v) : 0;
      if (n > m) m = n;
      sum += n;
    }
    const lastSlot = padded[padded.length - 1] ?? 0;
    return {
      points: padded,
      max: m,
      total: sum,
      last: lastSlot,
      hasData: arr.length > 0 && m > 0,
    };
  }, [data]);

  // Map (slot index, value) → polyline coordinates. Min is pinned at 0 so
  // idle stretches read as flatlines rather than bouncing off the baseline.
  const polyline = useMemo(() => {
    if (points.length === 0) return "";
    const denom = max > 0 ? max : 1;
    const stepX = points.length > 1 ? width / (points.length - 1) : width;
    return points
      .map((v, i) => {
        const x = i * stepX;
        const y = height - (Math.max(0, v) / denom) * (height - 2) - 1;
        return `${x.toFixed(2)},${y.toFixed(2)}`;
      })
      .join(" ");
  }, [points, max, width, height]);

  const strokeClass = hasData && last > 0
    ? "stroke-emerald-500/70"
    : "stroke-muted-foreground/40";

  const ariaLabel = hasData
    ? `Bars per minute sparkline: ${total} bars over the last ${SLOT_COUNT} minutes, latest slot ${last}`
    : "Bars per minute sparkline: no data yet";

  return (
    <svg
      role="img"
      aria-label={ariaLabel}
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className={cn("block overflow-visible", className)}
    >
      {/* Baseline — helps the eye anchor on empty/idle rows. */}
      <line
        x1={0}
        x2={width}
        y1={height - 1}
        y2={height - 1}
        className="stroke-border"
        strokeWidth={1}
      />
      {hasData && (
        <polyline
          points={polyline}
          fill="none"
          strokeWidth={1.25}
          strokeLinejoin="round"
          strokeLinecap="round"
          className={strokeClass}
        />
      )}
    </svg>
  );
}

export default BarsSparkline;
