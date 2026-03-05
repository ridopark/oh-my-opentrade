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
} from "@/lib/types";

// ---------------------------------------------------------------------------
// Query keys — centralised to avoid typos & enable targeted invalidation
// ---------------------------------------------------------------------------

export const queryKeys = {
  health: ["health", "services"] as const,
  strategyInstances: ["strategies", "instances"] as const,
  currentStrategy: ["strategies", "current"] as const,
  performanceDashboard: (range: string) =>
    ["performance", "dashboard", range] as const,
  performanceTrades: (range: string) =>
    ["performance", "trades", range] as const,
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
// Performance dashboard
// ---------------------------------------------------------------------------

export function usePerformanceDashboard(range: string) {
  return useQuery({
    queryKey: queryKeys.performanceDashboard(range),
    queryFn: () =>
      fetchJSON<PerformanceDashboard>(
        `/api/performance/dashboard?range=${range}`,
      ),
  });
}

// ---------------------------------------------------------------------------
// Performance trades (cursor-based infinite query)
// ---------------------------------------------------------------------------

export function usePerformanceTrades(range: string) {
  return useInfiniteQuery({
    queryKey: queryKeys.performanceTrades(range),
    queryFn: ({ pageParam }) => {
      const params = pageParam
        ? `cursor=${encodeURIComponent(pageParam)}`
        : `range=${range}&limit=50`;
      return fetchJSON<TradesResponse>(
        `/api/performance/trades?${params}`,
      );
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor,
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
