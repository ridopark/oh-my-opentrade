"use client";

import { useState, useRef, useEffect } from "react";
import { Send, Database, Loader2, CheckSquare } from "lucide-react";

import { Button } from "@/components/ui/button";
import { type AnswerKind, type ChatMessage, useChatMutation } from "./use-chat";

interface AssistantTurn {
  role: "assistant";
  content: string;
  kind: AnswerKind;
  evidence: string[];
  sql: string[];
  durationMs: number;
}

type Turn = { role: "user"; content: string } | AssistantTurn;

const KIND_STYLE: Record<AnswerKind, { label: string; className: string }> = {
  factual: { label: "FACTUAL", className: "bg-muted text-muted-foreground" },
  analysis: { label: "ANALYSIS", className: "bg-amber-500/15 text-amber-700 dark:text-amber-300" },
  recommendation: { label: "RECOMMENDATION", className: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-300" },
};

export function ChatPanel() {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [draft, setDraft] = useState("");
  const mutation = useChatMutation();
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
  }, [turns, mutation.isPending]);

  const send = () => {
    const text = draft.trim();
    if (!text || mutation.isPending) return;

    const nextTurns: Turn[] = [...turns, { role: "user", content: text }];
    setTurns(nextTurns);
    setDraft("");

    const history: ChatMessage[] = nextTurns.map((t) => ({ role: t.role, content: t.content }));
    mutation.mutate(history, {
      onSuccess: (resp) => {
        setTurns((prev) => [
          ...prev,
          {
            role: "assistant",
            content: resp.answer,
            kind: resp.kind,
            evidence: resp.evidence,
            sql: resp.sql_queries,
            durationMs: resp.duration_ms,
          },
        ]);
      },
    });
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  return (
    <div className="flex h-[calc(100vh-8rem)] flex-col rounded-lg border border-border bg-card">
      <div ref={scrollRef} className="flex-1 space-y-4 overflow-y-auto px-4 py-4">
        {turns.length === 0 && <EmptyState />}
        {turns.map((turn, i) =>
          turn.role === "user" ? <UserBubble key={i} content={turn.content} /> : <AssistantBubble key={i} turn={turn} />
        )}
        {mutation.isPending && <PendingBubble />}
        {mutation.isError && <ErrorBubble message={mutation.error.message} />}
      </div>
      <div className="border-t border-border p-3">
        <div className="flex gap-2">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Ask about trades, P&L, strategies..."
            rows={2}
            className="flex-1 resize-none rounded-md border border-border bg-background px-3 py-2 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            disabled={mutation.isPending}
          />
          <Button onClick={send} disabled={mutation.isPending || !draft.trim()} size="lg">
            <Send className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}

function EmptyState() {
  const examples = [
    "What was my total realized P&L last 7 days?",
    "Which strategy had the worst drawdown in March?",
    "Show me today's AVWAP trades.",
  ];
  return (
    <div className="mt-8 text-center text-sm text-muted-foreground">
      <p className="mb-3">Ask questions about your trading data.</p>
      <ul className="space-y-1">
        {examples.map((e) => (
          <li key={e} className="italic">&ldquo;{e}&rdquo;</li>
        ))}
      </ul>
    </div>
  );
}

function UserBubble({ content }: { content: string }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[80%] rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground whitespace-pre-wrap">
        {content}
      </div>
    </div>
  );
}

function AssistantBubble({ turn }: { turn: AssistantTurn }) {
  const [showSql, setShowSql] = useState(false);
  const kindStyle = KIND_STYLE[turn.kind] ?? KIND_STYLE.factual;
  return (
    <div className="max-w-[85%] space-y-2">
      <span className={`inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-semibold tracking-wide ${kindStyle.className}`}>
        {kindStyle.label}
      </span>
      <div className="rounded-lg bg-accent px-3 py-2 text-sm text-accent-foreground whitespace-pre-wrap">
        {turn.content || <span className="italic text-muted-foreground">(empty response)</span>}
      </div>
      {turn.evidence.length > 0 && (
        <div className="rounded border border-border/70 bg-background/50 px-2.5 py-2 text-xs">
          <div className="mb-1 flex items-center gap-1 text-muted-foreground">
            <CheckSquare className="h-3 w-3" />
            <span className="font-semibold">Evidence</span>
          </div>
          <ul className="space-y-1">
            {turn.evidence.map((e, i) => (
              <li key={i} className="text-muted-foreground">
                {e}
              </li>
            ))}
          </ul>
        </div>
      )}
      {turn.sql.length > 0 && (
        <div className="text-xs">
          <button
            onClick={() => setShowSql((v) => !v)}
            className="flex items-center gap-1 text-muted-foreground hover:text-foreground"
          >
            <Database className="h-3 w-3" />
            {turn.sql.length} SQL {turn.sql.length === 1 ? "query" : "queries"} · {turn.durationMs}ms
          </button>
          {showSql && (
            <div className="mt-1 space-y-1">
              {turn.sql.map((q, i) => (
                <pre
                  key={i}
                  className="overflow-x-auto rounded border border-border bg-background px-2 py-1 font-mono text-[11px] leading-snug"
                >
                  {q}
                </pre>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function PendingBubble() {
  return (
    <div className="flex justify-start">
      <div className="flex items-center gap-2 rounded-lg bg-accent px-3 py-2 text-sm text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        thinking...
      </div>
    </div>
  );
}

function ErrorBubble({ message }: { message: string }) {
  return (
    <div className="flex justify-start">
      <div className="max-w-[85%] rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
        {message}
      </div>
    </div>
  );
}
