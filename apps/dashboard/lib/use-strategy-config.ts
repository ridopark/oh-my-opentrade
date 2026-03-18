"use client";

import { useCallback, useEffect, useRef, useState } from "react";

export interface ParamMeta {
  key: string;
  type: string;
  default: unknown;
  description?: string;
  group: string;
  min?: number;
  max?: number;
  step?: number;
}

export interface ExitRule {
  type: string;
  params: Record<string, number>;
}

export interface SymbolOverride {
  Params: Record<string, unknown>;
  ExitRuleParams: Record<string, number>;
}

export interface StrategyConfig {
  strategy: { id: string; version: string; name: string; description: string; author: string };
  lifecycle: { state: string; paper_only: boolean };
  routing: {
    symbols: string[]; timeframes: string[]; asset_classes: string[];
    allowed_directions: string[]; priority: number; conflict_policy: string;
    exclusive_per_symbol: boolean; watchlist_mode: string;
  };
  params: Record<string, unknown>;
  param_schema: ParamMeta[];
  exit_rules: ExitRule[];
  symbol_overrides: Record<string, SymbolOverride>;
}

export function useStrategyConfig(strategyId: string) {
  const [config, setConfig] = useState<StrategyConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const originalRef = useRef<string>("");

  const fetchConfig = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch(`/api/strategies/config/${strategyId}/config`);
      if (!res.ok) throw new Error(await res.text());
      const data: StrategyConfig = await res.json();
      setConfig(data);
      originalRef.current = JSON.stringify(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load config");
    } finally {
      setLoading(false);
    }
  }, [strategyId]);

  useEffect(() => { fetchConfig(); }, [fetchConfig]);

  const isDirty = config ? JSON.stringify(config) !== originalRef.current : false;

  const updateParam = useCallback((key: string, value: unknown) => {
    setConfig(prev => prev ? { ...prev, params: { ...prev.params, [key]: value } } : prev);
  }, []);

  const updateExitRule = useCallback((index: number, params: Record<string, number>) => {
    setConfig(prev => {
      if (!prev) return prev;
      const rules = [...prev.exit_rules];
      rules[index] = { ...rules[index], params };
      return { ...prev, exit_rules: rules };
    });
  }, []);

  const addExitRule = useCallback((type: string) => {
    setConfig(prev => {
      if (!prev) return prev;
      return { ...prev, exit_rules: [...prev.exit_rules, { type, params: {} }] };
    });
  }, []);

  const removeExitRule = useCallback((index: number) => {
    setConfig(prev => {
      if (!prev) return prev;
      return { ...prev, exit_rules: prev.exit_rules.filter((_, i) => i !== index) };
    });
  }, []);

  const updateSymbolOverride = useCallback((symbol: string, key: string, value: unknown) => {
    setConfig(prev => {
      if (!prev) return prev;
      const existing = prev.symbol_overrides[symbol] ?? { Params: {}, ExitRuleParams: {} };
      return {
        ...prev,
        symbol_overrides: {
          ...prev.symbol_overrides,
          [symbol]: { ...existing, Params: { ...existing.Params, [key]: value } },
        },
      };
    });
  }, []);

  const updateRouting = useCallback(<K extends keyof StrategyConfig["routing"]>(key: K, value: StrategyConfig["routing"][K]) => {
    setConfig(prev => prev ? { ...prev, routing: { ...prev.routing, [key]: value } } : prev);
  }, []);

  const updateLifecycle = useCallback(<K extends keyof StrategyConfig["lifecycle"]>(key: K, value: StrategyConfig["lifecycle"][K]) => {
    setConfig(prev => prev ? { ...prev, lifecycle: { ...prev.lifecycle, [key]: value } } : prev);
  }, []);

  const updateStrategy = useCallback(<K extends keyof StrategyConfig["strategy"]>(key: K, value: StrategyConfig["strategy"][K]) => {
    setConfig(prev => prev ? { ...prev, strategy: { ...prev.strategy, [key]: value } } : prev);
  }, []);

  const save = useCallback(async () => {
    if (!config) return;
    setSaving(true);
    setError(null);
    try {
      const body: Record<string, unknown> = { ...config };
      const regime: Record<string, unknown> = {};
      const dynRisk: Record<string, unknown> = {};
      const params: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(config.params)) {
        if (k.startsWith("regime_filter.")) regime[k.replace("regime_filter.", "")] = v;
        else if (k.startsWith("dynamic_risk.")) dynRisk[k.replace("dynamic_risk.", "")] = v;
        else params[k] = v;
      }
      body.params = params;
      body.regime_filter = regime;
      body.dynamic_risk = dynRisk;

      const res = await fetch(`/api/strategies/config/${config.strategy.id}/config`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        const errData = await res.json().catch(() => ({ error: "Save failed" }));
        throw new Error(errData.error || "Save failed");
      }
      const updated: StrategyConfig = await res.json();
      setConfig(updated);
      originalRef.current = JSON.stringify(updated);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  }, [config]);

  return {
    config, loading, saving, error, isDirty,
    updateParam, updateExitRule, addExitRule, removeExitRule,
    updateSymbolOverride, updateRouting, updateLifecycle, updateStrategy, save,
  };
}
