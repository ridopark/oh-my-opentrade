import Link from "next/link";

export function MinimalFooter() {
  return (
    <footer className="px-6 md:px-16 py-16 border-t landing-hairline">
      <div className="flex flex-col md:flex-row md:items-end md:justify-between gap-10">
        <div>
          <p className="landing-label landing-cyan">OH-MY-OPENTRADE</p>
          <p className="landing-display mt-4 text-2xl max-w-lg">
            Algorithmic trading, built like infrastructure.
          </p>
        </div>
        <div className="flex flex-wrap gap-8">
          <Link href="/signals" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
            DASHBOARD
          </Link>
          <a
            href="https://github.com/ridopark/oh-my-opentrade"
            target="_blank"
            rel="noreferrer"
            className="landing-label hover:text-[var(--signal-cyan)] transition-colors"
          >
            GITHUB
          </a>
          <Link href="/services" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
            STATUS
          </Link>
        </div>
      </div>
      <div className="mt-16 pt-6 border-t landing-hairline flex flex-col md:flex-row justify-between gap-4">
        <p className="landing-label text-[var(--spectral-white)]/40">
          PAPER TRADING STABLE / LIVE VALIDATION IN PROGRESS
        </p>
        <p className="landing-label text-[var(--spectral-white)]/40">2026</p>
      </div>
    </footer>
  );
}
