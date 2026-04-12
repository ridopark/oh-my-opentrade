type Family = {
  code: string;
  name: string;
  thesis: string;
  pf: string;
  caveat: string;
  path: string;
};

const families: Family[] = [
  {
    code: "S1",
    name: "ORB",
    thesis:
      "Opening-range breakout with volume confirmation and directional regime filters. Trades only during TREND anchors on the 15m timeframe.",
    pf: "PF 1.6 – 1.9",
    caveat: "Touch-and-go performance, regime-dependent",
    path: "M0,40 L8,35 L16,38 L24,30 L32,32 L40,24 L48,26 L56,18 L64,22 L72,12 L80,16 L88,8",
  },
  {
    code: "S2",
    name: "MACD",
    thesis:
      "MACD line/signal crossover with swing-based stops and dynamic R:R targets. Dark-pool block-flow veto plus inverted Z-score regime filter suppresses entries in losing reversal regimes.",
    pf: "PF 1.4 – 1.6",
    caveat: "Z-conditioned: blocks PF<1 regime",
    path: "M0,38 L8,34 L16,40 L24,32 L32,36 L40,28 L48,34 L56,24 L64,30 L72,20 L80,26 L88,18",
  },
  {
    code: "S3",
    name: "AVWAP",
    thesis:
      "Anchored VWAP mean reversion from 5m/15m regime extremes. Confluence-weighted entries with late-session DP Z-score gating suppress adverse-regime trades for higher Sharpe.",
    pf: "PF 1.2 – 2.1",
    caveat: "Z-gated entries, Sharpe 2.08",
    path: "M0,42 L8,38 L16,32 L24,34 L32,26 L40,28 L48,20 L56,22 L64,14 L72,16 L80,8 L88,10",
  },
  {
    code: "S0",
    name: "Overnight Z",
    thesis:
      "Daily-horizon strategy using late-session dark-pool buy ratio Z-score as a next-day reversal signal. Long equity shares at open, exit MOC. Uncorrelated with intraday strategies.",
    pf: "Staged",
    caveat: "Paper validation pending",
    path: "M0,36 L8,32 L16,36 L24,28 L32,34 L40,22 L48,30 L56,18 L64,26 L72,14 L80,20 L88,12",
  },
  {
    code: "S4",
    name: "Confluence",
    thesis:
      "Scoring layer combining technical agreement (RSI/Stoch/ADX), dark-pool block ratio, 13F whale accumulation, and late-session Z-score conditioning. Gates all strategies above.",
    pf: "0–100 score",
    caveat: "Informational signal, most durable",
    path: "M0,36 L8,30 L16,34 L24,26 L32,28 L40,20 L48,22 L56,14 L64,18 L72,10 L80,12 L88,6",
  },
];

function Sparkline({ path }: { path: string }) {
  return (
    <svg
      viewBox="0 0 88 48"
      className="w-full h-16 mt-6"
      preserveAspectRatio="none"
      aria-hidden="true"
    >
      <path d={path} fill="none" stroke="var(--spectral-white)" strokeOpacity="0.4" strokeWidth="1" />
      <path d={path} fill="none" stroke="var(--signal-cyan)" strokeOpacity="0.9" strokeWidth="0.5" />
    </svg>
  );
}

export function StrategyFamilies() {
  return (
    <section id="strategies" className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">STRATEGY FAMILIES</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          Five signals.<br />One confluence gate.
        </h2>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-5 gap-10">
        {families.map((f) => (
          <div key={f.name} className="border-t landing-hairline pt-8">
            <div className="flex items-baseline justify-between">
              <p className="landing-label text-[var(--spectral-white)]/50">{f.code}</p>
              <p className="landing-label landing-cyan">{f.pf}</p>
            </div>
            <h3 className="landing-display text-4xl mt-4">{f.name}</h3>
            <p className="landing-body mt-4 text-sm">{f.thesis}</p>
            <Sparkline path={f.path} />
            <p className="landing-label mt-4 text-[var(--spectral-white)]/50">{f.caveat}</p>
          </div>
        ))}
      </div>
    </section>
  );
}
