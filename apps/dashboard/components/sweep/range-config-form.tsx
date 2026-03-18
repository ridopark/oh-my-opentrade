"use client";

import { useState, useMemo } from "react";
import type { ParamMeta } from "@/lib/use-strategy-config";
import type { SweepRange } from "@/lib/use-sweep";
import { Button } from "@/components/ui/button";

const inputCls =
  "bg-background border border-border rounded px-2 py-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500 w-full";

interface RangeConfigFormProps {
  schema: ParamMeta[];
  onStart: (ranges: SweepRange[], targetMetric: string, concurrency: number) => void;
  disabled?: boolean;
}

export function RangeConfigForm({ schema, onStart, disabled }: RangeConfigFormProps) {
  const numericParams = useMemo(
    () => schema.filter((m) => m.type === "integer" || m.type === "number"),
    [schema]
  );

  const [selected, setSelected] = useState<Record<string, SweepRange>>({});
  const [targetMetric, setTargetMetric] = useState("sharpe_ratio");
  const [concurrency, setConcurrency] = useState(4);

  const toggleParam = (key: string) => {
    setSelected((prev) => {
      if (prev[key]) {
        const next = { ...prev };
        delete next[key];
        return next;
      }
      const meta = numericParams.find((m) => m.key === key);
      return {
        ...prev,
        [key]: {
          key,
          min: meta?.min ?? 1,
          max: meta?.max ?? 10,
          step: meta?.step ?? 1,
        },
      };
    });
  };

  const updateRange = (key: string, field: "min" | "max" | "step", value: number) => {
    setSelected((prev) => ({
      ...prev,
      [key]: { ...prev[key], [field]: value },
    }));
  };

  const totalCombos = useMemo(() => {
    const ranges = Object.values(selected);
    if (ranges.length === 0) return 0;
    return ranges.reduce((acc, r) => acc * (Math.floor((r.max - r.min) / r.step) + 1), 1);
  }, [selected]);

  const handleStart = () => {
    onStart(Object.values(selected), targetMetric, concurrency);
  };

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <h3 className="text-sm font-semibold text-zinc-300">Select Parameters to Sweep</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
          {numericParams.map((meta) => {
            const isSelected = !!selected[meta.key];
            return (
              <div key={meta.key} className={`border rounded-lg p-3 transition-colors cursor-pointer ${
                isSelected ? "border-emerald-600 bg-emerald-900/10" : "border-border hover:border-zinc-600"
              }`} onClick={() => toggleParam(meta.key)}>
                <div className="flex items-center gap-2">
                  <input type="checkbox" checked={isSelected} readOnly className="h-3.5 w-3.5" />
                  <span className="text-xs font-mono">{meta.key}</span>
                  <span className="text-[10px] text-zinc-600">({String(meta.default)})</span>
                </div>
                {isSelected && (
                  <div className="flex gap-2 mt-2" onClick={(e) => e.stopPropagation()}>
                    <div className="flex-1 space-y-0.5">
                      <label className="text-[10px] text-zinc-500">Min</label>
                      <input type="number" className={inputCls} value={selected[meta.key].min}
                        step={meta.step ?? 1}
                        onChange={(e) => updateRange(meta.key, "min", parseFloat(e.target.value))} />
                    </div>
                    <div className="flex-1 space-y-0.5">
                      <label className="text-[10px] text-zinc-500">Max</label>
                      <input type="number" className={inputCls} value={selected[meta.key].max}
                        step={meta.step ?? 1}
                        onChange={(e) => updateRange(meta.key, "max", parseFloat(e.target.value))} />
                    </div>
                    <div className="flex-1 space-y-0.5">
                      <label className="text-[10px] text-zinc-500">Step</label>
                      <input type="number" className={inputCls} value={selected[meta.key].step}
                        step={meta.step ?? 0.1}
                        onChange={(e) => updateRange(meta.key, "step", parseFloat(e.target.value))} />
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>

      <div className="flex items-end gap-4">
        <div className="space-y-1">
          <label className="text-xs text-muted-foreground">Target Metric</label>
          <select className={`${inputCls} w-44`} value={targetMetric} onChange={(e) => setTargetMetric(e.target.value)}>
            <option value="sharpe_ratio">Sharpe Ratio</option>
            <option value="profit_factor">Profit Factor</option>
            <option value="total_pnl">Total P&L</option>
            <option value="win_rate_pct">Win Rate</option>
            <option value="max_drawdown_pct">Max Drawdown</option>
          </select>
        </div>
        <div className="space-y-1">
          <label className="text-xs text-muted-foreground">Concurrency</label>
          <input type="number" className={`${inputCls} w-20`} value={concurrency}
            min={1} max={8} onChange={(e) => setConcurrency(parseInt(e.target.value, 10) || 1)} />
        </div>
        <div className="flex-1" />
        <div className="text-right">
          <div className="text-xs text-zinc-500 mb-1">
            {totalCombos} combination{totalCombos !== 1 ? "s" : ""}
          </div>
          <Button size="sm" disabled={totalCombos === 0 || disabled}
            className="bg-emerald-600 hover:bg-emerald-500 text-white"
            onClick={handleStart}>
            Start Sweep
          </Button>
        </div>
      </div>
    </div>
  );
}
