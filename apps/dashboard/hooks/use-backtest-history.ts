import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(body || `Request failed: ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

export type EquityPoint = { t: number; eq: number };

export type BacktestRunSummary = {
  id: string;
  ran_at: string;
  strategies: string[];
  symbols: string[];
  period_start: string;
  period_end: string;
  pf: number;
  win_rate: number;
  expectancy: number;
  max_drawdown: number;
  sharpe: number;
  trade_count: number;
  net_pnl: number;
  total_return: number;
  equity_curve: EquityPoint[] | null;
  tags: string[];
  pinned: boolean;
};

export type BacktestTradeRow = {
  seq: number;
  symbol: string;
  side: string;
  direction?: string;
  quantity: number;
  price: number;
  filled_at: string;
  pnl?: number;
  strategy_id?: string;
  rationale?: string;
  regime?: string;
  vix_bucket?: string;
  market_context?: string;
};

export type BacktestRunDetail = {
  summary: BacktestRunSummary;
  initial_equity: number;
  final_equity: number;
  slippage_bps: number;
  no_ai: boolean;
  win_count: number;
  loss_count: number;
  dna_snapshot: Record<string, unknown>;
  notes: string;
  trades: BacktestTradeRow[];
};

export type BacktestHistoryFilter = {
  strategy?: string; // comma-separated
  symbol?: string;
  tags?: string;
  q?: string;
  from?: string;
  to?: string;
  min_pf?: number;
  pinned?: boolean;
  limit?: number;
  offset?: number;
  order_by?: string;
  order_dir?: "asc" | "desc";
};

function buildQS(filter: BacktestHistoryFilter): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filter)) {
    if (v === undefined || v === null || v === "") continue;
    if (typeof v === "boolean") {
      if (v) params.set(k, "true");
    } else {
      params.set(k, String(v));
    }
  }
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

const keys = {
  list: (f: BacktestHistoryFilter) => ["backtest", "history", "list", f] as const,
  detail: (id: string) => ["backtest", "history", "detail", id] as const,
};

export function useBacktestHistoryList(filter: BacktestHistoryFilter) {
  return useQuery({
    queryKey: keys.list(filter),
    queryFn: () =>
      fetchJSON<{ runs: BacktestRunSummary[]; total: number }>(
        `/api/backtest/history${buildQS(filter)}`,
      ),
    staleTime: 10_000,
  });
}

export function useBacktestHistoryDetail(id: string | null) {
  return useQuery({
    queryKey: keys.detail(id ?? ""),
    queryFn: () => fetchJSON<BacktestRunDetail>(`/api/backtest/history/${id}`),
    enabled: !!id,
    staleTime: 60_000,
  });
}

export function useSetBacktestTags() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, tags }: { id: string; tags: string[] }) =>
      fetchJSON<void>(`/api/backtest/history/${id}/tags`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tags }),
      }),
    onSuccess: (_data, { id }) => {
      qc.invalidateQueries({ queryKey: ["backtest", "history"] });
      qc.invalidateQueries({ queryKey: keys.detail(id) });
    },
  });
}

export function useSetBacktestPinned() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, pinned }: { id: string; pinned: boolean }) =>
      fetchJSON<void>(`/api/backtest/history/${id}/pin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pinned }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backtest", "history"] });
    },
  });
}
