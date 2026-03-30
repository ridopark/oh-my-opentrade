"use client";

import React, { useState, useMemo, useEffect, useRef, useCallback, Suspense } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { Badge } from "@/components/ui/badge";
import { Activity, Zap } from "lucide-react";
import { useChartData, type OHLCBar } from "@/lib/use-chart-data";
import { useStrategyList } from "@/hooks/queries";
import { useWatchlist } from "@/hooks/use-watchlist";
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
import type { StrategySignalEvent, RegimeType } from "@/lib/types";

/** A single incoming bar event for the Bars log tab. */
interface BarLogEntry {
  receivedAt: number; // Date.now() when received
  eventType: "bar" | "forming";
  symbol: string;
  timeframe: string;
  time: string; // ISO timestamp from event
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

/** Signal type for internal state — matches the old ChartSignal shape. */
interface LiveSignal {
  time: number;
  side: "buy" | "sell";
  kind: "entry" | "exit";
  status: string;
  strategy?: string;
  confidence?: number;
  signalId?: string;
  entryPrice?: number;
  stopPrice?: number;
  targetPrice?: number;
}

function getPayloadPrices(payload: Record<string, unknown> | null): {
  entryPrice?: number;
  stopPrice?: number;
  targetPrice?: number;
} {
  if (!payload) return {};
  const meta = payload.meta as Record<string, string | number> | undefined;
  return {
    entryPrice: typeof payload.limitPrice === "number" ? payload.limitPrice : undefined,
    stopPrice: typeof payload.stopLoss === "number" ? payload.stopLoss : undefined,
    targetPrice: meta?.target_price ? parseFloat(String(meta.target_price)) : undefined,
  };
}

const TIMEFRAMES = ["1m", "5m", "15m", "1h", "1d"] as const;
type Timeframe = (typeof TIMEFRAMES)[number];

/** Format a UTC unix timestamp as ET time string. */
function formatTimeET(isoString: string): string {
  try {
    return new Intl.DateTimeFormat("en-US", {
      timeZone: "America/New_York",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    }).format(new Date(isoString));
  } catch {
    return "";
  }
}

/** Format a UTC unix timestamp (seconds) as ET string using Intl. */
function formatET(utcSeconds: number, opts: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat("en-US", { timeZone: "America/New_York", hour12: false, ...opts }).format(new Date(utcSeconds * 1000));
}

export default function TradingSignalPage() {
  return (
    <Suspense fallback={<div className="flex items-center justify-center h-screen text-muted-foreground">Loading...</div>}>
      <TradingSignalContent />
    </Suspense>
  );
}

// ─── Client-side indicator computation ────────────────────────

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

/** Compute anchored VWAP starting from a given bar index. */
function computeAVWAPFromIndex(bars: OHLCBar[], startIdx: number): { time: number; value: number }[] {
  const result: { time: number; value: number }[] = [];
  let cumTPV = 0;
  let cumVol = 0;
  for (let i = startIdx; i < bars.length; i++) {
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
    hour: "2-digit", minute: "2-digit", hour12: false,
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

  // For session_open: each day gets its own AVWAP line from 9:30 open to end of data
  // But we only show the current day's segment (each new anchor restarts the line)
  const sessionOpenData: { time: number; value: number }[] = [];
  for (let i = 0; i < anchors.sessionOpen.length; i++) {
    const startIdx = anchors.sessionOpen[i];
    const endIdx = i + 1 < anchors.sessionOpen.length ? anchors.sessionOpen[i + 1] : bars.length;
    const segment = computeAVWAPFromIndex(bars.slice(0, endIdx), startIdx);
    sessionOpenData.push(...segment);
  }

  const pdHighData: { time: number; value: number }[] = [];
  for (let i = 0; i < anchors.pdHigh.length; i++) {
    const startIdx = anchors.pdHigh[i];
    // PD high AVWAP runs from anchor to next PD high anchor (or end)
    const endIdx = i + 1 < anchors.pdHigh.length ? anchors.pdHigh[i + 1] : bars.length;
    const segment = computeAVWAPFromIndex(bars.slice(0, endIdx), startIdx);
    pdHighData.push(...segment);
  }

  const pdLowData: { time: number; value: number }[] = [];
  for (let i = 0; i < anchors.pdLow.length; i++) {
    const startIdx = anchors.pdLow[i];
    const endIdx = i + 1 < anchors.pdLow.length ? anchors.pdLow[i + 1] : bars.length;
    const segment = computeAVWAPFromIndex(bars.slice(0, endIdx), startIdx);
    pdLowData.push(...segment);
  }

  return { sessionOpen: sessionOpenData, pdHigh: pdHighData, pdLow: pdLowData };
}

/** Color for each AVWAP anchor type */
function avwapAnchorColor(name: string): string {
  if (name === "session_open") return "rgba(56, 189, 248, 0.5)";
  if (name === "pd_high") return "rgba(251, 146, 60, 0.5)";
  if (name === "pd_low") return "rgba(167, 139, 250, 0.5)";
  return "rgba(148, 163, 184, 0.4)";
}

/** Human-readable label for an anchor name */
function avwapAnchorLabel(name: string): string {
  if (name === "session_open") return "AVWAP Open";
  if (name === "pd_high") return "AVWAP PD High";
  if (name === "pd_low") return "AVWAP PD Low";
  return `AVWAP ${name}`;
}

// ─── LiveMiniChart ─────────────────────────────────────────────
// Matches the backtest MiniChart visually but works with OHLCBar[] data.
function LiveMiniChart({
  symbol,
  bars,
  signals,
  orbWindowMinutes = 30,
  showLabels = true,
  hiddenSeries,
}: {
  symbol: string;
  bars: OHLCBar[];
  signals: SignalMarkerData[];
  orbWindowMinutes?: number;
  showLabels?: boolean;
  hiddenSeries?: Set<string>;
}) {
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

    // EMAs: compute client-side as baseline, then overlay server values
    // Server only provides EMAs for live bars; historical bars need client-side.
    const mergeEMA = (clientData: { time: number; value: number }[], field: "ema9" | "ema21" | "ema50" | "ema200"): { time: Time; value: number }[] => {
      const byTime = new Map(clientData.map(d => [d.time, d.value]));
      for (const b of sorted) {
        const v = b[field];
        if (v && v > 0) byTime.set(b.time, v);
      }
      return Array.from(byTime.entries())
        .sort((a, b) => a[0] - b[0])
        .map(([t, v]) => ({ time: t as Time, value: v }));
    };

    if (ema9Ref.current) ema9Ref.current.setData(mergeEMA(computeEMA(sorted, 9), "ema9"));
    if (ema21Ref.current) ema21Ref.current.setData(mergeEMA(computeEMA(sorted, 21), "ema21"));
    if (ema50Ref.current) ema50Ref.current.setData(mergeEMA(computeEMA(sorted, 50), "ema50"));
    if (ema200Ref.current) ema200Ref.current.setData(mergeEMA(computeEMA(sorted, 200), "ema200"));

    // AVWAPs: compute client-side as baseline, then overlay server values
    // Server only provides AVWAP for live bars; historical bars need client-side.
    const clientAVWAPs = computeAllAVWAPs(sorted);

    // Build time→index maps from client-side data for merging
    const mergeAVWAP = (
      clientData: { time: number; value: number }[],
      anchorKey: string,
    ): { time: Time; value: number }[] => {
      const byTime = new Map(clientData.map(d => [d.time, d.value]));
      // Overlay server values (more accurate, matches trading decisions)
      for (const b of sorted) {
        const v = b.avwaps?.[anchorKey];
        if (v && v > 0) byTime.set(b.time, v);
      }
      return Array.from(byTime.entries())
        .sort((a, b) => a[0] - b[0])
        .map(([t, v]) => ({ time: t as Time, value: v }));
    };

    if (avwapOpenRef.current) avwapOpenRef.current.setData(mergeAVWAP(clientAVWAPs.sessionOpen, "session_open"));
    if (avwapPDHighRef.current) avwapPDHighRef.current.setData(mergeAVWAP(clientAVWAPs.pdHigh, "pd_high"));
    if (avwapPDLowRef.current) avwapPDLowRef.current.setData(mergeAVWAP(clientAVWAPs.pdLow, "pd_low"));

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

    const ts = chartRef.current?.timeScale();
    if (!ts) return;
    const visibleCandles = 120;
    const dataLen = sorted.length;
    const from = Math.max(0, dataLen - visibleCandles);
    const to = dataLen - 1 + 5;
    ts.setVisibleLogicalRange({ from, to });
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
    <div className={`rounded-lg border bg-card overflow-hidden flex flex-col h-full ${hasActivity ? "border-emerald-500/30" : "border-border"}`}>
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
}


function TradingSignalContent() {
  const [timeframe, setTimeframe] = useState<Timeframe>("1m");

  const [signals, setSignals] = useState<LiveSignal[]>([]);
  const [recentSignalEvents, setRecentSignalEvents] = useState<StrategySignalEvent[]>([]);
  const [regimeBySymbol, setRegimeBySymbol] = useState<Record<string, { regime: RegimeType; strength: number; rsi: number }>>({});

  const [barLog, setBarLog] = useState<BarLogEntry[]>([]);

  const [symbolsOpen, setSymbolsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const [bottomTab, setBottomTab] = useState<"signals" | "market" | "bars">("signals");

  // Global legend + overlay state
  const [hiddenSeries, setHiddenSeries] = useState<Set<string>>(new Set());
  const [showLabels, setShowLabels] = useState(true);
  const [orbWindowMinutes, setOrbWindowMinutes] = useState(30);

  // Fetch ORB window config
  useEffect(() => {
    fetch("/api/strategies/config/orb_break_retest/config")
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (data?.orb_window_minutes) setOrbWindowMinutes(data.orb_window_minutes);
      })
      .catch(() => {});
  }, []);

  const globalLegend: { key?: string; label: string; color: string; thick?: boolean; isORB?: boolean }[] = [
    { label: "EMA 9", color: "rgba(251, 191, 36, 0.7)" },
    { label: "EMA 21", color: "rgba(139, 92, 246, 0.7)" },
    { label: "EMA 50", color: "rgba(236, 72, 153, 0.6)" },
    { label: "EMA 200", color: "rgba(249, 115, 22, 0.5)" },
    { key: "session_open", label: avwapAnchorLabel("session_open"), color: avwapAnchorColor("session_open"), thick: true },
    { key: "pd_high", label: avwapAnchorLabel("pd_high"), color: avwapAnchorColor("pd_high"), thick: true },
    { key: "pd_low", label: avwapAnchorLabel("pd_low"), color: avwapAnchorColor("pd_low"), thick: true },
    { label: "ORB", color: "rgba(59, 130, 246, 0.5)", isORB: true },
  ];

  const { data: strategies } = useStrategyList();
  const availableSymbols = useMemo(() => {
    if (!strategies || strategies.length === 0) return [];
    const set = new Set<string>();
    for (const s of strategies) {
      for (const sym of s.symbols) set.add(sym);
    }
    return Array.from(set).sort();
  }, [strategies]);

  const { symbols: watchlistSymbols, expandedSymbol, setExpandedSymbol, addSymbol, removeSymbol, maxSymbols, hydrated } = useWatchlist(availableSymbols);

  // URL param support: ?symbol=SPY opens expanded mode
  const searchParams = useSearchParams();
  const router = useRouter();
  const paramSymbol = searchParams.get("symbol") ?? "";

  useEffect(() => {
    if (paramSymbol && !expandedSymbol) {
      setExpandedSymbol(paramSymbol);
    }
  }, [paramSymbol, expandedSymbol, setExpandedSymbol]);

  const setSymbol = useCallback((s: string) => {
    setExpandedSymbol(s);
    const params = new URLSearchParams(searchParams.toString());
    params.set("symbol", s);
    router.replace(`?${params.toString()}`, { scroll: false });
  }, [searchParams, router, setExpandedSymbol]);

  const goBackToGrid = useCallback(() => {
    setExpandedSymbol(null);
    router.replace("/", { scroll: false });
  }, [router, setExpandedSymbol]);

  // Close dropdown on outside click
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) setSymbolsOpen(false);
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  // Load chart data for all watchlist symbols (or expanded symbol)
  const chartSymbols = useMemo(() => {
    if (expandedSymbol) return [expandedSymbol];
    return watchlistSymbols;
  }, [expandedSymbol, watchlistSymbols]);

