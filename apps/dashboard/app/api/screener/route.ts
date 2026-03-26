/**
 * GET /api/screener
 *
 * Proxies to the omo-core backend GET /screener endpoint which computes
 * ATR%, NR7, EMA200 bias, realized vol, and composite score for symbols.
 *
 * Query params (forwarded as-is):
 *   symbols - comma-separated, e.g. HIMS,SOXL,TSLA
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(req: NextRequest): Promise<Response> {
  const params = req.nextUrl.searchParams.toString();
  const url = `${BACKEND_URL}/screener${params ? `?${params}` : ""}`;

  try {
    const res = await fetch(url, {
      signal: AbortSignal.timeout(60000),
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
