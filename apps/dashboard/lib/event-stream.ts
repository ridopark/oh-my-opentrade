"use client";

import { useEffect, useRef, useCallback, useState, useMemo } from "react";
import type { DomainEvent, EventType, DebateEvent } from "@/lib/types";

interface UseEventStreamOptions {
  url?: string;
  eventTypes?: EventType[];
  maxEvents?: number;
}

interface EventStreamState {
  events: DomainEvent[];
  connected: boolean;
  error: string | null;
}

export function useEventStream({
  url = "/api/events",
  eventTypes,
  maxEvents = 100,
}: UseEventStreamOptions = {}): EventStreamState & {
  clearEvents: () => void;
} {
  const [state, setState] = useState<EventStreamState>({
    events: [],
    connected: false,
    error: null,
  });
  const eventSourceRef = useRef<EventSource | null>(null);

  const clearEvents = useCallback(() => {
    setState((prev) => ({ ...prev, events: [] }));
  }, []);

  // Stabilize eventTypes reference to prevent useEffect re-triggering on every render
  const eventTypesKey = eventTypes ? JSON.stringify(eventTypes) : "all";
  const stableEventTypes = useMemo(() => eventTypes, [eventTypesKey]);

  useEffect(() => {
    const eventSource = new EventSource(url);
    eventSourceRef.current = eventSource;

    eventSource.onopen = () => {
      setState((prev) => ({ ...prev, connected: true, error: null }));
    };

    eventSource.onerror = () => {
      setState((prev) => ({
        ...prev,
        connected: false,
        error: "Connection lost. Retrying...",
      }));
    };

    // Listen to all known event types
    const allTypes: EventType[] = stableEventTypes ?? [
      "MarketBarSanitized",
      "DebateCompleted",
      "OrderIntentCreated",
      "OrderIntentValidated",
      "OrderIntentRejected",
      "StateUpdated",
      "OrderSubmitted",
      "OrderAccepted",
      "OrderRejected",
      "FillReceived",
      "PositionUpdated",
      "KillSwitchEngaged",
      "CircuitBreakerTripped",
    ];

    for (const type of allTypes) {
      eventSource.addEventListener(type, (e: MessageEvent) => {
        try {
          const event: DomainEvent = JSON.parse(e.data);
          console.log(`[SSE] ${event.type}`, event);
          setState((prev) => ({
            ...prev,
            events: [event, ...prev.events].slice(0, maxEvents),
          }));
        } catch {
          // Skip malformed events
        }
      });
    }

    return () => {
      eventSource.close();
      eventSourceRef.current = null;
    };
  }, [url, maxEvents, stableEventTypes]);

  return { ...state, clearEvents };
}

// Typed filter hooks for specific event types
export function useDebateEvents(maxEvents = 50) {
  const { events, ...rest } = useEventStream({
    eventTypes: ["DebateCompleted"],
    maxEvents,
  });
  return {
    ...rest,
    debates: events.filter((e) => e.type === "DebateCompleted"),
  };
}

export function useOrderIntentEvents(maxEvents = 50) {
  const { events, ...rest } = useEventStream({
    eventTypes: [
      "OrderIntentCreated",
      "OrderIntentValidated",
      "OrderIntentRejected",
      "OrderSubmitted",
    ],
    maxEvents,
  });
  return {
    ...rest,
    orders: events.filter(
      (e) =>
        e.type === "OrderIntentCreated" ||
        e.type === "OrderIntentValidated" ||
        e.type === "OrderIntentRejected" ||
        e.type === "OrderSubmitted"
    ),
  };
}

export function useExecutionEvents(maxEvents = 100) {
  const { events, ...rest } = useEventStream({
    eventTypes: [
      "OrderIntentCreated",
      "OrderIntentValidated",
      "OrderIntentRejected",
      "OrderSubmitted",
      "DebateCompleted",
    ],
    maxEvents,
  });

  // Separate order events and debate events
  const orders = events.filter(
    (e) =>
      e.type === "OrderIntentCreated" ||
      e.type === "OrderIntentValidated" ||
      e.type === "OrderIntentRejected" ||
      e.type === "OrderSubmitted"
  );

  // Build debate lookup by symbol for correlation
  const debateMap = new Map<string, DebateEvent>();
  events
    .filter((e) => e.type === "DebateCompleted")
    .forEach((e) => {
      const debate = e.payload as DebateEvent;
      debateMap.set(debate.symbol, debate);
    });

  return { ...rest, orders, debates: debateMap };
}

export function useStateEvents(maxEvents = 20) {
  const { events, ...rest } = useEventStream({
    eventTypes: ["StateUpdated"],
    maxEvents,
  });
  return {
    ...rest,
    states: events.filter((e) => e.type === "StateUpdated"),
  };
}
