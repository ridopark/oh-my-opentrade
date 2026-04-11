import Link from "next/link";
import { CandlestickCanvas } from "./candlestick-canvas";

export function Hero() {
  return (
    <section className="relative h-screen min-h-[720px] w-full overflow-hidden">
      <CandlestickCanvas />
      <div
        aria-hidden="true"
        className="absolute inset-0"
        style={{
          background:
            "linear-gradient(180deg, rgba(0,0,0,0.85) 0%, rgba(0,0,0,0.35) 40%, rgba(0,0,0,0.8) 100%)",
        }}
      />
      <div className="relative z-10 h-full flex flex-col justify-end px-6 md:px-16 pb-24 md:pb-32">
        <p className="landing-label landing-cyan mb-6">ALGORITHMIC TRADING INFRASTRUCTURE</p>
        <h1 className="landing-display text-[clamp(2.5rem,7vw,6rem)] max-w-5xl">
          Built like<br />infrastructure.<br />
          <span className="text-[var(--spectral-white)]/60">Traded like software.</span>
        </h1>
        <p className="landing-body mt-8 max-w-2xl">
          Hexagonal Go core. Dark-pool and whale-accumulation confluence. Persistent order journal
          that survives crashes. Broker-agnostic execution across Alpaca and Interactive Brokers.
        </p>
        <div className="mt-10 flex flex-wrap gap-4">
          <Link href="/signals" className="landing-ghost-btn" data-variant="cyan">
            OPEN DASHBOARD
          </Link>
          <a
            href="https://github.com/ridopark/oh-my-opentrade"
            className="landing-ghost-btn"
            target="_blank"
            rel="noreferrer"
          >
            READ THE DOCS
          </a>
        </div>
      </div>
      <div className="absolute bottom-6 left-0 right-0 flex justify-center pointer-events-none z-10">
        <span className="landing-label text-[var(--spectral-white)]/50">SCROLL</span>
      </div>
    </section>
  );
}
