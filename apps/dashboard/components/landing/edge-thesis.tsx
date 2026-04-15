const layers = [
  {
    code: "EDGE / 01",
    title: "Liquidity Sweeps",
    body:
      "Exchanges rotate through obvious stop levels before reversing — that is structural, not noise. Trading with the sweep, not against it, captures the asymmetric move funded by retail stops getting hit.",
    tag: "Inducement detector",
  },
  {
    code: "EDGE / 02",
    title: "Dark-Pool Footprint",
    body:
      "Institutions split large orders to hide intent, but the SIP tape leaks the residue. Late-session Z-score on dark-pool prints flags accumulation or distribution that quote-tape watchers cannot see in real time.",
    tag: "Alpaca SIP backfill",
  },
  {
    code: "EDGE / 03",
    title: "Whale Position Lag",
    body:
      "13F filings are public but stale. Combined with current dark-pool flow they approximate real-time institutional positioning — a leading indicator that retail price-action traders systematically miss.",
    tag: "SEC 13F overlay",
  },
  {
    code: "EDGE / 04",
    title: "Time-of-Day Structure",
    body:
      "The opening drive, lunch chop, and closing auction have predictable structural flow from MOC orders, index rebalances, and gamma hedging. Graduated session weighting upweights conviction in those windows and damps it in noise.",
    tag: "Session-time weighting",
  },
];

export function EdgeThesis() {
  return (
    <section id="edge" className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">HOW IT MAKES MONEY</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-5xl">
          The trigger is not the edge.<br />
          <span className="text-[var(--spectral-white)]/60">
            The confluence is.
          </span>
        </h2>
        <p className="landing-body mt-8 max-w-3xl">
          Standard entry patterns — VWAP reclaim, MACD cross — are crowded and
          mean-revert to zero alpha once enough capital trades them. OMO uses
          those patterns as a <em>filter</em>, then stacks four independent data
          sources retail cannot replicate. A trade only fires when the layers
          agree. Edge comes from confluence; confidence from disagreement
          surface.
        </p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-12 md:gap-16">
        {layers.map((l) => (
          <div key={l.code} className="border-t landing-hairline pt-8">
            <p className="landing-label text-[var(--spectral-white)]/50">{l.code}</p>
            <h3 className="landing-display text-2xl md:text-3xl mt-4">{l.title}</h3>
            <p className="landing-body mt-6">{l.body}</p>
            <div className="mt-8 flex items-center gap-3">
              <span className="h-px w-4 bg-[var(--signal-cyan)]" />
              <span className="landing-label text-[var(--spectral-white)]/80">
                {l.tag}
              </span>
            </div>
          </div>
        ))}
      </div>

      <div className="mt-20 border-t landing-hairline pt-10 grid grid-cols-1 md:grid-cols-3 gap-10">
        <div>
          <p className="landing-label landing-cyan">PROFIT EQUATION</p>
          <pre className="mt-4 text-xs leading-relaxed text-[var(--spectral-white)]/80 font-mono whitespace-pre-wrap">
{`edge × frequency
  − slippage
  − commissions
  − taxes
  > 0`}
          </pre>
          <p className="mt-4 landing-body text-sm text-[var(--spectral-white)]/70">
            Confluence layering pushes per-trade
            win-rate × R-multiple high enough that
            modest frequency clears the cost floor.
          </p>
        </div>
        <div>
          <p className="landing-label landing-cyan">CONFLUENCE GATE</p>
          <ul className="mt-4 space-y-2 landing-label text-[var(--spectral-white)]/80">
            <li>price-action trigger</li>
            <li>session-time weight</li>
            <li>liquidity-sweep confirm</li>
            <li>dark-pool z-score align</li>
            <li>13F accumulation overlay</li>
            <li>regime tide (SPY / QQQ)</li>
          </ul>
          <p className="mt-4 landing-body text-sm text-[var(--spectral-white)]/70">
            Every blocked bar surfaces the failing
            gate in the live liveness panel — no
            silent suppressions.
          </p>
        </div>
        <div>
          <p className="landing-label landing-cyan">HONEST FRAMING</p>
          <p className="mt-4 landing-body text-sm">
            Most retail bots lose money. The thesis here only holds if the
            confluence sources stay independent, backtest realism layers
            (DoltHub bid/ask, IV crush, slippage guard) don't lie, and live
            execution slippage stays inside the modeled budget. The liveness
            telemetry is what proves (or disproves) that day-to-day.
          </p>
        </div>
      </div>
    </section>
  );
}
