"use client";

import { useState } from "react";
import type { SweepRunResult } from "@/lib/use-sweep";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";

interface SweepResultsTableProps {
  runs: SweepRunResult[];
  sweptKeys: string[];
  onApply: (runIndex: number) => void;
  appliedIndex?: number;
}

type SortKey = "rank" | "total_pnl" | "sharpe_ratio" | "profit_factor" | "win_rate_pct" | "trade_count" | "max_drawdown_pct";

export function SweepResultsTable({ runs, sweptKeys, onApply, appliedIndex }: SweepResultsTableProps) {
  const [sortKey, setSortKey] = useState<SortKey>("rank");
  const [sortAsc, setSortAsc] = useState(false);

  const handleSort = (key: SortKey) => {
    if (sortKey === key) setSortAsc(!sortAsc);
    else { setSortKey(key); setSortAsc(false); }
  };

  const sorted = [...runs].sort((a, b) => {
    if (sortKey === "rank") return 0;
    const av = metricVal(a, sortKey);
    const bv = metricVal(b, sortKey);
    return sortAsc ? av - bv : bv - av;
  });

  const th = (key: SortKey, label: string) => (
    <th className="px-2 py-1.5 text-left text-[10px] text-zinc-500 font-medium cursor-pointer hover:text-zinc-300 whitespace-nowrap"
      onClick={() => handleSort(key)}>
      {label} {sortKey === key ? (sortAsc ? "↑" : "↓") : ""}
    </th>
  );

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead>
          <tr className="border-b border-border">
            {th("rank", "#")}
            {sweptKeys.map((k) => <th key={k} className="px-2 py-1.5 text-left text-[10px] text-zinc-500 font-medium font-mono whitespace-nowrap">{k}</th>)}
            {th("trade_count", "Trades")}
            {th("win_rate_pct", "Win %")}
            {th("total_pnl", "P&L")}
            {th("sharpe_ratio", "Sharpe")}
            {th("profit_factor", "PF")}
            {th("max_drawdown_pct", "DD %")}
            <th className="px-2 py-1.5 text-[10px] text-zinc-500"></th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((run, idx) => {
            const isFirst = idx === 0 && sortKey === "rank";
            const pnl = run.metrics.total_pnl;
            return (
              <tr key={run.index} className={`border-b border-border/50 ${isFirst ? "bg-emerald-900/10" : ""}`}>
                <td className="px-2 py-1.5 font-mono text-zinc-500">{idx + 1}</td>
                {sweptKeys.map((k) => (
                  <td key={k} className="px-2 py-1.5 font-mono">{run.params[k]}</td>
                ))}
                <td className="px-2 py-1.5 font-mono">{run.metrics.trade_count}</td>
                <td className="px-2 py-1.5 font-mono">{run.metrics.win_rate_pct?.toFixed(1)}%</td>
                <td className={`px-2 py-1.5 font-mono ${pnl >= 0 ? "text-emerald-400" : "text-red-400"}`}>
                  ${pnl?.toFixed(2)}
                </td>
                <td className="px-2 py-1.5 font-mono">{run.metrics.sharpe_ratio?.toFixed(3)}</td>
                <td className="px-2 py-1.5 font-mono">{run.metrics.profit_factor?.toFixed(2)}</td>
                <td className="px-2 py-1.5 font-mono text-red-400">{run.metrics.max_drawdown_pct?.toFixed(2)}%</td>
                <td className="px-2 py-1.5">
                  {appliedIndex === run.index ? (
                    <Badge className="bg-emerald-600/20 text-emerald-400 border-emerald-600/30 text-[10px]">Applied</Badge>
                  ) : (
                    <Button variant="ghost" size="sm" className="text-[10px] h-5 px-2 text-zinc-400 hover:text-emerald-400"
                      onClick={() => onApply(run.index)}>
                      Apply
                    </Button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function metricVal(run: SweepRunResult, key: SortKey): number {
  switch (key) {
    case "total_pnl": return run.metrics.total_pnl ?? 0;
    case "sharpe_ratio": return run.metrics.sharpe_ratio ?? 0;
    case "profit_factor": return run.metrics.profit_factor ?? 0;
    case "win_rate_pct": return run.metrics.win_rate_pct ?? 0;
    case "trade_count": return run.metrics.trade_count ?? 0;
    case "max_drawdown_pct": return run.metrics.max_drawdown_pct ?? 0;
    default: return 0;
  }
}
