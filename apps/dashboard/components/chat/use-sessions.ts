"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { ChatMessage } from "./use-chat";

export interface SessionSummary {
  id: string;
  title: string;
  created_at: string;
  updated_at: string;
  last_turn_at: string | null;
  turn_count: number;
}

export interface SessionDetail extends SessionSummary {
  messages: ChatMessage[];
}

const SESSIONS_KEY = ["chat-sessions"] as const;
const sessionKey = (id: string) => ["chat-session", id] as const;

async function getJson<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${url} failed (${res.status}): ${text.slice(0, 200)}`);
  }
  return res.json();
}

export function useSessions() {
  return useQuery<SessionSummary[]>({
    queryKey: SESSIONS_KEY,
    queryFn: () => getJson<SessionSummary[]>("/api/chat/sessions"),
    staleTime: 10_000,
  });
}

export function useSession(id: string | null) {
  return useQuery<SessionDetail>({
    queryKey: id ? sessionKey(id) : ["chat-session-null"],
    queryFn: () => getJson<SessionDetail>(`/api/chat/sessions/${id}`),
    enabled: !!id,
    staleTime: 2_000,
  });
}

export function useRenameSession() {
  const qc = useQueryClient();
  return useMutation<SessionSummary, Error, { id: string; title: string }>({
    mutationFn: ({ id, title }) =>
      getJson<SessionSummary>(`/api/chat/sessions/${id}`, {
        method: "PATCH",
        body: JSON.stringify({ title }),
      }),
    onSuccess: (_data, { id }) => {
      qc.invalidateQueries({ queryKey: SESSIONS_KEY });
      qc.invalidateQueries({ queryKey: sessionKey(id) });
    },
  });
}

export function useDeleteSession() {
  const qc = useQueryClient();
  return useMutation<void, Error, string>({
    mutationFn: async (id) => {
      const res = await fetch(`/api/chat/sessions/${id}`, { method: "DELETE" });
      if (!res.ok && res.status !== 204) {
        throw new Error(`delete failed (${res.status})`);
      }
    },
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: SESSIONS_KEY });
      qc.removeQueries({ queryKey: sessionKey(id) });
    },
  });
}

export function useInvalidateSessions() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: SESSIONS_KEY });
}
