"use client";

import React, { useEffect, useRef, useState } from "react";
import {
  createChart,
  ColorType,
  LineSeries,
  CandlestickSeries,
  HistogramSeries,
  CrosshairMode,
  type IChartApi,
  type ISeriesApi,
  type Time,
  type CandlestickData,
  type HistogramData,
  type LineData,
  type LogicalRange,
  type MouseEventParams,
} from "lightweight-charts";
import type { OHLCBar } from "@/lib/use-chart-data";
import { SignalMarkerOverlay, type SignalMarkerData } from "@/lib/signal-markers";

/** Convert a UTC unix timestamp to a Date in America/New_York timezone parts. */
function toET(utcSeconds: number): { year: string; month: string; day: string; hour: string; minute: string } {
  const d = new Date(utcSeconds * 1000);
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: 'America/New_York',
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hour12: false,
  }).formatToParts(d);
  const get = (type: string) => parts.find(p => p.type === type)?.value ?? '';
  return { year: get('year'), month: get('month'), day: get('day'), hour: get('hour'), minute: get('minute') };
}

/** Signal marker that can be overlaid on the chart. */
export interface ChartSignal {
  time: number; // Unix seconds
  side: "buy" | "sell";
  kind: "entry" | "exit";
  status: string;
  strategy?: string;
  confidence?: number;
  signalId?: string;
}

interface TradingSignalChartProps {
  data: OHLCBar[];
  signals?: ChartSignal[];
  width: number;
  height: number;
  symbol?: string;
  timeframe?: string;
  showEMA?: boolean;
  showBollinger?: boolean;
  showRSI?: boolean;
  onLoadMore?: (beforeTs: number) => void;
}

function getViewKey(symbol: string, timeframe: string) {
  return `chart-view:${symbol}:${timeframe}`;
}

function computeEMA(data: OHLCBar[], period: number): LineData[] {
  if (data.length === 0) return [];
  const k = 2 / (period + 1);
  const emaValues: number[] = [data[0].close];
  for (let i = 1; i < data.length; i++) {
    emaValues.push(data[i].close * k + emaValues[i - 1] * (1 - k));
  }
  const out: LineData[] = [];
  for (let i = 0; i < data.length; i++) {
    if (i < period - 1) continue;
    out.push({ time: data[i].time as Time, value: emaValues[i] });
  }
  return out;
}

function computeBollingerBands(data: OHLCBar[], period: number = 20, multiplier: number = 2) {
  const upper: LineData[] = [];
  const middle: LineData[] = [];
  const lower: LineData[] = [];
  
  for (let i = period - 1; i < data.length; i++) {
    const slice = data.slice(i - period + 1, i + 1);
    const sum = slice.reduce((acc, val) => acc + val.close, 0);
    const mean = sum / period;
    
    const variance = slice.reduce((acc, val) => acc + Math.pow(val.close - mean, 2), 0) / period;
    const stddev = Math.sqrt(variance);
    
    const time = data[i].time as Time;
    middle.push({ time, value: mean });
    upper.push({ time, value: mean + multiplier * stddev });
    lower.push({ time, value: mean - multiplier * stddev });
  }
  
  return { upper, middle, lower };
}

function computeRSI(data: OHLCBar[], period: number = 14): LineData[] {
  if (data.length <= period) return [];
  
  const out: LineData[] = [];
  let gains = 0;
  let losses = 0;
  
  for (let i = 1; i <= period; i++) {
    const change = data[i].close - data[i - 1].close;
    if (change > 0) gains += change;
    else losses -= change;
  }
  
  let avgGain = gains / period;
  let avgLoss = losses / period;
  
  const initialRS = avgLoss === 0 ? 100 : avgGain / avgLoss;
  const initialRSI = avgLoss === 0 ? 100 : 100 - (100 / (1 + initialRS));
  
  out.push({ time: data[period].time as Time, value: initialRSI });
  
  for (let i = period + 1; i < data.length; i++) {
    const change = data[i].close - data[i - 1].close;
    const gain = change > 0 ? change : 0;
    const loss = change < 0 ? -change : 0;
    
    avgGain = (avgGain * (period - 1) + gain) / period;
    avgLoss = (avgLoss * (period - 1) + loss) / period;
    
    let rsi = 100;
    if (avgLoss > 0) {
      const rs = avgGain / avgLoss;
      rsi = 100 - (100 / (1 + rs));
    }
    out.push({ time: data[i].time as Time, value: rsi });
  }
  
  return out;
}

