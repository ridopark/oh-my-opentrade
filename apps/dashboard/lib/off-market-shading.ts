/**
 * Off-market-hours shading primitive for lightweight-charts v5.
 *
 * Draws semi-transparent rectangles behind the chart for time ranges
 * where no trading data exists (overnight, weekends, holidays).
 * Uses ISeriesPrimitive with zOrder "bottom" so it renders behind all series.
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

/** A time range representing an off-market gap */
export interface GapRange {
  from: Time; // last bar before gap
  to: Time; // first bar after gap
}

// ---------------------------------------------------------------------------
// Renderer — draws filled rectangles for each gap
// ---------------------------------------------------------------------------

interface GapRectData {
  x1: number; // media-coordinate left edge
  x2: number; // media-coordinate right edge
}

class OffMarketPaneRenderer implements IPrimitivePaneRenderer {
  private _rects: GapRectData[];
  private _color: string;

  constructor(rects: GapRectData[], color: string) {
    this._rects = rects;
    this._color = color;
  }

  draw(target: CanvasRenderingTarget2D): void {
    target.useBitmapCoordinateSpace((scope) => {
      const ctx = scope.context;
      const height = scope.bitmapSize.height;
      const hr = scope.horizontalPixelRatio;

      ctx.fillStyle = this._color;

      for (const r of this._rects) {
        const left = Math.round(r.x1 * hr);
        const right = Math.round(r.x2 * hr);
        if (right <= 0 || left >= scope.bitmapSize.width) continue;

        const clampedLeft = Math.max(0, left);
        const clampedRight = Math.min(scope.bitmapSize.width, right);
        ctx.fillRect(clampedLeft, 0, clampedRight - clampedLeft, height);
      }
    });
  }
}

// ---------------------------------------------------------------------------
// Pane view — converts gap time ranges to pixel rectangles
// ---------------------------------------------------------------------------

class OffMarketPaneView implements IPrimitivePaneView {
  private _source: OffMarketShading;
  private _rects: GapRectData[] = [];

  constructor(source: OffMarketShading) {
    this._source = source;
  }

  /** Called by the chart whenever the viewport changes */
  update(): void {
    const chart = this._source.chart;
    if (!chart) {
      this._rects = [];
      return;
    }

    const ts = chart.timeScale();
    this._rects = [];

    // lightweight-charts uses logical (index-based) positioning, so
    // overnight/weekend gaps compress to ~2px. To make the gap visually
    // distinct, we center a band on each gap boundary with a minimum
    // pixel width that's proportional to the visible bar spacing.
    const barSpacing = ts.options().barSpacing ?? 6;
    // Minimum visual width: 6× barSpacing or 20px, whichever is larger
    const minWidth = Math.max(barSpacing * 6, 20);

    for (const gap of this._source.gaps) {
      const x1 = ts.timeToCoordinate(gap.from);
      const x2 = ts.timeToCoordinate(gap.to);
      if (x1 === null || x2 === null) continue;

      const left = Math.min(x1, x2);
      const right = Math.max(x1, x2);
      const center = (left + right) / 2;
      const naturalWidth = right - left;
      const width = Math.max(naturalWidth, minWidth);
      this._rects.push({
        x1: center - width / 2,
        x2: center + width / 2,
      });
    }
  }

  zOrder(): PrimitivePaneViewZOrder {
    return "bottom";
  }

  renderer(): IPrimitivePaneRenderer | null {
    if (this._rects.length === 0) return null;
    return new OffMarketPaneRenderer(this._rects, this._source.color);
  }
}

// ---------------------------------------------------------------------------
// Primitive — public API
// ---------------------------------------------------------------------------

export class OffMarketShading implements ISeriesPrimitive<Time> {
  private _chart: IChartApi | null = null;
  private _requestUpdate: (() => void) | null = null;
  private _paneView: OffMarketPaneView;
  private _gaps: GapRange[] = [];
  private _color: string;

  constructor(color = "rgba(255, 255, 255, 0.04)") {
    this._color = color;
    this._paneView = new OffMarketPaneView(this);
  }

  // -- Public accessors used by the pane view --
  get chart(): IChartApi | null {
    return this._chart;
  }
  get gaps(): GapRange[] {
    return this._gaps;
  }
  get color(): string {
    return this._color;
  }

  // -- Lifecycle --
  attached(param: SeriesAttachedParameter<Time, SeriesType>): void {
    this._chart = param.chart;
    this._requestUpdate = param.requestUpdate;
  }

  detached(): void {
    this._chart = null;
    this._requestUpdate = null;
  }

  // -- Data updates --
  setGaps(gaps: GapRange[]): void {
    this._gaps = gaps;
    this._requestUpdate?.();
  }

  // -- Views --
  updateAllViews(): void {
    this._paneView.update();
  }

  paneViews(): readonly IPrimitivePaneView[] {
    return [this._paneView];
  }
}

// ---------------------------------------------------------------------------
// Helper — detect gaps from a sorted bar array
// ---------------------------------------------------------------------------

/**
 * Scan sorted bars and return gap ranges wherever the time delta exceeds
 * the given threshold (in seconds).
 */
export function detectGaps(
  bars: { time: number }[],
  gapThresholdSec: number,
): GapRange[] {
  const gaps: GapRange[] = [];
  for (let i = 0; i < bars.length - 1; i++) {
    const dt = bars[i + 1].time - bars[i].time;
    if (dt > gapThresholdSec) {
      gaps.push({
        from: bars[i].time as Time,
        to: bars[i + 1].time as Time,
      });
    }
  }
  return gaps;
}
