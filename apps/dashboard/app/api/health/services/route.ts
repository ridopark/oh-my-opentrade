
/**
 * GET /api/health/services
 *
 * Proxies to the omo-core backend's GET /healthz/services endpoint and
 * forwards the JSON response to the browser.  This avoids CORS issues when
 * the dashboard runs in Docker alongside the backend.
 */

const BACKEND_URL =
  process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(): Promise<Response> {
  const url = `${BACKEND_URL}/healthz/services`;

  try {
    const res = await fetch(url, {
      next: { revalidate: 0 },
      signal: AbortSignal.timeout(3000),
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
    return new Response(
      JSON.stringify({ healthy: false, services: [], error: message }),
      {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }
    );
  }
}