function findNearestBar(bars: OHLCBar[], time: number): OHLCBar | undefined {
  if (bars.length === 0) return undefined;
  let lo = 0;
  let hi = bars.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (bars[mid].time < time) lo = mid + 1;
    else hi = mid;
  }
  if (lo === 0) return bars[0];
  if (lo >= bars.length) return bars[bars.length - 1];
  const prevDist = Math.abs(bars[lo - 1].time - time);
  const currDist = Math.abs(bars[lo].time - time);
  return prevDist <= currDist ? bars[lo - 1] : bars[lo];
}

const MAX_EMPTY_FETCHES = 10;
const LOAD_MORE_DEBOUNCE_MS = 200;

const TradingSignalChart = (props: TradingSignalChartProps) => {
  const { data, signals, width, height, symbol = '', timeframe = '1m', showEMA, showBollinger, showRSI, onLoadMore } = props;

  const mainContainerRef = useRef<HTMLDivElement>(null);
  const rsiContainerRef = useRef<HTMLDivElement>(null);

  const chartRef = useRef<IChartApi | null>(null);
  const rsiChartRef = useRef<IChartApi | null>(null);

  const candleSeriesRef = useRef<ISeriesApi<"Candlestick", Time> | null>(null);
  const volumeSeriesRef = useRef<ISeriesApi<"Histogram", Time> | null>(null);
  const ema12SeriesRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const ema26SeriesRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const bbUpperRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const bbMiddleRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const bbLowerRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const rsiSeriesRef = useRef<ISeriesApi<"Line", Time> | null>(null);
  const signalOverlayRef = useRef<SignalMarkerOverlay | null>(null);

  const onLoadMoreRef = useRef(onLoadMore);
  onLoadMoreRef.current = onLoadMore;
  const loadMoreDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const prevLoadMoreOldestRef = useRef<number | null>(null);
  const noProgressCountRef = useRef(0);
  const noMoreDataRef = useRef(false);
  const isFirstRenderRef = useRef(true);
  const oldestTsRef = useRef<number | null>(null);
  const barCountRef = useRef(0);

  const dataRef = useRef(data);
  dataRef.current = data;
  
  const rsiDataMapRef = useRef(new Map<Time, number>());
  const viewSaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const suppressViewSaveRef = useRef(true);
  const atLiveEdgeRef = useRef(true);
  const currentRightOffsetRef = useRef(0);
  const [initialView] = useState<{ from: number; to: number } | null>(() => {
    try {
      const raw = localStorage.getItem(getViewKey(symbol, timeframe));
      return raw ? JSON.parse(raw) : null;
    } catch { return null; }
  });

  useEffect(() => {
    oldestTsRef.current = data.length > 0 ? data[0].time : null;
    barCountRef.current = data.length;
  }, [data]);

  const [tooltip, setTooltip] = useState<{
    x: number; y: number;
    date: string;
    open: number; high: number; low: number; close: number; volume: number;
    color: string;
    rsi?: number;
  } | null>(null);

  const rsiHeight = showRSI ? 125 : 0;
  const chartHeight = Math.max(0, height - rsiHeight - (showRSI ? 8 : 0));

  const getChartOptions = (h: number) => ({
    layout: {
      background: { type: ColorType.Solid, color: "transparent" },
      textColor: "rgba(148, 163, 184, 1)",
      fontFamily: "var(--font-geist-mono, monospace)",
      fontSize: 11,
    },
    grid: {
      vertLines: { color: "rgba(148, 163, 184, 0.08)" },
      horzLines: { color: "rgba(148, 163, 184, 0.08)" },
    },
    crosshair: {
      mode: CrosshairMode.Normal,
      vertLine: { color: "rgba(148, 163, 184, 0.3)", width: 1 as const, style: 3 as const, labelBackgroundColor: "#1f2937" },
      horzLine: { color: "rgba(148, 163, 184, 0.3)", width: 1 as const, style: 3 as const, labelBackgroundColor: "#1f2937" },
    },
    rightPriceScale: {
      borderColor: "rgba(148, 163, 184, 0.15)",
    },
    timeScale: {
      borderColor: "rgba(148, 163, 184, 0.15)",
      timeVisible: true,
      rightOffset: 0,
      tickMarkFormatter: (time: number) => {
        const et = toET(time);
        return `${et.hour}:${et.minute}`;
      },
    },
    width,
    height: h,
  });

  // 1. CHART CREATION (MOUNT)
  useEffect(() => {
    if (!mainContainerRef.current) return;

    const mainChart = createChart(mainContainerRef.current, getChartOptions(chartHeight));
    chartRef.current = mainChart;

    const volumeSeries = mainChart.addSeries(HistogramSeries, {
      priceScaleId: '',
      priceFormat: { type: 'volume' },
      lastValueVisible: false,
      priceLineVisible: false,
    });
    mainChart.priceScale('').applyOptions({
      scaleMargins: { top: 0.8, bottom: 0 },
      visible: false,
    });
    volumeSeriesRef.current = volumeSeries;

    const candleSeries = mainChart.addSeries(CandlestickSeries, {
      upColor: '#10b981',
      downColor: '#ef4444',
      borderVisible: false,
      wickUpColor: '#10b981',
      wickDownColor: '#ef4444',
    });
    candleSeriesRef.current = candleSeries;

    const signalOverlay = new SignalMarkerOverlay();
    candleSeries.attachPrimitive(signalOverlay);
    signalOverlayRef.current = signalOverlay;

    const handleMainCrosshair = (param: MouseEventParams<Time>) => {
      if (!param.point || !param.time || param.point.x < 0) {
        setTooltip(null);
        if (rsiChartRef.current && rsiSeriesRef.current) {
          rsiChartRef.current.clearCrosshairPosition();
        }
        return;
      }
      
      const candleData = param.seriesData.get(candleSeries) as CandlestickData | undefined;
      const volData = param.seriesData.get(volumeSeries) as HistogramData | undefined;

      if (candleData && 'open' in candleData) {
        const timeVal = param.time as number;
        const dateObj = new Date(timeVal * 1000);
        const et = toET(timeVal);
        const dateStr = `${et.year}-${et.month}-${et.day} ${et.hour}:${et.minute}`;

        setTooltip({
          x: param.point.x,
          y: param.point.y,
          date: dateStr,
          open: candleData.open,
          high: candleData.high,
          low: candleData.low,
          close: candleData.close,
          volume: volData && 'value' in volData ? volData.value : 0,
          color: candleData.close >= candleData.open ? '#10b981' : '#ef4444',
          rsi: rsiDataMapRef.current.get(param.time),
        });
      } else {
        setTooltip(null);
      }

      if (rsiChartRef.current && rsiSeriesRef.current) {
        const rsiVal = rsiDataMapRef.current.get(param.time);
        if (rsiVal !== undefined) {
          rsiChartRef.current.setCrosshairPosition(rsiVal, param.time, rsiSeriesRef.current);
        } else {
          rsiChartRef.current.setCrosshairPosition(0, param.time, rsiSeriesRef.current);
        }
      }
    };
    mainChart.subscribeCrosshairMove(handleMainCrosshair);

    mainChart.timeScale().subscribeVisibleLogicalRangeChange((range: LogicalRange | null) => {
      if (!range) return;

      const totalBars = barCountRef.current;
      atLiveEdgeRef.current = range.to >= totalBars - 3;
      currentRightOffsetRef.current = Math.round(range.to - range.from);

      if (!suppressViewSaveRef.current) {
        if (viewSaveTimerRef.current) clearTimeout(viewSaveTimerRef.current);
        viewSaveTimerRef.current = setTimeout(() => {
          try {
            const vr = mainChart.timeScale().getVisibleRange();
            if (vr) {
              localStorage.setItem(getViewKey(symbol, timeframe), JSON.stringify(vr));
            }
          } catch {}
        }, 500);
      }

      if (!onLoadMoreRef.current) return;
      if (noMoreDataRef.current) return;
      const threshold = Math.max(50, Math.floor(barCountRef.current * 0.15));
      if (range.from > threshold) return;
      const oldest = oldestTsRef.current;
      if (oldest === null) return;

      if (loadMoreDebounceRef.current) clearTimeout(loadMoreDebounceRef.current);
      loadMoreDebounceRef.current = setTimeout(() => {
        const ts = oldestTsRef.current;
        if (ts === null || noMoreDataRef.current || !onLoadMoreRef.current) return;

        const prev = prevLoadMoreOldestRef.current;
        if (prev !== null && ts >= prev) {
          noProgressCountRef.current += 1;
          if (noProgressCountRef.current >= MAX_EMPTY_FETCHES) {
            noMoreDataRef.current = true;
            return;
          }
        } else if (prev !== null) {
          noProgressCountRef.current = 0;
        }

        prevLoadMoreOldestRef.current = ts;
        onLoadMoreRef.current(ts);
      }, LOAD_MORE_DEBOUNCE_MS);
    });

    return () => {
      if (loadMoreDebounceRef.current) clearTimeout(loadMoreDebounceRef.current);
      if (viewSaveTimerRef.current) clearTimeout(viewSaveTimerRef.current);
      isFirstRenderRef.current = true;
      suppressViewSaveRef.current = true;
      mainChart.remove();
      chartRef.current = null;
      candleSeriesRef.current = null;
      volumeSeriesRef.current = null;
      ema12SeriesRef.current = null;
      ema26SeriesRef.current = null;
      bbUpperRef.current = null;
      bbMiddleRef.current = null;
      bbLowerRef.current = null;
      signalOverlayRef.current = null;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // 2. RSI CHART CREATION
  useEffect(() => {
    if (!showRSI || !rsiContainerRef.current || !chartRef.current) return;

    const rsiChart = createChart(rsiContainerRef.current, {
      ...getChartOptions(rsiHeight),
      rightPriceScale: {
        ...getChartOptions(rsiHeight).rightPriceScale,
        autoScale: false,
      },
    });
    rsiChartRef.current = rsiChart;

    const rsiSeries = rsiChart.addSeries(LineSeries, {
      color: '#a78bfa',
      lineWidth: 2,
      priceFormat: { type: 'price', precision: 2, minMove: 0.01 },
    });
    rsiSeriesRef.current = rsiSeries;

    const createRefLine = (value: number, color: string, style: number) => {
      rsiSeries.createPriceLine({
        price: value,
        color,
        lineWidth: 1 as const,
        lineStyle: style as any,
        axisLabelVisible: true,
        title: value.toString(),
      });
    };
    createRefLine(70, '#4b5563', 2);
    createRefLine(50, '#374151', 3);
    createRefLine(30, '#4b5563', 2);

    rsiChart.timeScale().applyOptions({ visible: false });

    const handleRsiCrosshair = (param: MouseEventParams<Time>) => {
      if (!param.point || !param.time || param.point.x < 0) {
        chartRef.current?.clearCrosshairPosition();
        return;
      }
      
      if (candleSeriesRef.current && chartRef.current) {
        const candleVal = param.seriesData.get(candleSeriesRef.current) as CandlestickData | undefined;
        if (candleVal && 'close' in candleVal) {
          chartRef.current.setCrosshairPosition(candleVal.close, param.time, candleSeriesRef.current);
        } else {
          chartRef.current.setCrosshairPosition(0, param.time, candleSeriesRef.current);
        }
      }
    };
    rsiChart.subscribeCrosshairMove(handleRsiCrosshair);

    const handleMainRangeChange = (timeRange: LogicalRange | null) => {
      if (timeRange && rsiChartRef.current) {
        rsiChartRef.current.timeScale().setVisibleLogicalRange(timeRange);
      }
    };
    const handleRsiRangeChange = (timeRange: LogicalRange | null) => {
      if (timeRange && chartRef.current) {
        chartRef.current.timeScale().setVisibleLogicalRange(timeRange);
      }
    };

    chartRef.current.timeScale().subscribeVisibleLogicalRangeChange(handleMainRangeChange);
    rsiChart.timeScale().subscribeVisibleLogicalRangeChange(handleRsiRangeChange);

    // Sync initial range
    const vr = chartRef.current.timeScale().getVisibleRange();
    if (vr) {
      try { rsiChart.timeScale().setVisibleRange(vr); } catch {}
    }

    if (dataRef.current.length > 0) {
      const rsiData = computeRSI(dataRef.current);
      rsiDataMapRef.current.clear();
      rsiData.forEach(d => rsiDataMapRef.current.set(d.time, d.value));
      rsiSeries.setData(rsiData);
      rsiChart.priceScale('right').applyOptions({ autoScale: true });
    }

    return () => {
      chartRef.current?.timeScale().unsubscribeVisibleLogicalRangeChange(handleMainRangeChange);
      rsiChart.remove();
      rsiChartRef.current = null;
      rsiSeriesRef.current = null;
    };
  }, [showRSI]); // eslint-disable-line react-hooks/exhaustive-deps

  // 3. EMA TOGGLE
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    if (showEMA) {
      if (!ema12SeriesRef.current) {
        ema12SeriesRef.current = chart.addSeries(LineSeries, {
          color: '#06b6d4',
          lineWidth: 1 as const,
          crosshairMarkerVisible: false,
          lastValueVisible: false,
          priceLineVisible: false,
        });
        ema26SeriesRef.current = chart.addSeries(LineSeries, {
          color: '#f59e0b',
          lineWidth: 1 as const,
          crosshairMarkerVisible: false,
          lastValueVisible: false,
          priceLineVisible: false,
        });
        if (dataRef.current.length > 0) {
          ema12SeriesRef.current.setData(computeEMA(dataRef.current, 12));
          ema26SeriesRef.current.setData(computeEMA(dataRef.current, 26));
        }
      }
    } else {
      if (ema12SeriesRef.current) { chart.removeSeries(ema12SeriesRef.current); ema12SeriesRef.current = null; }
      if (ema26SeriesRef.current) { chart.removeSeries(ema26SeriesRef.current); ema26SeriesRef.current = null; }
    }
  }, [showEMA]);

  // 4. BOLLINGER TOGGLE
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    if (showBollinger) {
      if (!bbUpperRef.current) {
        bbUpperRef.current = chart.addSeries(LineSeries, {
          color: '#6366f1',
          lineWidth: 1 as const,
          crosshairMarkerVisible: false,
          lastValueVisible: false,
          priceLineVisible: false,
        });
        bbMiddleRef.current = chart.addSeries(LineSeries, {
          color: '#4f46e5',
          lineWidth: 1 as const,
          lineStyle: 2 as const,
          crosshairMarkerVisible: false,
          lastValueVisible: false,
          priceLineVisible: false,
        });
        bbLowerRef.current = chart.addSeries(LineSeries, {
          color: '#6366f1',
          lineWidth: 1 as const,
          crosshairMarkerVisible: false,
          lastValueVisible: false,
          priceLineVisible: false,
        });
        if (dataRef.current.length > 0) {
          const bb = computeBollingerBands(dataRef.current);
          bbUpperRef.current.setData(bb.upper);
          bbMiddleRef.current.setData(bb.middle);
          bbLowerRef.current.setData(bb.lower);
        }
      }
    } else {
      if (bbUpperRef.current) { chart.removeSeries(bbUpperRef.current); bbUpperRef.current = null; }
      if (bbMiddleRef.current) { chart.removeSeries(bbMiddleRef.current); bbMiddleRef.current = null; }
      if (bbLowerRef.current) { chart.removeSeries(bbLowerRef.current); bbLowerRef.current = null; }
    }
  }, [showBollinger]);

  // 5. DATA UPDATES
  useEffect(() => {
    if (!chartRef.current || !candleSeriesRef.current || !volumeSeriesRef.current) return;

    const candles: CandlestickData[] = [];
    const volumes: HistogramData[] = [];

    data.forEach((d) => {
      const time = d.time as Time;
      const isUp = d.close >= d.open;
      candles.push({ time, open: d.open, high: d.high, low: d.low, close: d.close });
      volumes.push({
        time,
        value: d.volume,
        color: isUp ? 'rgba(16, 185, 129, 0.15)' : 'rgba(239, 68, 68, 0.15)',
      });
    });

    candleSeriesRef.current.setData(candles);
    volumeSeriesRef.current.setData(volumes);

    if (signalOverlayRef.current) {
      if (signals && signals.length > 0 && data.length > 0) {
        const barMap = new Map(data.map(d => [d.time, d]));
        const markerData: SignalMarkerData[] = signals
          .map(s => {
            let bar = barMap.get(s.time);
            if (!bar) {
              bar = findNearestBar(data, s.time);
            }
            if (!bar) return null;
              let labelPrefix = "";
              if (s.side === "buy" && s.kind === "entry") labelPrefix = "Long";
              else if (s.side === "sell" && s.kind === "exit") labelPrefix = "Exit";
              else if (s.side === "sell" && s.kind === "entry") labelPrefix = "Short";
              else if (s.side === "buy" && s.kind === "exit") labelPrefix = "Cover";
              else labelPrefix = s.side === "buy" ? "Buy" : "Sell";

              return {
                time: bar.time as Time,
                price: s.side === "buy" ? bar.low : bar.high,
                side: s.side,
                kind: s.kind,
                executed: s.status === "executed",
                label: s.strategy ? `${labelPrefix} (${s.strategy})` : labelPrefix,
              };
          })
          .filter((d): d is SignalMarkerData => d !== null);
        signalOverlayRef.current.setSignals(markerData);
      } else {
        signalOverlayRef.current.setSignals([]);
      }
    }

    if (showEMA && ema12SeriesRef.current && ema26SeriesRef.current) {
      ema12SeriesRef.current.setData(computeEMA(data, 12));
      ema26SeriesRef.current.setData(computeEMA(data, 26));
    }

    if (showBollinger && bbUpperRef.current && bbMiddleRef.current && bbLowerRef.current) {
      const bb = computeBollingerBands(data);
      bbUpperRef.current.setData(bb.upper);
      bbMiddleRef.current.setData(bb.middle);
      bbLowerRef.current.setData(bb.lower);
    }

    if (showRSI && rsiSeriesRef.current) {
      const rsiData = computeRSI(data);
      rsiDataMapRef.current.clear();
      rsiData.forEach(d => rsiDataMapRef.current.set(d.time, d.value));
      rsiSeriesRef.current.setData(rsiData);
      rsiChartRef.current?.priceScale('right').applyOptions({ autoScale: true });
    }

    if (!isFirstRenderRef.current && atLiveEdgeRef.current && data.length > 0) {
      const visibleBars = currentRightOffsetRef.current || 120;
      const gap = Math.max(3, Math.round(visibleBars * 0.15));
      const from = data.length - visibleBars;
      const to = data.length - 1 + gap;
      try {
        chartRef.current.timeScale().setVisibleLogicalRange({ from, to });
        rsiChartRef.current?.timeScale().setVisibleLogicalRange({ from, to });
      } catch {}
    }

    if (isFirstRenderRef.current && data.length > 0) {
      isFirstRenderRef.current = false;
      let restored = false;
      if (initialView) {
        try {
          chartRef.current.timeScale().setVisibleRange({ from: initialView.from as Time, to: initialView.to as Time });
          rsiChartRef.current?.timeScale().setVisibleRange({ from: initialView.from as Time, to: initialView.to as Time });
          restored = true;
        } catch {}
      }
      if (!restored) {
        const defaultBars = 120;
        const gap = Math.max(3, Math.round(defaultBars * 0.15));
        const from = data.length - defaultBars;
        const to = data.length - 1 + gap;
        currentRightOffsetRef.current = defaultBars;
        try {
          chartRef.current.timeScale().setVisibleLogicalRange({ from, to });
          rsiChartRef.current?.timeScale().setVisibleLogicalRange({ from, to });
        } catch {
          chartRef.current.timeScale().fitContent();
          rsiChartRef.current?.timeScale().fitContent();
        }
      }
      requestAnimationFrame(() => { suppressViewSaveRef.current = false; });
    }
  }, [data, signals, showEMA, showBollinger, showRSI]);

  // 6. RESIZE / OPTIONS UPDATE
  useEffect(() => {
    if (chartRef.current) {
      chartRef.current.applyOptions({ width, height: chartHeight });
    }
    if (rsiChartRef.current && showRSI) {
      rsiChartRef.current.applyOptions({ width, height: rsiHeight });
    }
  }, [width, chartHeight, rsiHeight, showRSI]);

  return (
    <div className="relative w-full" style={{ height }}>
      {tooltip && (
        <div
          className="pointer-events-none absolute z-10 rounded-md border border-border bg-background/95 px-2.5 py-2 shadow-lg backdrop-blur-sm"
          style={{
            left: tooltip.x + 12,
            top: Math.max(4, tooltip.y - 40),
            transform: "translateY(-50%)",
          }}
        >
          <div className="flex flex-col gap-0.5">
            <div className="flex items-center gap-2 text-xs font-mono font-medium text-foreground">
              {tooltip.date}
            </div>
            <div className="grid grid-cols-2 gap-x-3 gap-y-0.5 text-xs font-mono">
              <span className="text-muted-foreground">O</span>
              <span className="text-foreground">{tooltip.open.toFixed(2)}</span>
              <span className="text-muted-foreground">H</span>
              <span className="text-foreground">{tooltip.high.toFixed(2)}</span>
              <span className="text-muted-foreground">L</span>
              <span className="text-foreground">{tooltip.low.toFixed(2)}</span>
              <span className="text-muted-foreground">C</span>
              <span className="text-foreground" style={{ color: tooltip.color }}>
                {tooltip.close.toFixed(2)}
              </span>
              <span className="text-muted-foreground">Vol</span>
              <span className="text-foreground">{tooltip.volume.toLocaleString()}</span>
              {tooltip.rsi !== undefined && (
                <>
                  <span className="text-muted-foreground">RSI</span>
                  <span className="text-foreground" style={{ color: "#a78bfa" }}>
                    {tooltip.rsi.toFixed(2)}
                  </span>
                </>
              )}
            </div>
          </div>
        </div>
      )}

      <div className="flex flex-col gap-2">
        <div ref={mainContainerRef} style={{ width, height: chartHeight }} />
        {showRSI && (
          <div ref={rsiContainerRef} style={{ width, height: rsiHeight }} />
        )}
      </div>
    </div>
  );
};

export default TradingSignalChart;
