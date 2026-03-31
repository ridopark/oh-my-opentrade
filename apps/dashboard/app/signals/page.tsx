"use client";

import { useSignalProgress } from "@/lib/event-stream";
import { SignalProgressTable } from "@/components/signal-progress-table";

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
      <SignalProgressTable avwapProgress={avwapProgress} orbProgress={orbProgress} />
    </div>
  );
}
