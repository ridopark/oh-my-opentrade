"use client";

import { useEffect, useRef, useState } from "react";
import {
  createChart,
  ColorType,
  LineSeries,
  CandlestickSeries,
  HistogramSeries,
  type CandlestickData,
  type HistogramData,
  type IChartApi,
  type ISeriesApi,
  type LineData,
  type WhitespaceData,
  type Time,
  type MouseEventParams,
  type LogicalRange,
} from "lightweight-charts";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { BarChart2, LineChart } from "lucide-react";
import { useChartData, SYMBOLS, type OHLCBar } from "@/lib/use-chart-data";
import { OffMarketShading, detectGaps } from "@/lib/off-market-shading";

/**
 * Convert a UTC Unix timestamp (seconds) to a "fake UTC" timestamp that
 * displays as America/New_York local time on the chart.
 *
 * lightweight-charts v5 has no native timeZone option — it always renders
 * timestamps as UTC.  By shifting the timestamp so that the UTC representation
 * equals the ET wall-clock time, the x-axis labels show ET.
 *
 * The trick: extract the ET wall-clock components (year, month, day, h, m, s)
 * and build a Date using Date.UTC() so we get a Unix timestamp whose UTC
 * representation matches the ET wall-clock time.
 *
 * See: https://tradingview.github.io/lightweight-charts/docs/time-zones
 */
function timeToET(utcSecs: number): number {
  const d = new Date(utcSecs * 1000);
  // Get individual ET components via Intl formatter
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: 'America/New_York',
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
    hour12: false,
  }).formatToParts(d);
  const get = (type: string) => parseInt(parts.find((p) => p.type === type)?.value ?? '0', 10);
  // Build a UTC timestamp whose UTC h:m:s equals the ET wall-clock h:m:s
  const fakeUtc = Date.UTC(get('year'), get('month') - 1, get('day'), get('hour'), get('minute'), get('second'));
  return Math.floor(fakeUtc / 1000);
}
// Palette for 10 symbols — vibrant enough to distinguish on dark background
const SYMBOL_COLORS: Record<string, string> = {
  AAPL:      "#60a5fa", // blue-400
  MSFT:      "#34d399", // emerald-400
  GOOGL:     "#f59e0b", // amber-500
  AMZN:      "#f87171", // red-400
  TSLA:      "#a78bfa", // violet-400
  SOXL:      "#fb923c", // orange-400
  U:         "#38bdf8", // sky-400
  PLTR:      "#e879f9", // fuchsia-400
  SPY:       "#4ade80", // green-400
  META:      "#fbbf24", // yellow-400
  "BTC/USD": "#f97316", // orange-500
  "ETH/USD": "#06b6d4", // cyan-500
};

const TIMEFRAMES = ["1m", "5m", "15m", "1h", "1d"] as const;
type Timeframe = (typeof TIMEFRAMES)[number];

/** Expected interval (seconds) per timeframe — used for gap detection */
const TF_EXPECTED_SEC: Record<string, number> = {
  "1m": 60,
  "5m": 300,
  "15m": 900,
  "1h": 3600,
  "1d": 86400,
};

/** Minimum gap (seconds) to qualify for off-market background shading.
 *  Only shade true overnight/weekend gaps, not missing intra-day bars. */
const TF_SHADING_GAP_SEC: Record<string, number> = {
  "1m": 3600,    // > 1 hour
  "5m": 3600,    // > 1 hour
  "15m": 3600,   // > 1 hour
  "1h": 14400,   // > 4 hours
  "1d": 259200,  // > 3 days (weekends)
};

/**
 * Convert OHLCBar array to LineData with WhitespaceData gap breaks.
 * When the time delta between consecutive bars exceeds 1.5× the expected
 * interval, insert a whitespace-only point to break the line segment
 * so lightweight-charts doesn't draw a diagonal across the gap.
 */
