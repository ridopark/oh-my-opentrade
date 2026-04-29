"use client";

import { useEffect, useRef, useCallback, useState, useMemo } from "react";
import type { DomainEvent, EventType, DebateEvent, EntryGatedPayload, ORBPhaseUpdatePayload } from "@/lib/types";

const STREAM_URL = "/api/events";

const DEFAULT_EVENT_TYPES: EventType[] = [
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

type DomainListener = (evt: DomainEvent) => void;
type ConnectedListener = (connected: boolean) => void;

const BACKOFF_SEQUENCE_MS = [1000, 2000, 5000, 10000];
const CLOSE_DELAY_MS = 1000;

let es: EventSource | null = null;
let refCount = 0;
let closeTimer: ReturnType<typeof setTimeout> | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let backoffStep = 0;
let connectedState = false;

const typeListeners = new Map<EventType, Set<DomainListener>>();
const boundTypes = new Set<EventType>();
const connectedListeners = new Set<ConnectedListener>();

function setConnected(next: boolean) {
  if (connectedState === next) return;
  connectedState = next;
  for (const fn of connectedListeners) fn(next);
}

function dispatch(type: EventType, raw: string) {
  const listeners = typeListeners.get(type);
  if (!listeners || listeners.size === 0) return;
  let event: DomainEvent;
  try {
    event = JSON.parse(raw) as DomainEvent;
  } catch {
    return;
  }
  for (const fn of listeners) {
    try {
      fn(event);
    } catch {
      // swallow: one listener crashing must not block others
    }
  }
}

function bindType(type: EventType) {
  if (!es || boundTypes.has(type)) return;
  es.addEventListener(type, (e: MessageEvent) => dispatch(type, e.data));
  boundTypes.add(type);
}

function openConnection() {
  if (typeof window === "undefined") return;
  if (es) return;
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  es = new EventSource(STREAM_URL);
  es.onopen = () => {
    backoffStep = 0;
    setConnected(true);
  };
  es.onerror = () => {
    setConnected(false);
    if (!es) return;
    es.close();
    es = null;
    boundTypes.clear();
    if (refCount === 0) return;
    const delay = BACKOFF_SEQUENCE_MS[Math.min(backoffStep, BACKOFF_SEQUENCE_MS.length - 1)];
    backoffStep += 1;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (refCount > 0) openConnection();
    }, delay);
  };
  for (const type of typeListeners.keys()) bindType(type);
}

function closeConnection() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  if (es) {
    es.close();
    es = null;
  }
  boundTypes.clear();
  backoffStep = 0;
  setConnected(false);
}

function scheduleClose() {
  if (closeTimer) clearTimeout(closeTimer);
  // Debounced close protects against StrictMode / HMR double-mounts churning the connection.
  closeTimer = setTimeout(() => {
    closeTimer = null;
    if (refCount === 0) closeConnection();
  }, CLOSE_DELAY_MS);
}

function addListener(type: EventType, fn: DomainListener) {
  if (closeTimer) {
    clearTimeout(closeTimer);
    closeTimer = null;
  }
  let set = typeListeners.get(type);
  if (!set) {
    set = new Set();
    typeListeners.set(type, set);
  }
  set.add(fn);
  refCount += 1;
  if (!es) openConnection();
  else bindType(type);
}

function removeListener(type: EventType, fn: DomainListener) {
  const set = typeListeners.get(type);
  if (set) {
    set.delete(fn);
    if (set.size === 0) typeListeners.delete(type);
  }
  refCount = Math.max(0, refCount - 1);
  if (refCount === 0) scheduleClose();
}

function subscribeConnected(fn: ConnectedListener): () => void {
  connectedListeners.add(fn);
  return () => {
    connectedListeners.delete(fn);
  };
}

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

export function useSSEConnected(): boolean {
  const [connected, setLocal] = useState<boolean>(() => connectedState);
  useEffect(() => {
    setLocal(connectedState);
    return subscribeConnected(setLocal);
  }, []);
  return connected;
}

export function useEventListener(
  eventType: EventType,
  handler: (payload: DomainEvent) => void,
): void {
  const handlerRef = useRef(handler);
  useEffect(() => {
    handlerRef.current = handler;
  }, [handler]);

  useEffect(() => {
    const listener: DomainListener = (evt) => handlerRef.current(evt);
    addListener(eventType, listener);
    return () => removeListener(eventType, listener);
  }, [eventType]);
}

export function useEventStream({
  eventTypes,
  maxEvents = 100,
}: UseEventStreamOptions = {}): EventStreamState & {
  clearEvents: () => void;
} {
  const [events, setEvents] = useState<DomainEvent[]>([]);
  const [error] = useState<string | null>(null);
  const connected = useSSEConnected();

  const clearEvents = useCallback(() => setEvents([]), []);

  const stableEventTypes = useMemo(
    () => eventTypes ?? DEFAULT_EVENT_TYPES,
    [eventTypes],
  );

  useEffect(() => {
    const listener: DomainListener = (evt) => {
      setEvents((prev) => [evt, ...prev].slice(0, maxEvents));
    };
    for (const type of stableEventTypes) addListener(type, listener);
    return () => {
      for (const type of stableEventTypes) removeListener(type, listener);
    };
  }, [stableEventTypes, maxEvents]);

  return { events, connected, error, clearEvents };
}

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

  const orders = events.filter(
    (e) =>
      e.type === "OrderIntentCreated" ||
      e.type === "OrderIntentValidated" ||
      e.type === "OrderIntentRejected" ||
      e.type === "OrderSubmitted"
  );

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

const SIGNAL_PROGRESS_EVENTS: EventType[] = ["EntryGated", "ORBPhaseUpdate"];

export function useSignalProgress(maxEvents = 200) {
  const { events, ...rest } = useEventStream({
    eventTypes: SIGNAL_PROGRESS_EVENTS,
    maxEvents,
  });

  const avwapProgress = new Map<string, EntryGatedPayload>();
  const macdProgress = new Map<string, EntryGatedPayload>();
  const orbProgress = new Map<string, ORBPhaseUpdatePayload>();

  for (const evt of events) {
    if (evt.type === "EntryGated") {
      const p = evt.payload as EntryGatedPayload;
      if (p.strategy === "macd") {
        if (!macdProgress.has(p.symbol)) macdProgress.set(p.symbol, p);
      } else {
        if (!avwapProgress.has(p.symbol)) avwapProgress.set(p.symbol, p);
      }
    } else if (evt.type === "ORBPhaseUpdate") {
      const p = evt.payload as ORBPhaseUpdatePayload;
      if (!orbProgress.has(p.symbol)) orbProgress.set(p.symbol, p);
    }
  }

  return { ...rest, avwapProgress, macdProgress, orbProgress };
}
