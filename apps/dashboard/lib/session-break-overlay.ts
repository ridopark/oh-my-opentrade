/**
 * Session Break Overlay — draws thin vertical lines at session boundaries
 * (overnight gaps, weekends, holidays) on the backtest chart.
 *
 * Follows the same ISeriesPrimitive pattern as RTHShadingOverlay.
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
} from "lightweight-charts";
import { NYSE_HOLIDAYS, toETDateStr } from "./rth-shading-overlay";

export interface SessionBreak {
  beforeTime: number; // last bar before the gap (Unix seconds)
  afterTime: number;  // first bar after the gap (Unix seconds)
  type: "overnight" | "weekend" | "holiday";
}

// ---------------------------------------------------------------------------
// Renderer
// ---------------------------------------------------------------------------

class SessionBreakRenderer implements IPrimitivePaneRenderer {
  private _lines: { x: number; height: number; type: SessionBreak["type"] }[];

  constructor(lines: { x: number; height: number; type: SessionBreak["type"] }[]) {
    this._lines = lines;
  }

  draw(target: CanvasRenderingTarget2D): void {
    target.useMediaCoordinateSpace((scope) => {
      const ctx = scope.context;

      for (const line of this._lines) {
        ctx.beginPath();
        if (line.type === "overnight") {
          ctx.strokeStyle = "rgba(148, 163, 184, 0.15)";
          ctx.lineWidth = 1;
          ctx.setLineDash([2, 3]);
        } else {
          // weekend or holiday — slightly more visible
          ctx.strokeStyle = "rgba(148, 163, 184, 0.25)";
          ctx.lineWidth = 1;
          ctx.setLineDash([3, 2]);
        }
        ctx.moveTo(line.x + 0.5, 0);
        ctx.lineTo(line.x + 0.5, line.height);
        ctx.stroke();
      }
      ctx.setLineDash([]);
    });
  }
}

// ---------------------------------------------------------------------------
// Pane View
// ---------------------------------------------------------------------------

class SessionBreakPaneView implements IPrimitivePaneView {
  private _breaks: SessionBreak[] = [];
  private _chart: IChartApi | null = null;

  setContext(chart: IChartApi) {
    this._chart = chart;
  }

  update(breaks: SessionBreak[]) {
    this._breaks = breaks;
  }

  zOrder(): PrimitivePaneViewZOrder {
    return "bottom";
  }

  renderer(): IPrimitivePaneRenderer | null {
    const chart = this._chart;
    if (!chart || this._breaks.length === 0) return null;

    const timeScale = chart.timeScale();
    const chartHeight = (chart as unknown as { chartElement(): HTMLElement }).chartElement()?.clientHeight ?? 600;
    const lines: { x: number; height: number; type: SessionBreak["type"] }[] = [];

    for (const brk of this._breaks) {
      const x1 = timeScale.timeToCoordinate(brk.beforeTime as Time);
      const x2 = timeScale.timeToCoordinate(brk.afterTime as Time);
      if (x1 === null || x2 === null) continue;
      // Draw at midpoint between the two adjacent bars
      const x = Math.round((x1 + x2) / 2);
      lines.push({ x, height: chartHeight, type: brk.type });
    }

    return new SessionBreakRenderer(lines);
  }
}

// ---------------------------------------------------------------------------
// Primitive (attached to series)
// ---------------------------------------------------------------------------

export class SessionBreakOverlay implements ISeriesPrimitive<Time> {
  private _paneView = new SessionBreakPaneView();
  private _chart: IChartApi | null = null;
  private _requestUpdate?: () => void;

  attached(param: SeriesAttachedParameter<Time>): void {
    this._chart = param.chart;
    this._requestUpdate = param.requestUpdate;
    this._paneView.setContext(this._chart);
  }

  detached(): void {
    this._chart = null;
  }

  paneViews(): IPrimitivePaneView[] {
    return [this._paneView];
  }

  setBreaks(breaks: SessionBreak[]): void {
    this._paneView.update(breaks);
    this._requestUpdate?.();
  }
}

// ---------------------------------------------------------------------------
// Helper — detect session breaks from sorted bar data
// ---------------------------------------------------------------------------

/**
 * Detect session breaks (overnight, weekend, holiday gaps) from sorted bars.
 * A break is any gap between consecutive bars exceeding 2x the timeframe interval.
 */
export function detectSessionBreaks(
  bars: { time: number }[],
  timeframeSec: number,
): SessionBreak[] {
  const breaks: SessionBreak[] = [];
  const threshold = timeframeSec * 2;

  for (let i = 0; i < bars.length - 1; i++) {
    const gap = bars[i + 1].time - bars[i].time;
    if (gap <= threshold) continue;

    // Classify the gap
    const afterDateStr = toETDateStr(bars[i + 1].time);
    const beforeDateStr = toETDateStr(bars[i].time);

    let type: SessionBreak["type"] = "overnight";

    // Check if a weekend or holiday falls within the gap
    if (NYSE_HOLIDAYS.has(afterDateStr) || NYSE_HOLIDAYS.has(beforeDateStr)) {
      type = "holiday";
    } else if (gap > 86400) {
      // Gap > 24h likely crosses a weekend
      type = "weekend";
    }

    breaks.push({
      beforeTime: bars[i].time,
      afterTime: bars[i + 1].time,
      type,
    });
  }

  return breaks;
}