function toLineDataWithGaps(
  bars: OHLCBar[],
  timeframe: string,
): (LineData | WhitespaceData)[] {
  const expectedSec = TF_EXPECTED_SEC[timeframe] ?? 60;
  const gapThreshold = expectedSec * 1.5;
  const out: (LineData | WhitespaceData)[] = [];

  for (let i = 0; i < bars.length; i++) {
    out.push({ time: timeToET(bars[i].time) as Time, value: bars[i].close });

    if (i < bars.length - 1) {
      const dt = bars[i + 1].time - bars[i].time;
      if (dt > gapThreshold) {
        // Insert whitespace point just after current bar to break the line
        const gapBreakTime = timeToET(bars[i].time + expectedSec) as Time;
        out.push({ time: gapBreakTime });
      }
    }
  }

  return out;
}
function toCandleDataWithGaps(bars: OHLCBar[], timeframe: string): (CandlestickData | WhitespaceData)[] {
  const expectedSec = TF_EXPECTED_SEC[timeframe] ?? 60;
  const gapThreshold = expectedSec * 1.5;
  const out: (CandlestickData | WhitespaceData)[] = [];
  for (let i = 0; i < bars.length; i++) {
    out.push({
      time: timeToET(bars[i].time) as Time,
      open: bars[i].open,
      high: bars[i].high,
      low: bars[i].low,
      close: bars[i].close,
    });
    if (i < bars.length - 1) {
      const dt = bars[i + 1].time - bars[i].time;
      if (dt > gapThreshold) {
        out.push({ time: timeToET(bars[i].time + expectedSec) as Time });
      }
    }
  }
  return out;
}

function toVolumeDataWithGaps(bars: OHLCBar[], timeframe: string): (HistogramData | WhitespaceData)[] {
  const expectedSec = TF_EXPECTED_SEC[timeframe] ?? 60;
  const gapThreshold = expectedSec * 1.5;
  const out: (HistogramData | WhitespaceData)[] = [];
  for (let i = 0; i < bars.length; i++) {
    out.push({
      time: timeToET(bars[i].time) as Time,
      value: bars[i].volume,
      color: bars[i].close >= bars[i].open ? 'rgba(34, 197, 94, 0.5)' : 'rgba(239, 68, 68, 0.5)',
    });
    if (i < bars.length - 1) {
      const dt = bars[i + 1].time - bars[i].time;
      if (dt > gapThreshold) {
        out.push({ time: timeToET(bars[i].time + expectedSec) as Time });
      }
    }
  }
  return out;
}

function computeEMA(bars: OHLCBar[], period: number, timeframe: string): (LineData | WhitespaceData)[] {
  const expectedSec = TF_EXPECTED_SEC[timeframe] ?? 60;
  const gapThreshold = expectedSec * 1.5;
  if (bars.length === 0) return [];
  const k = 2 / (period + 1);
  const emaValues: number[] = [bars[0].close];
  for (let i = 1; i < bars.length; i++) {
    emaValues.push(bars[i].close * k + emaValues[i - 1] * (1 - k));
  }
  const out: (LineData | WhitespaceData)[] = [];
  for (let i = 0; i < bars.length; i++) {
    if (i < period - 1) continue; // skip until enough data
    out.push({ time: timeToET(bars[i].time) as Time, value: emaValues[i] });
    if (i < bars.length - 1) {
      const dt = bars[i + 1].time - bars[i].time;
      if (dt > gapThreshold) {
        out.push({ time: timeToET(bars[i].time + expectedSec) as Time });
      }
    }
  }
  return out;
}


