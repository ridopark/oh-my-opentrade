import { useQuery } from "@tanstack/react-query";
import type { RollingDecayPoint, ComponentAttribution } from "@/lib/decay-types";

// ---------------------------------------------------------------------------
// Shared fetch helper
// ---------------------------------------------------------------------------

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(body || `Request failed: ${res.status}`);
  }
  return res.json();
}

// ---------------------------------------------------------------------------
// Query keys
// ---------------------------------------------------------------------------

export const decayKeys = {
  rolling: (strategy: string) => ["decay", "rolling", strategy] as const,
  attribution: (strategy: string) => ["decay", "attribution", strategy] as const,
};

// ---------------------------------------------------------------------------
// Rolling decay curve
// ---------------------------------------------------------------------------

export function useRollingDecay(strategy: string) {
  return useQuery({
    queryKey: decayKeys.rolling(strategy),
    queryFn: () =>
      fetchJSON<RollingDecayPoint[]>(
        `/api/decay/rolling?strategy=${encodeURIComponent(strategy)}`,
      ),
    enabled: !!strategy,
    staleTime: 60_000,
  });
}

// ---------------------------------------------------------------------------
// Component attribution
// ---------------------------------------------------------------------------

export function useComponentAttribution(strategy: string) {
  return useQuery({
    queryKey: decayKeys.attribution(strategy),
    queryFn: () =>
      fetchJSON<ComponentAttribution[]>(
        `/api/decay/attribution?strategy=${encodeURIComponent(strategy)}`,
      ),
    enabled: !!strategy,
    staleTime: 60_000,
  });
}
