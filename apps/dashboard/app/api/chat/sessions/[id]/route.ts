import { type NextRequest } from "next/server";

const AGENT_API_URL =
  process.env.AGENT_API_URL?.replace(/\/$/, "") ?? "http://agent-api:8100";
const PROXY_SECRET = process.env.AGENT_PROXY_SHARED_SECRET ?? "";
const TIMEOUT_MS = 10_000;

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

type Ctx = { params: Promise<{ id: string }> };

function baseHeaders(): Record<string, string> {
  const h: Record<string, string> = { Accept: "application/json" };
  if (PROXY_SECRET) h["X-Proxy-Secret"] = PROXY_SECRET;
  return h;
}

async function proxy(
  method: string,
  url: string,
  body?: string,
): Promise<Response> {
  try {
    const headers = baseHeaders();
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await fetch(url, {
      method,
      headers,
      body,
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
    const text = await res.text();
    return new Response(text, {
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

export async function GET(_req: NextRequest, { params }: Ctx): Promise<Response> {
  const { id } = await params;
  return proxy("GET", `${AGENT_API_URL}/sessions/${id}`);
}

export async function PATCH(req: NextRequest, { params }: Ctx): Promise<Response> {
  const { id } = await params;
  const body = await req.text();
  return proxy("PATCH", `${AGENT_API_URL}/sessions/${id}`, body);
}

export async function DELETE(_req: NextRequest, { params }: Ctx): Promise<Response> {
  const { id } = await params;
  return proxy("DELETE", `${AGENT_API_URL}/sessions/${id}`);
}
