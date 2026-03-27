/**
 * Signal marker overlay primitive for lightweight-charts v5.
 *
 * Draws buy/sell signal labels as colored rounded-rect boxes with a small
 * arrow pointing to the bar. Provides much higher visibility than the
 * default plain-text markers via contrasting background + drop shadow.
 *
 * Follows the same ISeriesPrimitive pattern as OffMarketShading.
 */
import type { CanvasRenderingTarget2D } from "fancy-canvas";
import type {
  ISeriesPrimitive,
  SeriesAttachedParameter,
  IPrimitivePaneView,
  IPrimitivePaneRenderer,
  PrimitivePaneViewZOrder,
  Time,
  IChartApi,
  SeriesType,
} from "lightweight-charts";

/** Data needed to render a single signal marker */
export interface SignalMarkerData {
  time: Time;
  price: number; // bar low (buy) or bar high (sell) for y-positioning
  side: "buy" | "sell";
  kind: "entry" | "exit";
  executed: boolean;
  label: string; // e.g. "Buy (awap_v1)"
}

/** Minimal interface — we only need priceToCoordinate from the series */
interface PriceMapper {
  priceToCoordinate(price: number): number | null;
}

/** Pre-computed render coordinates (media/CSS pixel space) */
interface MarkerRenderInfo {
  x: number;
  y: number;
  side: "buy" | "sell";
  kind: "entry" | "exit";
  executed: boolean;
  label: string;
}

// ---------------------------------------------------------------------------
// Renderer
// ---------------------------------------------------------------------------

class SignalMarkerRenderer implements IPrimitivePaneRenderer {
  private _markers: MarkerRenderInfo[];

  constructor(markers: MarkerRenderInfo[]) {
    this._markers = markers;
  }

  draw(target: CanvasRenderingTarget2D): void {
    target.useMediaCoordinateSpace((scope) => {
      const ctx = scope.context;
      const { width: vw, height: vh } = scope.mediaSize;

      for (const m of this._markers) {
        // Skip markers well outside the viewport
        if (m.x < -80 || m.x > vw + 80) continue;
        if (m.y < -50 || m.y > vh + 50) continue;

        const isBuy = m.side === "buy";
        const isEntry = m.kind === "entry";

        let strokeColor: string;
        let fillColor: string;
        
        // 4 signal types: emerald/orange/rose/sky based on side + kind
        if (isBuy && isEntry) {
          // Open Long: Emerald #10b981
          strokeColor = "#10b981";
          fillColor = "#10b981";
        } else if (!isBuy && !isEntry) {
          // Close Long: Orange #f59e0b
          strokeColor = "#f59e0b";
          fillColor = "transparent";
        } else if (!isBuy && isEntry) {
          // Open Short: Rose #e11d48
          strokeColor = "#e11d48";
          fillColor = "#e11d48";
        } else {
          // Close Short: Sky #0ea5e9
          strokeColor = "#0ea5e9";
          fillColor = "transparent";
        }

        const isSolid = fillColor !== "transparent";

        ctx.save();
        if (!m.executed) {
          ctx.globalAlpha = 0.35;
        }

        // --- Measure text ---
        ctx.font = "bold 10px var(--font-geist-mono, monospace)";
        const tm = ctx.measureText(m.label);
        const textW = tm.width;
        const lineH = 12;
        const px = 5;
        const py = 2.5;
        const boxW = textW + px * 2;
        const boxH = lineH + py * 2;
        const r = 3;
        const arrowH = 5;
        const arrowHW = 4;
        const gap = 6;

        let boxX = m.x - boxW / 2;
        let boxY: number;
        let tipY: number;

        // BUY-side markers go BELOW the bar with arrow pointing UP.
        // SELL-side markers go ABOVE the bar with arrow pointing DOWN.
        if (isBuy) {
          tipY = m.y + gap;
          boxY = tipY + arrowH;
        } else {
          tipY = m.y - gap;
          boxY = tipY - arrowH - boxH;
        }

        // Clamp box horizontally so it doesn't overflow the chart
        if (boxX < 2) boxX = 2;
        if (boxX + boxW > vw - 2) boxX = vw - 2 - boxW;
        const boxCenterX = boxX + boxW / 2;

        // --- Shadow ---
        ctx.save();
        ctx.shadowColor = "rgba(0, 0, 0, 0.5)";
        ctx.shadowBlur = 6;
        ctx.shadowOffsetX = 0;
        ctx.shadowOffsetY = 2;

        ctx.fillStyle = strokeColor; // For the shadow trick and solid fill
        ctx.strokeStyle = strokeColor;
        ctx.lineWidth = 2;

        // --- Arrow ---
        ctx.beginPath();
        if (isBuy) {
          ctx.moveTo(m.x, tipY);
          ctx.lineTo(m.x - arrowHW, tipY + arrowH);
          ctx.lineTo(m.x + arrowHW, tipY + arrowH);
        } else {
          ctx.moveTo(m.x, tipY);
          ctx.lineTo(m.x - arrowHW, tipY - arrowH);
          ctx.lineTo(m.x + arrowHW, tipY - arrowH);
        }
        ctx.closePath();
        
        if (isSolid) {
          ctx.fill();
        } else {
          ctx.stroke();
          // Clear shadow for subsequent draws
          ctx.shadowColor = "transparent"; 
        }

        // --- Rounded rect ---
        ctx.beginPath();
        ctx.roundRect(boxX, boxY, boxW, boxH, r);
        
        if (isSolid) {
          ctx.fill();
        } else {
          ctx.shadowColor = "rgba(0, 0, 0, 0.5)"; // Ensure shadow applies to stroke if needed
          ctx.stroke();
        }

        ctx.restore();

        // --- Text ---
        ctx.fillStyle = isSolid ? "#ffffff" : strokeColor;
        ctx.font = "bold 10px var(--font-geist-mono, monospace)";
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.fillText(m.label, boxCenterX, boxY + boxH / 2);
        
        ctx.restore(); // Restore globalAlpha
      }
    });
  }
}

