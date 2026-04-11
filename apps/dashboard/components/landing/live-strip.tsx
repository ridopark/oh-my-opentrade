const stats = [
  { value: "3", label: "LIVE STRATEGIES" },
  { value: "34", label: "ACTIVE SYMBOLS" },
  { value: "8", label: "EXECUTION GATES" },
  { value: "<30s", label: "CRASH RECOVERY" },
];

export function LiveStrip() {
  return (
    <section className="border-y landing-hairline">
      <div className="grid grid-cols-2 md:grid-cols-4">
        {stats.map((s, i) => (
          <div
            key={s.label}
            className={`px-6 md:px-10 py-12 md:py-16 ${
              i > 0 ? "md:border-l landing-hairline" : ""
            } ${i === 1 ? "border-l landing-hairline md:border-l" : ""} ${
              i >= 2 ? "border-t landing-hairline md:border-t-0" : ""
            }`}
          >
            <div className="landing-display text-[clamp(2.5rem,5vw,4.5rem)] landing-cyan">
              {s.value}
            </div>
            <div className="landing-label mt-3 text-[var(--spectral-white)]/60">{s.label}</div>
          </div>
        ))}
      </div>
    </section>
  );
}
