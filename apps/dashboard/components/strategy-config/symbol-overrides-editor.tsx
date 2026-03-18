"use client";

import { useState } from "react";
import type { SymbolOverride } from "@/lib/use-strategy-config";

const inputCls =
  "bg-background border border-border rounded px-2 py-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500 w-full";

interface SymbolOverridesEditorProps {
  overrides: Record<string, SymbolOverride>;
  symbols: string[];
  baseParams: Record<string, unknown>;
  onChange: (symbol: string, key: string, value: unknown) => void;
}

export function SymbolOverridesEditor({ overrides, symbols, baseParams, onChange }: SymbolOverridesEditorProps) {
  const [activeTab, setActiveTab] = useState(symbols[0] ?? "");
  const [newKey, setNewKey] = useState("");

  const activeOverride = overrides[activeTab]?.Params ?? {};

  const handleAdd = () => {
    if (!newKey || !activeTab) return;
    const baseVal = baseParams[newKey];
    onChange(activeTab, newKey, baseVal ?? 0);
    setNewKey("");
  };

  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold text-zinc-300">Symbol Overrides</h3>

      <div className="flex gap-1 border-b border-border">
        {symbols.map((sym) => (
          <button
            key={sym}
            onClick={() => setActiveTab(sym)}
            className={`px-3 py-1.5 text-xs font-mono transition-colors ${
              activeTab === sym
                ? "text-emerald-400 border-b-2 border-emerald-400"
                : "text-zinc-500 hover:text-zinc-300"
            }`}
          >
            {sym}
            {overrides[sym] && Object.keys(overrides[sym].Params ?? {}).length > 0 && (
              <span className="ml-1 text-emerald-600">●</span>
            )}
          </button>
        ))}
      </div>

      {activeTab && (
        <div className="space-y-2">
          {Object.entries(activeOverride).map(([key, val]) => (
            <div key={key} className="flex items-end gap-2">
              <div className="flex-1 space-y-1">
                <label className="text-xs text-muted-foreground flex items-center gap-2">
                  {key.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase())}
                  {baseParams[key] !== undefined && (
                    <span className="text-[10px] text-zinc-600">base: {String(baseParams[key])}</span>
                  )}
                </label>
                <input
                  type="number"
                  className={inputCls}
                  value={val as number ?? ""}
                  step={0.1}
                  onChange={(e) => {
                    const v = parseFloat(e.target.value);
                    if (!isNaN(v)) onChange(activeTab, key, v);
                  }}
                />
              </div>
            </div>
          ))}

          <div className="flex items-end gap-2 pt-2 border-t border-border/50">
            <div className="flex-1 space-y-1">
              <label className="text-xs text-muted-foreground">Add override</label>
              <select className={inputCls} value={newKey} onChange={(e) => setNewKey(e.target.value)}>
                <option value="">Select param...</option>
                {Object.keys(baseParams)
                  .filter((k) => !(activeOverride[k] !== undefined))
                  .filter((k) => !k.startsWith("regime_filter.") && !k.startsWith("dynamic_risk."))
                  .sort()
                  .map((k) => <option key={k} value={k}>{k}</option>)}
              </select>
            </div>
            <button
              onClick={handleAdd}
              disabled={!newKey}
              className="px-3 py-1.5 text-xs bg-emerald-600 hover:bg-emerald-500 disabled:opacity-30 rounded text-white"
            >
              Add
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
