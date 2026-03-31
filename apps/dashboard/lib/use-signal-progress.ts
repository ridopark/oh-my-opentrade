"use client";

import { useEffect, useRef, useState, useCallback } from "react";
import type { DomainEvent, EntryGatedPayload, ORBPhaseUpdatePayload } from "@/lib/types";

interface SignalProgressState {
  avwapProgress: Map<string, EntryGatedPayload>;
  orbProgress: Map<string, ORBPhaseUpdatePayload>;
  connected: boolean;
  error: string | null;
}

export function useSignalProgress(url = "/api/events") {
  const [state, setState] = useState<SignalProgressState>({
    avwapProgress: new Map(),
    orbProgress: new Map(),
    connected: false,
    error: null,
  });
  const avwapRef = useRef(new Map<string, EntryGatedPayload>());
  const orbRef = useRef(new Map<string, ORBPhaseUpdatePayload>());
  const rafRef = useRef<number | null>(null);
  const dirtyRef = useRef(false);

  const flush = useCallback(() => {
    rafRef.current = null;
    if (dirtyRef.current) {
      dirtyRef.current = false;
      setState((prev) => ({
        ...prev,
        avwapProgress: new Map(avwapRef.current),
        orbProgress: new Map(orbRef.current),
      }));
    }
  }, []);

  const scheduleFlush = useCallback(() => {
    dirtyRef.current = true;
    if (rafRef.current === null) {
      rafRef.current = requestAnimationFrame(flush);
    }
  }, [flush]);

  useEffect(() => {
    const es = new EventSource(url);

    es.onopen = () => {
      setState((prev) => ({ ...prev, connected: true, error: null }));
    };

    es.onerror = () => {
      setState((prev) => ({
        ...prev,
        connected: false,
        error: "Connection lost. Retrying...",
      }));
    };

    es.addEventListener("EntryGated", (e: MessageEvent) => {
      try {
        const envelope: DomainEvent<EntryGatedPayload> = JSON.parse(e.data);
        avwapRef.current.set(envelope.payload.symbol, envelope.payload);
        scheduleFlush();
      } catch { /* skip malformed */ }
    });

    es.addEventListener("ORBPhaseUpdate", (e: MessageEvent) => {
      try {
        const envelope: DomainEvent<ORBPhaseUpdatePayload> = JSON.parse(e.data);
        orbRef.current.set(envelope.payload.symbol, envelope.payload);
        scheduleFlush();
      } catch { /* skip malformed */ }
    });

    return () => {
      es.close();
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current);
      }
    };
  }, [url, scheduleFlush]);

  return state;
}
