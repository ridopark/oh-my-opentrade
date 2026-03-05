import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { DnaApprovalWithVersion, DnaDiffResponse } from "@/lib/types";

// Local fetch wrapper (following patterns from hooks/queries.ts)
async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(body || `Request failed: ${res.status}`);
  }
  return res.json();
}

const queryKeys = {
  dnaApprovals: ["dna", "approvals"],
  dnaApprovalDiff: (id: string) => ["dna", "approvals", id, "diff"],
};

export function useDnaApprovals() {
  return useQuery({
    queryKey: queryKeys.dnaApprovals,
    queryFn: () => fetchJSON<DnaApprovalWithVersion[]>("/api/dna/approvals"),
    refetchInterval: 15_000,
    refetchOnWindowFocus: true,
  });
}

export function useDnaApprovalDiff(id: string | null) {
  return useQuery({
    queryKey: queryKeys.dnaApprovalDiff(id!),
    queryFn: () => fetchJSON<DnaDiffResponse>(`/api/dna/approvals/${id}/diff`),
    enabled: !!id,
    staleTime: Infinity,
  });
}

export function useApproveDna() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      fetchJSON(`/api/dna/approvals/${id}/approve`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ decidedBy: "dashboard", comment: "" }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.dnaApprovals });
    },
  });
}

export function useRejectDna() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      fetchJSON(`/api/dna/approvals/${id}/reject`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ decidedBy: "dashboard", comment: "" }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.dnaApprovals });
    },
  });
}
