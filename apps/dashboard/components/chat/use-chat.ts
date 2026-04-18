"use client";

import { useMutation } from "@tanstack/react-query";

export interface ChatMessage {
  role: "user" | "assistant";
  content: string;
}

export type AnswerKind = "factual" | "analysis" | "recommendation";

export interface ChatRequest {
  session_id?: string;
  user_message: string;
}

export interface ChatResponse {
  session_id: string;
  answer: string;
  kind: AnswerKind;
  evidence: string[];
  sql_queries: string[];
  prompt_version: string;
  duration_ms: number;
  created_session: boolean;
}

async function postChat(req: ChatRequest): Promise<ChatResponse> {
  const res = await fetch("/api/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`chat failed (${res.status}): ${text.slice(0, 200)}`);
  }
  return res.json();
}

export function useChatMutation() {
  return useMutation<ChatResponse, Error, ChatRequest>({
    mutationFn: postChat,
  });
}
