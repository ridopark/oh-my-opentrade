import { ChatPanel } from "@/components/chat/chat-panel";
import { SessionSidebar } from "@/components/chat/session-sidebar";

interface Props {
  params: Promise<{ session_id?: string[] }>;
}

export default async function ChatPage({ params }: Props) {
  const { session_id } = await params;
  const activeId = session_id?.[0] ?? null;

  return (
    <div className="-m-3 flex h-[calc(100vh-4.5rem)] md:-m-6">
      <SessionSidebar activeId={activeId} />
      <main className="flex-1 overflow-hidden p-3 md:p-6">
        <ChatPanel sessionId={activeId} />
      </main>
    </div>
  );
}
