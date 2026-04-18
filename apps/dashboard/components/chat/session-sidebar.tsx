"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { MessageSquarePlus, MoreVertical, Pencil, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  type SessionSummary,
  useDeleteSession,
  useRenameSession,
  useSessions,
} from "./use-sessions";

interface Props {
  activeId: string | null;
}

export function SessionSidebar({ activeId }: Props) {
  const router = useRouter();
  const { data: sessions, isLoading } = useSessions();

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-card/50">
      <div className="border-b border-border p-3">
        <Button
          onClick={() => router.push("/chat")}
          variant="outline"
          size="sm"
          className="w-full justify-start gap-2"
        >
          <MessageSquarePlus className="h-4 w-4" />
          New chat
        </Button>
      </div>
      <nav className="flex-1 overflow-y-auto px-2 py-2">
        {isLoading && (
          <p className="px-2 py-4 text-center text-xs text-muted-foreground">loading...</p>
        )}
        {sessions && sessions.length === 0 && (
          <p className="px-2 py-4 text-center text-xs text-muted-foreground">
            no sessions yet
          </p>
        )}
        <ul className="space-y-0.5">
          {sessions?.map((s) => (
            <SessionRow key={s.id} session={s} isActive={s.id === activeId} />
          ))}
        </ul>
      </nav>
    </aside>
  );
}

function SessionRow({
  session,
  isActive,
}: {
  session: SessionSummary;
  isActive: boolean;
}) {
  const router = useRouter();
  const rename = useRenameSession();
  const del = useDeleteSession();
  const [menuOpen, setMenuOpen] = useState(false);

  const onRename = () => {
    const next = window.prompt("Rename chat", session.title);
    if (next && next.trim() && next.trim() !== session.title) {
      rename.mutate({ id: session.id, title: next.trim() });
    }
    setMenuOpen(false);
  };

  const onDelete = () => {
    if (!window.confirm(`Delete "${session.title}"?`)) {
      setMenuOpen(false);
      return;
    }
    del.mutate(session.id, {
      onSuccess: () => {
        if (isActive) router.push("/chat");
      },
    });
    setMenuOpen(false);
  };

  return (
    <li
      className={cn(
        "group relative flex items-center gap-1 rounded-md px-2 py-1.5 text-sm",
        isActive ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      <button
        onClick={() => router.push(`/chat/${session.id}`)}
        className="flex-1 truncate text-left"
        title={session.title}
      >
        {session.title}
      </button>
      <button
        onClick={() => setMenuOpen((v) => !v)}
        className="rounded p-1 opacity-0 hover:bg-background group-hover:opacity-100"
        aria-label="session actions"
      >
        <MoreVertical className="h-3.5 w-3.5" />
      </button>
      {menuOpen && (
        <div className="absolute right-1 top-full z-10 mt-0.5 w-36 rounded-md border border-border bg-popover p-1 shadow-md">
          <button
            onClick={onRename}
            className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-accent"
          >
            <Pencil className="h-3 w-3" />
            Rename
          </button>
          <button
            onClick={onDelete}
            className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-destructive hover:bg-destructive/10"
          >
            <Trash2 className="h-3 w-3" />
            Delete
          </button>
        </div>
      )}
    </li>
  );
}
