"use client";

import { useConnection } from "@/lib/connection";
import { ConnectForm } from "./connect-form";
import { Header } from "./header";
import { Sidebar } from "./sidebar";

interface DashboardLayoutProps {
  children: React.ReactNode;
}

export function DashboardLayout({ children }: DashboardLayoutProps) {
  const { conn, loaded, rejected } = useConnection();

  let content = children;
  if (!loaded) {
    content = null;
  } else if (!conn || rejected) {
    content = <ConnectForm rejected={rejected} />;
  }

  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header />
        <main className="flex-1 overflow-auto bg-background p-6">{content}</main>
      </div>
    </div>
  );
}
