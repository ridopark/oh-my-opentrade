const columns = [
  {
    phase: "SHIPPED",
    kicker: "SPRINT 1 – 4",
    items: [
      "Panic recovery + feed-age watchdog",
      "Write-ahead order journal",
      "Startup broker reconciliation",
      "IBKR broker adapter (paper)",
      "Dark-pool 5m bar pipeline",
      "Late-session DP Z-score indicator",
      "Per-strategy Z conditioning (AVWAP + MACD)",
      "Overnight Z-Score Bias strategy (staged)",
    ],
  },
  {
    phase: "IN PROGRESS",
    kicker: "SPRINT 4.5 – 5",
    items: [
      "Overnight Z paper validation (20+ trades)",
      "Portfolio heat metric",
      "Sector/industry exposure limits",
      "Directional bias cap",
      "3-state kill switch",
    ],
  },
  {
    phase: "NEXT",
    kicker: "SPRINT 6 – 8",
    items: [
      "IBKR BAG combo options execution",
      "13F + options skew confirmation (S3)",
      "Options vol-spread informed trading proxy",
      "Pluggable backtest fill models",
      "Intrabar stop decomposition",
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
