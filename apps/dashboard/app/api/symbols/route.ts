/**
 * GET /api/symbols
 *
 * Returns the configured symbol universe from omo-core.
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(_req: NextRequest) {
  try {
    const res = await fetch(`${BACKEND_URL}/symbols`, {
      signal: AbortSignal.timeout(5000),
    });
    if (!res.ok) {
      return new Response(JSON.stringify([]), { status: res.status, headers: { "Content-Type": "application/json" } });
    }
    const data = await res.json();
    return Response.json(data);
  } catch {
    return new Response(JSON.stringify([]), { status: 503, headers: { "Content-Type": "application/json" } });
  }
}
