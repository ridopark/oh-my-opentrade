import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import { Sidebar } from "@/components/sidebar";
import { QueryProvider } from "@/components/query-provider";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "oh-my-opentrade | Trading Dashboard",
  description:
    "Real-time trading dashboard for oh-my-opentrade algorithmic trading system",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className="dark">
      <body
        className={`${geistSans.variable} ${geistMono.variable} font-sans antialiased overflow-hidden`}
      >
        <QueryProvider>
          <Sidebar />
          <main className="ml-0 md:ml-56 h-screen bg-background p-3 md:p-6 overflow-y-auto">
            {children}
          </main>
        </QueryProvider>
      </body>
    </html>
  );
}
