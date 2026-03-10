import {
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import type {
  PerformanceDashboard,
  TradesResponse,
  StrategyDNA,
  StrategyInfo,
  StrategyDashboard,
  StateSnapshot,
  StrategySignalsResponse,
  StrategyRow,
  SymbolAttribution,
} from "@/lib/types";

// ---------------------------------------------------------------------------
// Query keys — centralised to avoid typos & enable targeted invalidation
// ---------------------------------------------------------------------------

export interface PerformanceFilters {
  from?: string;  // RFC3339
  to?: string;    // RFC3339
  range?: string; // "7d" | "30d" | "90d" | "all"
  strategy?: string;
  symbol?: string;
}

export const queryKeys = {
  health: ["health", "services"] as const,
  strategyInstances: ["strategies", "instances"] as const,
  currentStrategy: ["strategies", "current"] as const,
  allStrategiesDNA: ["strategies", "dna", "all"] as const,
  performanceDashboard: (filters: PerformanceFilters) =>
    ["performance", "dashboard", filters] as const,
  performanceTrades: (filters: PerformanceFilters) =>
    ["performance", "trades", filters] as const,
  performanceStrategies: (filters: PerformanceFilters) =>
    ["performance", "strategies", filters] as const,
  performanceSymbols: (filters: PerformanceFilters) =>
    ["performance", "symbols", filters] as const,
  strategyList: ["strategies", "perf", "list"] as const,
  strategyDashboard: (id: string, range: string) =>
    ["strategies", "perf", id, "dashboard", range] as const,
  strategyState: (id: string) =>
    ["strategies", "perf", id, "state"] as const,
  strategySignals: (id: string) =>
    ["strategies", "perf", id, "signals"] as const,
};

// ---------------------------------------------------------------------------
// Shared fetch helper — throws on non-2xx so React Query treats it as error
// ---------------------------------------------------------------------------

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(body || `Request failed: ${res.status}`);
  }
  return res.json();
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

interface ServiceStatus {
  name: string;
  healthy: boolean;
  detail?: string;
}

export interface HealthResponse {
  healthy: boolean;
  services: ServiceStatus[];
}

export function useServiceHealth() {
  return useQuery({
    queryKey: queryKeys.health,
    queryFn: () => fetchJSON<HealthResponse>("/api/health/services"),
    refetchInterval: 10_000,
  });
}

// ---------------------------------------------------------------------------
// Strategy instances
// ---------------------------------------------------------------------------

export interface InstanceInfo {
  id: string;
  strategyName: string;
  lifecycle: string;
  symbols: string[];
  isActive: boolean;
  allowedTransitions: string[];
}

export function useStrategyInstances() {
  return useQuery({
    queryKey: queryKeys.strategyInstances,
    queryFn: async () => {
      const data = await fetchJSON<InstanceInfo[] | null>(
        "/api/strategies/v2/instances",
      );
      return data ?? [];
    },
  });
}

// ---------------------------------------------------------------------------
// Current strategy DNA
// ---------------------------------------------------------------------------

interface CurrentStrategyResponse {
  id: string;
  version?: number;
  description?: string;
  parameters: Record<string, string | number | boolean>;
}

export function useCurrentStrategy() {
  return useQuery({
    queryKey: queryKeys.currentStrategy,
    queryFn: async (): Promise<StrategyDNA | null> => {
      const res = await fetch("/api/strategies/current");
      if (!res.ok) return null;
      const data: CurrentStrategyResponse = await res.json();
      if (!data?.id || !data?.parameters) return null;
      return {
        id: data.id,
        version: data.version ?? 1,
        description: data.description,
        parameters: data.parameters,
        performanceMetrics: {},
      };
    },
  });
}

// ---------------------------------------------------------------------------
// All strategy DNAs
// ---------------------------------------------------------------------------

export function useAllStrategiesDNA() {
  return useQuery({
    queryKey: queryKeys.allStrategiesDNA,
    queryFn: async (): Promise<StrategyDNA[]> => {
      const res = await fetch("/api/strategies/dna");
      if (!res.ok) return [];
      const data = await res.json();
      if (!Array.isArray(data)) return [];
      return data.map((d: Record<string, unknown>) => ({
        id: (d.id as string) ?? "",
        version: d.version as number ?? 1,
        description: d.description as string | undefined,
        parameters: (d.parameters as Record<string, string | number | boolean>) ?? {},
        performanceMetrics: {},
      }));
    },
  });
}

// ---------------------------------------------------------------------------
// Performance dashboard
// ---------------------------------------------------------------------------

