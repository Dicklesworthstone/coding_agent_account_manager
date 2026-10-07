"use client";

import { Fragment } from "react";
import { useConnection } from "@/lib/connection";
import { ConnectForm } from "./connect-form";
import { Header } from "./header";
import { Sidebar } from "./sidebar";

interface DashboardLayoutProps {
  children: React.ReactNode;
}

export function DashboardLayout({ children }: DashboardLayoutProps) {
  const { conn, loaded, rejected, error, generation } = useConnection();

  let content = children;
  if (!loaded) {
    content = null;
  } else if (!conn || rejected) {
    content = <ConnectForm rejected={rejected} connectionError={error} />;
  }

  return (
    <div className="flex h-dvh flex-col overflow-hidden lg:flex-row">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <Header key={generation} />
        <main className="flex-1 overflow-auto bg-background p-4 sm:p-6"><Fragment key={generation}>{content}</Fragment></main>
      </div>
    </div>
  );
}
