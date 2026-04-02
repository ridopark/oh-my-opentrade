/**
 * RTH Shading Overlay — draws subtle background shading on non-RTH (pre/post market) regions.
 *
 * Regular Trading Hours: 9:30 AM - 4:00 PM ET
 * Shaded regions: everything outside RTH (pre-market and after-hours).
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

export interface NonRTHRegion {
  startTime: number; // Unix seconds
  endTime: number;   // Unix seconds
}

// ---------------------------------------------------------------------------
// Renderer
// ---------------------------------------------------------------------------

class RTHShadingRenderer implements IPrimitivePaneRenderer {
  private _regions: { x1: number; x2: number; height: number }[];

  constructor(regions: { x1: number; x2: number; height: number }[]) {
    this._regions = regions;
  }

  draw(target: CanvasRenderingTarget2D): void {
    target.useMediaCoordinateSpace((scope) => {
      const ctx = scope.context;
      ctx.fillStyle = "rgba(128, 128, 128, 0.06)";

      for (const region of this._regions) {
        const w = region.x2 - region.x1;
        if (w <= 0) continue;
        ctx.fillRect(region.x1, 0, w, region.height);
      }
    });
  }
}

// ---------------------------------------------------------------------------
// Pane View
// ---------------------------------------------------------------------------

class RTHShadingPaneView implements IPrimitivePaneView {
  private _regions: NonRTHRegion[];
  private _chart: IChartApi | null = null;

  constructor(regions: NonRTHRegion[]) {
    this._regions = regions;
  }

  setContext(chart: IChartApi) {
    this._chart = chart;
  }

  update(regions: NonRTHRegion[]) {
    this._regions = regions;
  }

  zOrder(): PrimitivePaneViewZOrder {
    return "bottom";
  }

  renderer(): IPrimitivePaneRenderer | null {
    const chart = this._chart;
    if (!chart || this._regions.length === 0) return null;

    const timeScale = chart.timeScale();
    const chartHeight = (chart as unknown as { chartElement(): HTMLElement }).chartElement()?.clientHeight ?? 600;
    const rendered: { x1: number; x2: number; height: number }[] = [];

    for (const region of this._regions) {
      const x1 = timeScale.timeToCoordinate(region.startTime as Time);
      const x2 = timeScale.timeToCoordinate(region.endTime as Time);
      if (x1 === null || x2 === null) continue;
      rendered.push({ x1, x2, height: chartHeight });
    }

    return new RTHShadingRenderer(rendered);
  }
}

// ---------------------------------------------------------------------------
// Primitive (attached to series)
// ---------------------------------------------------------------------------

export class RTHShadingOverlay implements ISeriesPrimitive<Time> {
  private _paneView: RTHShadingPaneView;
  private _chart: IChartApi | null = null;
  private _requestUpdate?: () => void;

  constructor() {
    this._paneView = new RTHShadingPaneView([]);
  }

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

  setRegions(regions: NonRTHRegion[]): void {
    this._paneView.update(regions);
    this._requestUpdate?.();
  }
}

/**
 * Parse a Unix timestamp into ET components.
 */
function parseET(unix: number): { dayOfWeek: number; minsFromMidnight: number } {
  const d = new Date(unix * 1000);
  const etStr = d.toLocaleString("en-US", { timeZone: "America/New_York", hour12: false });
  const timeParts = (etStr.split(", ")[1] ?? "0:0").split(":");
  const dayStr = d.toLocaleString("en-US", { timeZone: "America/New_York", weekday: "short" });
  const dayMap: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };
  return {
    dayOfWeek: dayMap[dayStr] ?? 0,
    minsFromMidnight: parseInt(timeParts[0]) * 60 + parseInt(timeParts[1]),
  };
}

/**
 * Compute non-RTH regions from bar data.
 * Groups consecutive non-RTH bars into contiguous shaded regions.
 * Also inserts shading for weekend gaps (Saturday + Sunday) between bars.
 * RTH = 9:30 AM - 4:00 PM ET (weekdays).
 */
export function computeNonRTHRegions(bars: { time: number }[]): NonRTHRegion[] {
  if (bars.length === 0) return [];

  const regions: NonRTHRegion[] = [];
  let regionStart: number | null = null;
  let regionEnd: number | null = null;

  const flushRegion = () => {
    if (regionStart !== null && regionEnd !== null) {
      regions.push({ startTime: regionStart, endTime: regionEnd });
      regionStart = null;
      regionEnd = null;
    }
  };

  for (let i = 0; i < bars.length; i++) {
    const bar = bars[i];
    const et = parseET(bar.time);
    const isWeekend = et.dayOfWeek === 0 || et.dayOfWeek === 6;
    const isRTH = !isWeekend && et.minsFromMidnight >= 570 && et.minsFromMidnight < 960;

    // Detect multi-day gaps (weekends, holidays) where no bars exist.
    // Any gap >24h gets shaded as non-trading time.
    if (i > 0) {
      const gap = bar.time - bars[i - 1].time;
      if (gap > 86400) {
        flushRegion();
        regions.push({ startTime: bars[i - 1].time, endTime: bar.time });
      }
    }

    if (!isRTH) {
      if (regionStart === null) {
        regionStart = bar.time;
      }
      regionEnd = bar.time;
    } else {
      flushRegion();
    }
  }

  // Close final region
  flushRegion();

  return regions;
}
