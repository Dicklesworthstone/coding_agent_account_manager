"use client";

import { motion } from "framer-motion";
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
  const status = useApi(api.status, 30_000);
  const loggedIn = status.data?.tools.filter((t) => t.logged_in) ?? [];

  return (
    <aside className="flex h-screen w-64 flex-col border-r border-border bg-surface">
      {/* Logo / Brand */}
      <div className="flex h-16 items-center gap-2 border-b border-border px-6">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-accent-foreground">
          <span className="text-sm font-bold">C</span>
        </div>
        <span className="text-lg font-semibold">CAAM</span>
      </div>

      {/* Navigation */}
      <nav className="flex-1 space-y-1 p-4">
        {navItems.map((item) => {
          const isActive = pathname === item.href;
          const Icon = item.icon;

          return (
            <Link
              key={item.href}
              href={item.href}
              aria-current={isActive ? "page" : undefined}
              className="relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors hover:bg-surface-muted"
            >
              {isActive && (
                <motion.div
                  layoutId="activeNav"
                  className="absolute inset-0 rounded-lg bg-accent/10"
                  transition={{ type: "spring", bounce: 0.2, duration: 0.4 }}
                />
              )}
              <Icon className={`relative h-5 w-5 ${isActive ? "text-accent" : "text-muted"}`} />
              <span className={`relative ${isActive ? "text-accent" : "text-foreground"}`}>
                {item.label}
              </span>
            </Link>
          );
        })}
      </nav>

      {/* Footer */}
      <div className="border-t border-border p-4">
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
        </div>
      </div>
    </aside>
  );
}
