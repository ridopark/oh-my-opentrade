const pillars = [
  {
    n: "01",
    title: "Spread-Aware Exit Pricing",
    body:
      "Option exits read live bid/ask and price the limit at mid − k·spread, where k scales with days-to-expiry (0.25 ≥14d, 0.35 5-14d, 0.45 <5d). Near-expiry gamma makes mid stale faster, so the limit gives up more spread to clear. A blown-spread guard (spread/mid > 25%) falls back to a fixed-bps cap when the quote is garbage.",
  },
  {
    n: "02",
    title: "Asymmetric Timeout + Re-Peg",
    body:
      "Stops timeout fast (10s, 1 re-peg) because protecting capital matters. Targets get 30s and up to 3 re-pegs that tighten toward mid by one tick each attempt. A 120s absolute wall-time guard and a no-re-peg-in-the-last-15-minutes rule keep EOD liquidation deterministic.",
  },
  {
    n: "03",
    title: "Cancel-Await-Terminal",
    body:
      "Before any retry or escalation fires, the broker cancel is awaited to terminal status and the exit-pending gate is cleared only after confirmation. Eliminates the cancel/resubmit race where the next tick resubmits before the first order has actually terminated, which previously produced no_position_to_exit rejections and forced dust-sweep fallback.",
  },
  {
    n: "04",
    title: "Marketable-Limit Dust Sweep",
    body:
      "The last-resort sweep is no longer a pure market order. It submits a marketable limit at max(bid − tick, bid·(1 − 150bps)) with a spread-adaptive floor, then falls back to true market after 15s if unfilled. Halt detection (bid==0) and near-close override skip the limit phase to avoid OCC exercise-by-exception on 0DTE ITM contracts.",
  },
  {
    n: "05",
    title: "Attribution Without Ledger Rewrites",
    body:
      "Dust-sweep fills keep Strategy=\"dust_sweep\" on the raw ledger row (SEC 17a-4 / FINRA 4511 immutability), but the originating strategy is threaded through the rationale and the FillReceived event payload. Per-strategy P&L reports credit the origin strategy; audit queries still see the raw broker-authoritative record. Circuit-breaker counts one exit attempt as one unit, so re-pegs don't inflate failures 4x.",
  },
];

export function SlippageSection() {
  return (
    <section className="landing-section border-b landing-hairline">
      <div className="mb-16">
        <p className="landing-label landing-cyan">SLIPPAGE CONTROL</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          The price you see<br />is the price you get.
        </h2>
        <p className="landing-body mt-6 max-w-3xl">
          Options exits used to price blindly at a flat 5% below estimated mid. On a $1.80
          contract with a $0.10 spread, that landed below the bid — guaranteed non-fill,
          forced dust-sweep at market, and attribution drift into the wrong strategy bucket.
          The exit pricer now consumes live bid/ask, scales aggression with DTE, and awaits
          broker terminal status before any retry. Capital that used to leak on every
          profit-taking fill stays in the account.
        </p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-x-12 gap-y-10">
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
    </section>
  );
}
