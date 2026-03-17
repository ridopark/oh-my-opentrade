import { type NextRequest } from "next/server";

/**
 * GET /api/events
 *
 * Server-Sent Events proxy. Connects to the omo-core backend SSE endpoint
 * and forwards all events to the browser client.
 *
 * BACKEND_URL defaults to http://omo-core:8080 (Docker service name) so
 * it works out-of-the-box in Docker Compose.  Override via the BACKEND_URL
 * environment variable for local development:
 *
 *   BACKEND_URL=http://localhost:8080 npm run dev
 */

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

/** Minimal structured logger for server-side Next.js routes. */
const log = {
  info: (msg: string, fields?: Record<string, unknown>) =>
    console.warn(JSON.stringify({ level: "info", route: "/api/events", msg, ...fields })),
  warn: (msg: string, fields?: Record<string, unknown>) =>
    console.warn(JSON.stringify({ level: "warn", route: "/api/events", msg, ...fields })),
  error: (msg: string, fields?: Record<string, unknown>) =>
    console.error(JSON.stringify({ level: "error", route: "/api/events", msg, ...fields })),
};

export async function GET(req: NextRequest): Promise<Response> {
  const backendURL = `${BACKEND_URL}/events`;

  log.info("connecting to backend SSE stream", { backend_url: backendURL });

  // Forward the request to the backend, propagating cancellation.
  let backendRes: Response;
  try {
    backendRes = await fetch(backendURL, {
      signal: req.signal,
      headers: { Accept: "text/event-stream", "Cache-Control": "no-cache" },
      // @ts-expect-error -- Node.js fetch needs duplex for streaming
      duplex: "half",
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    log.error("failed to connect to backend SSE stream", { backend_url: backendURL, error: message });
    return new Response(
      `event: error\ndata: ${JSON.stringify({ message: "Backend unreachable" })}\n\n`,
      {
        status: 200, // keep 200 so EventSource doesn't hard-fail immediately
        headers: sseHeaders(),
      }
    );
  }

  if (!backendRes.ok || !backendRes.body) {
    log.warn("backend returned non-OK status", { backend_url: backendURL, status: backendRes.status });
    return new Response(
      `event: error\ndata: ${JSON.stringify({ message: `Backend returned ${backendRes.status}` })}\n\n`,
      { status: 200, headers: sseHeaders() }
    );
  }

  log.info("SSE stream established, proxying to client", { backend_url: backendURL });

  // Stream backend body directly to the client.
  return new Response(backendRes.body, {
    status: 200,
    headers: sseHeaders(),
  });
}

function sseHeaders(): HeadersInit {
  return {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  };
}
