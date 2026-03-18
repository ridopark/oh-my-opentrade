"use client";

import { use, useState } from "react";
import Link from "next/link";
import { ArrowLeft, Loader2, BarChart3 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useStrategyConfig } from "@/lib/use-strategy-config";
import { useSweep, type SweepRange } from "@/lib/use-sweep";
import { RangeConfigForm } from "@/components/sweep/range-config-form";
import { SweepProgressBar } from "@/components/sweep/sweep-progress";
import { SweepResultsTable } from "@/components/sweep/sweep-results-table";

const inputCls =
  "bg-background border border-border rounded px-2 py-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500 w-full";

export default function SweepPage({ params }: { params: Promise<{ strategyID: string }> }) {
  const { strategyID } = use(params);
  const sc = useStrategyConfig(strategyID);
  const sweep = useSweep(strategyID);
  const [appliedIndex, setAppliedIndex] = useState<number | undefined>();

  const [from, setFrom] = useState(new Date(Date.now() - 30 * 86400000).toISOString().split("T")[0]);
  const [to, setTo] = useState(new Date().toISOString().split("T")[0]);
  const [timeframe, setTimeframe] = useState("5m");
  const [equity, setEquity] = useState(100000);
  const [slippage, setSlippage] = useState(5);

  const handleStart = (ranges: SweepRange[], targetMetric: string, concurrency: number) => {
    if (!sc.config) return;
    sweep.start({
      ranges,
      target_metric: targetMetric,
      symbols: sc.config.routing.symbols,
      strategies: [strategyID],
      from,
      to,
      timeframe,
      initial_equity: equity,
      slippage_bps: slippage,
      no_ai: true,
      max_concurrency: concurrency,
    });
  };

  const handleApply = async (runIndex: number) => {
    try {
      await sweep.apply(runIndex);
      setAppliedIndex(runIndex);
    } catch {}
  };

  if (sc.loading) {
    return (
      <div className="flex items-center justify-center h-[calc(100vh-3rem)]">
        <Loader2 className="w-6 h-6 animate-spin text-zinc-500" />
      </div>
    );
  }

  if (!sc.config) {
    return (
      <div className="flex flex-col items-center justify-center h-[calc(100vh-3rem)] gap-4">
        <p className="text-zinc-400">Strategy not found</p>
        <Link href="/strategies"><Button variant="ghost" size="sm"><ArrowLeft className="w-4 h-4 mr-1" />Back</Button></Link>
      </div>
    );
  }

  const sweptKeys = sweep.finalRuns.length > 0
    ? Object.keys(sweep.finalRuns[0]?.params ?? {}).sort()
    : sweep.runs.length > 0
      ? Object.keys(sweep.runs[0]?.params ?? {}).sort()
      : [];

  return (
    <div className="flex flex-col h-[calc(100vh-3rem)]">
      <div className="sticky top-0 z-10 bg-background/95 backdrop-blur border-b border-border px-4 py-2 flex items-center gap-3">
        <Link href={`/strategies/${strategyID}/config`}>
          <Button variant="ghost" size="sm"><ArrowLeft className="w-4 h-4" /></Button>
        </Link>
        <BarChart3 className="w-4 h-4 text-zinc-500" />
        <span className="font-semibold text-sm">{sc.config.strategy.name}</span>
        <span className="text-xs text-zinc-500">Parameter Sweep</span>
      </div>

      <div className="flex-1 overflow-y-auto px-4 py-4 space-y-4">
        <Card>
          <CardHeader className="py-3 px-4"><CardTitle className="text-sm">Backtest Parameters</CardTitle></CardHeader>
          <CardContent className="px-4 pb-4">
            <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">From</label>
                <input type="date" className={inputCls} value={from} onChange={(e) => setFrom(e.target.value)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">To</label>
                <input type="date" className={inputCls} value={to} onChange={(e) => setTo(e.target.value)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Timeframe</label>
                <select className={inputCls} value={timeframe} onChange={(e) => setTimeframe(e.target.value)}>
                  {["1m", "5m", "15m", "1h"].map((tf) => <option key={tf} value={tf}>{tf}</option>)}
                </select>
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Equity</label>
                <input type="number" className={inputCls} value={equity} onChange={(e) => setEquity(parseFloat(e.target.value) || 100000)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Slippage (bps)</label>
                <input type="number" className={inputCls} value={slippage} onChange={(e) => setSlippage(parseInt(e.target.value, 10) || 5)} />
              </div>
            </div>
          </CardContent>
        </Card>

        {sweep.status === "idle" && (
          <Card>
            <CardContent className="px-4 py-4">
              <RangeConfigForm
                schema={sc.config.param_schema}
                onStart={handleStart}
              />
            </CardContent>
          </Card>
        )}

        {sweep.status === "running" && (
          <Card>
            <CardContent className="px-4 py-4">
              <SweepProgressBar progress={sweep.progress} onCancel={sweep.cancel} />
            </CardContent>
          </Card>
        )}

        {sweep.error && (
          <Card>
            <CardContent className="px-4 py-4">
              <p className="text-sm text-red-400">{sweep.error}</p>
            </CardContent>
          </Card>
        )}

        {(sweep.status === "completed" || sweep.finalRuns.length > 0) && (
          <Card>
            <CardHeader className="py-3 px-4">
              <div className="flex items-center justify-between">
                <CardTitle className="text-sm">Results ({sweep.finalRuns.length} runs)</CardTitle>
                <Button variant="ghost" size="sm" className="text-xs" onClick={sweep.reset}>New Sweep</Button>
              </div>
            </CardHeader>
            <CardContent className="px-4 pb-4">
              <SweepResultsTable
                runs={sweep.finalRuns}
                sweptKeys={sweptKeys}
                onApply={handleApply}
                appliedIndex={appliedIndex}
              />
            </CardContent>
          </Card>
        )}

        {sweep.status === "running" && sweep.runs.length > 0 && (
          <Card>
            <CardHeader className="py-3 px-4"><CardTitle className="text-sm">Live Results ({sweep.runs.length})</CardTitle></CardHeader>
            <CardContent className="px-4 pb-4">
              <SweepResultsTable runs={sweep.runs} sweptKeys={sweptKeys} onApply={handleApply} />
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}