export function MultiSymbolChart() {
  const [timeframe, setTimeframe] = useState<Timeframe>("1m");
  const [chartMode, setChartMode] = useState<'line' | 'candle'>('line');
  const [candleSymbol, setCandleSymbol] = useState<string>(SYMBOLS[0]);

  const chartContainerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<Record<string, ISeriesApi<"Line", Time>>>({});
  const shadingRef = useRef<OffMarketShading | null>(null);
  const shadingHostRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const candleSeriesRef = useRef<ISeriesApi<"Candlestick", Time> | null>(null);
  const volumeSeriesRef = useRef<ISeriesApi<"Histogram", Time> | null>(null);
  const ema9SeriesRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema21SeriesRef = useRef<ISeriesApi<"Line", Time> | null>(null);

  const tooltipRef = useRef<HTMLDivElement>(null);
  const [tooltip, setTooltip] = useState<{
    x: number;
    y: number;
    items: { symbol: string; price: string; color: string }[];
  } | null>(null);
  const [candleTooltip, setCandleTooltip] = useState<{
    x: number; y: number; symbol: string;
    open: number; high: number; low: number; close: number; volume: number;
    color: string;
  } | null>(null);


  const { barsBySymbol, dataTimeframe, loading, loadingMore, loadMore, formingSymbols } = useChartData(timeframe);

  // Refs for animation loop — avoids re-creating the rAF loop on every data update
  const formingSymbolsRef = useRef(formingSymbols);
  formingSymbolsRef.current = formingSymbols;
  const barsBySymbolRef = useRef(barsBySymbol);
  barsBySymbolRef.current = barsBySymbol;

  // Stable ref so the range-change handler always sees the latest loadMore
  const loadMoreRef = useRef(loadMore);
  loadMoreRef.current = loadMore;

  // Oldest loaded timestamp per symbol — used to know when to trigger loadMore
  const oldestTsRef = useRef<number | null>(null);

  // Whether the chart has been seeded with initial data (controls fitContent)
  const seededRef = useRef(false);

  // Debounce timer for range-change handler
  const rangeDebounce = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Consecutive debounce-triggered loadMore calls where oldestTs didn't move.
  // After MAX_EMPTY_FETCHES consecutive no-progress pans we stop fetching.
  // With exponential window backoff in the hook, each attempt covers much
  // more ground, so 10 attempts can span weeks of calendar gaps.
  const MAX_EMPTY_FETCHES = 10;
  const noProgressCountRef = useRef(0);
  const noMoreDataRef = useRef(false);
  // Snapshot of oldestTs at the time we last called loadMore — used in the
  // debounce callback to detect whether the previous fetch made progress.
  const prevLoadMoreOldestRef = useRef<number | null>(null);

  // Saved visible time range (ET fake-UTC seconds) — captured before
  // a timeframe switch so we can restore the same calendar window
  // after the new data loads (instead of fitContent zooming to all data).
  const savedVisibleRangeRef = useRef<{ from: number; to: number } | null>(null);

  // Brief cooldown after seeding (range restore) to suppress loadMore triggers.
  // Without this, restoring a visible range on sparse data causes range.from ~0,
  // which triggers loadMore chains that pull in tons of older data.
  const seedCooldownRef = useRef(false);

  // Reset seed flag whenever timeframe changes so fitContent fires again on new data.
  // IMPORTANT: Capture the current visible range BEFORE resetting, so we can restore
  // the same calendar window after the new timeframe's data loads.
  useEffect(() => {
     // Capture visible range from the chart (ET fake-UTC timestamps)
     const chart = chartRef.current;
     if (chart) {
       const vr = chart.timeScale().getVisibleRange();
       if (vr) {
         savedVisibleRangeRef.current = {
           from: vr.from as number,
           to: vr.to as number,
         };
       }
     }
    // Reset candle mode refs on timeframe change
    if (candleSeriesRef.current) candleSeriesRef.current.setData([]);
    if (volumeSeriesRef.current) volumeSeriesRef.current.setData([]);
    if (ema9SeriesRef.current) ema9SeriesRef.current.setData([]);
    if (ema21SeriesRef.current) ema21SeriesRef.current.setData([]);

    // Clear stale series data so the barsBySymbol useEffect doesn't
    // see old data and prematurely seed the chart before new data arrives.
    for (const symbol of SYMBOLS) {
      const series = seriesRef.current[symbol];
      if (series) series.setData([]);
    }
     if (shadingHostRef.current) shadingHostRef.current.setData([]);
     if (shadingRef.current) shadingRef.current.setGaps([]);
     seededRef.current = false;
     oldestTsRef.current = null;
     noProgressCountRef.current = 0;
     noMoreDataRef.current = false;
     prevLoadMoreOldestRef.current = null;
   }, [timeframe]);

  // Create chart once on mount
  useEffect(() => {
    const container = chartContainerRef.current;
    if (!container) return;

    const chart = createChart(container, {
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "rgba(148, 163, 184, 1)", // slate-400
        fontFamily: "var(--font-geist-mono, monospace)",
        fontSize: 11,
      },
      grid: {
        vertLines: { color: "rgba(148, 163, 184, 0.08)" },
        horzLines: { color: "rgba(148, 163, 184, 0.08)" },
      },
      rightPriceScale: {
        visible: false,
      },
      timeScale: {
        borderColor: "rgba(148, 163, 184, 0.15)",
        timeVisible: true,
        secondsVisible: false,
      },
      crosshair: {
        vertLine: { color: "rgba(148, 163, 184, 0.3)" },
        horzLine: { color: "rgba(148, 163, 184, 0.3)" },
      },
      width: container.clientWidth,
      height: 340,
    });

    chartRef.current = chart;

    // Invisible host series for the off-market shading primitive.
    // Must be added BEFORE symbol series so zOrder 'bottom' renders behind them.
    const shadingHostSeries = chart.addSeries(LineSeries, {
      color: 'transparent',
      lineWidth: 1,
      priceScaleId: '__shading_host',
      lastValueVisible: false,
      priceLineVisible: false,
      crosshairMarkerVisible: false,
    });
    chart.priceScale('__shading_host').applyOptions({ visible: false });

    const shading = new OffMarketShading('rgba(148, 163, 184, 0.18)');
    shadingHostSeries.attachPrimitive(shading);
    shadingRef.current = shading;
    shadingHostRef.current = shadingHostSeries;

    // Create one line series per symbol
    for (const symbol of SYMBOLS) {
      const color = SYMBOL_COLORS[symbol];
      const series = chart.addSeries(LineSeries, {
        color,
        lineWidth: 1,
        priceScaleId: `overlay_${symbol}`,
        title: symbol,
        lastValueVisible: true,
        priceLineVisible: false,
        crosshairMarkerVisible: true,
        crosshairMarkerRadius: 4,
      });
      chart.priceScale(`overlay_${symbol}`).applyOptions({
        visible: false,
        scaleMargins: { top: 0.1, bottom: 0.1 },
      });
      seriesRef.current[symbol] = series;
    }

    // Subscribe to visible range changes — trigger loadMore when panning left
    chart.timeScale().subscribeVisibleLogicalRangeChange((range: LogicalRange | null) => {
      if (!range) return;
      if (seedCooldownRef.current) return;
      if (range.from > 10) return;
      // Once we've exhausted gap-skipping attempts, stop scheduling fetches
      if (noMoreDataRef.current) return;
      const oldest = oldestTsRef.current;
      if (oldest === null) return;
      if (rangeDebounce.current) clearTimeout(rangeDebounce.current);
      rangeDebounce.current = setTimeout(() => {
        const ts = oldestTsRef.current;
        if (ts === null) return;
        if (noMoreDataRef.current) return;

         // Check if previous loadMore made progress (oldestTs moved backward)
         const prevSnapshot = prevLoadMoreOldestRef.current;
         if (prevSnapshot !== null && ts >= prevSnapshot) {
           // No progress since last loadMore call
           noProgressCountRef.current += 1;
           if (noProgressCountRef.current >= MAX_EMPTY_FETCHES) {
             noMoreDataRef.current = true;
             return;
           }
         } else if (prevSnapshot !== null) {
           // Progress was made — reset counter
           noProgressCountRef.current = 0;
         }

         // Snapshot current oldestTs before triggering loadMore
         prevLoadMoreOldestRef.current = ts;
         loadMoreRef.current(ts);
      }, 200);
    });

    // Resize observer to keep chart responsive
    const ro = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry && chartRef.current) {
        chartRef.current.applyOptions({ width: entry.contentRect.width });
      }
    });
    ro.observe(container);

    return () => {
      if (rangeDebounce.current) clearTimeout(rangeDebounce.current);
      ro.disconnect();
      chart.remove();
      chartRef.current = null;
      seriesRef.current = {};
      shadingRef.current = null;
      shadingHostRef.current = null;
    };
  }, []);
  function removeCandleSeries() {
    const chart = chartRef.current;
    if (!chart) return;
    if (candleSeriesRef.current) { chart.removeSeries(candleSeriesRef.current); candleSeriesRef.current = null; }
    if (volumeSeriesRef.current) { chart.removeSeries(volumeSeriesRef.current); volumeSeriesRef.current = null; }
    if (ema9SeriesRef.current) { chart.removeSeries(ema9SeriesRef.current); ema9SeriesRef.current = null; }
    if (ema21SeriesRef.current) { chart.removeSeries(ema21SeriesRef.current); ema21SeriesRef.current = null; }
  }

  function removeLineSeries() {
    const chart = chartRef.current;
    if (!chart) return;
    for (const symbol of SYMBOLS) {
      const series = seriesRef.current[symbol];
      if (series) { chart.removeSeries(series); }
    }
    seriesRef.current = {};
    // Also remove shading host
    if (shadingHostRef.current) { chart.removeSeries(shadingHostRef.current); shadingHostRef.current = null; shadingRef.current = null; }
  }

  function buildLineSeries() {
    const chart = chartRef.current;
    if (!chart) return;
    // Recreate shading host
    const shadingHostSeries = chart.addSeries(LineSeries, {
      color: 'transparent', lineWidth: 1, priceScaleId: '__shading_host',
      lastValueVisible: false, priceLineVisible: false, crosshairMarkerVisible: false,
    });
    chart.priceScale('__shading_host').applyOptions({ visible: false });
    const shading = new OffMarketShading('rgba(148, 163, 184, 0.18)');
    shadingHostSeries.attachPrimitive(shading);
    shadingRef.current = shading;
    shadingHostRef.current = shadingHostSeries;
    // Recreate line series per symbol
    for (const symbol of SYMBOLS) {
      const color = SYMBOL_COLORS[symbol];
      const series = chart.addSeries(LineSeries, {
        color, lineWidth: 1, priceScaleId: `overlay_${symbol}`, title: symbol,
        lastValueVisible: true, priceLineVisible: false,
        crosshairMarkerVisible: true, crosshairMarkerRadius: 4,
      });
      chart.priceScale(`overlay_${symbol}`).applyOptions({ visible: false, scaleMargins: { top: 0.1, bottom: 0.1 } });
      seriesRef.current[symbol] = series;
    }
    // Hide right price scale (line mode uses overlay scales)
    chart.priceScale('right').applyOptions({ visible: false });
  }

  function buildCandleSeries() {
    const chart = chartRef.current;
    if (!chart) return;
    // Show right price scale for candlestick
    chart.priceScale('right').applyOptions({
      visible: true,
      scaleMargins: { top: 0.05, bottom: 0.25 },
    });
    // Candlestick series on right price scale
    const candleSeries = chart.addSeries(CandlestickSeries, {
      upColor: '#22c55e', downColor: '#ef4444',
      wickUpColor: '#22c55e', wickDownColor: '#ef4444',
      borderUpColor: '#22c55e', borderDownColor: '#ef4444',
      priceScaleId: 'right',
    });
    candleSeriesRef.current = candleSeries;
    // Volume histogram
    const volumeSeries = chart.addSeries(HistogramSeries, {
      priceScaleId: 'volume',
      priceFormat: { type: 'volume' },
      lastValueVisible: false,
      priceLineVisible: false,
    });
    chart.priceScale('volume').applyOptions({
      scaleMargins: { top: 0.80, bottom: 0.00 },
      visible: false,
    });
    volumeSeriesRef.current = volumeSeries;
    // EMA9 line (thin, semi-transparent)
    const ema9 = chart.addSeries(LineSeries, {
      color: 'rgba(251, 191, 36, 0.7)', lineWidth: 1,
      priceScaleId: 'right', title: 'EMA9',
      lastValueVisible: false, priceLineVisible: false,
      crosshairMarkerVisible: false,
    });
    ema9SeriesRef.current = ema9;
    // EMA21 line
    const ema21 = chart.addSeries(LineSeries, {
      color: 'rgba(96, 165, 250, 0.7)', lineWidth: 1,
      priceScaleId: 'right', title: 'EMA21',
      lastValueVisible: false, priceLineVisible: false,
      crosshairMarkerVisible: false,
    });
    ema21SeriesRef.current = ema21;
    // Reattach off-market shading to candlestick series
    const shading = new OffMarketShading('rgba(148, 163, 184, 0.18)');
    candleSeries.attachPrimitive(shading);
    shadingRef.current = shading;
  }

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    
    // Snapshot visible range
    const vr = chart.timeScale().getVisibleRange();
    
    if (chartMode === 'line') {
      // Switching TO line mode
      removeCandleSeries();
      buildLineSeries();
      // Repopulate line data
      for (const symbol of SYMBOLS) {
        const series = seriesRef.current[symbol];
        const bars = barsBySymbol[symbol];
        if (series && bars?.length) series.setData(toLineDataWithGaps(bars, timeframe));
      }
      // Repopulate shading
      // (the barsBySymbol useEffect will handle this on next render)
    } else {
      // Switching TO candle mode
      removeLineSeries();
      buildCandleSeries();
      // Populate candle data
      const bars = barsBySymbol[candleSymbol];
      if (bars?.length) {
        candleSeriesRef.current?.setData(toCandleDataWithGaps(bars, timeframe));
        volumeSeriesRef.current?.setData(toVolumeDataWithGaps(bars, timeframe));
        ema9SeriesRef.current?.setData(computeEMA(bars, 9, timeframe));
        ema21SeriesRef.current?.setData(computeEMA(bars, 21, timeframe));
      }
    }
    
    // Restore range
    if (vr) {
      try {
        chart.timeScale().setVisibleRange(vr);
      } catch { /* fallback */ }
    }
  }, [chartMode, candleSymbol]); // eslint-disable-line react-hooks/exhaustive-deps


  // Crosshair tooltip
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    const handler = (param: MouseEventParams<Time>) => {
      if (!param.point || !param.time) {
        setTooltip(null);
        setCandleTooltip(null);
        return;
      }

      if (chartMode === 'candle') {
        setCandleTooltip(null);
        const candleSeries = candleSeriesRef.current;
        const volumeSeries = volumeSeriesRef.current;
        if (!candleSeries) { setTooltip(null); setCandleTooltip(null); return; }
        const data = param.seriesData.get(candleSeries) as CandlestickData | undefined;
        if (!data || !('open' in data)) { setTooltip(null); setCandleTooltip(null); return; }
        const volData = volumeSeries ? param.seriesData.get(volumeSeries) as HistogramData | undefined : undefined;
        const vol = volData && 'value' in volData ? volData.value : 0;
        setCandleTooltip({
          x: param.point.x, y: param.point.y,
          symbol: candleSymbol,
          open: data.open, high: data.high, low: data.low, close: data.close, volume: vol,
          color: data.close >= data.open ? '#22c55e' : '#ef4444',
        });
        setTooltip(null);
      } else {
        setCandleTooltip(null);
        const items = SYMBOLS.flatMap((symbol) => {
          const series = seriesRef.current[symbol];
          if (!series) return [];
          const data = param.seriesData.get(series);
          if (!data || !("value" in data)) return [];
          return [{
            symbol,
            price: (data as LineData).value.toFixed(2),
            color: SYMBOL_COLORS[symbol],
          }];
        });
        if (items.length === 0) { setTooltip(null); return; }
        setTooltip({ x: param.point.x, y: param.point.y, items });
      }
    };

    chart.subscribeCrosshairMove(handler);
    return () => chart.unsubscribeCrosshairMove(handler);
  }, [chartMode, candleSymbol]);

  // Forming candle pulsation — animate the latest candle when it's still forming
  useEffect(() => {
    if (chartMode !== 'candle') return;

    let animId: number;

    const pulse = () => {
      const candleSeries = candleSeriesRef.current;
      if (!candleSeries) {
        animId = requestAnimationFrame(pulse);
        return;
      }

      const formingTime = formingSymbolsRef.current[candleSymbol];
      if (formingTime === undefined || formingTime === null) {
        animId = requestAnimationFrame(pulse);
        return;
      }

      const bars = barsBySymbolRef.current[candleSymbol];
      if (!bars?.length) {
        animId = requestAnimationFrame(pulse);
        return;
      }

      const lastBar = bars[bars.length - 1];
      if (lastBar.time !== formingTime) {
        animId = requestAnimationFrame(pulse);
        return;
      }

      const phase = (Math.sin(Date.now() * Math.PI / 500) + 1) / 2;
      const isBullish = lastBar.close >= lastBar.open;
      const baseRgb = isBullish ? '34, 197, 94' : '239, 68, 68';
      const alpha = 0.3 + phase * 0.7;
      const wickAlpha = 0.4 + phase * 0.6;

      candleSeries.update({
        time: timeToET(lastBar.time) as Time,
        open: lastBar.open,
        high: lastBar.high,
        low: lastBar.low,
        close: lastBar.close,
        color: `rgba(${baseRgb}, ${alpha})`,
        borderColor: `rgba(${baseRgb}, ${alpha})`,
        wickColor: `rgba(${baseRgb}, ${wickAlpha})`,
      });

      animId = requestAnimationFrame(pulse);
    };

    animId = requestAnimationFrame(pulse);
    return () => cancelAnimationFrame(animId);
  }, [chartMode, candleSymbol]);

  // Update series data whenever barsBySymbol changes
  useEffect(() => {
    let newOldest: number | null = null;
    for (const symbol of SYMBOLS) {
      const series = seriesRef.current[symbol];
      if (!series) continue;
      const bars = barsBySymbol[symbol];
      if (bars && bars.length > 0) {
        series.setData(toLineDataWithGaps(bars, timeframe));
        const first = bars[0].time;
        if (newOldest === null || first < newOldest) newOldest = first;
      }
    }
    // -- Candle mode: update active symbol's series --
    if (chartMode === 'candle') {
      const bars = barsBySymbol[candleSymbol];
      if (bars?.length) {
        candleSeriesRef.current?.setData(toCandleDataWithGaps(bars, timeframe));
        volumeSeriesRef.current?.setData(toVolumeDataWithGaps(bars, timeframe));
        ema9SeriesRef.current?.setData(computeEMA(bars, 9, timeframe));
        ema21SeriesRef.current?.setData(computeEMA(bars, 21, timeframe));
      }
    }


    // -- Off-market shading: detect gaps & feed host series --
    if (shadingRef.current && shadingHostRef.current) {
      // Collect all bars from all symbols into one sorted timeline
      const allBars: { time: number }[] = [];
      const seen = new Set<number>();
      for (const symbol of SYMBOLS) {
        const bars = barsBySymbol[symbol];
        if (!bars) continue;
        for (const b of bars) {
          if (!seen.has(b.time)) {
            seen.add(b.time);
            allBars.push({ time: b.time });
          }
        }
      }
      allBars.sort((a, b) => a.time - b.time);

      const shadingThreshold = TF_SHADING_GAP_SEC[timeframe] ?? 3600;
      const gaps = detectGaps(allBars, shadingThreshold);

      // Feed gap boundary timestamps as whitespace data to the host series
      // so timeToCoordinate() resolves for those times.
      const hostPoints: WhitespaceData[] = allBars.map((b) => ({ time: timeToET(b.time) as Time }));
      // Convert gap boundaries to ET so they align with the converted series data
      const etGaps = gaps.map((g) => ({ from: timeToET(g.from as number) as Time, to: timeToET(g.to as number) as Time }));
      shadingHostRef.current.setData(hostPoints);
      shadingRef.current.setGaps(etGaps);
    }
    if (chartMode === 'candle' && shadingRef.current) {
      // For candle mode, shading is attached to candlestick series.
      // Feed ET gaps directly to the shading primitive.
      // Recompute gaps from candleSymbol bars only
      const bars = barsBySymbol[candleSymbol];
      if (bars?.length) {
        const shadingThreshold = TF_SHADING_GAP_SEC[timeframe] ?? 3600;
        const gaps = detectGaps(bars.map(b => ({ time: b.time })), shadingThreshold);
        const etGaps = gaps.map(g => ({ from: timeToET(g.from as number) as Time, to: timeToET(g.to as number) as Time }));
        shadingRef.current.setGaps(etGaps);
      }
    }

     if (newOldest !== null) {
       oldestTsRef.current = newOldest;
     }
    // Seed logic: restore saved visible range (from previous timeframe) or fitContent on first-ever load
    if (!seededRef.current && dataTimeframe === timeframe && newOldest !== null) {
      seededRef.current = true;
      const chart = chartRef.current;
      const saved = savedVisibleRangeRef.current;
       if (chart && saved) {
         // Activate cooldown to suppress loadMore triggers while range settles
         seedCooldownRef.current = true;
         // Restore the same calendar window the user was looking at
         try {
           chart.timeScale().setVisibleRange({
             from: saved.from as Time,
             to: saved.to as Time,
           });
         } catch {
           // Fallback to fitContent if setVisibleRange fails (e.g., no data in that range)
           chart.timeScale().fitContent();
         }
         savedVisibleRangeRef.current = null; // Consume the saved range
         // Release cooldown after a brief delay so the range-change handler
         // doesn't immediately trigger a loadMore cascade.
         setTimeout(() => { seedCooldownRef.current = false; }, 500);
       } else {
         // First-ever load — no previous range to restore
         chart?.timeScale().fitContent();
       }
    }
  }, [barsBySymbol, timeframe, dataTimeframe, chartMode, candleSymbol]);


  const symbolCount = SYMBOLS.filter((s) => (barsBySymbol[s]?.length ?? 0) > 0).length;

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <CardTitle className="text-base">Multi-Symbol — Overlay Scales</CardTitle>
            <CardDescription className="mt-0.5">
              {chartMode === 'line' 
                ? `Each symbol auto-scales independently · ${timeframe} bars`
                : `${candleSymbol} · OHLCV + EMA(9,21) · ${timeframe} bars`
              }
            </CardDescription>

          </div>

          {/* Timeframe selector */}
          <div className="flex items-center gap-1">
            {TIMEFRAMES.map((tf) => (
              <Button
                key={tf}
                variant={timeframe === tf ? "secondary" : "ghost"}
                size="sm"
                className="h-6 px-2 text-xs font-mono"
                onClick={() => setTimeframe(tf)}
              >
                {tf}
              </Button>
            ))}
          </div>
          {/* Chart mode toggle */}
          <div className="flex items-center gap-1 border-l border-border pl-2 ml-1">
            <Button
              variant={chartMode === 'line' ? 'secondary' : 'ghost'}
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => setChartMode('line')}
              title="Line chart — all symbols"
            >
              <LineChart className="h-3 w-3" />
            </Button>
            <Button
              variant={chartMode === 'candle' ? 'secondary' : 'ghost'}
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => setChartMode('candle')}
              title="Candlestick — single symbol"
            >
              <BarChart2 className="h-3 w-3" />
            </Button>
          </div>

          {chartMode === 'candle' && (
            <select
              value={candleSymbol}
              onChange={(e) => setCandleSymbol(e.target.value)}
              className="h-6 rounded border border-border bg-background px-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            >
              {SYMBOLS.map((sym) => (
                <option key={sym} value={sym}>{sym}</option>
              ))}
            </select>
          )}


          {chartMode === 'line' && (

          <div className="flex flex-wrap justify-end gap-x-3 gap-y-1">
            {SYMBOLS.map((sym) => {
              const active = (barsBySymbol[sym]?.length ?? 0) > 0;
              return (
                <span
                  key={sym}
                  className="flex items-center gap-1 text-xs font-mono"
                  style={{ color: active ? SYMBOL_COLORS[sym] : "rgba(148,163,184,0.35)" }}
                >
                  <span
                    className="inline-block h-0.5 w-3 rounded-full"
                    style={{ backgroundColor: active ? SYMBOL_COLORS[sym] : "rgba(148,163,184,0.2)" }}
                  />
                  {sym}
                </span>
              );
            })}
          </div>
          )}

        </div>
        {symbolCount === 0 && !loading && (
          <p className="mt-1 text-xs text-muted-foreground">
            Waiting for market bars… Chart will populate when bars arrive.
          </p>
        )}
      </CardHeader>
      <CardContent className="p-0 pb-2">
        <div className="relative">
          <div ref={chartContainerRef} className="w-full px-1" />
          {tooltip && (
            <div
              ref={tooltipRef}
              className="pointer-events-none absolute z-10 rounded-md border border-border bg-background/95 px-2.5 py-2 shadow-lg backdrop-blur-sm"
              style={{
                left: tooltip.x + 12,
                top: Math.max(4, tooltip.y - tooltip.items.length * 10),
                transform: "translateY(-50%)",
              }}
            >
              <div className="flex flex-col gap-0.5">
                {tooltip.items.map(({ symbol, price, color }) => (
                  <div key={symbol} className="flex items-center gap-2 text-xs font-mono">
                    <span className="inline-block h-1.5 w-1.5 rounded-full flex-shrink-0" style={{ backgroundColor: color }} />
                    <span className="text-muted-foreground w-10">{symbol}</span>
                    <span className="font-medium" style={{ color }}>${price}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
          {candleTooltip && (
            <div
              className="pointer-events-none absolute z-10 rounded-md border border-border bg-background/95 px-2.5 py-2 shadow-lg backdrop-blur-sm"
              style={{
                left: candleTooltip.x + 12,
                top: Math.max(4, candleTooltip.y - 40),
                transform: "translateY(-50%)",
              }}
            >
              <div className="flex flex-col gap-0.5">
                <div className="flex items-center gap-2 text-xs font-mono font-medium" style={{ color: candleTooltip.color }}>
                  {candleTooltip.symbol}
                </div>
                <div className="grid grid-cols-2 gap-x-3 gap-y-0.5 text-xs font-mono">
                  <span className="text-muted-foreground">O</span>
                  <span className="text-foreground">${candleTooltip.open.toFixed(2)}</span>
                  <span className="text-muted-foreground">H</span>
                  <span className="text-foreground">${candleTooltip.high.toFixed(2)}</span>
                  <span className="text-muted-foreground">L</span>
                  <span className="text-foreground">${candleTooltip.low.toFixed(2)}</span>
                  <span className="text-muted-foreground">C</span>
                  <span className="text-foreground" style={{ color: candleTooltip.color }}>${candleTooltip.close.toFixed(2)}</span>
                  <span className="text-muted-foreground">Vol</span>
                  <span className="text-foreground">{candleTooltip.volume.toLocaleString()}</span>
                </div>
              </div>
            </div>
          )}

          {/* Loading overlay — shown during initial fetch */}
          {loading && (
            <div className="absolute inset-0 flex items-center justify-center rounded-lg bg-background/60 backdrop-blur-[2px]">
              <span className="animate-pulse text-xs text-muted-foreground">Loading {timeframe} bars…</span>
            </div>
          )}
          {/* Spinner overlay — shown while fetching older data on pan-left */}
          {loadingMore && !loading && (
            <div className="absolute inset-0 flex items-center justify-center rounded-lg bg-background/50 backdrop-blur-[2px]">
              <div className="flex flex-col items-center gap-3">
                <svg
                  className="h-10 w-10 animate-spin text-muted-foreground"
                  xmlns="http://www.w3.org/2000/svg"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                  <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                </svg>
                <span className="text-sm font-medium text-muted-foreground">Loading more data...</span>
              </div>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