function buildFilterQuery(filters: PerformanceFilters): string {
  const params = new URLSearchParams();
  if (filters.from && filters.to) {
    params.set("from", filters.from);
    params.set("to", filters.to);
  } else if (filters.range) {
    params.set("range", filters.range);
  }
  if (filters.strategy && filters.strategy !== "all") {
    params.set("strategy", filters.strategy);
  }
  if (filters.symbol && filters.symbol !== "all") {
    params.set("symbol", filters.symbol);
  }
  return params.toString();
}

export function usePerformanceDashboard(filters: PerformanceFilters) {
  return useQuery({
    queryKey: queryKeys.performanceDashboard(filters),
    queryFn: () => {
      const qs = buildFilterQuery(filters);
      return fetchJSON<PerformanceDashboard>(`/api/performance/dashboard?${qs}`);
    },
  });
}

// ---------------------------------------------------------------------------
// Performance trades (cursor-based infinite query)
// ---------------------------------------------------------------------------

export function usePerformanceTrades(filters: PerformanceFilters) {
  return useInfiniteQuery({
    queryKey: queryKeys.performanceTrades(filters),
    queryFn: ({ pageParam }) => {
      const qs = buildFilterQuery(filters);
      const params = pageParam
        ? `cursor=${encodeURIComponent(pageParam)}&${qs}`
        : `${qs}&limit=50`;
      return fetchJSON<TradesResponse>(`/api/performance/trades?${params}`);
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor,
  });
}

// ---------------------------------------------------------------------------
// Performance strategies and symbols
// ---------------------------------------------------------------------------

export function usePerformanceStrategies(filters: PerformanceFilters) {
  return useQuery({
    queryKey: queryKeys.performanceStrategies(filters),
    queryFn: () => {
      const qs = buildFilterQuery(filters);
      return fetchJSON<StrategyRow[]>(`/api/performance/strategies?${qs}`);
    },
  });
}

export function usePerformanceSymbols(filters: PerformanceFilters) {
  return useQuery({
    queryKey: queryKeys.performanceSymbols(filters),
    queryFn: () => {
      const qs = buildFilterQuery(filters);
      return fetchJSON<SymbolAttribution[]>(`/api/performance/symbols?${qs}`);
    },
  });
}

// ---------------------------------------------------------------------------
// Mutations — strategy lifecycle
// ---------------------------------------------------------------------------

export function usePromoteInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, target }: { id: string; target: string }) => {
      return fetchJSON<unknown>(
        `/api/strategies/v2/instances/${encodeURIComponent(id)}/promote`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ target }),
        },
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: queryKeys.strategyInstances });
    },
  });
}

export function useLifecycleAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      action,
    }: {
      id: string;
      action: "deactivate" | "archive";
    }) => {
      return fetchJSON<unknown>(
        `/api/strategies/v2/instances/${encodeURIComponent(id)}/${action}`,
        { method: "POST" },
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: queryKeys.strategyInstances });
    },
  });
}

// ---------------------------------------------------------------------------
// Per-strategy performance
// ---------------------------------------------------------------------------

export function useStrategyList() {
  return useQuery({
    queryKey: queryKeys.strategyList,
    queryFn: () => fetchJSON<StrategyInfo[]>("/api/strategies/"),
  });
}

export function useStrategyDashboard(strategyID: string, range: string) {
  return useQuery({
    queryKey: queryKeys.strategyDashboard(strategyID, range),
    queryFn: () =>
      fetchJSON<StrategyDashboard>(
        `/api/strategies/${encodeURIComponent(strategyID)}/dashboard?range=${range}`,
      ),
    enabled: !!strategyID,
  });
}

export function useStrategyState(strategyID: string) {
  return useQuery({
    queryKey: queryKeys.strategyState(strategyID),
    queryFn: () =>
      fetchJSON<StateSnapshot[]>(
        `/api/strategies/${encodeURIComponent(strategyID)}/state`,
      ),
    enabled: !!strategyID,
    refetchInterval: 15_000,
  });
}

export function useStrategySignals(strategyID: string) {
  return useInfiniteQuery({
    queryKey: queryKeys.strategySignals(strategyID),
    queryFn: ({ pageParam }) => {
      const params = pageParam
        ? `cursor=${encodeURIComponent(pageParam)}`
        : "limit=50";
      return fetchJSON<StrategySignalsResponse>(
        `/api/strategies/${encodeURIComponent(strategyID)}/signals?${params}`,
      );
    },
    enabled: !!strategyID,
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor,
  });
}
