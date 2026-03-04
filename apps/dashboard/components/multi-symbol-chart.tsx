"use client";

import { useEffect, useRef, useState } from "react";
import {
  createChart,
  ColorType,
  LineSeries,
  type IChartApi,
  type ISeriesApi,
  type LineData,
  type Time,
  type MouseEventParams,
  type LogicalRange,
} from "lightweight-charts";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { useChartData, SYMBOLS, type OHLCBar } from "@/lib/use-chart-data";

// Palette for 10 symbols — vibrant enough to distinguish on dark background
const SYMBOL_COLORS: Record<string, string> = {
  AAPL:  "#60a5fa", // blue-400
  MSFT:  "#34d399", // emerald-400
  GOOGL: "#f59e0b", // amber-500
  AMZN:  "#f87171", // red-400
  TSLA:  "#a78bfa", // violet-400
  SOXL:  "#fb923c", // orange-400
  U:     "#38bdf8", // sky-400
  PLTR:  "#e879f9", // fuchsia-400
  SPY:   "#4ade80", // green-400
  META:  "#fbbf24", // yellow-400
};

const TIMEFRAMES = ["1m", "5m", "15m", "1h", "1d"] as const;
type Timeframe = (typeof TIMEFRAMES)[number];

/** Convert OHLCBar array to close-price LineData for lightweight-charts */
function toLineData(bars: OHLCBar[]): LineData[] {
  return bars.map((b) => ({ time: b.time as Time, value: b.close }));
}

export function MultiSymbolChart() {
  const [timeframe, setTimeframe] = useState<Timeframe>("1m");
  const chartContainerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<Record<string, ISeriesApi<"Line", Time>>>({});
  const tooltipRef = useRef<HTMLDivElement>(null);
  const [tooltip, setTooltip] = useState<{
    x: number;
    y: number;
    items: { symbol: string; price: string; color: string }[];
  } | null>(null);

  const { barsBySymbol, loading, loadMore } = useChartData(timeframe);

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
  const MAX_EMPTY_FETCHES = 3;
  const noProgressCountRef = useRef(0);
  const noMoreDataRef = useRef(false);
  // Snapshot of oldestTs at the time we last called loadMore — used in the
  // debounce callback to detect whether the previous fetch made progress.
  const prevLoadMoreOldestRef = useRef<number | null>(null);

  // Reset seed flag whenever timeframe changes so fitContent fires again on new data
  useEffect(() => {
    seededRef.current = false;
    oldestTsRef.current = null;
    noProgressCountRef.current = 0;
    noMoreDataRef.current = false;
    prevLoadMoreOldestRef.current = null;
    console.log('[Chart] timeframe changed to', timeframe, '— resetting all pagination guards');
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
          console.log('[Chart] debounce: no progress since last loadMore — noProgressCount:',
            noProgressCountRef.current, '/', MAX_EMPTY_FETCHES,
            '| oldestTs still at', ts, new Date(ts * 1000).toISOString());
          if (noProgressCountRef.current >= MAX_EMPTY_FETCHES) {
            noMoreDataRef.current = true;
            console.log('[Chart] no older data after', MAX_EMPTY_FETCHES,
              'consecutive empty fetches — suppressing further loadMore');
            return;
          }
        } else if (prevSnapshot !== null) {
          // Progress was made — reset counter
          noProgressCountRef.current = 0;
          console.log('[Chart] debounce: progress detected — oldestTs moved from',
            prevSnapshot, 'to', ts, '| counter reset');
        }

        // Snapshot current oldestTs before triggering loadMore
        prevLoadMoreOldestRef.current = ts;
        console.log('[Chart] debounce fired — calling loadMore with oldestTs:', ts, new Date(ts * 1000).toISOString());
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
    };
  }, []);

  // Crosshair tooltip
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    const handler = (param: MouseEventParams<Time>) => {
      if (!param.point || !param.time) {
        setTooltip(null);
        return;
      }
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
    };

    chart.subscribeCrosshairMove(handler);
    return () => chart.unsubscribeCrosshairMove(handler);
  }, []);

  // Update series data whenever barsBySymbol changes
  useEffect(() => {
    let newOldest: number | null = null;
    for (const symbol of SYMBOLS) {
      const series = seriesRef.current[symbol];
      if (!series) continue;
      const bars = barsBySymbol[symbol];
      if (bars && bars.length > 0) {
        series.setData(toLineData(bars));
        const first = bars[0].time;
        if (newOldest === null || first < newOldest) newOldest = first;
      }
    }
    if (newOldest !== null) {
      const prevOldest = oldestTsRef.current;
      oldestTsRef.current = newOldest;
      if (prevOldest === null || newOldest < prevOldest) {
        console.log('[Chart] barsBySymbol updated — oldestTs moved backward to', newOldest,
          new Date(newOldest * 1000).toISOString());
      }
    }
    // fitContent only on initial seed — never after loadMore (would reset pan position)
    if (!seededRef.current && newOldest !== null) {
      seededRef.current = true;
      chartRef.current?.timeScale().fitContent();
      console.log('[Chart] fitContent called (initial seed)');
    }
  }, [barsBySymbol]);

  const symbolCount = SYMBOLS.filter((s) => (barsBySymbol[s]?.length ?? 0) > 0).length;

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <CardTitle className="text-base">Multi-Symbol — Overlay Scales</CardTitle>
            <CardDescription className="mt-0.5">
              Each symbol auto-scales independently · {timeframe} bars
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
          {/* Loading overlay — shown while fetching from DB/Alpaca */}
          {loading && (
            <div className="absolute inset-0 flex items-center justify-center rounded-lg bg-background/60 backdrop-blur-[2px]">
              <span className="animate-pulse text-xs text-muted-foreground">Loading {timeframe} bars…</span>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
