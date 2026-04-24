"use client";

import { useEffect, useRef } from "react";
import {
  createChart,
  ColorType,
  CandlestickSeries,
  CrosshairMode,
  TickMarkType,
  type IChartApi,
  type ISeriesApi,
  type Time,
} from "lightweight-charts";

const ET_TZ = "America/New_York";

function formatET(timeSec: number, opts: Intl.DateTimeFormatOptions): string {
  return new Date(timeSec * 1000).toLocaleString("en-US", { timeZone: ET_TZ, ...opts });
}

function tickMarkFormatterET(time: Time, tickMarkType: TickMarkType): string {
  const sec = typeof time === "number" ? time : 0;
  if (sec === 0) return "";
  switch (tickMarkType) {
    case TickMarkType.Year:
      return formatET(sec, { year: "numeric" });
    case TickMarkType.Month:
      return formatET(sec, { month: "short" });
    case TickMarkType.DayOfMonth:
      return formatET(sec, { month: "short", day: "numeric" });
    case TickMarkType.TimeWithSeconds:
      return formatET(sec, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
    default:
      return formatET(sec, { hour: "2-digit", minute: "2-digit", hour12: false });
  }
}

function crosshairFormatterET(time: Time): string {
  const sec = typeof time === "number" ? time : 0;
  if (sec === 0) return "";
  return formatET(sec, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
}
import type { OHLCBar } from "@/lib/use-chart-data";

interface Props {
  bars: OHLCBar[];
  forming: boolean;
  stale: boolean;
}

export function LiveSymbolChart({ bars, forming, stale }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Candlestick"> | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = createChart(containerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "#a1a1aa",
        fontFamily: "ui-monospace, SFMono-Regular, monospace",
      },
      grid: {
        vertLines: { color: "rgba(82, 82, 91, 0.15)" },
        horzLines: { color: "rgba(82, 82, 91, 0.15)" },
      },
      rightPriceScale: { borderColor: "rgba(82, 82, 91, 0.3)" },
      timeScale: {
        borderColor: "rgba(82, 82, 91, 0.3)",
        timeVisible: true,
        secondsVisible: false,
        tickMarkFormatter: tickMarkFormatterET,
      },
      localization: {
        timeFormatter: crosshairFormatterET,
      },
      crosshair: { mode: CrosshairMode.Normal },
      autoSize: true,
    });
    const series = chart.addSeries(CandlestickSeries, {
      upColor: "#10b981",
      downColor: "#ef4444",
      borderUpColor: "#10b981",
      borderDownColor: "#ef4444",
      wickUpColor: "#10b981",
      wickDownColor: "#ef4444",
    });
    chartRef.current = chart;
    seriesRef.current = series;
    return () => {
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
    };
  }, []);

  useEffect(() => {
    const s = seriesRef.current;
    if (!s || bars.length === 0) return;
    s.setData(
      bars.map((b) => ({
        time: b.time as Time,
        open: b.open,
        high: b.high,
        low: b.low,
        close: b.close,
      })),
    );
    chartRef.current?.timeScale().fitContent();
  }, [bars]);

  return (
    <div
      className="relative h-full w-full rounded-lg border border-border bg-card/40 p-2 transition-[box-shadow,border-color] duration-500"
      style={{
        boxShadow: stale
          ? "0 0 24px -8px rgba(245, 158, 11, 0.25) inset"
          : forming
            ? "0 0 32px -6px rgba(16, 185, 129, 0.45) inset, 0 0 16px -6px rgba(16, 185, 129, 0.25)"
            : "0 0 12px -8px rgba(16, 185, 129, 0.15) inset",
        borderColor: stale ? "rgba(245, 158, 11, 0.5)" : undefined,
      }}
    >
      <div ref={containerRef} className="h-full w-full" />
      {bars.length === 0 && (
        <div className="absolute inset-0 flex items-center justify-center text-sm text-muted-foreground">
          Waiting for bars…
        </div>
      )}
    </div>
  );
}
