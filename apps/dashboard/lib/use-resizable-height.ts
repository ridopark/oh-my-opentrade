"use client";

import { useCallback, useEffect, useRef, useState } from "react";

interface Options {
  min?: number;
  max?: number;
}

export interface ResizeHandleProps {
  onMouseDown: (e: React.MouseEvent) => void;
  onTouchStart: (e: React.TouchEvent) => void;
  dragging: boolean;
}

interface UseResizableHeightReturn {
  height: number;
  handleProps: ResizeHandleProps;
}

const STORAGE_PREFIX = "resizable-height:";

// Exported storage keys so call sites don't hand-craft raw strings —
// a typo would silently reset the user's saved layout.
export const SIGNALS_BOTTOM_KEY = "signals-bottom-panel";
export const BACKTEST_BOTTOM_KEY = "backtest-bottom-panel";

export function useResizableHeight(
  key: string,
  defaultPx: number,
  opts: Options = {},
): UseResizableHeightReturn {
  const min = opts.min ?? 120;
  const max = opts.max ?? 700;

  const clamp = useCallback(
    (v: number) => Math.max(min, Math.min(max, v)),
    [min, max],
  );

  const [height, setHeight] = useState<number>(defaultPx);
  const [dragging, setDragging] = useState(false);
  // heightRef mirrors height so the drag effect and mousedown handlers
  // can read the current value without re-binding window listeners on
  // every mousemove-driven setHeight.
  const heightRef = useRef<number>(defaultPx);

  const setHeightBoth = useCallback((v: number) => {
    heightRef.current = v;
    setHeight(v);
  }, []);

  // Hydrate from localStorage after mount so SSR and first client render match.
  useEffect(() => {
    try {
      const raw = window.localStorage.getItem(STORAGE_PREFIX + key);
      if (raw !== null) {
        const parsed = Number(raw);
        if (Number.isFinite(parsed)) setHeightBoth(clamp(parsed));
      }
    } catch {
      // localStorage may be blocked — fall back to default silently.
    }
  }, [key, clamp, setHeightBoth]);

  const dragStartRef = useRef<{ y: number; startHeight: number } | null>(null);

  useEffect(() => {
    if (!dragging) return;
    const onMove = (clientY: number) => {
      const d = dragStartRef.current;
      if (!d) return;
      // Panel grows DOWN from the drag handle, so dragging up (smaller
      // clientY) means increase height. Sign is startY - clientY.
      setHeightBoth(clamp(d.startHeight + (d.y - clientY)));
    };
    const mm = (e: MouseEvent) => onMove(e.clientY);
    const tm = (e: TouchEvent) => {
      if (e.touches.length > 0) onMove(e.touches[0].clientY);
    };
    const up = () => {
      // Persist on release so we don't spam localStorage during drag.
      try {
        window.localStorage.setItem(STORAGE_PREFIX + key, String(heightRef.current));
      } catch {
        /* ignore */
      }
      dragStartRef.current = null;
      setDragging(false);
    };
    window.addEventListener("mousemove", mm);
    window.addEventListener("mouseup", up);
    window.addEventListener("touchmove", tm, { passive: false });
    window.addEventListener("touchend", up);
    window.addEventListener("touchcancel", up);
    document.body.style.userSelect = "none";
    document.body.style.cursor = "ns-resize";
    return () => {
      window.removeEventListener("mousemove", mm);
      window.removeEventListener("mouseup", up);
      window.removeEventListener("touchmove", tm);
      window.removeEventListener("touchend", up);
      window.removeEventListener("touchcancel", up);
      // Always restore body styles, even if the component unmounts
      // mid-drag without a mouseup firing.
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
    };
  }, [dragging, clamp, key, setHeightBoth]);

  const onMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    dragStartRef.current = { y: e.clientY, startHeight: heightRef.current };
    setDragging(true);
  }, []);

  const onTouchStart = useCallback((e: React.TouchEvent) => {
    if (e.touches.length === 0) return;
    dragStartRef.current = { y: e.touches[0].clientY, startHeight: heightRef.current };
    setDragging(true);
  }, []);

  return {
    height,
    handleProps: { onMouseDown, onTouchStart, dragging },
  };
}
