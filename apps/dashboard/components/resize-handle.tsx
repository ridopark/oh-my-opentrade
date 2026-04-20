"use client";

import type { ResizeHandleProps } from "@/lib/use-resizable-height";

export function ResizeHandle({ onMouseDown, onTouchStart, dragging }: ResizeHandleProps) {
  return (
    <div
      className={`h-1 w-full shrink-0 cursor-ns-resize rounded-t-lg transition-colors ${
        dragging ? "bg-emerald-500/70" : "bg-zinc-800 hover:bg-zinc-600"
      }`}
      title="Drag to resize"
      onMouseDown={onMouseDown}
      onTouchStart={onTouchStart}
    />
  );
}
