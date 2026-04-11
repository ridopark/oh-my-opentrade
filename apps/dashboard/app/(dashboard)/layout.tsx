import { Sidebar } from "@/components/sidebar";
import { QueryProvider } from "@/components/query-provider";

export default function DashboardLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <QueryProvider>
      <Sidebar />
      <main className="ml-0 md:ml-56 h-screen bg-background p-3 md:p-6 overflow-y-auto">
        {children}
      </main>
    </QueryProvider>
  );
}
