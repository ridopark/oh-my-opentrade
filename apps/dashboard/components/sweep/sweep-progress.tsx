"use client";

import type { SweepProgress } from "@/lib/use-sweep";
import { Button } from "@/components/ui/button";

interface SweepProgressProps {
  progress: SweepProgress;
  onCancel: () => void;
}

export function SweepProgressBar({ progress, onCancel }: SweepProgressProps) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-sm text-zinc-300">
          Running: {progress.completed} / {progress.total} ({progress.pct.toFixed(0)}%)
        </span>
        <Button variant="ghost" size="sm" className="text-xs text-red-400 hover:text-red-300" onClick={onCancel}>
          Cancel
        </Button>
      </div>
      <div className="w-full bg-zinc-800 rounded-full h-2">
        <div
          className="bg-emerald-500 h-2 rounded-full transition-all duration-300"
          style={{ width: `${Math.min(progress.pct, 100)}%` }}
        />
      </div>
    </div>
  );
}
