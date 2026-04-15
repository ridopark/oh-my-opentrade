"use client";

import { useEffect, useState } from "react";
import { SYMBOLS as FALLBACK_SYMBOLS } from "@/lib/use-chart-data";

export function useSymbols(): { symbols: string[]; loading: boolean } {
  const [symbols, setSymbols] = useState<string[]>(FALLBACK_SYMBOLS);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/symbols")
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (cancelled) return;
        if (Array.isArray(data) && data.length > 0) {
          setSymbols(data.map(String).sort());
        }
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { symbols, loading };
}
