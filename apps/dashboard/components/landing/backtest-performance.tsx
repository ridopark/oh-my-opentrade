const stats = [
  { label: "30 SYM / 1 YEAR", before: "~130s", after: "18s", pct: "86%" },
  { label: "8 SYM / 1 YEAR", before: "31.6s", after: "5.0s", pct: "84%" },
  { label: "8 SYM / 3 MONTHS", before: "10.1s", after: "1.9s", pct: "81%" },
];

const phases = [
  {
    n: "P1",
    title: "Direct Dispatch",
    body: "Replaced the pub/sub event bus with direct function calls on the backtest hot path. No event wrapping, no routing, no interface boxing.",
    result: "130s \u2192 30s",
  },
  {
    n: "P2",
    title: "Shard Infrastructure",
    body: "Per-symbol sharding of monitor and runner services into isolated shard-owned state. Worker pool with zero shared mutable data.",
    result: "Groundwork for P3",
  },
  {
    n: "P3",
    title: "Slice-to-Completion",
    body: "Each shard runs its entire bar stream to completion with no per-tick synchronization. K-way merge replays signals in tick order. Eliminated 240k barrier wakeups.",
    result: "30s \u2192 19s",
  },
  {
    n: "P4",
    title: "Allocation Reduction",
    body: "Typed hot paths bypass Event construction. Pre-sized slabs, zero-alloc ingestion, eliminated 9 GB of per-run garbage. GC dropped from 12s to 8s of CPU.",
    result: "19 GB \u2192 9.7 GB allocs",
  },
];

function BarChart({ pct }: { pct: string }) {
  const width = parseInt(pct);
  return (
    <div className="relative h-2 w-full mt-3 overflow-hidden" style={{ background: "rgba(240,240,250,0.06)" }}>
      <div
        className="absolute inset-y-0 left-0"
        style={{
          width: `${width}%`,
          background: "linear-gradient(90deg, var(--signal-cyan), rgba(95,184,255,0.4))",
        }}
      />
    </div>
  );
}

export function BacktestPerformance() {
  return (
    <section id="backtest" className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">BACKTEST ENGINE</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          86% faster.<br />Same deterministic output.
        </h2>
        <p className="landing-body mt-8 max-w-2xl">
          Five-phase optimization sprint took a 30-symbol, 1-year backtest from over two minutes
          to under twenty seconds. Every phase was gated by a benchmark &mdash; we only moved forward
          when the previous phase wasn&apos;t enough.
        </p>
      </div>

      {/* Stats strip */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-10 mb-20">
        {stats.map((s) => (
          <div key={s.label} className="border-t landing-hairline pt-8">
            <p className="landing-label text-[var(--spectral-white)]/50">{s.label}</p>
            <div className="flex items-baseline gap-3 mt-4">
              <span className="landing-display text-4xl md:text-5xl landing-cyan">{s.after}</span>
              <span className="landing-label text-[var(--spectral-white)]/30 line-through">{s.before}</span>
            </div>
            <BarChart pct={s.pct} />
            <p className="landing-label mt-2 landing-cyan">&minus;{s.pct} WALL TIME</p>
          </div>
        ))}
      </div>

      {/* Phase breakdown */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-10">
        {phases.map((p) => (
          <div key={p.n} className="border-t landing-hairline pt-6">
            <div className="flex items-baseline gap-4">
              <span className="landing-label landing-cyan">{p.n}</span>
              <h3 className="landing-display text-lg">{p.title}</h3>
            </div>
            <p className="landing-body mt-3 text-sm">{p.body}</p>
            <p className="landing-label mt-4 text-[var(--spectral-white)]/60">{p.result}</p>
          </div>
        ))}
      </div>
    </section>
  );
}
