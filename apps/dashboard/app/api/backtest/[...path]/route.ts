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
  const qs = req.nextUrl.search;

  const isSSE = subPath.endsWith("/events");
  const url = `${BACKEND_URL}/backtest/${subPath}${qs}`;

  try {
    const init: RequestInit = {
      method: req.method,
      headers: { Accept: isSSE ? "text/event-stream" : "application/json" },
    };

    if (req.method !== "GET" && req.method !== "HEAD") {
      init.body = await req.text();
      init.headers = {
        ...init.headers,
        "Content-Type": "application/json",
      };
    }

    const res = await fetch(url, init);

    if (isSSE && res.body) {
      return new Response(res.body, {
        status: res.status,
        headers: {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          Connection: "keep-alive",
        },
      });
    }

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
export const DELETE = proxy;
