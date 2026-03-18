"use client";

import { use, useMemo } from "react";
import Link from "next/link";
import { ArrowLeft, Save, Settings, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { useStrategyConfig } from "@/lib/use-strategy-config";
import { ParamsSection } from "@/components/strategy-config/params-section";
import { ExitRulesEditor } from "@/components/strategy-config/exit-rules-editor";
import { SymbolOverridesEditor } from "@/components/strategy-config/symbol-overrides-editor";

const inputCls =
  "bg-background border border-border rounded px-2 py-1.5 text-xs font-mono text-foreground focus:outline-none focus:ring-1 focus:ring-slate-500 w-full";

export default function StrategyConfigPage({ params }: { params: Promise<{ strategyID: string }> }) {
  const { strategyID } = use(params);
  const sc = useStrategyConfig(strategyID);

  const groups = useMemo(() => {
    if (!sc.config) return [];
    const set = new Set(sc.config.param_schema.map((m) => m.group));
    return Array.from(set).sort();
  }, [sc.config]);

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
        {sc.error && <p className="text-red-400 text-sm">{sc.error}</p>}
        <Link href="/strategies"><Button variant="ghost" size="sm"><ArrowLeft className="w-4 h-4 mr-1" />Back</Button></Link>
      </div>
    );
  }

  const cfg = sc.config;

  return (
    <div className="flex flex-col h-[calc(100vh-3rem)]">
      <div className="sticky top-0 z-10 bg-background/95 backdrop-blur border-b border-border px-4 py-2 flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Link href={`/strategies/${strategyID}`}>
            <Button variant="ghost" size="sm"><ArrowLeft className="w-4 h-4" /></Button>
          </Link>
          <Settings className="w-4 h-4 text-zinc-500" />
          <span className="font-semibold text-sm">{cfg.strategy.name}</span>
          <Badge variant="outline" className="font-mono text-xs">{cfg.strategy.version}</Badge>
          {sc.isDirty && <Badge className="bg-amber-600/20 text-amber-400 border-amber-600/30 text-[10px]">Unsaved</Badge>}
        </div>
        <div className="flex items-center gap-2">
          {sc.error && <span className="text-xs text-red-400">{sc.error}</span>}
          <Button size="sm" disabled={!sc.isDirty || sc.saving} onClick={sc.save}
            className="bg-emerald-600 hover:bg-emerald-500 text-white">
            {sc.saving ? <Loader2 className="w-3 h-3 animate-spin mr-1" /> : <Save className="w-3 h-3 mr-1" />}
            Save
          </Button>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto px-4 py-4 space-y-4">
        <Card>
          <CardHeader className="py-3 px-4"><CardTitle className="text-sm">Strategy</CardTitle></CardHeader>
          <CardContent className="px-4 pb-4">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Name</label>
                <input className={inputCls} value={cfg.strategy.name}
                  onChange={(e) => sc.updateStrategy("name", e.target.value)} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Description</label>
                <input className={inputCls} value={cfg.strategy.description}
                  onChange={(e) => sc.updateStrategy("description", e.target.value)} />
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="py-3 px-4"><CardTitle className="text-sm">Lifecycle</CardTitle></CardHeader>
          <CardContent className="px-4 pb-4">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">State</label>
                <select className={inputCls} value={cfg.lifecycle.state}
                  onChange={(e) => sc.updateLifecycle("state", e.target.value)}>
                  {["live_active", "paper_only", "backtest_only", "archived"].map((s) => (
                    <option key={s} value={s}>{s}</option>
                  ))}
                </select>
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Paper Only</label>
                <input type="checkbox" checked={cfg.lifecycle.paper_only}
                  onChange={(e) => sc.updateLifecycle("paper_only", e.target.checked)}
                  className="h-4 w-4 rounded border-border bg-background" />
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="py-3 px-4"><CardTitle className="text-sm">Routing</CardTitle></CardHeader>
          <CardContent className="px-4 pb-4">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Symbols</label>
                <input className={inputCls} value={cfg.routing.symbols?.join(", ") ?? ""}
                  onChange={(e) => sc.updateRouting("symbols", e.target.value.split(",").map((s) => s.trim()).filter(Boolean))} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Timeframes</label>
                <input className={inputCls} value={cfg.routing.timeframes?.join(", ") ?? ""}
                  onChange={(e) => sc.updateRouting("timeframes", e.target.value.split(",").map((s) => s.trim()).filter(Boolean))} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Allowed Directions</label>
                <input className={inputCls} value={cfg.routing.allowed_directions?.join(", ") ?? ""}
                  onChange={(e) => sc.updateRouting("allowed_directions", e.target.value.split(",").map((s) => s.trim()).filter(Boolean))} />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-muted-foreground">Priority</label>
                <input type="number" className={inputCls} value={cfg.routing.priority}
                  onChange={(e) => sc.updateRouting("priority", parseInt(e.target.value, 10) || 0)} />
              </div>
            </div>
          </CardContent>
        </Card>

        {groups.map((group) => (
          <Card key={group}>
            <CardContent className="px-4 py-4">
              <ParamsSection params={cfg.params} schema={cfg.param_schema} group={group} onChange={sc.updateParam} />
            </CardContent>
          </Card>
        ))}

        <Card>
          <CardContent className="px-4 py-4">
            <ExitRulesEditor
              rules={cfg.exit_rules}
              onUpdate={sc.updateExitRule}
              onAdd={sc.addExitRule}
              onRemove={sc.removeExitRule}
            />
          </CardContent>
        </Card>

        <Card>
          <CardContent className="px-4 py-4">
            <SymbolOverridesEditor
              overrides={cfg.symbol_overrides}
              symbols={cfg.routing.symbols ?? []}
              baseParams={cfg.params}
              onChange={sc.updateSymbolOverride}
            />
          </CardContent>
        </Card>

        <div className="flex justify-end pb-8">
          <Link href={`/backtest?strategy=${strategyID}&symbols=${(cfg.routing.symbols ?? []).join(",")}&timeframe=${(cfg.routing.timeframes ?? [])[0] ?? "5m"}`}>
            <Button variant="outline" size="sm" className="text-xs">Run Backtest with These Params →</Button>
          </Link>
        </div>
      </div>
    </div>
  );
}
