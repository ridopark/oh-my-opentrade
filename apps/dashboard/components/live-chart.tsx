"use client";

import React, { useEffect, useRef, memo } from "react";
import {
  createChart,
  ColorType,
  CandlestickSeries,
  HistogramSeries,
  LineSeries,
  CrosshairMode,
  type IChartApi,
  type ISeriesApi,
  type Time,
} from "lightweight-charts";
import { SignalMarkerOverlay, type SignalMarkerData } from "@/lib/signal-markers";
import { ORBBoxOverlay, computeORBRanges } from "@/lib/orb-box-overlay";
import { RTHShadingOverlay, computeNonRTHRegions } from "@/lib/rth-shading-overlay";
import type { OHLCBar } from "@/lib/use-chart-data";

// ─── Helper functions ────────────────────────────────────────

/** Format a UTC unix timestamp (seconds) as ET string using Intl. */
function formatET(utcSeconds: number, opts: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat("en-US", { timeZone: "America/New_York", hourCycle: "h23", ...opts }).format(new Date(utcSeconds * 1000));
}

/** Compute EMA from close prices. Returns data points starting after `period` bars. */
function computeEMA(bars: OHLCBar[], period: number): { time: number; value: number }[] {
  if (bars.length < period) return [];
  const k = 2 / (period + 1);
  const result: { time: number; value: number }[] = [];
  let sum = 0;
  for (let i = 0; i < period; i++) sum += bars[i].close;
  let ema = sum / period;
  result.push({ time: bars[period - 1].time, value: ema });
  for (let i = period; i < bars.length; i++) {
    ema = bars[i].close * k + ema * (1 - k);
    result.push({ time: bars[i].time, value: ema });
  }
  return result;
}

/** Check if a UTC timestamp (seconds) falls within RTH (9:30-16:00 ET). */
const _rthFmt = new Intl.DateTimeFormat("en-US", {
  timeZone: "America/New_York",
  hour: "2-digit", minute: "2-digit", hourCycle: "h23",
});
function isRTH(utcSeconds: number): boolean {
  const parts = _rthFmt.formatToParts(new Date(utcSeconds * 1000));
  const h = parseInt(parts.find(p => p.type === "hour")?.value ?? "0");
  const m = parseInt(parts.find(p => p.type === "minute")?.value ?? "0");
  const mins = h * 60 + m;
  return mins >= 570 && mins < 960; // 9:30 (570) to 16:00 (960)
}

/** Compute anchored VWAP starting from a given bar index. When rthOnly is true, only RTH bars contribute to the accumulator and appear in output. */
function computeAVWAPFromIndex(bars: OHLCBar[], startIdx: number, rthOnly = false): { time: number; value: number }[] {
  const result: { time: number; value: number }[] = [];
  let cumTPV = 0;
  let cumVol = 0;
  for (let i = startIdx; i < bars.length; i++) {
    if (rthOnly && !isRTH(bars[i].time)) continue;
    const tp = (bars[i].high + bars[i].low + bars[i].close) / 3;
    cumTPV += tp * bars[i].volume;
    cumVol += bars[i].volume;
    if (cumVol > 0) {
      result.push({ time: bars[i].time, value: cumTPV / cumVol });
    }
  }
  return result;
}

interface AVWAPAnchors {
  sessionOpen: number[]; // bar indices of session opens (9:30 ET)
  pdHigh: number[];      // bar indices where PD high AVWAP should start (current session open, using PD high price context)
  pdLow: number[];       // bar indices where PD low AVWAP should start
  pdHighPriceIdx: number[]; // bar indices of actual PD high bars (for anchor line start)
  pdLowPriceIdx: number[];  // bar indices of actual PD low bars
}