  const { barsBySymbol, loading, formingSymbols } = useChartData(
    timeframe,
    "/api/events",
    chartSymbols.length > 0 ? chartSymbols : undefined,
  );

  const symbol = expandedSymbol ?? "";
  const bars: OHLCBar[] = barsBySymbol[symbol] ?? [];

  // Fetch signals/regime for expanded symbol
  useEffect(() => {
    if (!symbol) return;

    Promise.all([
      fetch(`/api/state?symbol=${encodeURIComponent(symbol)}`)
        .then((res) => (res.ok ? res.json() : null)),
      fetch(`/api/signals/recent?symbol=${encodeURIComponent(symbol)}&limit=50`)
        .then((res) => (res.ok ? res.json() : null)),
    ])
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .then(([snap, data]: [any, { items: StrategySignalEvent[] } | null]) => {
        if (snap) {
          const currentRegime = snap.anchorRegimes?.[snap.Timeframe];
          if (currentRegime) {
            setRegimeBySymbol((prev) => ({
              ...prev,
              [snap.Symbol]: { regime: currentRegime.Type, strength: currentRegime.Strength, rsi: snap.RSI },
            }));
          }
        }

        if (data?.items?.length) {
          setRecentSignalEvents((prev) => {
            const existingIds = new Set(prev.map((e) => `${e.SignalID}:${e.Status}`));
            const newItems = data.items.filter((e) => !existingIds.has(`${e.SignalID}:${e.Status}`));
            return [...newItems, ...prev].slice(0, 50);
          });
          setSignals((prev) => {
            const next = [...prev];
            for (const sig of data.items) {
              if (!sig.SignalID) continue;
              const prices = getPayloadPrices(sig.Payload);
              const mapped: LiveSignal = {
                time: Math.floor(new Date(sig.TS).getTime() / 1000),
                side: (sig.Side?.toLowerCase() === "sell" ? "sell" : "buy"),
                kind: (sig.Kind?.toLowerCase() === "exit" ? "exit" : "entry"),
                status: sig.Status ?? "generated",
                strategy: sig.Strategy,
                confidence: sig.Confidence,
                signalId: sig.SignalID,
                ...prices,
              };
              const idx = next.findIndex((s) => s.signalId === sig.SignalID);
              if (idx >= 0) {
                next[idx] = { ...next[idx], ...mapped };
              } else {
                next.push(mapped);
              }
            }
            return next.slice(-200);
          });
        }
      })
      .catch(() => {});
  }, [symbol]);

