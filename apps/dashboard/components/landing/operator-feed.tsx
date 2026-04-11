type Message = {
  channel: string;
  title: string;
  timestamp: string;
  body: string;
  tone?: "default" | "alert" | "success";
};

const messages: Message[] = [
  {
    channel: "#omo-lifecycle",
    title: "STRATEGY ACTIVATED",
    timestamp: "09:28:04 ET",
    body: "ORB v1 (paper) online · 34 symbols · 15m anchors loaded · confluence gate armed.",
    tone: "default",
  },
  {
    channel: "#omo-orders",
    title: "ORDER FILLED",
    timestamp: "10:12:47 ET",
    body: "AAPL LONG 100 @ 187.42 · strategy AVWAP_v1 · risk $210 · stop 185.32 · target 191.61",
    tone: "success",
  },
  {
    channel: "#omo-alerts",
    title: "IBKR GATEWAY OFFLINE",
    timestamp: "11:04:19 ET",
    body: "Feed age 182s · 2 reconnect attempts failed · escalating to ops channel.",
    tone: "alert",
  },
  {
    channel: "#omo-alerts",
    title: "GATEWAY RESTORED",
    timestamp: "11:06:52 ET",
    body: "IBKR reconnected · 3 open orders reconciled from journal · no state loss.",
    tone: "success",
  },
  {
    channel: "#omo-daily",
    title: "SESSION CLOSE",
    timestamp: "16:00:00 ET",
    body: "12 trades · win rate 66.7% · profit factor 1.72 · realized +$240.18 · max DD -$58.40",
    tone: "default",
  },
];

const toneColor: Record<NonNullable<Message["tone"]>, string> = {
  default: "var(--signal-cyan)",
  success: "rgba(120, 220, 160, 0.85)",
  alert: "rgba(255, 170, 90, 0.9)",
};

function EmbedCard({ m }: { m: Message }) {
  const accent = toneColor[m.tone ?? "default"];
  return (
    <div
      className="relative flex gap-4 border landing-hairline bg-black/40 backdrop-blur-sm"
      style={{ padding: "18px 20px 18px 16px" }}
    >
      <span
        aria-hidden="true"
        className="absolute left-0 top-0 bottom-0 w-[3px]"
        style={{ background: accent }}
      />
      <div className="flex-1 min-w-0 pl-2">
        <div className="flex items-baseline justify-between gap-4">
          <span className="landing-label" style={{ color: accent }}>
            {m.title}
          </span>
          <span className="landing-label text-[var(--spectral-white)]/40 text-[0.6rem] shrink-0">
            {m.timestamp}
          </span>
        </div>
        <p className="mt-2 text-xs md:text-[0.8125rem] font-mono text-[var(--spectral-white)]/80 leading-relaxed">
          {m.body}
        </p>
        <p className="mt-3 landing-label text-[var(--spectral-white)]/30 text-[0.6rem]">
          {m.channel}
        </p>
      </div>
    </div>
  );
}

export function OperatorFeed() {
  return (
    <section className="landing-section border-b landing-hairline">
      <div className="mb-16">
        <p className="landing-label landing-cyan">OPERATOR FEED</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          The system<br />talks back.
        </h2>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-5 gap-12 lg:gap-16 items-start">
        <div className="lg:col-span-2">
          <p className="landing-body max-w-md">
            Every order, every strategy lifecycle event, every reconnect escalation, and every
            end-of-day summary is piped to Discord in real time. The system tells you what it
            is doing, what it is seeing, and when something goes wrong — without you having
            to watch a dashboard.
          </p>
          <ul className="mt-10 space-y-4">
            <li className="flex gap-3">
              <span className="h-px w-5 mt-[0.7rem] shrink-0 bg-[var(--signal-cyan)]" />
              <div>
                <p className="landing-label">ORDER LIFECYCLE</p>
                <p className="landing-body text-xs mt-1">
                  Entry, partial fill, full fill, stop hit, target hit.
                </p>
              </div>
            </li>
            <li className="flex gap-3">
              <span className="h-px w-5 mt-[0.7rem] shrink-0 bg-[var(--signal-cyan)]" />
              <div>
                <p className="landing-label">STRATEGY STATE</p>
                <p className="landing-body text-xs mt-1">
                  Activation, configuration swap, blue/green handoff, deactivation.
                </p>
              </div>
            </li>
            <li className="flex gap-3">
              <span className="h-px w-5 mt-[0.7rem] shrink-0 bg-[var(--signal-cyan)]" />
              <div>
                <p className="landing-label">RECONNECT ESCALATION</p>
                <p className="landing-body text-xs mt-1">
                  Feed-age breaches, broker disconnects, 3m alert + 1h auto-halt.
                </p>
              </div>
            </li>
            <li className="flex gap-3">
              <span className="h-px w-5 mt-[0.7rem] shrink-0 bg-[var(--signal-cyan)]" />
              <div>
                <p className="landing-label">DAILY SUMMARY</p>
                <p className="landing-body text-xs mt-1">
                  Session close report with P&L, PF, drawdown, attribution chart.
                </p>
              </div>
            </li>
            <li className="flex gap-3">
              <span className="h-px w-5 mt-[0.7rem] shrink-0 bg-[var(--spectral-white)]/30" />
              <div>
                <p className="landing-label text-[var(--spectral-white)]/50">DEPLOY FEED (OPTIONAL)</p>
                <p className="landing-body text-xs mt-1">
                  Commit and release notifications via GitHub Actions webhook.
                </p>
              </div>
            </li>
          </ul>
        </div>

        <div className="lg:col-span-3">
          <div className="flex flex-col gap-3">
            {messages.map((m) => (
              <EmbedCard key={m.title + m.timestamp} m={m} />
            ))}
          </div>
          <p className="mt-6 landing-label text-[var(--spectral-white)]/30 text-right">
            ILLUSTRATIVE · WEBHOOKS CONFIGURED PER TENANT
          </p>
        </div>
      </div>
    </section>
  );
}
