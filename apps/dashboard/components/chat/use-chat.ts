"use client";

import { useMutation } from "@tanstack/react-query";

export interface ChatMessage {
  role: "user" | "assistant";
  content: string;
}

export type AnswerKind = "factual" | "analysis" | "recommendation";

export interface ChatResponse {
  answer: string;
  kind: AnswerKind;
  evidence: string[];
  sql_queries: string[];
  prompt_version: string;
  duration_ms: number;
}

async function postChat(messages: ChatMessage[]): Promise<ChatResponse> {
  const res = await fetch("/api/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ messages }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`chat failed (${res.status}): ${text.slice(0, 200)}`);
  }
  return res.json();
}

export function useChatMutation() {
  return useMutation<ChatResponse, Error, ChatMessage[]>({
    mutationFn: postChat,
  });
}