  // SSE for live signals + regime updates
  useEffect(() => {
    const es = new EventSource("/api/events");

    es.addEventListener("StrategySignalLifecycle", (e: MessageEvent) => {
       try {
         const envelope = JSON.parse(e.data) as { payload: StrategySignalEvent };
         const sig = envelope.payload;
         if (!sig?.Symbol || !sig?.TS) return;

        const side = sig.Side?.toLowerCase() === "sell" ? "sell" as const : "buy" as const;
        const kind = sig.Kind?.toLowerCase() === "exit" ? "exit" as const : "entry" as const;
        const time = Math.floor(new Date(sig.TS).getTime() / 1000);

        setSignals((prev) => {
          const prices = getPayloadPrices(sig.Payload);
          const mapped: LiveSignal = { time, side, kind, status: sig.Status ?? "generated", strategy: sig.Strategy, confidence: sig.Confidence, signalId: sig.SignalID, ...prices };
          const idx = prev.findIndex((s) => s.signalId === sig.SignalID);
          if (idx >= 0) {
            const next = [...prev];
            next[idx] = { ...prev[idx], ...mapped };
            return next;
          }
          return [...prev, mapped].slice(-200);
        });

        setRecentSignalEvents((prev) => [sig, ...prev].slice(0, 50));
      } catch {
        // noop
      }
    });

    es.addEventListener("StateUpdated", (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as {
          payload: {
            Symbol: string;
            Timeframe: string;
            RSI: number;
            anchorRegimes?: Record<string, { Type: RegimeType; Strength: number }>;
          };
        };
        const snap = envelope.payload;
        if (!snap?.Symbol) return;
        const currentRegime = snap.anchorRegimes?.[snap.Timeframe];
        if (!currentRegime) return;
        setRegimeBySymbol((prev) => ({
          ...prev,
          [snap.Symbol]: { regime: currentRegime.Type, strength: currentRegime.Strength, rsi: snap.RSI },
        }));
      } catch {
        // noop
      }
    });

    const handleBarLog = (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as { type?: string; payload: { symbol: string; timeframe: string; time: string; open: number; high: number; low: number; close: number; volume: number } };
        const bar = envelope.payload;
        if (!bar?.symbol || !bar?.time) return;
        const eventType = envelope.type === "FormingBar" ? "forming" as const : "bar" as const;
        setBarLog((prev) => [{
          receivedAt: Date.now(),
          eventType,
          symbol: bar.symbol,
          timeframe: bar.timeframe,
          time: bar.time,
          open: bar.open,
          high: bar.high,
          low: bar.low,
          close: bar.close,
          volume: bar.volume,
        }, ...prev].slice(0, 200));
      } catch {
        // noop
      }
    };

