"use client";

import React, { useEffect, useRef, memo } from "react";
import {
  createChart,
  CandlestickSeries,
  HistogramSeries,
  type IChartApi,
  type ISeriesApi,
  type CandlestickData,
  type Time,
  ColorType,
} from "lightweight-charts";
import type { OHLCBar } from "@/lib/use-chart-data";

interface MiniChartProps {
  data: OHLCBar[];
  width: number;
  height: number;
  formingTime?: number | null;
}

function MiniChartInner({ data, width, height, formingTime }: MiniChartProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const candleRef = useRef<any>(null);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const volumeRef = useRef<any>(null);
  const dataRef = useRef(data);
  dataRef.current = data;

  // Create chart once
  useEffect(() => {
    if (!containerRef.current || width <= 0 || height <= 0) return;

    const chart = createChart(containerRef.current, {
      width,
      height,
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "#94a3b8",
        fontSize: 10,
      },
      grid: {
        vertLines: { color: "rgba(51,65,85,0.3)" },
        horzLines: { color: "rgba(51,65,85,0.3)" },
      },
      crosshair: { mode: 0 },
      rightPriceScale: {
        borderColor: "rgba(51,65,85,0.5)",
        scaleMargins: { top: 0.1, bottom: 0.2 },
      },
      timeScale: {
        borderColor: "rgba(51,65,85,0.5)",
        timeVisible: true,
        secondsVisible: false,
        rightOffset: 3,
      },
      handleScroll: false,
      handleScale: false,
    });

    const candle = chart.addSeries(CandlestickSeries, {
      upColor: "#10b981",
      downColor: "#ef4444",
      borderUpColor: "#10b981",
      borderDownColor: "#ef4444",
      wickUpColor: "#10b981",
      wickDownColor: "#ef4444",
    });

    const volume = chart.addSeries(HistogramSeries, {
      priceFormat: { type: "volume" },
      priceScaleId: "volume",
    });
    volume.priceScale().applyOptions({
      scaleMargins: { top: 0.85, bottom: 0 },
    });

    chartRef.current = chart;
    candleRef.current = candle;
    volumeRef.current = volume;

    return () => {
      chart.remove();
      chartRef.current = null;
      candleRef.current = null;
      volumeRef.current = null;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Resize
  useEffect(() => {
    if (chartRef.current && width > 0 && height > 0) {
      chartRef.current.resize(width, height);
    }
  }, [width, height]);

  // Update data
  useEffect(() => {
    if (!candleRef.current || !volumeRef.current || data.length === 0) return;

    const candles: CandlestickData[] = data.map((b) => ({
      time: b.time as Time,
      open: b.open,
      high: b.high,
      low: b.low,
      close: b.close,
    }));

    const volumes = data.map((b) => ({
      time: b.time as Time,
      value: b.volume,
      color: b.close >= b.open ? "rgba(16,185,129,0.2)" : "rgba(239,68,68,0.2)",
    }));

    candleRef.current.setData(candles);
    volumeRef.current.setData(volumes);

    // Auto-scroll to latest
    chartRef.current?.timeScale().scrollToRealTime();
  }, [data]);

  // Forming candle pulse
  useEffect(() => {
    if (!candleRef.current || !formingTime) return;
    let animId: number;

    const pulse = () => {
      const bars = dataRef.current;
      const lastBar = bars[bars.length - 1];
      if (!lastBar || !candleRef.current) {
        animId = requestAnimationFrame(pulse);
        return;
      }

      const phase = (Math.sin(Date.now() * Math.PI / 500) + 1) / 2;
      const isBullish = lastBar.close >= lastBar.open;
      const baseRgb = isBullish ? "16, 185, 129" : "239, 68, 68";
      const alpha = 0.5 + phase * 0.5;
      const color = `rgba(${baseRgb}, ${alpha})`;

      try {
        candleRef.current.update({
          time: lastBar.time as Time,
          open: lastBar.open,
          high: lastBar.high,
          low: lastBar.low,
          close: lastBar.close,
          color,
          borderColor: color,
          wickColor: color,
        });
      } catch {
        // Ignore stale timestamp errors
      }

      animId = requestAnimationFrame(pulse);
    };

    animId = requestAnimationFrame(pulse);
    return () => cancelAnimationFrame(animId);
  }, [formingTime]);

  return <div ref={containerRef} />;
}

export const MiniChart = memo(MiniChartInner, (prev, next) => {
  return (
    prev.data === next.data &&
    prev.width === next.width &&
    prev.height === next.height &&
    prev.formingTime === next.formingTime
  );
});
