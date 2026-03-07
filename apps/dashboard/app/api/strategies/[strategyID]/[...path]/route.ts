/**
 * GET /api/strategies/:strategyID/:path*
 *
 * Catch-all proxy for per-strategy endpoints on the omo-core backend:
 *   - /api/strategies/:id/dashboard?range=...
 *   - /api/strategies/:id/state
 *   - /api/strategies/:id/signals?limit=...&cursor=...
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ strategyID: string; path: string[] }> },
): Promise<Response> {
  const { strategyID, path } = await params;
  const subPath = path.join("/");
  const qs = req.nextUrl.search; // includes leading "?"

  const url = `${BACKEND_URL}/api/strategies/${encodeURIComponent(strategyID)}/${subPath}${qs}`;

  try {
    const res = await fetch(url, {
      signal: AbortSignal.timeout(10_000),
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
