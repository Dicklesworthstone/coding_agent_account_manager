"use client";

import { AlertCircle, ArrowLeftRight, LogIn, Power, RefreshCw } from "lucide-react";
import { DashboardLayout } from "@/components";
import { EmptyState, ErrorBanner, Loading, PageHeader, Panel, RefreshButton, timeAgo } from "@/components/ui";
import { describeEvent } from "@/lib/activity";
import { api } from "@/lib/api";
import { useApi } from "@/lib/connection";

const eventIcons: Record<string, React.ReactNode> = {
  activate: <Power className="h-4 w-4 text-accent" />,
  switch: <ArrowLeftRight className="h-4 w-4 text-accent" />,
  login: <LogIn className="h-4 w-4 text-success" />,
  refresh: <RefreshCw className="h-4 w-4 text-success" />,
  error: <AlertCircle className="h-4 w-4 text-danger" />,
};

function Activity() {
  const activity = useApi(api.activity, 15_000);
  const events = activity.data?.events ?? [];
  const cooldowns = activity.data?.cooldowns ?? [];

  return (
    <div className="space-y-6">
      <PageHeader title="Activity" subtitle="Switches, refreshes, logins, and errors caam recorded"
        action={<RefreshButton loading={activity.loading} reload={activity.reload} />} />
      <ErrorBanner error={activity.error} stale={!!activity.data} />

      {activity.data?.available && cooldowns.length > 0 && (
        <Panel title="Cooling down">
          <ul className="divide-y divide-border">
            {cooldowns.map((c) => (
              <li key={`${c.tool}/${c.profile}`} className="flex items-center justify-between gap-4 px-6 py-3 text-sm">
                <span>
                  <span className="capitalize text-muted">{c.tool}</span>{" "}
                  <span className="font-medium">{c.profile}</span>
                  {c.notes && <span className="text-muted"> · {c.notes}</span>}
                </span>
                <span className="text-muted">until {new Date(c.until).toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </Panel>
      )}

      <Panel title="Recent activity">
        {!activity.data ? (
          activity.loading ? <Loading /> : <EmptyState>Recent activity could not be loaded.</EmptyState>
        ) : !activity.data.available ? (
          <EmptyState>Activity database unavailable. Recent events and cooldowns cannot be measured right now.</EmptyState>
        ) : events.length === 0 ? (
          <EmptyState>Nothing recorded yet.</EmptyState>
        ) : (
          <ul className="divide-y divide-border">
            {events.map((e, i) => (
              <li key={`${e.timestamp}-${i}`} className="flex items-start gap-3 px-6 py-3">
                <span className="mt-0.5">{eventIcons[e.type] ?? <Power className="h-4 w-4 text-muted" />}</span>
                <div className="flex-1">
                  <p className="text-sm">{describeEvent(e)}</p>
                  <p className="mt-0.5 text-xs text-muted" title={new Date(e.timestamp).toLocaleString()}>
                    {timeAgo(e.timestamp)}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  );
}

export default function ActivityPage() {
  return (
    <DashboardLayout>
      <Activity />
    </DashboardLayout>
  );
}
