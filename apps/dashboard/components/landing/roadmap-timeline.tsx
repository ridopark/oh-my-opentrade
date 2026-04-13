const columns = [
  {
    phase: "SHIPPED",
    kicker: "SPRINT 1 – 3",
    items: [
      "Panic recovery + feed-age watchdog",
      "Write-ahead order journal",
      "Startup broker reconciliation",
      "IBKR broker adapter (paper)",
      "Dark-pool 5m bar pipeline + late-session Z-score",
      "DoltHub historical options + Finnhub earnings calendar",
      "Dynamic IV adjustments (VIX-beta, time-of-day, earnings ramp)",
      "86% backtest performance optimization (5-phase sprint)",
      "Per-strategy Z conditioning (AVWAP + MACD)",
      "Overnight Z-Score Bias strategy (staged)",
    ],
  },
  {
    phase: "IN PROGRESS",
    kicker: "SPRINT 3.5 – 4",
    items: [
      "Journal flag removal (pending 24h validation gate)",
      "Overnight Z paper validation (20+ trades)",
      "Risk management gates (portfolio heat, sector limits)",
      "3-state kill switch (ACTIVE / HALTED / REDUCING)",
    ],
  },
  {
    phase: "NEXT",
    kicker: "SPRINT 5 – 7",
    items: [
      "IBKR BAG combo options execution",
      "Signal quality (block filter, regime weighting)",
      "Pluggable backtest fill models",
      "Intrabar stop decomposition + fee modeling",
    ],
  },
];

export function RoadmapTimeline() {
  return (
    <section id="roadmap" className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">ROADMAP</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          Shipping in<br />sprints.
        </h2>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-12">
        {columns.map((col) => (
          <div key={col.phase} className="border-t landing-hairline pt-8">
            <p className="landing-label landing-cyan">{col.phase}</p>
            <p className="landing-label mt-2 text-[var(--spectral-white)]/40">{col.kicker}</p>
            <ul className="mt-8 space-y-5">
              {col.items.map((it) => (
                <li key={it} className="flex gap-4">
                  <span className="h-px w-4 mt-[0.7rem] shrink-0 bg-[var(--signal-cyan)]" />
                  <span className="landing-body text-sm">{it}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </section>
  );
}
