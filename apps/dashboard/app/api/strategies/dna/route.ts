/**
 * GET /api/strategies/dna
 *
 * Proxies to the omo-core backend GET /strategies/dna/all endpoint which
 * returns all loaded strategy DNAs (id, version, description, parameters).
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(_req: NextRequest): Promise<Response> {
  const url = `${BACKEND_URL}/strategies/dna/all`;

  try {
    const res = await fetch(url, {
      signal: AbortSignal.timeout(5000),
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
