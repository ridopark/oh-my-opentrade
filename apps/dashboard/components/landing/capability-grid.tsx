const columns = [
  {
    kicker: "01 / DATA",
    title: "Multi-Source Intake",
    body:
      "Real-time 1m/5m/15m bars from Alpaca WebSocket with 4-sigma sanitization. Dark-pool aggregation from SIP prints. Whale accumulation scoring from SEC 13F filings. Historical options chains sourced from DoltHub.",
    items: ["Alpaca WebSocket + REST", "SEC 13F whale filings", "DoltHub options history", "YahooFinance fallback"],
  },
  {
    kicker: "02 / STRATEGY",
    title: "Deterministic Engines",
    body:
      "Three live strategies run as plain Go code on the hot path. Multi-timeframe regime detection (5m/15m anchors) gates 1m entries. Confluence scoring combines technical, dark-pool, and institutional signals before a single order is emitted.",
    items: ["ORB opening-range breakout", "AVWAP anchored mean reversion", "MACD crossover with dark-pool veto", "Confluence-gated entries"],
  },
  {
    kicker: "03 / EXECUTION",
    title: "Crash-Resilient Orders",
    body:
      "Every order intent is journaled before the broker sees it. Eight execution gates enforce risk, exposure, slippage, and trading windows. Startup reconciliation resumes open orders from the journal instead of blindly cancelling protective stops.",
    items: ["Write-ahead order journal", "8-gate risk chain", "Alpaca + IBKR adapters", "SystemD watchdog"],
  },
];

export function CapabilityGrid() {
  return (
    <section id="capabilities" className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">WHAT OMO DOES</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          Three layers.<br />One single binary.
        </h2>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-12 md:gap-16">
        {columns.map((col) => (
          <div key={col.kicker} className="border-t landing-hairline pt-8">
            <p className="landing-label text-[var(--spectral-white)]/50">{col.kicker}</p>
            <h3 className="landing-display text-2xl md:text-3xl mt-4">{col.title}</h3>
            <p className="landing-body mt-6">{col.body}</p>
            <ul className="mt-8 space-y-3">
              {col.items.map((it) => (
                <li key={it} className="flex items-center gap-3">
                  <span className="h-px w-4 bg-[var(--signal-cyan)]" />
                  <span className="landing-label text-[var(--spectral-white)]/80">{it}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </section>
  );
}
