"use client";

import { motion } from "framer-motion";
import { HeartPulse, LogIn, Network, Users } from "lucide-react";
import Link from "next/link";
import { DashboardLayout } from "@/components";
import {
  EmptyState,
  ErrorBanner,
  Loading,
  PageHeader,
  Panel,
  StatusChip,
  timeAgo,
} from "@/components/ui";
import { api } from "@/lib/api";
import { useApi } from "@/lib/connection";

interface StatCardProps {
  title: string;
  value: string | number;
  detail?: string;
  icon: React.ReactNode;
}

function StatCard({ title, value, detail, icon }: StatCardProps) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      className="rounded-xl border border-border bg-surface p-6"
    >
      <div className="flex items-start justify-between">
        <div>
          <p className="text-sm text-muted">{title}</p>
          <p className="mt-2 text-3xl font-semibold">{value}</p>
          {detail && <p className="mt-1 text-sm text-muted">{detail}</p>}
        </div>
        <div className="rounded-lg bg-accent/10 p-3 text-accent">{icon}</div>
      </div>
    </motion.div>
  );
}

function Overview() {
  const status = useApi(api.status);
  const profiles = useApi(api.profiles);
  const coordinators = useApi(api.coordinators);

  const tools = status.data?.tools ?? [];
  const loggedIn = tools.filter((t) => t.logged_in).length;
  const vault = (profiles.data?.profiles ?? []).filter((p) => !p.system);
  const attention = vault.filter((p) => p.health && ["warning", "critical"].includes(p.health.status));
  const coords = coordinators.data?.coordinators ?? [];
  const healthyCoords = coords.filter((c) => c.status === "healthy").length;
  const dash = (ready: unknown) => (ready ? undefined : "—");

  return (
    <div className="space-y-8">
      <PageHeader title="Dashboard" subtitle="Your AI coding assistant accounts at a glance" />
      <ErrorBanner error={status.error ?? profiles.error ?? coordinators.error} />

      <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Tools logged in"
          value={dash(status.data) ?? `${loggedIn}/${tools.length}`}
          detail={tools.length ? tools.filter((t) => t.logged_in).map((t) => t.tool).join(", ") : undefined}
          icon={<LogIn className="h-6 w-6" />}
        />
        <StatCard
          title="Saved profiles"
          value={dash(profiles.data) ?? vault.length}
          detail={profiles.data ? `${new Set(vault.map((p) => p.tool)).size} tool(s)` : undefined}
          icon={<Users className="h-6 w-6" />}
        />
        <StatCard
          title="Profiles needing attention"
          value={dash(profiles.data) ?? attention.length}
          detail={profiles.data ? (attention.length ? "warning or critical health" : "all healthy") : undefined}
          icon={<HeartPulse className="h-6 w-6" />}
        />
        <StatCard
          title="Coordinators healthy"
          value={dash(coordinators.data) ?? `${healthyCoords}/${coords.length}`}
          detail={coordinators.data && coords.length === 0 ? "distributed recovery not set up" : undefined}
          icon={<Network className="h-6 w-6" />}
        />
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Panel title="Active accounts">
          {!status.data ? (
            <Loading />
          ) : tools.length === 0 ? (
            <EmptyState>No supported tools found.</EmptyState>
          ) : (
            <ul className="divide-y divide-border">
              {tools.map((t) => (
                <li key={t.tool} className="flex items-center justify-between gap-4 px-6 py-4">
                  <div>
                    <p className="font-medium capitalize">{t.tool}</p>
                    <p className="text-sm text-muted">
                      {t.logged_in
                        ? [t.active_profile, t.identity?.email].filter(Boolean).join(" · ") ||
                          "logged in (unsaved login)"
                        : "not logged in"}
                    </p>
                  </div>
                  {t.logged_in ? (
                    <StatusChip status={t.health?.status} />
                  ) : (
                    <StatusChip status="unknown" label="logged out" />
                  )}
                </li>
              ))}
            </ul>
          )}
        </Panel>

        <Panel
          title="Coordinators"
          action={
            <Link href="/coordinators" className="text-sm text-accent hover:underline">
              All
            </Link>
          }
        >
          {!coordinators.data ? (
            <Loading />
          ) : coords.length === 0 ? (
            <EmptyState>
              No coordinators. Set up distributed recovery with{" "}
              <code className="font-mono">caam setup distributed</code>.
            </EmptyState>
          ) : (
            <ul className="divide-y divide-border">
              {coords.slice(0, 6).map((c) => (
                <li key={c.id} className="flex items-center justify-between gap-4 px-6 py-4">
                  <div>
                    <p className="font-medium">{c.display_name || c.id}</p>
                    <p className="text-sm text-muted">
                      {c.error || (c.last_seen ? `seen ${timeAgo(c.last_seen)}` : c.endpoint)}
                    </p>
                  </div>
                  <StatusChip status={c.status} />
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  );
}

export default function DashboardPage() {
  return (
    <DashboardLayout>
      <Overview />
    </DashboardLayout>
  );
}
