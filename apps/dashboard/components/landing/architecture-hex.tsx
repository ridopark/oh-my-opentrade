// Regular pointy-top hexagon centered at (cx, cy) with radius r.
// Vertices: (cx, cy-r), (cx + r*sqrt(3)/2, cy - r/2), (cx + r*sqrt(3)/2, cy + r/2),
//           (cx, cy+r),  (cx - r*sqrt(3)/2, cy + r/2), (cx - r*sqrt(3)/2, cy - r/2)
const CX = 300;
const CY = 260;
const R_OUTER = 180;
const R_INNER = 130;

function hexVertices(cx: number, cy: number, r: number) {
  const dx = (r * Math.sqrt(3)) / 2;
  const dy = r / 2;
  return {
    top: { x: cx, y: cy - r },
    topRight: { x: cx + dx, y: cy - dy },
    bottomRight: { x: cx + dx, y: cy + dy },
    bottom: { x: cx, y: cy + r },
    bottomLeft: { x: cx - dx, y: cy + dy },
    topLeft: { x: cx - dx, y: cy - dy },
  };
}

const outer = hexVertices(CX, CY, R_OUTER);
const inner = hexVertices(CX, CY, R_INNER);

const toPoints = (v: ReturnType<typeof hexVertices>) =>
  `${v.top.x},${v.top.y} ${v.topRight.x},${v.topRight.y} ${v.bottomRight.x},${v.bottomRight.y} ${v.bottom.x},${v.bottom.y} ${v.bottomLeft.x},${v.bottomLeft.y} ${v.topLeft.x},${v.topLeft.y}`;

type LabelAnchor = "start" | "middle" | "end";
type Baseline = "auto" | "hanging";

const edgePorts: Array<{
  label: string;
  vertex: keyof ReturnType<typeof hexVertices>;
  inner: keyof ReturnType<typeof hexVertices>;
  dx: number;
  dy: number;
  anchor: LabelAnchor;
  baseline: Baseline;
}> = [
  { label: "ALPACA",    vertex: "top",         inner: "top",         dx: 0,   dy: -14, anchor: "middle", baseline: "auto" },
  { label: "IBKR",      vertex: "topRight",    inner: "topRight",    dx: 12,  dy: -6,  anchor: "start",  baseline: "auto" },
  { label: "TIMESCALE", vertex: "bottomRight", inner: "bottomRight", dx: 12,  dy: 12,  anchor: "start",  baseline: "hanging" },
  { label: "NATS BUS",  vertex: "bottom",      inner: "bottom",      dx: 0,   dy: 18,  anchor: "middle", baseline: "hanging" },
  { label: "DOLTHUB",   vertex: "bottomLeft",  inner: "bottomLeft",  dx: -12, dy: 12,  anchor: "end",    baseline: "hanging" },
  { label: "SEC 13F",   vertex: "topLeft",     inner: "topLeft",     dx: -12, dy: -6,  anchor: "end",    baseline: "auto" },
];

export function ArchitectureHex() {
  return (
    <section id="architecture" className="landing-section border-b landing-hairline">
      <div className="mb-16">
        <p className="landing-label landing-cyan">ARCHITECTURE</p>
        <h2 className="landing-display mt-6 text-[clamp(2rem,4.5vw,3.75rem)] max-w-4xl">
          Hexagonal core.<br />Swappable everything.
        </h2>
        <p className="landing-body mt-8 max-w-2xl">
          Ports and adapters. The Go core holds domain entities and strategy engines; every
          external system (broker, database, data source, event bus) is a pluggable adapter
          behind a port interface. Swapping Alpaca for IBKR is a two-hour adapter rewrite,
          not a refactor.
        </p>
      </div>
      <div className="flex justify-center">
        <svg
          viewBox="0 0 600 520"
          className="w-full max-w-3xl"
          aria-hidden="true"
        >
          <defs>
            <linearGradient id="hexGlow" x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="rgba(95,184,255,0.08)" />
              <stop offset="100%" stopColor="rgba(95,184,255,0.02)" />
            </linearGradient>
          </defs>

          <polygon
            points={toPoints(outer)}
            fill="url(#hexGlow)"
            stroke="rgba(240,240,250,0.45)"
            strokeWidth="1.2"
          />
          <polygon
            points={toPoints(inner)}
            fill="none"
            stroke="rgba(240,240,250,0.2)"
            strokeWidth="1"
          />

          <text x={CX} y={CY - 20} textAnchor="middle"
                fill="var(--spectral-white)" fontSize="20" letterSpacing="2"
                fontWeight="700" style={{ textTransform: "uppercase" }}>
            OMO CORE
          </text>
          <line x1={CX - 80} y1={CY - 8} x2={CX + 80} y2={CY - 8}
                stroke="var(--signal-cyan)" strokeOpacity="0.5" />

          <text x={CX} y={CY + 14} textAnchor="middle"
                fill="rgba(240,240,250,0.7)" fontSize="11" letterSpacing="1.5">
            STRATEGY ENGINE
          </text>
          <text x={CX} y={CY + 32} textAnchor="middle"
                fill="rgba(240,240,250,0.7)" fontSize="11" letterSpacing="1.5">
            EXECUTION GATES
          </text>
          <text x={CX} y={CY + 50} textAnchor="middle"
                fill="rgba(240,240,250,0.7)" fontSize="11" letterSpacing="1.5">
            ORDER JOURNAL
          </text>

          <g stroke="rgba(240,240,250,0.15)" strokeWidth="0.8" strokeDasharray="2 3">
            {edgePorts.map((p) => {
              const o = outer[p.vertex];
              const i = inner[p.inner];
              return <line key={`${p.label}-dash`} x1={o.x} y1={o.y} x2={i.x} y2={i.y} />;
            })}
          </g>

          {edgePorts.map((p) => {
            const v = outer[p.vertex];
            return (
              <g key={p.label}>
                <circle cx={v.x} cy={v.y} r="4" fill="var(--signal-cyan)" fillOpacity="0.9" />
                <text
                  x={v.x + p.dx}
                  y={v.y + p.dy}
                  textAnchor={p.anchor}
                  dominantBaseline={p.baseline}
                  fill="var(--spectral-white)"
                  fontSize="10"
                  letterSpacing="1.5"
                >
                  {p.label}
                </text>
              </g>
            );
          })}
        </svg>
      </div>
    </section>
  );
}
