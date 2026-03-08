const BACKEND_URL = process.env.BACKEND_URL?.replace(/\/$/, "") ?? "http://omo-core:8080";
export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(): Promise<Response> {
  const url = `${BACKEND_URL}/api/v1/notifications/kakao/status`;

  try {
    const res = await fetch(url, {
      method: "GET",
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
      JSON.stringify({ error: message }),
      {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }
    );
  }
}
