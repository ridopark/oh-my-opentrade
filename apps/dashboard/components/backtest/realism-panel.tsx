"use client";

import type { RealismEstimate, RealismFlagLevel } from "@/lib/use-backtest";

function formatCurrency(n: number): string {
  if (n >= 1000 || n <= -1000) {
    return `$${(n / 1000).toFixed(1)}k`;
  }
  return `$${n.toFixed(0)}`;
}

const flagColor: Record<RealismFlagLevel, string> = {
  red: "border-red-500/40 bg-red-500/10 text-red-300",
  yellow: "border-amber-500/40 bg-amber-500/10 text-amber-300",
  green: "border-emerald-500/40 bg-emerald-500/10 text-emerald-300",
};

const flagDot: Record<RealismFlagLevel, string> = {
  red: "bg-red-400",
  yellow: "bg-amber-400",
  green: "bg-emerald-400",
};

interface Props {
  realism: RealismEstimate;
  backtestPnL: number;
}

export function RealismPanel({ realism, backtestPnL }: Props) {
  const rows: Array<{ label: string; backtest: string; live: string; hint?: string }> = [
    {
      label: "Sharpe",
      backtest: "",
      live: realism.live_sharpe > 0 ? realism.live_sharpe.toFixed(2) : "n/a",
      hint: "backtest / 2.8",
    },
    {
      label: "Max Drawdown",
      backtest: "",
      live: `${realism.live_dd_pct.toFixed(2)}%`,
      hint: "backtest × 1.8",
    },
    {
      label: "Profit Factor",
      backtest: "",
      live: realism.live_pf.toFixed(2),
      hint: "1 + (pf−1) / 1.5",
    },
    {
      label: "PnL (fixed-notional)",
      backtest: formatCurrency(backtestPnL),
      live: formatCurrency(realism.fixed_notional_pnl),
      hint: "compounding removed",
    },
    {
      label: "PnL (realistic compounded)",
      backtest: "",
      live: formatCurrency(realism.compounded_pnl_estimate),
      hint: "fixed × 1.5",
    },
    {
      label: "Compounding ramp",
      backtest: `${realism.compounding_ramp.toFixed(2)}×`,
      live: "",
      hint: "final / initial equity",
    },
  ];

  return (
    <div className="mt-4 rounded-lg border border-border bg-card/50 p-4">
      <div className="flex items-center justify-between mb-3">
        <div>
          <div className="text-[11px] uppercase tracking-wider text-muted-foreground">Realistic Live Expectations</div>
          <div className="text-xs text-muted-foreground mt-0.5">
            Deflated estimates derived from the backtest. Not a forecast.
          </div>
        </div>
      </div>

      <div className="grid grid-cols-3 gap-3 mb-3">
        {rows.map((row) => (
          <div key={row.label} className="rounded border border-border/50 px-3 py-2 bg-background/30">
            <div className="text-[10px] text-muted-foreground uppercase tracking-wider">{row.label}</div>
            <div className="text-sm font-mono font-medium text-foreground mt-1">
              {row.live || row.backtest}
            </div>
            {row.hint ? <div className="text-[9px] text-muted-foreground mt-0.5">{row.hint}</div> : null}
          </div>
        ))}
      </div>

      {realism.flags.length > 0 ? (
        <div className="space-y-1.5">
          {realism.flags.map((flag, idx) => (
            <div
              key={idx}
              className={`flex items-start gap-2 rounded border px-3 py-1.5 text-xs ${flagColor[flag.level]}`}
            >
              <span className={`mt-1 h-1.5 w-1.5 shrink-0 rounded-full ${flagDot[flag.level]}`} aria-hidden />
              <div>
                <span className="font-mono text-[10px] uppercase tracking-wider opacity-70">{flag.metric}</span>
                <div className="leading-snug mt-0.5">{flag.message}</div>
              </div>
            </div>
          ))}
        </div>
      ) : null}

      <div className="text-[10px] text-muted-foreground italic mt-3">{realism.disclaimer}</div>
    </div>
  );
}