/** Find session-open bar indices, PD-high/low anchor indices for AVWAP computation. */
function findAVWAPAnchors(bars: OHLCBar[]): AVWAPAnchors {
  const etFormatter = new Intl.DateTimeFormat("en-US", {
    timeZone: "America/New_York",
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  });

  type BarInfo = { idx: number; hour: number; minute: number; bar: OHLCBar };
  type SessionInfo = { date: string; bars: BarInfo[] };
  const sessions = new Map<string, SessionInfo>();

  for (let i = 0; i < bars.length; i++) {
    const parts = etFormatter.formatToParts(new Date(bars[i].time * 1000));
    const dateStr = `${parts.find(p => p.type === "year")?.value}-${parts.find(p => p.type === "month")?.value}-${parts.find(p => p.type === "day")?.value}`;
    const hour = parseInt(parts.find(p => p.type === "hour")?.value ?? "0");
    const minute = parseInt(parts.find(p => p.type === "minute")?.value ?? "0");

    if (!sessions.has(dateStr)) sessions.set(dateStr, { date: dateStr, bars: [] });
    sessions.get(dateStr)!.bars.push({ idx: i, hour, minute, bar: bars[i] });
  }

  const sessionDays = Array.from(sessions.values()).sort((a, b) => a.date.localeCompare(b.date));

  const sessionOpen: number[] = [];
  const pdHigh: number[] = [];
  const pdLow: number[] = [];
  const pdHighPriceIdx: number[] = [];
  const pdLowPriceIdx: number[] = [];

  for (let d = 0; d < sessionDays.length; d++) {
    const day = sessionDays[d];
    const openBar = day.bars.find(b => b.hour > 9 || (b.hour === 9 && b.minute >= 30));
    if (openBar) sessionOpen.push(openBar.idx);

    if (d > 0) {
      const prevDay = sessionDays[d - 1];
      const rthBars = prevDay.bars.filter(b =>
        (b.hour > 9 || (b.hour === 9 && b.minute >= 30)) && b.hour < 16
      );
      if (rthBars.length > 0 && openBar) {
        let highIdx = rthBars[0].idx;
        let lowIdx = rthBars[0].idx;
        let highVal = rthBars[0].bar.high;
        let lowVal = rthBars[0].bar.low;
        for (const rb of rthBars) {
          if (rb.bar.high > highVal) { highVal = rb.bar.high; highIdx = rb.idx; }
          if (rb.bar.low < lowVal) { lowVal = rb.bar.low; lowIdx = rb.idx; }
        }
        // AVWAP for PD high/low anchored at the bar where the high/low occurred
        pdHighPriceIdx.push(highIdx);
        pdLowPriceIdx.push(lowIdx);
        pdHigh.push(highIdx);
        pdLow.push(lowIdx);
      }
    }
  }

  return { sessionOpen, pdHigh, pdLow, pdHighPriceIdx, pdLowPriceIdx };
}

/** Compute all 3 AVWAP lines from bar data. Returns merged data arrays for each anchor type. */
function computeAllAVWAPs(bars: OHLCBar[]): {
  sessionOpen: { time: number; value: number }[];
  pdHigh: { time: number; value: number }[];
  pdLow: { time: number; value: number }[];
} {
  const anchors = findAVWAPAnchors(bars);

  // For session_open: each day gets its own AVWAP line from 9:30 open, RTH-only
  const sessionOpenData: { time: number; value: number }[] = [];
  for (let i = 0; i < anchors.sessionOpen.length; i++) {
    const startIdx = anchors.sessionOpen[i];
    const endIdx = i + 1 < anchors.sessionOpen.length ? anchors.sessionOpen[i + 1] : bars.length;
    const segment = computeAVWAPFromIndex(bars.slice(0, endIdx), startIdx, true);
    sessionOpenData.push(...segment);
  }

  // For PD high/low: accumulate from anchor bar (in previous day's RTH) but
  // only EMIT data points starting from the current session open.
  // This avoids jagged lines bridging across overnight/pre-market gaps.
  const pdHighData: { time: number; value: number }[] = [];
  for (let i = 0; i < anchors.pdHigh.length; i++) {
    const startIdx = anchors.pdHigh[i];
    const endIdx = i + 1 < anchors.pdHigh.length ? anchors.pdHigh[i + 1] : bars.length;
    // Find the current session open that follows this PD anchor
    const sessionOpenIdx = anchors.sessionOpen.find(s => s > startIdx) ?? startIdx;
    const segment = computeAVWAPFromIndex(bars.slice(0, endIdx), startIdx, true);
    // Filter: only emit points at or after the current session open bar time
    const sessionOpenTime = bars[sessionOpenIdx]?.time ?? 0;
    const filtered = segment.filter(p => p.time >= sessionOpenTime);
    pdHighData.push(...filtered);
  }

  const pdLowData: { time: number; value: number }[] = [];
  for (let i = 0; i < anchors.pdLow.length; i++) {
    const startIdx = anchors.pdLow[i];
    const endIdx = i + 1 < anchors.pdLow.length ? anchors.pdLow[i + 1] : bars.length;
    const sessionOpenIdx = anchors.sessionOpen.find(s => s > startIdx) ?? startIdx;
    const segment = computeAVWAPFromIndex(bars.slice(0, endIdx), startIdx, true);
    const sessionOpenTime = bars[sessionOpenIdx]?.time ?? 0;
    const filtered = segment.filter(p => p.time >= sessionOpenTime);
    pdLowData.push(...filtered);
  }

  return { sessionOpen: sessionOpenData, pdHigh: pdHighData, pdLow: pdLowData };
}

