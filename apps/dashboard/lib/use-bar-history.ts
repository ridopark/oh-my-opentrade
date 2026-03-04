"use client";

import { useEffect, useRef, useState } from "react";
import type { MarketBarEvent } from "@/lib/types";

export interface OHLCBar {
  time: number; // Unix timestamp (seconds)
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

// Per-symbol sorted bar history (oldest first)
export type BarHistory = Record<string, OHLCBar[]>;

const SYMBOLS = [
  "AAPL", "MSFT", "GOOGL", "AMZN", "TSLA",
  "SOXL", "U", "PLTR", "SPY", "META",
];
const MAX_BARS_PER_SYMBOL = 390; // ~1 full trading day at 1m

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

function upsertBar(bars: OHLCBar[], bar: OHLCBar): OHLCBar[] {
  const idx = bars.findLastIndex((b) => b.time === bar.time);
  if (idx !== -1) {
    const updated = bars.slice();
    updated[idx] = bar;
    return updated;
  }
  return [...bars, bar].slice(-MAX_BARS_PER_SYMBOL);
}

/**
 * useBarHistory seeds per-symbol OHLCV history from TimescaleDB on mount
 * (via GET /api/bars), then keeps it up-to-date with live MarketBarSanitized
 * SSE events. Returns a stable map of symbol → bars sorted oldest-first.
 */
export function useBarHistory(url = "/api/events"): {
  history: BarHistory;
  seeded: boolean;
} {
  const [history, setHistory] = useState<BarHistory>({});
  const [seeded, setSeeded] = useState(false);
  const historyRef = useRef<BarHistory>({});

  // 1. Seed from DB on mount
  useEffect(() => {
    const now = new Date();
    const from = new Date(
      Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate())
    ).toISOString();

    const params = new URLSearchParams({
      symbols: SYMBOLS.join(","),
      timeframe: "1m",
      from,
      to: now.toISOString(),
    });

    fetch(`/api/bars?${params}`)
      .then((r) => r.json())
      .then((rows: MarketBarEvent[]) => {
        if (!Array.isArray(rows)) return;

        const seed: BarHistory = {};
        for (const row of rows) {
          if (!row?.symbol) continue;
          const ohlc = toOHLC(row);
          if (!seed[row.symbol]) seed[row.symbol] = [];
          seed[row.symbol].push(ohlc);
        }
        // rows are already time-ASC from DB; trim to max
        for (const sym of Object.keys(seed)) {
          seed[sym] = seed[sym].slice(-MAX_BARS_PER_SYMBOL);
        }

        historyRef.current = seed;
        setHistory(seed);
        setSeeded(true);
      })
      .catch(() => {
        // Seed failed — live SSE will still populate incrementally
        setSeeded(true);
      });
  }, []);

  // 2. Layer live bars from SSE on top
  useEffect(() => {
    const es = new EventSource(url);

    es.addEventListener("MarketBarSanitized", (e: MessageEvent) => {
      try {
        const envelope = JSON.parse(e.data) as { payload: MarketBarEvent };
        const bar = envelope.payload;
        if (!bar?.symbol || !bar?.time) return;

        const ohlc = toOHLC(bar);
        const current = historyRef.current;
        const prev = current[bar.symbol] ?? [];
        const next: BarHistory = {
          ...current,
          [bar.symbol]: upsertBar(prev, ohlc),
        };
        historyRef.current = next;
        setHistory(next);
      } catch {
        // skip malformed events
      }
    });

    es.onerror = () => {
      // auto-retries; state is preserved
    };

    return () => es.close();
  }, [url]);

  return { history, seeded };
}