// ---------------------------------------------------------------------------
// Pane View
// ---------------------------------------------------------------------------

class SignalMarkerPaneView implements IPrimitivePaneView {
  private _source: SignalMarkerOverlay;
  private _markers: MarkerRenderInfo[] = [];

  constructor(source: SignalMarkerOverlay) {
    this._source = source;
  }

  update(): void {
    const chart = this._source.chart;
    const series = this._source.series;
    if (!chart || !series) {
      this._markers = [];
      return;
    }

    const ts = chart.timeScale();
    this._markers = [];

    for (const s of this._source.signals) {
      const x = ts.timeToCoordinate(s.time);
      if (x === null) continue;
      const y = series.priceToCoordinate(s.price);
      if (y === null) continue;
      this._markers.push({ x, y, side: s.side, kind: s.kind, executed: s.executed, label: s.label });
    }
  }

  zOrder(): PrimitivePaneViewZOrder {
    return "top";
  }

  renderer(): IPrimitivePaneRenderer | null {
    if (this._markers.length === 0) return null;
    return new SignalMarkerRenderer(this._markers);
  }
}

// ---------------------------------------------------------------------------
// Primitive — public API
// ---------------------------------------------------------------------------

export class SignalMarkerOverlay implements ISeriesPrimitive<Time> {
  private _chart: IChartApi | null = null;
  private _series: PriceMapper | null = null;
  private _requestUpdate: (() => void) | null = null;
  private _paneView: SignalMarkerPaneView;
  private _signals: SignalMarkerData[] = [];

  constructor() {
    this._paneView = new SignalMarkerPaneView(this);
  }

  get chart(): IChartApi | null {
    return this._chart;
  }
  get series(): PriceMapper | null {
    return this._series;
  }
  get signals(): SignalMarkerData[] {
    return this._signals;
  }

  attached(param: SeriesAttachedParameter<Time, SeriesType>): void {
    this._chart = param.chart;
    this._series = param.series;
    this._requestUpdate = param.requestUpdate;
  }

  detached(): void {
    this._chart = null;
    this._series = null;
    this._requestUpdate = null;
  }

  setSignals(signals: SignalMarkerData[]): void {
    this._signals = signals;
    this._requestUpdate?.();
  }

  /** Hit-test a click at (x, y) in media coordinates against rendered markers.
   *  Returns the index into the signals array, or -1 if nothing hit. */
  hitTestSignal(x: number, y: number): number {
    if (!this._chart || !this._series) return -1;
    const ts = this._chart.timeScale();
    for (let i = 0; i < this._signals.length; i++) {
      const s = this._signals[i];
      const mx = ts.timeToCoordinate(s.time);
      if (mx === null) continue;
      const my = this._series.priceToCoordinate(s.price);
      if (my === null) continue;

      // Box dimensions matching the renderer
      const boxW = 120; // generous hit area
      const boxH = 22;
      const gap = 6;
      const arrowH = 5;
      const isBuy = s.side === "buy";
      const boxY = isBuy ? my + gap + arrowH : my - gap - arrowH - boxH;
      const boxX = mx - boxW / 2;

      if (x >= boxX && x <= boxX + boxW && y >= boxY && y <= boxY + boxH) {
        return i;
      }
    }
    return -1;
  }

  updateAllViews(): void {
    this._paneView.update();
  }

  paneViews(): readonly IPrimitivePaneView[] {
    return [this._paneView];
  }
}
