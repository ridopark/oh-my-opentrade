"use client";

import { useSignalProgress } from "@/lib/event-stream";
import { AVWAPConfluenceMatrix } from "@/components/avwap-confluence-matrix";
import { ORBPhaseTimeline } from "@/components/orb-phase-timeline";

export default function SignalsPage() {
  const { avwapProgress, orbProgress, connected } = useSignalProgress();

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">Signal Formation Progress</h1>
        <div className="flex items-center gap-2">
          <span className={`h-2 w-2 rounded-full ${connected ? "bg-emerald-500" : "bg-red-500"}`} />
          <span className="text-xs text-zinc-500">
            {connected ? "Live" : "Disconnected"}
          </span>
        </div>
      </div>

      <section>
        <h2 className="text-lg font-semibold mb-3 text-zinc-300">AVWAP Confluence</h2>
        <AVWAPConfluenceMatrix data={avwapProgress} />
      </section>

      <section>
        <h2 className="text-lg font-semibold mb-3 text-zinc-300">ORB Phase Timeline</h2>
        <ORBPhaseTimeline data={orbProgress} />
      </section>
    </div>
  );
}
