/**
 * /api/dna/:path*
 *
 * Catch-all proxy for DNA approval endpoints on the omo-core backend:
 *   - GET  /api/dna/approvals
 *   - GET  /api/dna/approvals/:id
 *   - GET  /api/dna/approvals/:id/diff
 *   - POST /api/dna/approvals/:id/approve
 *   - POST /api/dna/approvals/:id/reject
 *   - GET  /api/dna/strategies/:key/active
 */

import { type NextRequest } from "next/server";

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

async function proxy(
  req: NextRequest,
  { params }: { params: Promise<{ path: string[] }> },
): Promise<Response> {
  const { path } = await params;
  const subPath = path.join("/");
  const qs = req.nextUrl.search; // includes leading "?"

  const url = `${BACKEND_URL}/api/dna/${subPath}${qs}`;

  try {
    const init: RequestInit = {
      method: req.method,
      signal: AbortSignal.timeout(10_000),
      headers: { Accept: "application/json" },
    };

    // Forward body for POST/PUT/PATCH
    if (req.method !== "GET" && req.method !== "HEAD") {
      init.body = await req.text();
      init.headers = {
        ...init.headers,
        "Content-Type": "application/json",
      };
    }

    const res = await fetch(url, init);
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

export const GET = proxy;
export const POST = proxy;
