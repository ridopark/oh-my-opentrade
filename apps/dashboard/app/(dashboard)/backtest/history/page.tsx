"use client";

import { useMemo, useState } from "react";
import { formatQty } from "@/lib/format";
import {
  Search,
  Star,
  GitCompare,
  Download,
  Tag as TagIcon,
  SlidersHorizontal,
  TrendingUp,
  TrendingDown,
  Loader2,
  X,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import {
  useBacktestHistoryList,
  useBacktestHistoryDetail,
  useSetBacktestPinned,
  useSetBacktestTags,
  type BacktestRunSummary,
  type EquityPoint,
} from "@/hooks/use-backtest-history";

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

function formatDateTime(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleString(undefined, {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return iso;
  }
}

function formatDate(iso: string): string {
  try {
    return new Date(iso).toISOString().slice(0, 10);
  } catch {
    return iso;
  }
}

function curveValues(points: EquityPoint[] | null): number[] {
  if (!points || points.length === 0) return [1, 1];
  return points.map((p) => p.eq);
}

function Sparkline({ points, positive }: { points: EquityPoint[] | null; positive: boolean }) {
  const data = curveValues(points);
  const w = 120;
  const h = 32;
  if (data.length < 2) {
    return <svg width={w} height={h} />;
  }
  const min = Math.min(...data);
  const max = Math.max(...data);
  const range = max - min || 1;
  const pts = data
    .map((v, i) => {
      const x = (i / (data.length - 1)) * w;
      const y = h - ((v - min) / range) * h;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const areaPts = `0,${h} ${pts} ${w},${h}`;
  const color = positive ? "#10b981" : "#ef4444";
  return (
    <svg width={w} height={h} className="overflow-visible">
      <polygon points={areaPts} fill={color} fillOpacity={0.12} />
      <polyline points={pts} fill="none" stroke={color} strokeWidth={1.5} />
    </svg>
  );
}

function BigSparkline({ points, positive }: { points: EquityPoint[] | null; positive: boolean }) {
  const data = curveValues(points);
  const w = 560;
  const h = 140;
  if (data.length < 2) {
    return (
      <div className="flex items-center justify-center h-[140px] text-xs text-muted-foreground">
        no equity samples
      </div>
    );
  }
  const min = Math.min(...data);
  const max = Math.max(...data);
  const range = max - min || 1;
  const pts = data
    .map((v, i) => {
      const x = (i / (data.length - 1)) * w;
      const y = h - ((v - min) / range) * h;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const color = positive ? "#10b981" : "#ef4444";
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-[140px]">
      <polygon points={`0,${h} ${pts} ${w},${h}`} fill={color} fillOpacity={0.12} />
      <polyline points={pts} fill="none" stroke={color} strokeWidth={2} />
    </svg>
  );
}

function OverlayChart({ runs }: { runs: BacktestRunSummary[] }) {
  const w = 760;
  const h = 220;
  const colors = ["#10b981", "#3b82f6", "#f59e0b", "#ef4444"];
  const all = runs.flatMap((r) => curveValues(r.equity_curve));
  if (all.length === 0) {
    return <div className="h-[220px] flex items-center justify-center text-xs text-muted-foreground">no curves</div>;
  }
  const min = Math.min(...all, 1);
  const max = Math.max(...all, 1);
  const range = max - min || 1;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-[220px]">
      <line
        x1={0}
        y1={h - ((1 - min) / range) * h}
        x2={w}
        y2={h - ((1 - min) / range) * h}
        stroke="currentColor"
        strokeOpacity={0.15}
        strokeDasharray="4 4"
      />
      {runs.map((r, idx) => {
        const data = curveValues(r.equity_curve);
        if (data.length < 2) return null;
        const pts = data
          .map((v, i) => {
            const x = (i / (data.length - 1)) * w;
            const y = h - ((v - min) / range) * h;
            return `${x.toFixed(1)},${y.toFixed(1)}`;
          })
          .join(" ");
        return (
          <polyline
            key={r.id}
            points={pts}
            fill="none"
            stroke={colors[idx] ?? "#888"}
            strokeWidth={2}
          />
        );
      })}
    </svg>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Page
// ─────────────────────────────────────────────────────────────────────────────

type OrderBy = "ran_at" | "pf" | "win_rate" | "expectancy" | "max_drawdown" | "trade_count" | "net_pnl";

export default function BacktestHistoryPage() {
  const [search, setSearch] = useState("");
  const [strategy, setStrategy] = useState<string>("");
  const [minPF, setMinPF] = useState(0);
  const [orderBy, setOrderBy] = useState<OrderBy>("ran_at");
  const [orderDir, setOrderDir] = useState<"asc" | "desc">("desc");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [drillId, setDrillId] = useState<string | null>(null);
  const [compareOpen, setCompareOpen] = useState(false);

  const filter = useMemo(
    () => ({
      q: search.trim() || undefined,
      strategy: strategy || undefined,
      min_pf: minPF > 0 ? minPF : undefined,
      order_by: orderBy,
      order_dir: orderDir,
      limit: 100,
    }),
    [search, strategy, minPF, orderBy, orderDir],
  );

  const list = useBacktestHistoryList(filter);
  const runs = list.data?.runs ?? [];
  const total = list.data?.total ?? 0;

  const detail = useBacktestHistoryDetail(drillId);
  const setPinned = useSetBacktestPinned();

  const selectedRuns = runs.filter((r) => selected.has(r.id));

  const toggleSelect = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else if (next.size < 4) next.add(id);
      return next;
    });
  };

  const toggleSort = (k: OrderBy) => {
    if (orderBy === k) setOrderDir((d) => (d === "asc" ? "desc" : "asc"));
    else {
      setOrderBy(k);
      setOrderDir("desc");
    }
  };

  return (
    <div className="p-6 space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Backtest History</h1>
          <p className="text-sm text-muted-foreground">
            {list.isLoading ? "loading…" : `${runs.length} of ${total} runs`}
            {" · "}
            {selected.size}/4 selected for compare
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" disabled>
            <Download className="h-4 w-4 mr-1.5" /> Export
          </Button>
          <Button
            size="sm"
            disabled={selected.size < 2}
            onClick={() => setCompareOpen(true)}
          >
            <GitCompare className="h-4 w-4 mr-1.5" />
            Compare ({selected.size})
          </Button>
        </div>
      </div>

      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-2 p-3 rounded-lg border bg-card">
        <div className="relative flex-1 min-w-[220px]">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search symbol, id, tag…"
            className="w-full h-9 pl-8 pr-3 rounded-md bg-background border text-sm outline-none focus:ring-2 focus:ring-ring"
          />
        </div>

        <input
          value={strategy}
          onChange={(e) => setStrategy(e.target.value)}
          placeholder="Strategy filter (comma sep)"
          className="h-9 px-3 rounded-md bg-background border text-sm w-56 outline-none focus:ring-2 focus:ring-ring"
        />

        <div className="flex items-center gap-2 h-9 px-3 rounded-md border bg-background">
          <span className="text-xs text-muted-foreground">Min PF</span>
          <input
            type="range"
            min={0}
            max={3}
            step={0.1}
            value={minPF}
            onChange={(e) => setMinPF(parseFloat(e.target.value))}
            className="w-24 accent-primary"
          />
          <span className="text-xs font-mono tabular-nums w-8">{minPF.toFixed(1)}</span>
        </div>

        <Button variant="outline" size="sm" className="h-9" disabled>
          <SlidersHorizontal className="h-4 w-4 mr-1.5" /> More filters
        </Button>

        {list.isFetching && !list.isLoading ? (
          <span className="text-xs text-muted-foreground flex items-center gap-1">
            <Loader2 className="h-3 w-3 animate-spin" /> refreshing
          </span>
        ) : null}
      </div>

      {/* Table */}
      <div className="rounded-lg border bg-card overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-10"></TableHead>
              <TableHead className="w-10"></TableHead>
              <SortHead label="Ran" k="ran_at" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} />
              <TableHead>Strategies</TableHead>
              <TableHead>Symbols</TableHead>
              <TableHead>Period</TableHead>
              <SortHead label="PF" k="pf" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} align="right" />
              <SortHead label="Win %" k="win_rate" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} align="right" />
              <SortHead label="Expct" k="expectancy" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} align="right" />
              <SortHead label="Max DD" k="max_drawdown" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} align="right" />
              <SortHead label="Trades" k="trade_count" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} align="right" />
              <SortHead label="Net P&L" k="net_pnl" orderBy={orderBy} orderDir={orderDir} onClick={toggleSort} align="right" />
              <TableHead className="text-center">Equity</TableHead>
              <TableHead>Tags</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.isLoading ? (
              <TableRow>
                <TableCell colSpan={14} className="text-center py-12 text-muted-foreground">
                  <Loader2 className="inline h-4 w-4 animate-spin mr-2" />
                  loading history…
                </TableCell>
              </TableRow>
            ) : list.isError ? (
              <TableRow>
                <TableCell colSpan={14} className="text-center py-12 text-red-500">
                  error: {String(list.error)}
                </TableCell>
              </TableRow>
            ) : runs.length === 0 ? (
              <TableRow>
                <TableCell colSpan={14} className="text-center py-12 text-muted-foreground">
                  no runs yet — kick off a backtest and check back
                </TableCell>
              </TableRow>
            ) : (
              runs.map((r) => {
                const isSel = selected.has(r.id);
                const positive = r.net_pnl >= 0;
                return (
                  <TableRow
                    key={r.id}
                    className={cn("cursor-pointer", isSel && "bg-primary/5 hover:bg-primary/10")}
                    onClick={() => setDrillId(r.id)}
                  >
                    <TableCell onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        checked={isSel}
                        onChange={() => toggleSelect(r.id)}
                        className="h-4 w-4 accent-primary cursor-pointer"
                      />
                    </TableCell>
                    <TableCell onClick={(e) => e.stopPropagation()}>
                      <button
                        onClick={() => setPinned.mutate({ id: r.id, pinned: !r.pinned })}
                        className="cursor-pointer"
                        title={r.pinned ? "unpin" : "pin"}
                      >
                        <Star
                          className={cn(
                            "h-4 w-4",
                            r.pinned ? "fill-yellow-400 text-yellow-400" : "text-muted-foreground/40",
                          )}
                        />
                      </button>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground whitespace-nowrap">
                      {formatDateTime(r.ran_at)}
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1 flex-wrap max-w-[140px]">
                        {r.strategies.slice(0, 2).map((s) => (
                          <Badge key={s} variant="outline" className="font-mono text-[10px]">
                            {s}
                          </Badge>
                        ))}
                        {r.strategies.length > 2 && (
                          <span className="text-[10px] text-muted-foreground">+{r.strategies.length - 2}</span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs max-w-[160px] truncate">
                      {r.symbols.slice(0, 3).join(", ")}
                      {r.symbols.length > 3 && ` +${r.symbols.length - 3}`}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground whitespace-nowrap">
                      {formatDate(r.period_start)} → {formatDate(r.period_end)}
                    </TableCell>
                    <TableCell
                      className={cn(
                        "text-right font-mono tabular-nums font-semibold",
                        r.pf >= 2 ? "text-emerald-500" : r.pf >= 1 ? "text-foreground" : "text-red-500",
                      )}
                    >
                      {r.pf.toFixed(2)}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{r.win_rate.toFixed(1)}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {r.expectancy >= 0 ? "+" : ""}
                      {r.expectancy.toFixed(2)}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums text-red-500">
                      {r.max_drawdown.toFixed(1)}%
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums text-muted-foreground">
                      {r.trade_count}
                    </TableCell>
                    <TableCell
                      className={cn(
                        "text-right font-mono tabular-nums font-semibold",
                        positive ? "text-emerald-500" : "text-red-500",
                      )}
                    >
                      {positive ? "+" : ""}${r.net_pnl.toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <Sparkline points={r.equity_curve} positive={positive} />
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1 flex-wrap max-w-[160px]">
                        {r.tags.map((t) => (
                          <Badge key={t} variant="secondary" className="text-[10px] px-1.5 py-0">
                            {t}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>

      {/* Drill-in sheet */}
      <Sheet open={!!drillId} onOpenChange={(o) => !o && setDrillId(null)}>
        <SheetContent side="right" className="w-[640px] sm:max-w-[640px] overflow-y-auto">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-3">
              {detail.isLoading ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" /> loading…
                </>
              ) : detail.data ? (
                <>
                  <span>{detail.data.summary.symbols.slice(0, 3).join(", ")}</span>
                  {detail.data.summary.strategies.map((s) => (
                    <Badge key={s} variant="outline">{s}</Badge>
                  ))}
                  <span className="text-xs font-mono text-muted-foreground truncate max-w-[160px]">
                    {detail.data.summary.id}
                  </span>
                </>
              ) : (
                "backtest detail"
              )}
            </SheetTitle>
          </SheetHeader>
          {detail.data && (
            <div className="p-4 space-y-4">
              <div className="grid grid-cols-4 gap-3">
                <Metric label="PF" value={detail.data.summary.pf.toFixed(2)} positive={detail.data.summary.pf >= 1} />
                <Metric label="Win %" value={detail.data.summary.win_rate.toFixed(1)} />
                <Metric
                  label="Expectancy"
                  value={detail.data.summary.expectancy.toFixed(2)}
                  positive={detail.data.summary.expectancy >= 0}
                />
                <Metric label="Max DD" value={`${detail.data.summary.max_drawdown.toFixed(1)}%`} negative />
                <Metric label="Trades" value={detail.data.summary.trade_count.toString()} />
                <Metric
                  label="Net P&L"
                  value={`$${detail.data.summary.net_pnl.toLocaleString()}`}
                  positive={detail.data.summary.net_pnl >= 0}
                />
                <Metric label="Sharpe" value={detail.data.summary.sharpe.toFixed(2)} />
                <Metric label="Ran" value={formatDateTime(detail.data.summary.ran_at)} small />
              </div>

              <div className="rounded-md border p-3">
                <div className="text-xs text-muted-foreground mb-2">Equity curve</div>
                <BigSparkline
                  points={detail.data.summary.equity_curve}
                  positive={detail.data.summary.net_pnl >= 0}
                />
              </div>

              <TagEditor id={detail.data.summary.id} tags={detail.data.summary.tags} />

              <div className="rounded-md border p-3">
                <div className="text-xs text-muted-foreground mb-2">Strategy DNA (snapshot)</div>
                <pre className="text-xs font-mono bg-muted p-2 rounded overflow-x-auto max-h-[300px]">
                  {JSON.stringify(detail.data.dna_snapshot, null, 2)}
                </pre>
              </div>

              <div className="rounded-md border p-3">
                <div className="text-xs text-muted-foreground mb-2">
                  Trades ({detail.data.trades.length})
                </div>
                <div className="max-h-[300px] overflow-y-auto text-xs font-mono">
                  {detail.data.trades.slice(0, 200).map((t) => (
                    <div key={t.seq} className="flex justify-between py-0.5 border-b last:border-0">
                      <span>
                        {t.symbol} {t.side} {formatQty(t.quantity)}@${t.price.toFixed(2)}
                      </span>
                      <span className={cn(t.pnl && t.pnl > 0 ? "text-emerald-500" : t.pnl && t.pnl < 0 ? "text-red-500" : "")}>
                        {t.pnl ? `$${t.pnl.toFixed(2)}` : ""}
                      </span>
                    </div>
                  ))}
                  {detail.data.trades.length > 200 && (
                    <div className="text-center py-2 text-muted-foreground">
                      +{detail.data.trades.length - 200} more
                    </div>
                  )}
                </div>
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>

      {/* Compare sheet */}
      <Sheet open={compareOpen} onOpenChange={setCompareOpen}>
        <SheetContent side="right" className="w-[820px] sm:max-w-[820px] overflow-y-auto">
          <SheetHeader>
            <SheetTitle>Compare runs ({selectedRuns.length})</SheetTitle>
          </SheetHeader>
          <div className="p-4 space-y-4">
            <div className="rounded-md border p-3">
              <div className="text-xs text-muted-foreground mb-2">Equity curves (overlay)</div>
              <OverlayChart runs={selectedRuns} />
              <div className="flex gap-3 mt-2 text-xs flex-wrap">
                {selectedRuns.map((r, i) => (
                  <div key={r.id} className="flex items-center gap-1.5">
                    <span
                      className="inline-block w-2.5 h-2.5 rounded-sm"
                      style={{ background: ["#10b981", "#3b82f6", "#f59e0b", "#ef4444"][i] }}
                    />
                    {r.symbols.slice(0, 2).join("/")} · {r.strategies[0]} · {r.id.slice(-5)}
                  </div>
                ))}
              </div>
            </div>

            <div className="rounded-md border overflow-hidden">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Metric</TableHead>
                    {selectedRuns.map((r) => (
                      <TableHead key={r.id} className="text-right">
                        {r.id.slice(-5)}
                      </TableHead>
                    ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <CompareRow label="PF" runs={selectedRuns} pick={(r) => r.pf.toFixed(2)} />
                  <CompareRow label="Win %" runs={selectedRuns} pick={(r) => r.win_rate.toFixed(1)} />
                  <CompareRow label="Expectancy" runs={selectedRuns} pick={(r) => r.expectancy.toFixed(2)} />
                  <CompareRow label="Max DD" runs={selectedRuns} pick={(r) => `${r.max_drawdown.toFixed(1)}%`} />
                  <CompareRow label="Sharpe" runs={selectedRuns} pick={(r) => r.sharpe.toFixed(2)} />
                  <CompareRow label="Trades" runs={selectedRuns} pick={(r) => r.trade_count.toString()} />
                  <CompareRow label="Net P&L" runs={selectedRuns} pick={(r) => `$${r.net_pnl.toLocaleString()}`} />
                </TableBody>
              </Table>
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Small helpers
// ─────────────────────────────────────────────────────────────────────────────

function SortHead({
  label,
  k,
  orderBy,
  orderDir,
  onClick,
  align,
}: {
  label: string;
  k: OrderBy;
  orderBy: OrderBy;
  orderDir: "asc" | "desc";
  onClick: (k: OrderBy) => void;
  align?: "right";
}) {
  const active = orderBy === k;
  return (
    <TableHead
      onClick={() => onClick(k)}
      className={cn("cursor-pointer select-none hover:text-foreground", align === "right" && "text-right")}
    >
      <span className="inline-flex items-center gap-1">
        {label}
        {active && (orderDir === "asc" ? <TrendingUp className="h-3 w-3" /> : <TrendingDown className="h-3 w-3" />)}
      </span>
    </TableHead>
  );
}

function Metric({
  label,
  value,
  positive,
  negative,
  small,
}: {
  label: string;
  value: string;
  positive?: boolean;
  negative?: boolean;
  small?: boolean;
}) {
  return (
    <div className="rounded-md border p-2.5">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div
        className={cn(
          "font-mono tabular-nums font-semibold mt-0.5",
          small ? "text-xs" : "text-lg",
          positive && "text-emerald-500",
          negative && "text-red-500",
        )}
      >
        {value}
      </div>
    </div>
  );
}

function TagEditor({ id, tags }: { id: string; tags: string[] }) {
  const mutate = useSetBacktestTags();
  // Local draft so the UI updates instantly; the server roundtrip runs in
  // the background via onSuccess invalidation. Re-sync when the source tags
  // change (e.g. selecting a different run) via the `id` key on the parent.
  const [draft, setDraft] = useState<string[]>(tags);
  const [input, setInput] = useState("");

  // When switching to a different run, reset local state.
  // `id` is the dependency; we use useEffect to avoid an effect-free render.
  useMemo(() => {
    setDraft(tags);
    setInput("");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const commit = (next: string[]) => {
    setDraft(next);
    mutate.mutate({ id, tags: next });
  };

  const addFromInput = () => {
    const parts = input
      .split(",")
      .map((s) => s.trim())
      .filter((s) => s.length > 0 && !draft.includes(s));
    if (parts.length === 0) {
      setInput("");
      return;
    }
    commit([...draft, ...parts]);
    setInput("");
  };

  const removeTag = (t: string) => {
    commit(draft.filter((x) => x !== t));
  };

  return (
    <div className="rounded-md border p-3 space-y-2">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <TagIcon className="h-3.5 w-3.5" /> Tags
        {mutate.isPending && <Loader2 className="h-3 w-3 animate-spin" />}
      </div>
      <div className="flex gap-1 flex-wrap items-center">
        {draft.map((t) => (
          <Badge key={t} variant="secondary" className="gap-1 pr-1">
            {t}
            <button
              type="button"
              onClick={() => removeTag(t)}
              className="rounded-sm hover:bg-foreground/10 p-0.5"
              aria-label={`remove tag ${t}`}
            >
              <X className="h-3 w-3" />
            </button>
          </Badge>
        ))}
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault();
              addFromInput();
            } else if (e.key === "Backspace" && input === "" && draft.length > 0) {
              e.preventDefault();
              commit(draft.slice(0, -1));
            }
          }}
          onBlur={() => {
            if (input.trim()) addFromInput();
          }}
          placeholder={draft.length === 0 ? "add tags (Enter to commit)" : "+ add"}
          className="flex-1 min-w-[140px] h-7 px-2 text-xs bg-transparent border-0 outline-none focus:ring-0 placeholder:text-muted-foreground"
        />
      </div>
      {mutate.isError && (
        <div className="text-[10px] text-red-500">save failed: {String(mutate.error)}</div>
      )}
    </div>
  );
}

function CompareRow({
  label,
  runs,
  pick,
}: {
  label: string;
  runs: BacktestRunSummary[];
  pick: (r: BacktestRunSummary) => string;
}) {
  return (
    <TableRow>
      <TableCell className="text-xs text-muted-foreground">{label}</TableCell>
      {runs.map((r) => (
        <TableCell key={r.id} className="text-right font-mono tabular-nums">
          {pick(r)}
        </TableCell>
      ))}
    </TableRow>
  );
}
