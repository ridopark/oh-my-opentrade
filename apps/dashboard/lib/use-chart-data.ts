"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { MarketBarEvent } from "@/lib/types";

export interface OHLCBar {
  time: number; // Unix timestamp (seconds)
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

export type BarsBySymbol = Record<string, OHLCBar[]>;

export const SYMBOLS = [
  "AAPL", "MSFT", "GOOGL", "AMZN", "TSLA",
  "SOXL", "U", "PLTR", "SPY", "META",
];

/** How many bars to request per fetch window */
const FETCH_WINDOW = 300;

/** Max window scaling factor for gap-skipping (2^n multiplier cap) */
const MAX_WINDOW_SCALE = 6; // 2^6 = 64x base window ≈ 13+ days for 1m

/** Max consecutive empty fetches before the hook stops auto-retrying.
 *  The chart component has its own MAX_EMPTY_FETCHES for the debounce-based
 *  retry; this limit governs the internal auto-chain in loadMore. */
const MAX_AUTO_RETRIES = 10;

/** Minutes per timeframe — used for bucketing live 1m SSE bars */
const TF_MINUTES: Record<string, number> = {
  "1m": 1,
  "5m": 5,
  "15m": 15,
  "1h": 60,
  "1d": 1440,
};

/** Return the bucket start (Unix seconds) for a given timestamp and timeframe */
function bucketStart(ts: number, tf: string): number {
  const mins = TF_MINUTES[tf] ?? 1;
  const secs = mins * 60;
  return Math.floor(ts / secs) * secs;
}

function toOHLC(bar: MarketBarEvent): OHLCBar {
  return {
    time: Math.floor(new Date(bar.time).getTime() / 1000),
    open: bar.open,
    high: bar.high,
    low: bar.low,
    close: bar.close,
    volume: bar.volume,
  };
}

/** Upsert bar into sorted array (by time ascending). Returns new array. */
function upsertBar(bars: OHLCBar[], bar: OHLCBar): OHLCBar[] {
  const idx = bars.findLastIndex((b) => b.time === bar.time);
  if (idx !== -1) {
    const updated = bars.slice();
    updated[idx] = bar;
    return updated;
  }
  // Insert in-order
  const insertAt = bars.findIndex((b) => b.time > bar.time);
  if (insertAt === -1) return [...bars, bar];
  const next = bars.slice();
  next.splice(insertAt, 0, bar);
  return next;
}

/**
 * Merge a live 1m bar into the active timeframe bucket.
 * For 1m this is a direct upsert. For other timeframes, fold OHLCV into the
 * bucket candle that covers the bar's timestamp.
 */
function mergeLiveBar(bars: OHLCBar[], liveBar: OHLCBar, tf: string): OHLCBar[] {
  const bucket = bucketStart(liveBar.time, tf);
  const existing = bars.find((b) => b.time === bucket);
  const merged: OHLCBar = existing
    ? {
        time: bucket,
        open: existing.open,
        high: Math.max(existing.high, liveBar.high),
        low: Math.min(existing.low, liveBar.low),
        close: liveBar.close,
        volume: existing.volume + liveBar.volume,
      }
    : { ...liveBar, time: bucket };
  return upsertBar(bars, merged);
}

/**
 * useChartData — multi-timeframe, zoom/pan, live-updating chart data hook.
 *
 * - On mount / timeframe change: fetches the last FETCH_WINDOW bars from /api/bars.
 * - loadMore(beforeTs): fetches an older window ending at beforeTs (triggered on zoom/pan).
 * - Live 1m SSE events are merged into the active timeframe bucket in real time.
 * - A per-request in-flight guard prevents duplicate concurrent fetches.
 */
export function useChartData(
  timeframe: string,
  sseUrl = "/api/events"
): {
  barsBySymbol: BarsBySymbol;
  dataTimeframe: string;
  loading: boolean;
  loadingMore: boolean;
  loadMore: (beforeTs: number) => void;
} {
  const [barsBySymbol, setBarsBySymbol] = useState<BarsBySymbol>({});
  const [dataTimeframe, setDataTimeframe] = useState(timeframe);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  // Stable ref so SSE handler always reads current state
  const barsRef = useRef<BarsBySymbol>({});
  const timeframeRef = useRef(timeframe);
  timeframeRef.current = timeframe;

  // In-flight guard: Set of "tf|fromISO|toISO" fetch keys
  const inFlight = useRef<Set<string>>(new Set());

  // Pagination cursor for loadMore — tracks how far back we've fetched
  // independently of oldestTs, enabling gap-skipping over non-trading hours.
  const loadMoreCursorRef = useRef<number | null>(null);

  // Consecutive empty fetches — used to exponentially widen the lookback
  // window so we can jump over overnight, weekend, and holiday gaps.
  const emptyFetchCountRef = useRef(0);

  const fetchBars = useCallback(
    async (from: Date, to: Date, showLoading: boolean) => {
      const key = `${timeframeRef.current}|${from.toISOString()}|${to.toISOString()}`;
      console.log('[useChartData] fetchBars called', { tf: timeframeRef.current, from: from.toISOString(), to: to.toISOString(), showLoading, alreadyInFlight: inFlight.current.has(key) });
      if (inFlight.current.has(key)) {
        console.log('[useChartData] fetchBars SKIPPED — already in-flight', key);
        return 0;
      }
      inFlight.current.add(key);
      if (showLoading) setLoading(true);

      try {
        const params = new URLSearchParams({
          symbols: SYMBOLS.join(','),
          timeframe: timeframeRef.current,
          from: from.toISOString(),
          to: to.toISOString(),
        });
        const url = `/api/bars?${params}`;
        console.log('[useChartData] fetching', url);
        const res = await fetch(url);
        console.log('[useChartData] fetch response', { status: res.status, ok: res.ok });
        if (!res.ok) {
          console.warn('[useChartData] fetch failed with status', res.status);
          return 0;
        }
        const rows: MarketBarEvent[] = await res.json();
        if (!Array.isArray(rows)) {
          console.warn('[useChartData] unexpected response shape', rows);
          return 0;
        }
        console.log('[useChartData] received', rows.length, 'bars');

        // Merge new bars into current state (prepend older bars)
        const current = barsRef.current;
        const next: BarsBySymbol = { ...current };
        for (const row of rows) {
          if (!row?.symbol) continue;
          const ohlc = toOHLC(row);
          const prev = next[row.symbol] ?? [];
          next[row.symbol] = upsertBar(prev, ohlc);
        }
        const symbolCounts = Object.fromEntries(Object.entries(next).map(([s, b]) => [s, b.length]));
        console.log('[useChartData] updated barsBySymbol counts', symbolCounts);
        barsRef.current = next;
        setBarsBySymbol(next);
        setDataTimeframe(timeframeRef.current);
        return rows.length;
      } catch (err) {
        console.error('[useChartData] fetchBars error', err);
        return 0;
      } finally {
        inFlight.current.delete(key);
        if (showLoading) setLoading(false);
      }
    },
    []
  );

  // Initial load / timeframe switch
  useEffect(() => {
    // Reset state for new timeframe
    barsRef.current = {};
    setBarsBySymbol({});
    inFlight.current.clear();
    loadMoreCursorRef.current = null;
    emptyFetchCountRef.current = 0;

    const to = new Date();
    const mins = TF_MINUTES[timeframe] ?? 1;
    // Always look back at least 2 calendar days so pre-market loads still
    // capture the most recent completed trading session (US market opens
    // 14:30 UTC; a 5h window at 13:00 UTC would miss yesterday entirely).
    const minLookbackMs = 2 * 24 * 60 * 60 * 1000;
    const windowMs = FETCH_WINDOW * mins * 60 * 1000;
    const from = new Date(to.getTime() - Math.max(windowMs, minLookbackMs));
    console.log('[useChartData] initial load for timeframe', timeframe, { from: from.toISOString(), to: to.toISOString(), windowBars: FETCH_WINDOW });
    fetchBars(from, to, true);
  }, [timeframe, fetchBars]);

  // loadMore: fetch an older window ending before the given timestamp.
  // Maintains an internal cursor so that consecutive calls with the same
  // beforeTs (because oldestTs didn't move) still page backward through gaps.
  //
  // Gap-skipping: When a fetch returns 0 bars (e.g., overnight or weekend),
  // emptyFetchCountRef increments and the next call's window is exponentially
  // wider: base × 2^emptyFetchCount. This lets us jump over multi-day gaps
  // (weekends, holidays) without exhausting the retry budget.
  const loadMore = useCallback(
    async (beforeTs: number) => {
      // Use the internal cursor if it has advanced past beforeTs,
      // otherwise start from the caller-supplied beforeTs.
      const cursor = loadMoreCursorRef.current;
      const effectiveTo = cursor !== null && cursor < beforeTs ? cursor : beforeTs;
      const to = new Date(effectiveTo * 1000);
      const mins = TF_MINUTES[timeframeRef.current] ?? 1;
      const baseWindowMs = FETCH_WINDOW * mins * 60 * 1000;
      // Exponential backoff: widen the window on consecutive empty fetches
      const scale = Math.pow(2, Math.min(emptyFetchCountRef.current, MAX_WINDOW_SCALE));
      const windowMs = baseWindowMs * scale;
      const from = new Date(to.getTime() - windowMs);
      // Advance cursor to `from` so next call pages further back
      loadMoreCursorRef.current = Math.floor(from.getTime() / 1000);
      console.log('[useChartData] loadMore triggered', {
        beforeTs, effectiveTo, beforeDate: to.toISOString(),
        tf: timeframeRef.current, from: from.toISOString(),
        scale, emptyFetches: emptyFetchCountRef.current,
      });
      // Show loadingMore spinner for the entire retry chain
      setLoadingMore(true);
      const count = await fetchBars(from, to, false);
      if (count === 0) {
        emptyFetchCountRef.current += 1;
        const nextScale = Math.pow(2, Math.min(emptyFetchCountRef.current, MAX_WINDOW_SCALE));
        console.log('[useChartData] loadMore got 0 bars — emptyFetchCount now',
          emptyFetchCountRef.current, '| next window scale:', nextScale, 'x');
        // Auto-retry with wider window if under the limit, so the user
        // doesn't have to keep panning through overnight/weekend gaps.
        if (emptyFetchCountRef.current < MAX_AUTO_RETRIES) {
          console.log('[useChartData] auto-retrying with wider window…');
          // Use setTimeout to avoid deep recursion and let React batch
          setTimeout(() => loadMore(beforeTs), 50);
        } else {
          console.log('[useChartData] exhausted', MAX_AUTO_RETRIES, 'auto-retries — giving up');
          setLoadingMore(false);
        }
      } else {
        emptyFetchCountRef.current = 0;
        console.log('[useChartData] loadMore got', count, 'bars — emptyFetchCount reset');
        setLoadingMore(false);
      }
    },
    [fetchBars]
  );

  // Live SSE — merge 1m bars into active timeframe
  useEffect(() => {
    const es = new EventSource(sseUrl);

    es.addEventListener('MarketBarSanitized', (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as { payload: MarketBarEvent };
        const bar = envelope.payload;
        if (!bar?.symbol || !bar?.time) return;

        console.log('[useChartData] SSE bar received', { symbol: bar.symbol, time: bar.time, tf: timeframeRef.current });
        const ohlc = toOHLC(bar);
        const tf = timeframeRef.current;
        const current = barsRef.current;
        const prev = current[bar.symbol] ?? [];
        const next: BarsBySymbol = {
          ...current,
          [bar.symbol]: mergeLiveBar(prev, ohlc, tf),
        };
        barsRef.current = next;
        setBarsBySymbol(next);
      } catch (err) {
        console.error('[useChartData] SSE parse error', err);
      }
    });

    es.onerror = (err) => {
      console.warn('[useChartData] SSE error / reconnecting', err);
    };

    es.onerror = () => {
      // auto-retries; state is preserved
    };

    return () => es.close();
  }, [sseUrl]);

  return { barsBySymbol, dataTimeframe, loading, loadingMore, loadMore };
}
