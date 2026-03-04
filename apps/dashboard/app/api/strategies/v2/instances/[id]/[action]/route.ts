// Proxy POST /api/strategies/v2/instances/{id}/{action} → backend POST /strategies/v2/instances/{id}/{action}
import { type NextRequest } from "next/server";

const BACKEND_URL = process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ id: string; action: string }> }
): Promise<Response> {
  const { id, action } = await params;
  const url = `${BACKEND_URL}/strategies/v2/instances/${encodeURIComponent(id)}/${encodeURIComponent(action)}`;
  try {
    const body = await req.text();
    const res = await fetch(url, {
      method: "POST",
      signal: AbortSignal.timeout(5000),
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: body || undefined,
    });
    const resBody = await res.text();
    return new Response(resBody, {
      status: res.status,
      headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(JSON.stringify({ error: message }), {
      status: 503,
      headers: { "Content-Type": "application/json" },
    });
  }
}
