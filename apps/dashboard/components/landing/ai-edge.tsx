const pillars = [
  {
    code: "AI / 01",
    title: "Bull / Bear / Judge Debate",
    body:
      "Every entry signal is stress-tested by a structured adversarial debate. A Bull argues the thesis, a Bear argues the opposing case, and a Judge returns a JSON verdict: direction, confidence (0-1), risk modifier (TIGHT | NORMAL | WIDE), and rationale. The Judge can veto, tighten, or widen risk.",
    items: [
      "Adversarial multi-role prompt",
      "JSON-structured verdict",
      "Veto + graduated risk modifier",
    ],
  },
  {
    code: "AI / 02",
    title: "LLM Anchor Selection",
    body:
      "Anchored-VWAP candidates — swing highs, swing lows, volume rotations, weekly opens — are ranked by an LLM with confidence scores before the numerical engine computes the band. The LLM picks which anchor matters; the deterministic engine decides when to fire.",
    items: [
      "Swing + rotation + session anchors",
      "Confidence-weighted ranking",
      "Feeds deterministic AVWAP engine",
    ],
  },
  {
    code: "AI / 03",
    title: "Provider-Agnostic Port",
    body:
      "LLM access lives behind a hexagonal port. OpenAI-compatible endpoint works with OpenRouter, Anthropic, Ollama, or vLLM — swap providers with a config change. Context injection attaches option chains, news, and strategy performance without coupling the debate to any strategy.",
    items: [
      "OpenRouter + Anthropic + local vLLM",
      "Functional context options",
      "News + options + performance inject",
    ],
  },
  {
    code: "AI / 04",
    title: "Hot-Path Safe",
    body:
      "SignalEnrichment is a first-class domain type with explicit status (ok | timeout | error | skipped | vetoed). When the LLM is unreachable, strategies fall back to deterministic rules — AI never blocks execution. The LLM sees only public market data: strategy DNA and parameters never cross the port.",
    items: [
      "Typed enrichment status",
      "Deterministic fallback on timeout",
      "Strategy DNA never leaked to LLM",
    ],
  },
];

export function AIEdge() {
  return (
    <section id="ai-edge" className="landing-section border-b landing-hairline">
      <div className="mb-20">
        <p className="landing-label landing-cyan">AI-AUGMENTED DECISION LAYER</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-5xl">
          Deterministic signals.<br />
          <span className="text-[var(--spectral-white)]/60">
            Adversarial second opinion.
          </span>
        </h2>
        <p className="landing-body mt-8 max-w-3xl">
          Confluence tells us <em>whether</em> the setup is clean. The LLM debate
          tells us <em>whether the story makes sense right now</em>. Two independent
          signals that must agree before capital moves — the edge is the disagreement
          surface, not the model.
        </p>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-12 md:gap-16">
        {pillars.map((p) => (
          <div key={p.code} className="border-t landing-hairline pt-8">
            <p className="landing-label text-[var(--spectral-white)]/50">{p.code}</p>
            <h3 className="landing-display text-2xl md:text-3xl mt-4">{p.title}</h3>
            <p className="landing-body mt-6">{p.body}</p>
            <ul className="mt-8 space-y-3">
              {p.items.map((it) => (
                <li key={it} className="flex items-center gap-3">
                  <span className="h-px w-4 bg-[var(--signal-cyan)]" />
                  <span className="landing-label text-[var(--spectral-white)]/80">
                    {it}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      <div className="mt-20 border-t landing-hairline pt-10 grid grid-cols-1 md:grid-cols-3 gap-10">
        <div>
          <p className="landing-label landing-cyan">VERDICT SHAPE</p>
          <pre className="mt-4 text-xs leading-relaxed text-[var(--spectral-white)]/80 font-mono whitespace-pre-wrap">
{`{
  "direction": "LONG",
  "confidence": 0.78,
  "risk_modifier": "TIGHT",
  "rationale": "…",
  "bull_argument": "…",
  "bear_argument": "…",
  "judge_reasoning": "…"
}`}
          </pre>
        </div>
        <div>
          <p className="landing-label landing-cyan">FALLBACK STATUS</p>
          <ul className="mt-4 space-y-2 landing-label text-[var(--spectral-white)]/80">
            <li>ok — debate completed</li>
            <li>timeout — strategy falls back</li>
            <li>error — strategy falls back</li>
            <li>skipped — config-disabled</li>
            <li>vetoed — Judge blocked the trade</li>
          </ul>
        </div>
        <div>
          <p className="landing-label landing-cyan">PRIVACY BOUNDARY</p>
          <p className="mt-4 landing-body text-sm">
            The LLM sees RSI, Stochastic, VWAP distance, EMA stack, regime class,
            and confluence score. It never sees strategy parameters, DNA versions,
            position sizing, or the order journal. Prompt surface is auditable and
            replayable.
          </p>
        </div>
      </div>
    </section>
  );
}
