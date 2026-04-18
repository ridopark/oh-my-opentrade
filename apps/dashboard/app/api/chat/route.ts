import { type NextRequest } from "next/server";

const AGENT_API_URL =
  process.env.AGENT_API_URL?.replace(/\/$/, "") ?? "http://agent-api:8100";
const PROXY_SECRET = process.env.AGENT_PROXY_SHARED_SECRET ?? "";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function POST(req: NextRequest): Promise<Response> {
  const body = await req.text();
  try {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Accept: "application/json",
    };
    if (PROXY_SECRET) headers["X-Proxy-Secret"] = PROXY_SECRET;

    const res = await fetch(`${AGENT_API_URL}/chat`, {
      method: "POST",
      headers,
      body,
      signal: AbortSignal.timeout(90_000),
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