    es.addEventListener("MarketBarSanitized", handleBarLog);
    es.addEventListener("FormingBar", handleBarLog);

    es.onerror = () => {};

    return () => es.close();
  }, []);

  const toggleSymbol = (sym: string) => {
    if (watchlistSymbols.includes(sym)) {
      removeSymbol(sym);
    } else {
      addSymbol(sym);
    }
  };

  /** Convert LiveSignal[] to SignalMarkerData[] by matching against bar times. */
  const buildSignalMarkers = useCallback((liveSigs: LiveSignal[], symBars: OHLCBar[]): SignalMarkerData[] => {
    if (liveSigs.length === 0 || symBars.length === 0) return [];

    const barTimesArr = symBars.map((b) => b.time).sort((a, b) => a - b);
    const barTimesSet = new Set(barTimesArr);
    const barMap = new Map(symBars.map((b) => [b.time, b]));

    const findClosestBarTime = (unixSec: number): number => {
      if (barTimesSet.has(unixSec)) return unixSec;
      let closest = barTimesArr[0] ?? unixSec;
      for (const bt of barTimesArr) {
        if (Math.abs(bt - unixSec) < Math.abs(closest - unixSec)) closest = bt;
        if (bt > unixSec) break;
      }
      return closest;
    };

    const earliest = symBars[0].time;
    return liveSigs
      .filter((s) => s.time >= earliest)
      .map((sig) => {
        const matchedTime = findClosestBarTime(sig.time);
        const bar = barMap.get(matchedTime);
        const isEntry = sig.kind === "entry";
        const isLong = sig.side === "buy";
        const label = isEntry
          ? (isLong ? "LONG" : "SHORT")
          : (isLong ? "COVER" : "SELL");
        const price = bar
          ? (isEntry ? bar.low * 0.999 : bar.high * 1.001)
          : (sig.entryPrice ?? 0);
        return {
          time: matchedTime as Time,
          price,
          side: sig.side,
          kind: sig.kind,
          executed: sig.status === "executed",
          label: `${label} @ $${sig.entryPrice?.toFixed(2) ?? "?"}`,
        } as SignalMarkerData;
      });
  }, []);

  // Badge helper for signal side
  function sideBadge(sig: StrategySignalEvent) {
    const isBuy = sig.Side?.toLowerCase() === "buy";
    const isEntry = sig.Kind?.toLowerCase() !== "exit";
    let text = "";
    let cls = "";
    if (isBuy && isEntry) { text = "LONG"; cls = "bg-emerald-500 text-white"; }
    else if (!isBuy && !isEntry) { text = "EXIT"; cls = "bg-orange-500 text-white"; }
    else if (!isBuy && isEntry) { text = "SHORT"; cls = "bg-rose-600 text-white"; }
    else { text = "COVER"; cls = "bg-sky-500 text-white"; }
    return { text, cls };
  }

  // Regime badge helper
  function regimeBadge(regime: RegimeType) {
    const cls =
      regime === "TREND" || regime === "TREND_UP" || regime === "TREND_DOWN"
        ? "bg-emerald-500/15 text-emerald-500 border-emerald-500/30"
        : regime === "REVERSAL"
          ? "bg-red-500/15 text-red-500 border-red-500/30"
          : "bg-amber-500/15 text-amber-500 border-amber-500/30";
    return cls;
  }

  const inputCls = "bg-background border border-border rounded px-2 py-1 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500";
  const pillCls = (active: boolean) => `px-2 py-1 text-[10px] font-mono rounded transition-colors ${active ? "bg-white/10 text-foreground" : "text-muted-foreground hover:bg-white/5"}`;

  // Grid column count
  const gridCols = watchlistSymbols.length <= 1
    ? 1
    : watchlistSymbols.length <= 4
      ? 2
      : watchlistSymbols.length <= 6
        ? 3
        : 4;

  // ─── EXPANDED MODE ──────────────────────────────────────
  if (expandedSymbol) {
    const expandedMarkers = buildSignalMarkers(signals, bars);

    return (
      <div className="flex flex-col min-h-[calc(100vh-3rem)]">
        {/* TopBar */}
        <div className="relative z-40 rounded-lg border border-border bg-card px-4 py-2.5 flex items-center gap-4 flex-wrap">
          <button
            onClick={goBackToGrid}
            className="text-xs text-muted-foreground hover:text-foreground flex items-center gap-1 shrink-0"
          >
            &larr; Grid
          </button>
          <h1 className="text-sm font-semibold text-foreground shrink-0">{expandedSymbol}</h1>

          {regimeBySymbol[expandedSymbol] && (
            <Badge variant="outline" className={regimeBadge(regimeBySymbol[expandedSymbol].regime)}>
              {regimeBySymbol[expandedSymbol].regime}
            </Badge>
          )}

          {/* Live pulse */}
          {!loading && bars.length > 0 && (
            <span className="relative flex h-2.5 w-2.5">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-emerald-500" />
            </span>
          )}

          <div className="flex items-center gap-0.5">
            {TIMEFRAMES.map((tf) => (
              <button key={tf} onClick={() => setTimeframe(tf)} className={pillCls(timeframe === tf)}>{tf}</button>
            ))}
          </div>

          {/* Symbol quick-switch */}
          <select
            value={symbol}
            onChange={(e) => setSymbol(e.target.value)}
            className={`${inputCls} w-28 ml-auto`}
          >
            {availableSymbols.map((sym) => (
              <option key={sym} value={sym}>{sym}</option>
            ))}
          </select>
        </div>

        {/* Global legend */}
        <GlobalLegendBar
          globalLegend={globalLegend}
          hiddenSeries={hiddenSeries}
          setHiddenSeries={setHiddenSeries}
          showLabels={showLabels}
          onToggleLabels={() => setShowLabels((v) => !v)}
        />

        {/* Expanded chart */}
        <div
          className="mt-2 overflow-hidden"
          style={{ height: "calc(100vh - 520px)", minHeight: "300px" }}
        >
          {bars.length > 0 ? (
            <LiveMiniChart
              key={`expanded-${expandedSymbol}-${timeframe}`}
              symbol={expandedSymbol}
              bars={bars}
              signals={expandedMarkers}
              orbWindowMinutes={orbWindowMinutes}
              showLabels={showLabels}
              hiddenSeries={hiddenSeries}
            />
          ) : (
            <div className="rounded-lg border bg-card flex items-center justify-center h-full">
              <p className="text-sm text-muted-foreground">
                No bar data for {symbol} ({timeframe}). Waiting for market data...
              </p>
            </div>
          )}
        </div>

        {/* Bottom panel */}
        <BottomPanel
          bottomTab={bottomTab}
          setBottomTab={setBottomTab}
          recentSignalEvents={recentSignalEvents}
          regimeBySymbol={regimeBySymbol}
          sideBadge={sideBadge}
          regimeBadge={regimeBadge}
          onSymbolClick={setSymbol}
          barLog={barLog}
        />
      </div>
    );
  }

  // ─── GRID MODE ──────────────────────────────────────────
  return (
    <div className="flex flex-col min-h-[calc(100vh-3rem)]">
      {/* TopBar */}
      <div className="relative z-40 rounded-lg border border-border bg-card px-4 py-2.5 flex items-center gap-4 flex-wrap">
        <h1 className="text-sm font-semibold text-foreground shrink-0">Trading Signals</h1>

        {/* Symbol multi-select dropdown */}
        <div className="flex items-center gap-1.5 relative" ref={dropdownRef}>
          <span className="text-[10px] text-muted-foreground uppercase">Symbols</span>
          <button
            onClick={() => setSymbolsOpen(!symbolsOpen)}
            className={`${inputCls} w-48 text-left flex items-center justify-between`}
          >
            <span className="truncate">
              {watchlistSymbols.length === 0 ? "Select..." : [...watchlistSymbols].sort().join(", ")}
            </span>
            <span className="text-muted-foreground ml-1">{symbolsOpen ? "\u25B2" : "\u25BC"}</span>
          </button>
          {symbolsOpen && (
            <div className="absolute top-full left-0 mt-1 z-50 w-56 max-h-64 overflow-y-auto rounded-lg border border-border bg-card shadow-xl">
              {availableSymbols.map((sym) => {
                const selected = watchlistSymbols.includes(sym);
                return (
                  <button
                    key={sym}
                    onClick={() => toggleSymbol(sym)}
                    className={`w-full px-3 py-1.5 text-xs font-mono text-left flex items-center gap-2 hover:bg-white/5 transition-colors ${selected ? "text-emerald-400" : "text-muted-foreground"}`}
                  >
                    <span className={`w-3.5 h-3.5 rounded border flex items-center justify-center text-[9px] ${selected ? "border-emerald-500 bg-emerald-500/20" : "border-border"}`}>
                      {selected && "\u2713"}
                    </span>
                    {sym}
                  </button>
                );
              })}
            </div>
          )}
        </div>

        {/* Timeframe pills */}
        <div className="flex items-center gap-0.5">
          {TIMEFRAMES.map((tf) => (
            <button key={tf} onClick={() => setTimeframe(tf)} className={pillCls(timeframe === tf)}>{tf}</button>
          ))}
        </div>

        {/* Live pulse + symbol count */}
        <div className="flex items-center gap-2 ml-auto shrink-0">
          {!loading && watchlistSymbols.length > 0 && (
            <span className="relative flex h-2.5 w-2.5">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-emerald-500" />
            </span>
          )}
          <Badge variant="outline" className="text-[10px] font-mono">
            {watchlistSymbols.length}/{maxSymbols}
          </Badge>
        </div>
      </div>

      {/* Global legend */}
      <GlobalLegendBar
        globalLegend={globalLegend}
        hiddenSeries={hiddenSeries}
        setHiddenSeries={setHiddenSeries}
        showLabels={showLabels}
        onToggleLabels={() => setShowLabels((v) => !v)}
      />

      {/* Chart Grid */}
      <div className="mt-2">
        {hydrated && watchlistSymbols.length === 0 ? (
          <div className="flex items-center justify-center rounded-lg border border-border bg-card text-muted-foreground text-sm h-64">
            Select symbols from the dropdown above to start monitoring
          </div>
        ) : (
          <div
            className="grid gap-2 w-full"
            style={{
              gridTemplateColumns: `repeat(${gridCols}, minmax(0, 1fr))`,
              gridAutoRows: "260px",
            }}
          >
            {watchlistSymbols.map((sym) => {
              const symBars = barsBySymbol[sym] ?? [];
              const regime = regimeBySymbol[sym];
              const symMarkers = buildSignalMarkers(signals, symBars);
              return (
                <div key={sym} className="relative group">
                  {symBars.length > 0 ? (
                    <LiveMiniChart
                      key={`grid-${sym}-${timeframe}`}
                      symbol={sym}
                      bars={symBars}
                      signals={symMarkers}
                      orbWindowMinutes={orbWindowMinutes}
                      showLabels={showLabels}
                      hiddenSeries={hiddenSeries}
                    />
                  ) : (
                    <div className="rounded-lg border bg-card overflow-hidden flex flex-col h-full">
                      <div className="flex items-center justify-between px-3 py-1.5 border-b border-border/50">
                        <span className="text-xs font-mono font-semibold text-foreground">{sym}</span>
                      </div>
                      <div className="flex-1 flex items-center justify-center">
                        <p className="text-[10px] text-muted-foreground">No bar data</p>
                      </div>
                    </div>
                  )}
                  {/* Regime badge overlay */}
                  {regime && (
                    <div className="absolute top-1 left-14 z-10">
                      <Badge variant="outline" className={`text-[9px] px-1 py-0 ${regimeBadge(regime.regime)}`}>
                        {regime.regime}
                      </Badge>
                    </div>
                  )}
                  {/* Forming pulse */}
                  {formingSymbols[sym] && (
                    <div className="absolute top-2.5 right-8 z-10">
                      <span className="relative flex h-1.5 w-1.5">
                        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                        <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
                      </span>
                    </div>
                  )}
                  {/* Expand button */}
                  <button
                    onClick={() => setSymbol(sym)}
                    className="absolute top-1 right-1 z-10 opacity-0 group-hover:opacity-100 transition-opacity p-0.5 rounded text-muted-foreground hover:text-foreground"
                    title="Expand"
                  >
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <polyline points="15 3 21 3 21 9" /><polyline points="9 21 3 21 3 15" />
                      <line x1="21" y1="3" x2="14" y2="10" /><line x1="3" y1="21" x2="10" y2="14" />
                    </svg>
                  </button>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* Bottom Panel */}
      <BottomPanel
        bottomTab={bottomTab}
        setBottomTab={setBottomTab}
        recentSignalEvents={recentSignalEvents}
        regimeBySymbol={regimeBySymbol}
        sideBadge={sideBadge}
        regimeBadge={regimeBadge}
        onSymbolClick={setSymbol}
        barLog={barLog}
      />
    </div>
  );
}

/** Global legend bar for toggling overlay series visibility. */
function GlobalLegendBar({
  globalLegend,
  hiddenSeries,
  setHiddenSeries,
  showLabels,
  onToggleLabels,
}: {
  globalLegend: { key?: string; label: string; color: string; thick?: boolean; isORB?: boolean }[];
  hiddenSeries: Set<string>;
  setHiddenSeries: React.Dispatch<React.SetStateAction<Set<string>>>;
  showLabels: boolean;
  onToggleLabels: () => void;
}) {
  return (
    <div className="flex items-center gap-2 px-2 py-1">
      {globalLegend.map((e) => {
        const seriesKey = e.key ?? e.label;
        const isHidden = hiddenSeries.has(seriesKey);
        return (
          <button
            key={seriesKey}
            className={`flex items-center gap-1 cursor-pointer transition-opacity ${isHidden ? "opacity-30" : "opacity-100"}`}
            onClick={() => setHiddenSeries((prev) => {
              const next = new Set(prev);
              if (next.has(seriesKey)) next.delete(seriesKey); else next.add(seriesKey);
              return next;
            })}
            title={`Click to ${isHidden ? "show" : "hide"} ${e.label}`}
          >
            {e.isORB ? (
              <span className="w-2.5 h-2 rounded-[1px] border border-dashed" style={{ borderColor: e.color, backgroundColor: "rgba(59, 130, 246, 0.1)" }} />
            ) : (
              <span className={`w-2.5 rounded-full ${e.thick ? "h-[3px]" : "h-[2px]"}`} style={{ backgroundColor: e.color }} />
            )}
            <span className="text-[9px] font-mono text-muted-foreground">{e.label}</span>
          </button>
        );
      })}
      <button
        className={`flex items-center gap-1 cursor-pointer transition-opacity ${showLabels ? "opacity-100" : "opacity-30"}`}
        onClick={() => onToggleLabels()}
        title={`Click to ${showLabels ? "hide" : "show"} entry/exit labels`}
      >
        <span className="w-2.5 h-2.5 text-[8px] leading-none text-center font-bold" style={{ color: "rgba(52, 211, 153, 0.8)" }}>&#x25B2;</span>
        <span className="text-[9px] font-mono text-muted-foreground">Labels</span>
      </button>
    </div>
  );
}

/** Bottom Panel with Signals, Market Info, and Bars tabs. */
function BottomPanel({
  bottomTab,
  setBottomTab,
  recentSignalEvents,
  regimeBySymbol,
  sideBadge,
  regimeBadge,
  onSymbolClick,
  barLog,
}: {
  bottomTab: "signals" | "market" | "bars";
  setBottomTab: (tab: "signals" | "market" | "bars") => void;
  recentSignalEvents: StrategySignalEvent[];
  regimeBySymbol: Record<string, { regime: RegimeType; strength: number; rsi: number }>;
  sideBadge: (sig: StrategySignalEvent) => { text: string; cls: string };
  regimeBadge: (regime: RegimeType) => string;
  onSymbolClick: (sym: string) => void;
  barLog: BarLogEntry[];
}) {
  const symbolsWithRegime = useMemo(() => Object.keys(regimeBySymbol).sort(), [regimeBySymbol]);

  return (
    <div className="h-[350px] mt-1 rounded-t-lg border border-border bg-card flex flex-col shrink-0">
      {/* Tab bar */}
      <div className="flex items-center gap-0 border-b border-border shrink-0">
        {(["signals", "market", "bars"] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setBottomTab(tab)}
            className={`px-4 py-2 text-xs font-mono transition-colors relative ${
              bottomTab === tab ? "text-foreground" : "text-muted-foreground hover:text-foreground"
            }`}
          >
            {tab === "signals" ? (
              <span className="flex items-center gap-1.5">
                <Zap className="w-3 h-3" />
                Signals ({recentSignalEvents.length})
              </span>
            ) : tab === "bars" ? (
              <span className="flex items-center gap-1.5">
                <Activity className="w-3 h-3" />
                Bars ({barLog.length})
              </span>
            ) : (
              <span className="flex items-center gap-1.5">
                <Activity className="w-3 h-3" />
                Market Info
              </span>
            )}
            {bottomTab === tab && (
              <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-emerald-500" />
            )}
          </button>
        ))}
      </div>

      {/* Tab content */}
      <div className="flex-1 min-h-0 overflow-auto">
        {bottomTab === "signals" && (
          <div className="p-2">
            {recentSignalEvents.length === 0 ? (
              <p className="text-xs text-muted-foreground text-center py-8">
                No signals yet. Signals appear when strategies generate buy/sell decisions.
              </p>
            ) : (
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-[10px] text-muted-foreground uppercase border-b border-border/50">
                    <th className="text-left py-1 px-2 font-medium">Time (ET)</th>
                    <th className="text-left py-1 px-2 font-medium">Symbol</th>
                    <th className="text-left py-1 px-2 font-medium">Strategy</th>
                    <th className="text-left py-1 px-2 font-medium">Side</th>
                    <th className="text-left py-1 px-2 font-medium">Kind</th>
                    <th className="text-left py-1 px-2 font-medium">Status</th>
                    <th className="text-left py-1 px-2 font-medium">Confidence</th>
                  </tr>
                </thead>
                <tbody>
                  {recentSignalEvents.map((sig, idx) => {
                    const badge = sideBadge(sig);
                    return (
                      <tr
                        key={`${sig.SignalID}-${idx}`}
                        className="border-b border-border/30 hover:bg-muted/30 cursor-pointer transition-colors"
                        onClick={() => onSymbolClick(sig.Symbol)}
                      >
                        <td className="py-1.5 px-2 font-mono text-muted-foreground">{formatTimeET(sig.TS)}</td>
                        <td className="py-1.5 px-2 font-bold">{sig.Symbol}</td>
                        <td className="py-1.5 px-2 font-mono text-muted-foreground">{sig.Strategy}</td>
                        <td className="py-1.5 px-2">
                          <Badge className={`text-[9px] px-1.5 py-0 ${badge.cls}`}>{badge.text}</Badge>
                        </td>
                        <td className="py-1.5 px-2 text-muted-foreground">{sig.Kind}</td>
                        <td className="py-1.5 px-2 text-muted-foreground">{sig.Status}</td>
                        <td className="py-1.5 px-2">
                          {sig.Confidence > 0 && (
                            <div className="flex items-center gap-1.5">
                              <div className="w-16 h-1.5 bg-secondary rounded-full overflow-hidden">
                                <div
                                  className="h-full bg-emerald-500 rounded-full"
                                  style={{ width: `${sig.Confidence * 100}%` }}
                                />
                              </div>
                              <span className="text-[10px] font-mono text-muted-foreground">
                                {(sig.Confidence * 100).toFixed(0)}%
                              </span>
                            </div>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        )}

        {bottomTab === "market" && (
          <div className="p-3 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
            {symbolsWithRegime.length === 0 ? (
              <p className="text-xs text-muted-foreground col-span-full text-center py-8">
                No regime data yet. Waiting for StateUpdated events...
              </p>
            ) : (
              symbolsWithRegime.map((sym) => {
                const r = regimeBySymbol[sym];
                return (
                  <div
                    key={sym}
                    className="rounded-lg border border-border/50 bg-muted/20 p-3 cursor-pointer hover:bg-muted/40 transition-colors"
                    onClick={() => onSymbolClick(sym)}
                  >
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-sm font-bold">{sym}</span>
                      <Badge variant="outline" className={regimeBadge(r.regime)}>
                        {r.regime}
                      </Badge>
                    </div>
                    <div className="flex items-center gap-2 mb-1.5">
                      <span className="text-[10px] text-muted-foreground w-14">Strength</span>
                      <div className="flex-1 h-1.5 bg-secondary rounded-full overflow-hidden">
                        <div
                          className={`h-full rounded-full transition-all duration-500 ${
                            r.regime === "TREND" || r.regime === "TREND_UP" || r.regime === "TREND_DOWN"
                              ? "bg-emerald-500"
                              : r.regime === "REVERSAL"
                                ? "bg-red-500"
                                : "bg-amber-500"
                          }`}
                          style={{ width: `${Math.min(r.strength * 100, 100)}%` }}
                        />
                      </div>
                      <span className="text-[10px] font-mono text-muted-foreground w-8 text-right">
                        {(r.strength * 100).toFixed(0)}%
                      </span>
                    </div>
                    {r.rsi > 0 && (
                      <div className="flex items-center gap-2">
                        <span className="text-[10px] text-muted-foreground w-14">RSI</span>
                        <span className={`text-xs font-mono font-medium ${
                          r.rsi > 70 ? "text-red-500" : r.rsi < 30 ? "text-emerald-500" : "text-foreground"
                        }`}>
                          {r.rsi.toFixed(1)}
                        </span>
                      </div>
                    )}
                  </div>
                );
              })
            )}
          </div>
        )}

        {bottomTab === "bars" && (
          <div className="p-2">
            {barLog.length === 0 ? (
              <p className="text-xs text-muted-foreground text-center py-8">
                No bars received yet. Waiting for market data...
              </p>
            ) : (
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-[10px] text-muted-foreground uppercase border-b border-border/50">
                    <th className="text-left py-1 px-2 font-medium">Received</th>
                    <th className="text-left py-1 px-2 font-medium">Type</th>
                    <th className="text-left py-1 px-2 font-medium">Symbol</th>
                    <th className="text-left py-1 px-2 font-medium">TF</th>
                    <th className="text-left py-1 px-2 font-medium">Bar Time</th>
                    <th className="text-right py-1 px-2 font-medium">Open</th>
                    <th className="text-right py-1 px-2 font-medium">High</th>
                    <th className="text-right py-1 px-2 font-medium">Low</th>
                    <th className="text-right py-1 px-2 font-medium">Close</th>
                    <th className="text-right py-1 px-2 font-medium">Volume</th>
                  </tr>
                </thead>
                <tbody>
                  {barLog.map((entry, idx) => {
                    const isGreen = entry.close >= entry.open;
                    return (
                      <tr
                        key={`${entry.receivedAt}-${idx}`}
                        className="border-b border-border/30 hover:bg-muted/30 cursor-pointer transition-colors"
                        onClick={() => onSymbolClick(entry.symbol)}
                      >
                        <td className="py-1 px-2 font-mono text-muted-foreground">
                          {formatTimeET(new Date(entry.receivedAt).toISOString())}
                        </td>
                        <td className="py-1 px-2">
                          <span className={`text-[9px] px-1.5 py-0.5 rounded font-mono ${
                            entry.eventType === "forming"
                              ? "bg-amber-500/15 text-amber-400"
                              : "bg-blue-500/15 text-blue-400"
                          }`}>
                            {entry.eventType === "forming" ? "FORMING" : "BAR"}
                          </span>
                        </td>
                        <td className="py-1 px-2 font-bold">{entry.symbol}</td>
                        <td className="py-1 px-2 font-mono text-muted-foreground">{entry.timeframe}</td>
                        <td className="py-1 px-2 font-mono text-muted-foreground">
                          {formatTimeET(entry.time)}
                        </td>
                        <td className="py-1 px-2 font-mono text-right">{entry.open.toFixed(2)}</td>
                        <td className="py-1 px-2 font-mono text-right text-emerald-400">{entry.high.toFixed(2)}</td>
                        <td className="py-1 px-2 font-mono text-right text-red-400">{entry.low.toFixed(2)}</td>
                        <td className={`py-1 px-2 font-mono text-right font-medium ${isGreen ? "text-emerald-400" : "text-red-400"}`}>
                          {entry.close.toFixed(2)}
                        </td>
                        <td className="py-1 px-2 font-mono text-right text-muted-foreground">
                          {entry.volume >= 1000 ? `${(entry.volume / 1000).toFixed(1)}K` : entry.volume.toFixed(0)}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
