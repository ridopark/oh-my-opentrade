/**
 * GET /api/screener
 *
 * Proxies to the omo-core backend GET /screener endpoint.
 * Supports both JSON (custom mode) and SSE streaming (universe mode with stream=1).
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(req: NextRequest): Promise<Response> {
  const params = req.nextUrl.searchParams.toString();
  const isStream = req.nextUrl.searchParams.get("stream") === "1";
  const url = `${BACKEND_URL}/screener${params ? `?${params}` : ""}`;

  try {
    const res = await fetch(url, {
      signal: AbortSignal.timeout(120000),
      headers: { Accept: isStream ? "text/event-stream" : "application/json" },
    });

    if (isStream && res.body) {
      // Pass SSE stream through directly
      return new Response(res.body, {
        status: res.status,
        headers: {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          "Connection": "keep-alive",
        },
      });
    }

    // Regular JSON response
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
