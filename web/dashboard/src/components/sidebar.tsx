"use client";

import { Activity, BarChart3, Home, type LucideIcon, Network, Settings, Users } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { api } from "@/lib/api";
import { useApi } from "@/lib/connection";

interface NavItem {
  label: string;
  href: string;
  icon: LucideIcon;
}

const navItems: NavItem[] = [
  { label: "Dashboard", href: "/", icon: Home },
  { label: "Profiles", href: "/profiles", icon: Users },
  { label: "Coordinators", href: "/coordinators", icon: Network },
  { label: "Activity", href: "/activity", icon: Activity },
  { label: "Usage", href: "/usage", icon: BarChart3 },
  { label: "Settings", href: "/settings", icon: Settings },
];

export function Sidebar() {
  const pathname = usePathname();
  const status = useApi(api.status);
  const loggedIn = status.data?.tools.filter((t) => t.logged_in) ?? [];

  return (
    <aside className="flex shrink-0 flex-col border-b border-border bg-surface lg:w-52 lg:border-r lg:border-b-0">
      <div className="hidden h-16 items-center gap-2 border-b border-border px-6 lg:flex">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground">
          <span className="text-sm font-bold">C</span>
        </div>
        <span className="text-lg font-semibold">CAAM</span>
      </div>

      <nav aria-label="Dashboard sections" className="flex gap-1 overflow-x-auto p-2 lg:flex-1 lg:flex-col lg:p-4">
        {navItems.map((item) => {
          const isActive = pathname === item.href;
          const Icon = item.icon;

          return (
            <Link
              key={item.href}
              href={item.href}
              aria-current={isActive ? "page" : undefined}
              className={`flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium whitespace-nowrap transition-colors hover:bg-surface-muted ${isActive ? "bg-accent/10 text-accent" : ""}`}
            >
              <Icon className="h-4 w-4 text-muted" aria-hidden="true" />
              <span>{item.label}</span>
            </Link>
          );
        })}
      </nav>

      {/* Footer */}
      <div className="hidden border-t border-border p-4 lg:block">
        <div className="rounded-lg bg-surface-muted p-3">
          <p className="text-xs text-muted">Logged in</p>
          <div className="mt-2 flex flex-wrap gap-2">
            {loggedIn.length === 0 ? (
              <span className="text-xs text-muted">{status.data ? "no tools" : "—"}</span>
            ) : (
              loggedIn.map((t) => (
                <span
                  key={t.tool}
                  className="inline-flex h-6 items-center rounded bg-accent/10 px-2 text-xs font-medium capitalize text-accent"
                >
                  {t.tool}
                </span>
              ))
            )}
          </div>
          {status.data && <p className="mt-2 text-xs text-muted">caam {status.data.version}</p>}
          {status.error && <p className="mt-2 text-xs text-warning">Tool status could not refresh{status.data ? "; showing the last result" : ""}.</p>}
        </div>
      </div>
    </aside>
  );
}