/** Color for each AVWAP anchor type */
export function avwapAnchorColor(name: string): string {
  if (name === "session_open") return "rgba(56, 189, 248, 0.5)";
  if (name === "pd_high") return "rgba(251, 146, 60, 0.5)";
  if (name === "pd_low") return "rgba(167, 139, 250, 0.5)";
  return "rgba(148, 163, 184, 0.4)";
}

/** Human-readable label for an anchor name */
export function avwapAnchorLabel(name: string): string {
  if (name === "session_open") return "AVWAP Open";
  if (name === "pd_high") return "AVWAP PD High";
  if (name === "pd_low") return "AVWAP PD Low";
  return `AVWAP ${name}`;
}

// ─── LiveChart component ─────────────────────────────────────

export interface LiveChartProps {
  symbol: string;
  bars: OHLCBar[];
  signals?: SignalMarkerData[];
  orbWindowMinutes?: number;
  showLabels?: boolean;
  hiddenSeries?: Set<string>;
  height?: number;
}

export const LiveChart = memo(function LiveChart({
  symbol,
  bars,
  signals = [],
  orbWindowMinutes = 30,
  showLabels = true,
  hiddenSeries,
  height,
}: LiveChartProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const candleRef = useRef<ISeriesApi<"Candlestick", Time> | null>(null);
  const volumeRef = useRef<ISeriesApi<"Histogram", Time> | null>(null);
  const ema9Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema21Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema50Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema200Ref = useRef<ISeriesApi<"Line", Time> | null>(null);
  const avwapOpenRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const avwapPDHighRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const avwapPDLowRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const overlayRef = useRef<SignalMarkerOverlay | null>(null);
  const orbOverlayRef = useRef<ORBBoxOverlay | null>(null);
  const rthOverlayRef = useRef<RTHShadingOverlay | null>(null);
  const lastBarCountRef = useRef(0);
  const initialRangeSetRef = useRef(false);

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = createChart(containerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "rgba(148, 163, 184, 0.8)",
        fontFamily: "var(--font-geist-mono, monospace)",
        fontSize: 9,
      },
      grid: {
        vertLines: { color: "rgba(148, 163, 184, 0.05)" },
        horzLines: { color: "rgba(148, 163, 184, 0.05)" },
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: "rgba(148, 163, 184, 0.2)", width: 1 as const, style: 3 as const, labelBackgroundColor: "#1f2937" },
        horzLine: { color: "rgba(148, 163, 184, 0.2)", width: 1 as const, style: 3 as const, labelBackgroundColor: "#1f2937" },
      },
      rightPriceScale: { borderColor: "rgba(148, 163, 184, 0.1)", scaleMargins: { top: 0.05, bottom: 0.15 } },
      localization: {
        timeFormatter: (time: number) => {
          return formatET(time, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
        },
      },
      timeScale: {
        borderColor: "rgba(148, 163, 184, 0.1)", timeVisible: true, rightOffset: 5, fixLeftEdge: true, fixRightEdge: true,
        tickMarkFormatter: (time: number) => {
          return formatET(time, { hour: "2-digit", minute: "2-digit" });
        },
      },
    });
    chartRef.current = chart;

    const volume = chart.addSeries(HistogramSeries, {
      priceScaleId: "", priceFormat: { type: "volume" }, lastValueVisible: false, priceLineVisible: false,
    });
    chart.priceScale("").applyOptions({ scaleMargins: { top: 0.85, bottom: 0 }, visible: false });
    volumeRef.current = volume;

    const candle = chart.addSeries(CandlestickSeries, {
      upColor: "#10b981", downColor: "#ef4444", borderVisible: false,
      wickUpColor: "#10b981", wickDownColor: "#ef4444",
    });
    candleRef.current = candle;

    // EMA lines
    ema9Ref.current = chart.addSeries(LineSeries, {
      color: "rgba(251, 191, 36, 0.7)", lineWidth: 1, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema21Ref.current = chart.addSeries(LineSeries, {
      color: "rgba(139, 92, 246, 0.7)", lineWidth: 1, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema50Ref.current = chart.addSeries(LineSeries, {
      color: "rgba(236, 72, 153, 0.6)", lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    ema200Ref.current = chart.addSeries(LineSeries, {
      color: "rgba(249, 115, 22, 0.5)", lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });

    // AVWAP lines
    avwapOpenRef.current = chart.addSeries(LineSeries, {
      color: avwapAnchorColor("session_open"), lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    avwapPDHighRef.current = chart.addSeries(LineSeries, {
      color: avwapAnchorColor("pd_high"), lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });
    avwapPDLowRef.current = chart.addSeries(LineSeries, {
      color: avwapAnchorColor("pd_low"), lineWidth: 1, lineStyle: 2, priceLineVisible: false, lastValueVisible: false, crosshairMarkerVisible: false,
    });

    // Signal marker overlay
    const overlay = new SignalMarkerOverlay();
    candle.attachPrimitive(overlay);
    overlayRef.current = overlay;

    // ORB box overlay
    const orbOverlay = new ORBBoxOverlay();
    candle.attachPrimitive(orbOverlay);
    orbOverlayRef.current = orbOverlay;

    // RTH shading overlay
    const rthOverlay = new RTHShadingOverlay();
    candle.attachPrimitive(rthOverlay);
    rthOverlayRef.current = rthOverlay;

    const containerEl = containerRef.current;
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        chart.applyOptions({ width: entry.contentRect.width, height: entry.contentRect.height });
      }
    });
    observer.observe(containerEl);

    return () => {
      observer.disconnect();
      chart.remove();
      chartRef.current = null;
      candleRef.current = null;
      volumeRef.current = null;
      ema9Ref.current = null;
      ema21Ref.current = null;
      ema50Ref.current = null;
      ema200Ref.current = null;
      avwapOpenRef.current = null;
      avwapPDHighRef.current = null;
      avwapPDLowRef.current = null;
      overlayRef.current = null;
      orbOverlayRef.current = null;
      rthOverlayRef.current = null;
      lastBarCountRef.current = 0;
      initialRangeSetRef.current = false;
    };
  }, []);

  // Update bar data + indicators
  useEffect(() => {
    if (!candleRef.current || !volumeRef.current || bars.length === 0) return;
    if (bars.length === lastBarCountRef.current) return;

    const deduped = new Map<number, OHLCBar>();
    for (const b of bars) deduped.set(b.time, b);
    const sorted = Array.from(deduped.values()).sort((a, b) => a.time - b.time);

    candleRef.current.setData(sorted.map((b) => ({ time: b.time as Time, open: b.open, high: b.high, low: b.low, close: b.close })));
    volumeRef.current.setData(sorted.map((b) => ({ time: b.time as Time, value: b.volume, color: b.close >= b.open ? "rgba(16, 185, 129, 0.15)" : "rgba(239, 68, 68, 0.15)" })));

    // EMAs: computed on RTH bars only (9:30-16:00 ET). Pre-market bars are excluded
    // because their thin volume produces unreliable EMA values that distort the
    // critical 9:30-10:00 window. The EMA carries over from previous session close.
    const toLine = (data: { time: number; value: number }[]): { time: Time; value: number }[] =>
      data.map(d => ({ time: d.time as Time, value: d.value }));

    const rthBars = sorted.filter(b => isRTH(b.time));
    if (ema9Ref.current) ema9Ref.current.setData(toLine(computeEMA(rthBars, 9)));
    if (ema21Ref.current) ema21Ref.current.setData(toLine(computeEMA(rthBars, 21)));
    if (ema50Ref.current) ema50Ref.current.setData(toLine(computeEMA(rthBars, 50)));
    if (ema200Ref.current) ema200Ref.current.setData(toLine(computeEMA(rthBars, 200)));

    // AVWAPs: client-side only, RTH-filtered
    const clientAVWAPs = computeAllAVWAPs(sorted);
    if (avwapOpenRef.current) avwapOpenRef.current.setData(toLine(clientAVWAPs.sessionOpen));
    if (avwapPDHighRef.current) avwapPDHighRef.current.setData(toLine(clientAVWAPs.pdHigh));
    if (avwapPDLowRef.current) avwapPDLowRef.current.setData(toLine(clientAVWAPs.pdLow));

    // ORB range boxes
    if (orbOverlayRef.current) {
      const ranges = computeORBRanges(sorted, orbWindowMinutes);
      orbOverlayRef.current.setRanges(ranges);
    }

    // Non-RTH shading
    if (rthOverlayRef.current) {
      const regions = computeNonRTHRegions(sorted);
      rthOverlayRef.current.setRegions(regions);
    }

    lastBarCountRef.current = bars.length;

    // Force chart to match container size
    if (containerRef.current && chartRef.current) {
      const rect = containerRef.current.getBoundingClientRect();
      if (rect.width > 0 && rect.height > 0) {
        chartRef.current.applyOptions({ width: rect.width, height: rect.height });
      }
    }

    if (!initialRangeSetRef.current) {
      const ts = chartRef.current?.timeScale();
      if (ts) {
        const visibleCandles = 120;
        const dataLen = sorted.length;
        const from = Math.max(0, dataLen - visibleCandles);
        const to = dataLen - 1 + 5;
        ts.setVisibleLogicalRange({ from, to });
        initialRangeSetRef.current = true;
      }
    }
  }, [bars, orbWindowMinutes]);

  // Update signal markers
  useEffect(() => {
    if (!overlayRef.current) return;
    overlayRef.current.setSignals(signals);
  }, [signals]);

  // Toggle label visibility
  useEffect(() => {
    overlayRef.current?.setVisible(showLabels);
  }, [showLabels]);

  // Toggle series visibility from global legend clicks
  useEffect(() => {
    const hidden = hiddenSeries ?? new Set<string>();
    const seriesMap: Record<string, ISeriesApi<"Line", Time> | null> = {
      "EMA 9": ema9Ref.current,
      "EMA 21": ema21Ref.current,
      "EMA 50": ema50Ref.current,
      "EMA 200": ema200Ref.current,
      "session_open": avwapOpenRef.current,
      "pd_high": avwapPDHighRef.current,
      "pd_low": avwapPDLowRef.current,
    };
    for (const [key, series] of Object.entries(seriesMap)) {
      if (series) series.applyOptions({ visible: !hidden.has(key) });
    }
    if (orbOverlayRef.current) orbOverlayRef.current.setVisible(!hidden.has("ORB"));
  }, [hiddenSeries]);

  const signalCount = signals.length;
  const hasActivity = signalCount > 0;

  return (
    <div
      className={`rounded-lg border bg-card overflow-hidden flex flex-col ${hasActivity ? "border-emerald-500/30" : "border-border"}`}
      style={height !== undefined ? { height } : { height: "100%" }}
    >
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-border/50">
        <div className="flex items-center gap-3">
          <span className="text-xs font-mono font-semibold text-foreground">{symbol}</span>
        </div>
        {signalCount > 0 && (
          <span className="text-[10px] font-mono text-emerald-400">{signalCount} signal{signalCount !== 1 ? "s" : ""}</span>
        )}
      </div>
      <div ref={containerRef} className="flex-1 min-h-0" />
    </div>
  );
});
