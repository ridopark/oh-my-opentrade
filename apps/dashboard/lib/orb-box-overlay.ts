/**
 * ORB Box Overlay — draws shaded rectangles for each day's opening range.
 *
 * Follows the same ISeriesPrimitive pattern as SignalMarkerOverlay.
 * Each box spans from the ORB window start to the end of the trading day,
 * shaded in translucent blue.
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

export interface ORBRange {
  /** First bar timestamp of the ORB window (Unix seconds) */
  startTime: number;
  /** Last bar timestamp of the trading day (Unix seconds) */
  endTime: number;
  high: number;
  low: number;
  label: string; // e.g. "01-20"
}

interface PriceMapper {
  priceToCoordinate(price: number): number | null;
}

interface BoxRenderInfo {
  x1: number; // left edge (pixels)
  x2: number; // right edge (pixels)
  y1: number; // top (high price)
  y2: number; // bottom (low price)
  label: string;
}

// ---------------------------------------------------------------------------
// Renderer
// ---------------------------------------------------------------------------

class ORBBoxRenderer implements IPrimitivePaneRenderer {
  private _boxes: BoxRenderInfo[];

  constructor(boxes: BoxRenderInfo[]) {
    this._boxes = boxes;
  }

  draw(target: CanvasRenderingTarget2D): void {
    target.useMediaCoordinateSpace((scope) => {
      const ctx = scope.context;

      for (const box of this._boxes) {
        const w = box.x2 - box.x1;
        const h = box.y2 - box.y1;
        if (w <= 0 || h <= 0) continue;

        // Shaded rectangle
        ctx.fillStyle = "rgba(59, 130, 246, 0.06)";
        ctx.fillRect(box.x1, box.y1, w, h);

        // Border lines (top = high, bottom = low)
        ctx.strokeStyle = "rgba(59, 130, 246, 0.35)";
        ctx.lineWidth = 1;
        ctx.setLineDash([4, 3]);

        ctx.beginPath();
        ctx.moveTo(box.x1, box.y1);
        ctx.lineTo(box.x2, box.y1);
        ctx.stroke();

        ctx.beginPath();
        ctx.moveTo(box.x1, box.y2);
        ctx.lineTo(box.x2, box.y2);
        ctx.stroke();

        ctx.setLineDash([]);

        // Label
        ctx.font = "9px monospace";
        ctx.fillStyle = "rgba(59, 130, 246, 0.5)";
        ctx.fillText(box.label, box.x1 + 4, box.y1 + 11);
      }
    });
  }
}

// ---------------------------------------------------------------------------
// Pane View
// ---------------------------------------------------------------------------

class ORBBoxPaneView implements IPrimitivePaneView {
  private _ranges: ORBRange[];
  private _chart: IChartApi | null = null;
  private _series: PriceMapper | null = null;

  constructor(ranges: ORBRange[]) {
    this._ranges = ranges;
  }

  setContext(chart: IChartApi, series: PriceMapper) {
    this._chart = chart;
    this._series = series;
  }

  update(ranges: ORBRange[]) {
    this._ranges = ranges;
  }

  zOrder(): PrimitivePaneViewZOrder {
    return "bottom";
  }

  renderer(): IPrimitivePaneRenderer | null {
    const chart = this._chart;
    const series = this._series;
    if (!chart || !series || this._ranges.length === 0) return null;

    const timeScale = chart.timeScale();
    const boxes: BoxRenderInfo[] = [];

    for (const range of this._ranges) {
      const x1 = timeScale.timeToCoordinate(range.startTime as Time);
      const x2 = timeScale.timeToCoordinate(range.endTime as Time);
      const y1 = series.priceToCoordinate(range.high);
      const y2 = series.priceToCoordinate(range.low);

      if (x1 === null || x2 === null || y1 === null || y2 === null) continue;

      boxes.push({ x1, x2, y1, y2, label: range.label });
    }

    return new ORBBoxRenderer(boxes);
  }
}

// ---------------------------------------------------------------------------
// Primitive (attached to series)
// ---------------------------------------------------------------------------

export class ORBBoxOverlay implements ISeriesPrimitive<Time> {
  private _paneView: ORBBoxPaneView;
  private _chart: IChartApi | null = null;
  private _series: PriceMapper | null = null;
  private _requestUpdate?: () => void;

  constructor() {
    this._paneView = new ORBBoxPaneView([]);
  }

  attached(param: SeriesAttachedParameter<Time>): void {
    this._chart = param.chart;
    this._series = param.series as unknown as PriceMapper;
    this._requestUpdate = param.requestUpdate;
    this._paneView.setContext(this._chart, this._series);
  }

  detached(): void {
    this._chart = null;
    this._series = null;
  }

  paneViews(): IPrimitivePaneView[] {
    return [this._paneView];
  }

  setRanges(ranges: ORBRange[]): void {
    this._paneView.update(ranges);
    this._requestUpdate?.();
  }
}

/**
 * Compute ORB ranges from bar data.
 * Returns one ORBRange per trading day with the high/low of the first
 * `windowMinutes` of RTH (9:30 ET).
 */
export function computeORBRanges(bars: { time: number; high: number; low: number }[], windowMinutes = 30): ORBRange[] {
  const dayData = new Map<string, { high: number; low: number; startTime: number; endTime: number }>();

  for (const bar of bars) {
    const d = new Date(bar.time * 1000);
    const etHour = (d.getUTCHours() - 5 + 24) % 24;
    const etMin = d.getUTCMinutes();
    const minsFromOpen = (etHour - 9) * 60 + (etMin - 30);
    const dayKey = d.toISOString().slice(0, 10);

    // Track ORB window bars
    if (minsFromOpen >= 0 && minsFromOpen < windowMinutes) {
      const existing = dayData.get(dayKey);
      if (existing) {
        existing.high = Math.max(existing.high, bar.high);
        existing.low = Math.min(existing.low, bar.low);
      } else {
        dayData.set(dayKey, { high: bar.high, low: bar.low, startTime: bar.time, endTime: bar.time });
      }
    }

    // Track last bar of each day for endTime
    const existing = dayData.get(dayKey);
    if (existing) {
      existing.endTime = Math.max(existing.endTime, bar.time);
    }
  }

  const ranges: ORBRange[] = [];
  for (const [dayKey, data] of dayData) {
    if (data.high === data.low) continue;
    ranges.push({
      startTime: data.startTime,
      endTime: data.endTime,
      high: data.high,
      low: data.low,
      label: dayKey.slice(5), // "MM-DD"
    });
  }

  return ranges.sort((a, b) => a.startTime - b.startTime);
}
