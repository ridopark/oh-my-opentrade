"use client";

import { useState, useEffect, useCallback } from "react";

const STORAGE_KEY = "watchlist-symbols";
const DEFAULT_SYMBOLS = ["SPY", "TSLA", "NVDA", "META"];
const MAX_SYMBOLS = 8;

export function useWatchlist(availableSymbols: string[]) {
  const [symbols, setSymbols] = useState<string[]>([]);
  const [expandedSymbol, setExpandedSymbol] = useState<string | null>(null);
  const [hydrated, setHydrated] = useState(false);

  // Hydrate from localStorage
  useEffect(() => {
    try {
      const saved = localStorage.getItem(STORAGE_KEY);
      if (saved) {
        const parsed = JSON.parse(saved) as string[];
        if (Array.isArray(parsed) && parsed.length > 0) {
          setSymbols(parsed.slice(0, MAX_SYMBOLS));
          setHydrated(true);
          return;
        }
      }
    } catch {}

    // Default: first 4 available symbols, or hardcoded defaults
    if (availableSymbols.length > 0) {
      setSymbols(availableSymbols.slice(0, 4));
    } else {
      setSymbols(DEFAULT_SYMBOLS);
    }
    setHydrated(true);
  }, [availableSymbols]);

  // Persist to localStorage
  useEffect(() => {
    if (hydrated && symbols.length > 0) {
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(symbols));
      } catch {}
    }
  }, [symbols, hydrated]);

  // Filter out stale symbols when available list changes
  useEffect(() => {
    if (!hydrated || availableSymbols.length === 0) return;
    setSymbols((prev) => {
      const valid = prev.filter((s) => availableSymbols.includes(s));
      return valid.length === prev.length ? prev : valid.length > 0 ? valid : availableSymbols.slice(0, 4);
    });
  }, [hydrated, availableSymbols]);

  const addSymbol = useCallback((sym: string) => {
    setSymbols((prev) => {
      if (prev.includes(sym) || prev.length >= MAX_SYMBOLS) return prev;
      return [...prev, sym];
    });
  }, []);

  const removeSymbol = useCallback((sym: string) => {
    setSymbols((prev) => prev.filter((s) => s !== sym));
    setExpandedSymbol((prev) => (prev === sym ? null : prev));
  }, []);

  return {
    symbols,
    expandedSymbol,
    setExpandedSymbol,
    addSymbol,
    removeSymbol,
    maxSymbols: MAX_SYMBOLS,
    hydrated,
  };
}
