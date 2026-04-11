const limits = [
  {
    n: "01",
    title: "Options backtests run ±20% optimistic",
    body:
      "Black-Scholes pricing assumes entry IV persists through exit and ignores bid-ask spread and theta bleed. Realistic fill models queued for Sprint 7.",
  },
  {
    n: "02",
    title: "IBKR live execution is unvalidated",
    body:
      "The IBKR adapter is fully implemented and runs in paper mode. Live validation on a funded account is pending.",
  },
  {
    n: "03",
    title: "Universe is 34 hardcoded symbols",
    body:
      "Adding a symbol currently requires a code redeploy. Dynamic symbol discovery is not yet in the roadmap.",
  },
  {
    n: "04",
    title: "Single-broker dependency during outages",
    body:
      "Alpaca outages halt the system; multi-broker failover is not architected yet. Paper testing has been stable across multiple sessions.",
  },
];

export function LimitationsSection() {
  return (
    <section className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">HONESTY</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          What omo is<br />not yet.
        </h2>
        <p className="landing-body mt-8 max-w-2xl">
          Every trading system has sharp edges. These are ours, in writing, before you find them.
        </p>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-10">
        {limits.map((l) => (
          <div key={l.n} className="border-t landing-hairline pt-6">
            <div className="flex items-baseline gap-4">
              <span className="landing-label text-[var(--spectral-white)]/40">{l.n}</span>
              <h3 className="landing-display text-xl">{l.title}</h3>
            </div>
            <p className="landing-body mt-4 text-sm">{l.body}</p>
          </div>
        ))}
      </div>
    </section>
  );
}
