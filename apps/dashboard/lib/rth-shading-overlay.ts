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

// ---------------------------------------------------------------------------
// NYSE holiday & early-close calendar (mirrors backend exchange_calendar.go)
// ---------------------------------------------------------------------------

export const NYSE_HOLIDAYS = new Set([
  // 2025
  "2025-01-01", "2025-01-20", "2025-02-17", "2025-04-18",
  "2025-05-26", "2025-06-19", "2025-07-04", "2025-09-01",
  "2025-11-27", "2025-12-25",
  // 2026
  "2026-01-01", "2026-01-19", "2026-02-16", "2026-04-03",
  "2026-05-25", "2026-06-19", "2026-07-03", "2026-09-07",
  "2026-11-26", "2026-12-25",
  // 2027
  "2027-01-01", "2027-01-18", "2027-02-15", "2027-03-26",
  "2027-05-31", "2027-06-18", "2027-07-05", "2027-09-06",
  "2027-11-25", "2027-12-24",
  // 2028
  "2028-01-17", "2028-02-21", "2028-04-14", "2028-05-29",
  "2028-06-19", "2028-07-04", "2028-09-04", "2028-11-23",
  "2028-12-25",
]);

const NYSE_EARLY_CLOSES = new Set([
  "2025-07-03", "2025-11-28", "2025-12-24",
  "2026-11-27", "2026-12-24",
  "2027-11-26",
  "2028-07-03", "2028-11-24",
]);

/** Format a unix timestamp to "YYYY-MM-DD" in ET */
export function toETDateStr(unix: number): string {
  const d = new Date(unix * 1000);
  return d.toLocaleDateString("en-CA", { timeZone: "America/New_York" }); // "YYYY-MM-DD"
}

/**
 * Parse a Unix timestamp into ET components.
 */
function parseET(unix: number): { dayOfWeek: number; minsFromMidnight: number; isHoliday: boolean; earlyClose: boolean } {
  const d = new Date(unix * 1000);
  const etStr = d.toLocaleString("en-US", { timeZone: "America/New_York", hour12: false });
  const timeParts = (etStr.split(", ")[1] ?? "0:0").split(":");
  const dayStr = d.toLocaleString("en-US", { timeZone: "America/New_York", weekday: "short" });
  const dayMap: Record<string, number> = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 };
  const dateStr = toETDateStr(unix);
  return {
    dayOfWeek: dayMap[dayStr] ?? 0,
    minsFromMidnight: parseInt(timeParts[0]) * 60 + parseInt(timeParts[1]),
    isHoliday: NYSE_HOLIDAYS.has(dateStr),
    earlyClose: NYSE_EARLY_CLOSES.has(dateStr),
  };
}

/** Parse a timeframe string like "1m", "5m", "1h" into seconds. */
export function parseTimeframeSec(tf: string): number {
  const match = tf.match(/^(\d+)(s|m|h|d)$/);
  if (!match) return 300; // default 5m
  const n = parseInt(match[1]);
  switch (match[2]) {
    case "s": return n;
    case "m": return n * 60;
    case "h": return n * 3600;
    case "d": return n * 86400;
    default: return 300;
  }
}

/**
 * Compute non-RTH regions from actual bar data.
 * Groups consecutive non-RTH bars into contiguous shaded regions.
 * RTH = 9:30 AM - 4:00 PM ET (weekdays, non-holiday).
 * Early-close days: RTH ends at 1:00 PM ET.
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

  for (const bar of bars) {
    const et = parseET(bar.time);
    const isWeekend = et.dayOfWeek === 0 || et.dayOfWeek === 6;
    const rthEnd = et.earlyClose ? 780 : 960;
    const isRTH = !isWeekend && !et.isHoliday && et.minsFromMidnight >= 570 && et.minsFromMidnight < rthEnd;

    if (!isRTH) {
      if (regionStart === null) {
        regionStart = bar.time;
      }
      regionEnd = bar.time;
    } else {
      flushRegion();
    }
  }

  flushRegion();
  return regions;
}
