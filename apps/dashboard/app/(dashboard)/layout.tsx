import { Sidebar } from "@/components/sidebar";
import { QueryProvider } from "@/components/query-provider";
import { DataSourceHeader } from "@/components/DataSourceHeader";

export default function DashboardLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <QueryProvider>
      <Sidebar />
      <main className="ml-0 md:ml-56 h-screen bg-background overflow-y-auto">
        {/* Subtle, single-row health strip — sits above every dashboard page
            so outages surface immediately without stealing focus from the
            main content. */}
        <DataSourceHeader />
        <div className="p-3 md:p-6">{children}</div>
      </main>
    </QueryProvider>
  );
}
