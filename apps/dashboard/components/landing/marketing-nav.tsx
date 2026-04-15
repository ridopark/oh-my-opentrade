import Link from "next/link";

export function MarketingNav() {
  return (
    <nav className="fixed top-0 left-0 right-0 z-50 flex items-center justify-between px-6 md:px-12 py-6">
      <Link href="/" className="landing-label tracking-[0.2em] text-[var(--spectral-white)]">
        OH-MY-OPENTRADE
      </Link>
      <div className="hidden md:flex items-center gap-8">
        <a href="#capabilities" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
          CAPABILITIES
        </a>
        <a href="#ai-edge" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
          AI EDGE
        </a>
        <a href="#strategies" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
          STRATEGIES
        </a>
        <a href="#architecture" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
          ARCHITECTURE
        </a>
        <a href="#roadmap" className="landing-label hover:text-[var(--signal-cyan)] transition-colors">
          ROADMAP
        </a>
      </div>
      <Link href="/signals" className="landing-ghost-btn" data-variant="cyan">
        OPEN DASHBOARD
      </Link>
    </nav>
  );
}
