const pillars = [
  {
    n: "01",
    title: "Write-Ahead Order Journal",
    body:
      "Every order intent is persisted to PostgreSQL before the broker API is called. A crash mid-submission leaves a recoverable trail, not orphaned state.",
  },
  {
    n: "02",
    title: "Startup Reconciliation",
    body:
      "On boot, open broker orders are matched against the journal before any new decision. Protective stops are never blindly cancelled.",
  },
  {
    n: "03",
    title: "SystemD Watchdog",
    body:
      "A heartbeat from the main loop keeps the process alive. Hangs are detected and auto-restarted within 30 seconds.",
  },
  {
    n: "04",
    title: "Kill Switch",
    body:
      "Feed-age monitoring, Discord escalation, and a three-state ACTIVE/HALTED/REDUCING switch (Sprint 4) take the system offline cleanly on anomaly.",
  },
];

export function ReliabilitySection() {
  return (
    <section className="landing-section border-b landing-hairline">
      <div className="mb-16">
        <p className="landing-label landing-cyan">RELIABILITY</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          Survives crashes.<br />Resumes cleanly.
        </h2>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-5 gap-10 items-start">
        <div className="lg:col-span-3 border landing-hairline aspect-video relative overflow-hidden">
          {/* Placeholder for dashboard screencast. Replace /public/landing/dashboard-loop.mp4 */}
          <div className="absolute inset-0 flex items-center justify-center">
            <div className="text-center">
              <div className="landing-label landing-cyan">LIVE DASHBOARD LOOP</div>
              <div className="landing-label mt-3 text-[var(--spectral-white)]/40">
                /public/landing/dashboard-loop.mp4
              </div>
            </div>
          </div>
          <div
            aria-hidden="true"
            className="absolute inset-0"
            style={{
              background:
                "radial-gradient(ellipse at center, rgba(95,184,255,0.08), transparent 70%)",
            }}
          />
          <div
            aria-hidden="true"
            className="absolute inset-0 opacity-[0.06]"
            style={{
              backgroundImage:
                "linear-gradient(rgba(240,240,250,0.3) 1px, transparent 1px), linear-gradient(90deg, rgba(240,240,250,0.3) 1px, transparent 1px)",
              backgroundSize: "24px 24px",
            }}
          />
        </div>

        <div className="lg:col-span-2 space-y-8">
          {pillars.map((p) => (
            <div key={p.n} className="border-t landing-hairline pt-5">
              <div className="flex items-baseline gap-4">
                <span className="landing-label landing-cyan">{p.n}</span>
                <h3 className="landing-display text-lg">{p.title}</h3>
              </div>
              <p className="landing-body mt-3 text-sm">{p.body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
