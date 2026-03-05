/**
 * GET /api/orders
 *
 * Proxies to the omo-core backend GET /orders endpoint which queries
 * TimescaleDB for historical order intents with strategy + debate data.
 *
 * Accepted query params (forwarded as-is):
 *   range    - e.g. 7d, 30d, 90d, all (default: 30d)
 *   symbol   - e.g. AAPL
 *   side     - BUY or SELL
 *   strategy - e.g. debate
 *   limit    - max rows (default: 50, max: 200)
 *   cursor   - opaque pagination cursor
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(req: NextRequest): Promise<Response> {
  const params = req.nextUrl.searchParams.toString();
  const url = `${BACKEND_URL}/orders${params ? `?${params}` : ""}`;

  try {
    const res = await fetch(url, {
      signal: AbortSignal.timeout(8000),
      headers: { Accept: "application/json" },
    });

    const body = await res.text();
    return new Response(body, {
      status: res.status,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(JSON.stringify({ error: message }), {
      status: 503,
      headers: { "Content-Type": "application/json" },
    });
  }
}
