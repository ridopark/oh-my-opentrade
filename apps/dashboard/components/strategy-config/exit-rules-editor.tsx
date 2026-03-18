"use client";

import type { ExitRule } from "@/lib/use-strategy-config";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

const inputCls =
  "bg-background border border-border rounded px-2 py-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500 w-full";

const EXIT_RULE_TYPES = [
  "VOLATILITY_STOP", "SD_TARGET", "STEP_STOP", "STAGNATION_EXIT",
  "TRAILING_STOP", "PROFIT_TARGET", "TIME_EXIT", "EOD_FLATTEN",
  "MAX_HOLDING_TIME", "MAX_LOSS", "BREAKEVEN_STOP",
];

interface ExitRulesEditorProps {
  rules: ExitRule[];
  onUpdate: (index: number, params: Record<string, number>) => void;
  onAdd: (type: string) => void;
  onRemove: (index: number) => void;
}

export function ExitRulesEditor({ rules, onUpdate, onAdd, onRemove }: ExitRulesEditorProps) {
  const usedTypes = new Set(rules.map((r) => r.type));

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-zinc-300">Exit Rules</h3>
        <select
          className={`${inputCls} w-48`}
          value=""
          onChange={(e) => { if (e.target.value) onAdd(e.target.value); }}
        >
          <option value="">Add rule...</option>
          {EXIT_RULE_TYPES.filter((t) => !usedTypes.has(t)).map((t) => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
      </div>

      {rules.map((rule, idx) => (
        <div key={`${rule.type}-${idx}`} className="border border-border rounded-lg p-3 space-y-2">
          <div className="flex items-center justify-between">
            <Badge variant="outline" className="font-mono text-xs">{rule.type}</Badge>
            <Button variant="ghost" size="sm" className="text-xs text-red-400 hover:text-red-300 h-6 px-2"
              onClick={() => onRemove(idx)}>
              Remove
            </Button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            {Object.entries(rule.params).map(([key, val]) => (
              <div key={key} className="space-y-1">
                <label className="text-xs text-muted-foreground">
                  {key.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase())}
                </label>
                <input
                  type="number"
                  className={inputCls}
                  value={val}
                  step={0.1}
                  onChange={(e) => {
                    const v = parseFloat(e.target.value);
                    if (!isNaN(v)) onUpdate(idx, { ...rule.params, [key]: v });
                  }}
                />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
