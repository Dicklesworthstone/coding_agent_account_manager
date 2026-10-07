"use client";

import { motion } from "framer-motion";
import { HeartPulse, LogIn, Network, Users } from "lucide-react";
import Link from "next/link";
import { DashboardLayout } from "@/components";
import { EmptyState, ErrorBanner, Loading, PageHeader, Panel, RefreshButton, StatusChip, timeAgo } from "@/components/ui";
import { api } from "@/lib/api";
import { useApi } from "@/lib/connection";

function StatCard({ title, value, detail, icon }: {
  title: string; value: string | number; detail?: string; icon: React.ReactNode;
}) {
  return <motion.div initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }}
    className="rounded-xl border border-border bg-surface p-6">
    <div className="flex items-start justify-between">
      <div><p className="text-sm text-muted">{title}</p>
        <p className="mt-2 text-3xl font-semibold">{value}</p>
        {detail && <p className="mt-1 text-sm text-muted">{detail}</p>}</div>
      <div className="rounded-lg bg-accent/10 p-3 text-accent">{icon}</div>
    </div>
  </motion.div>;
}

function Overview() {
  const status = useApi(api.status);
  const profiles = useApi(api.profiles);
  const coordinators = useApi(api.coordinators);
  const tools = status.data?.tools ?? [];
  const loggedIn = tools.filter((tool) => tool.logged_in).length;
  const vault = (profiles.data?.profiles ?? []).filter((profile) => !profile.system);
  const attention = vault.filter((profile) => profile.health && ["warning", "critical"].includes(profile.health.status));
  const coords = coordinators.data?.coordinators ?? [];
  const healthyCoords = coords.filter((coordinator) => coordinator.status === "healthy").length;

  return <div className="space-y-8">
    <PageHeader title="Dashboard" subtitle="Your AI coding assistant accounts at a glance"
      action={<RefreshButton loading={status.loading || profiles.loading || coordinators.loading}
        reload={() => { status.reload(); profiles.reload(); coordinators.reload(); }} />} />
    <ErrorBanner error={status.error} stale={!!status.data} />
    <ErrorBanner error={profiles.error} stale={!!profiles.data} />
    <ErrorBanner error={coordinators.error} stale={!!coordinators.data} />
    <div className="grid gap-6 sm:grid-cols-2 xl:grid-cols-4">
      <StatCard title="Tools logged in" value={status.data ? `${loggedIn}/${tools.length}` : "—"}
        detail={tools.length ? tools.filter((tool) => tool.logged_in).map((tool) => tool.tool).join(", ") : undefined}
        icon={<LogIn className="h-6 w-6" aria-hidden="true" />} />
      <StatCard title="Saved profiles" value={profiles.data ? vault.length : "—"}
        detail={profiles.data ? `${new Set(vault.map((profile) => profile.tool)).size} tool(s)` : undefined}
        icon={<Users className="h-6 w-6" aria-hidden="true" />} />
      <StatCard title="Profiles needing attention" value={profiles.data ? attention.length : "—"}
        detail={profiles.data ? (attention.length ? "warning or critical health" : "no warnings or critical health recorded") : undefined}
        icon={<HeartPulse className="h-6 w-6" aria-hidden="true" />} />
      <StatCard title="Coordinators healthy" value={coordinators.data ? `${healthyCoords}/${coords.length}` : "—"}
        detail={coordinators.data && coords.length === 0 ? "distributed recovery not set up" : undefined}
        icon={<Network className="h-6 w-6" aria-hidden="true" />} />
    </div>
    <div className="grid gap-6 lg:grid-cols-2">
      <Panel title="Active accounts">
        {!status.data ? (status.loading ? <Loading /> : <EmptyState>Current tool status is unavailable.</EmptyState>)
          : tools.length === 0 ? <EmptyState>No supported tools found.</EmptyState>
          : <ul className="divide-y divide-border">{tools.map((tool) => <li key={tool.tool}
            className="flex items-center justify-between gap-4 px-6 py-4">
            <div><p className="font-medium capitalize">{tool.tool}</p>
              <p className="text-sm text-muted">{tool.logged_in
                ? [tool.active_profile, tool.identity?.email].filter(Boolean).join(" · ") || "logged in (unsaved login)"
                : "not logged in"}</p></div>
            {tool.logged_in ? <StatusChip status={tool.health?.status} /> : <StatusChip status="unknown" label="logged out" />}
          </li>)}</ul>}
      </Panel>
      <Panel title="Coordinators" action={<Link href="/coordinators" className="text-sm text-accent hover:underline">All</Link>}>
        {!coordinators.data ? (coordinators.loading ? <Loading /> : <EmptyState>Coordinator status is unavailable.</EmptyState>)
          : coords.length === 0 ? <EmptyState>No coordinators. Set up distributed recovery with <code className="font-mono">caam setup distributed</code>.</EmptyState>
          : <ul className="divide-y divide-border">{coords.slice(0, 6).map((coordinator) =>
            <li key={coordinator.id} className="flex items-center justify-between gap-4 px-6 py-4">
              <div><p className="font-medium">{coordinator.display_name || coordinator.id}</p>
                <p className="text-sm text-muted">{coordinator.error || (coordinator.last_seen
                  ? `seen ${timeAgo(coordinator.last_seen)}` : coordinator.endpoint)}</p></div>
              <StatusChip status={coordinator.status} />
            </li>)}</ul>}
      </Panel>
    </div>
    <p className="text-xs text-muted">Read-only data updates every 15 seconds.</p>
  </div>;
}

export default function DashboardPage() {
  return <DashboardLayout><Overview /></DashboardLayout>;
}
