"use client";

import React, { useRef, useState, useEffect } from "react";
import { Badge } from "@/components/ui/badge";
import { X, Maximize2 } from "lucide-react";
import { MiniChart } from "./mini-chart";
import type { OHLCBar } from "@/lib/use-chart-data";
import type { RegimeType } from "@/lib/types";

interface MiniChartCardProps {
  symbol: string;
  bars: OHLCBar[];
  regime?: { regime: RegimeType; strength: number; rsi: number };
  formingTime?: number | null;
  onExpand: () => void;
  onRemove: () => void;
}

const REGIME_COLORS: Record<string, string> = {
  TREND: "bg-emerald-500/20 text-emerald-400 border-emerald-500/30",
  BALANCE: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  REVERSAL: "bg-red-500/20 text-red-400 border-red-500/30",
};

export function MiniChartCard({ symbol, bars, regime, formingTime, onExpand, onRemove }: MiniChartCardProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [dims, setDims] = useState({ width: 0, height: 0 });

  useEffect(() => {
    if (!containerRef.current) return;
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setDims({ width: entry.contentRect.width, height: entry.contentRect.height });
      }
    });
    ro.observe(containerRef.current);
    return () => ro.disconnect();
  }, []);

  const lastBar = bars.length > 0 ? bars[bars.length - 1] : null;
  const prevBar = bars.length > 1 ? bars[bars.length - 2] : null;
  const lastPrice = lastBar?.close ?? 0;
  const change = prevBar ? lastPrice - prevBar.close : 0;
  const changePct = prevBar && prevBar.close !== 0 ? (change / prevBar.close) * 100 : 0;
  const isPositive = change >= 0;

  return (
    <div className="bg-card border rounded-lg shadow-sm overflow-hidden flex flex-col group relative">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-border/50">
        <div className="flex items-center gap-2">
          <span className="font-bold text-foreground">{symbol}</span>
          {regime && (
            <Badge variant="outline" className={`text-[10px] px-1.5 py-0 ${REGIME_COLORS[regime.regime] ?? ""}`}>
              {regime.regime}
            </Badge>
          )}
        </div>
        <div className="flex items-center gap-2">
          {lastBar && (
            <div className="text-right">
              <span className="font-mono text-sm font-medium text-foreground">
                ${lastPrice.toFixed(2)}
              </span>
              <span className={`ml-1 text-xs font-mono ${isPositive ? "text-emerald-400" : "text-red-400"}`}>
                {isPositive ? "+" : ""}{changePct.toFixed(2)}%
              </span>
            </div>
          )}
          <button
            onClick={(e) => { e.stopPropagation(); onRemove(); }}
            className="opacity-0 group-hover:opacity-100 transition-opacity p-0.5 rounded hover:bg-muted"
            title="Remove from watchlist"
          >
            <X className="h-3 w-3 text-muted-foreground" />
          </button>
        </div>
      </div>

      {/* Chart area — clickable to expand */}
      <div
        ref={containerRef}
        className="flex-1 min-h-[160px] cursor-pointer relative"
        onClick={onExpand}
      >
        {dims.width > 0 && dims.height > 0 && bars.length > 0 && (
          <MiniChart
            data={bars}
            width={dims.width}
            height={dims.height}
            formingTime={formingTime}
          />
        )}
        {bars.length === 0 && (
          <div className="absolute inset-0 flex items-center justify-center text-xs text-muted-foreground">
            Waiting for data...
          </div>
        )}
        {/* Expand icon on hover */}
        <div className="absolute top-2 right-2 opacity-0 group-hover:opacity-100 transition-opacity">
          <div className="p-1 rounded bg-background/80 backdrop-blur-sm">
            <Maximize2 className="h-3 w-3 text-muted-foreground" />
          </div>
        </div>
      </div>
    </div>
  );
}
