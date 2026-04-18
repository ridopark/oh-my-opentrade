import { ChatPanel } from "@/components/chat/chat-panel";
import { SessionSidebar } from "@/components/chat/session-sidebar";

interface Props {
  params: Promise<{ session_id?: string[] }>;
}

export default async function ChatPage({ params }: Props) {
  const { session_id } = await params;
  // Only accept a single-segment session id. Deeper paths like /chat/a/b are
  // bogus; treat them as a no-session landing instead of silently snapping
  // onto the first segment.
  const activeId = session_id && session_id.length === 1 ? session_id[0] : null;

  return (
    <div className="-m-3 flex h-[calc(100vh-4.5rem)] md:-m-6">
      <SessionSidebar activeId={activeId} />
      <main className="flex-1 overflow-hidden p-3 md:p-6">
        <ChatPanel sessionId={activeId} />
      </main>
    </div>
  );
}
