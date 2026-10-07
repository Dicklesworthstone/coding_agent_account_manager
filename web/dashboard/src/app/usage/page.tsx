"use client";

import { useCallback, useState } from "react";
import { DashboardLayout } from "@/components";
import { EmptyState, ErrorBanner, Loading, PageHeader, Panel, RefreshButton, timeAgo } from "@/components/ui";
import { api, type Connection, type Period } from "@/lib/api";
import { useApi } from "@/lib/connection";

function activeTime(seconds: number): string {
  if (seconds > 0 && seconds < 60) return "<1m";
  const minutes = Math.floor(seconds / 60);
  return minutes >= 60 ? `${Math.floor(minutes / 60)}h ${minutes % 60}m` : `${minutes}m`;
}

function Usage() {
  const [period, setPeriod] = useState<Period>("24h");
  const [provider, setProvider] = useState("");
  const fetchUsage = useCallback((conn: Connection, signal: AbortSignal) => api.usage(conn, period, signal), [period]);
  const usage = useApi(fetchUsage);
  const entries = usage.data?.usage ?? [];
  const providers = Array.from(new Set(entries.map((entry) => entry.tool))).sort();
  const shown = entries.filter((entry) => !provider || entry.tool === provider);

  return <div className="space-y-6">
    <PageHeader title="Usage" subtitle="CAAM-recorded activations, errors, and completed session time"
      action={<RefreshButton loading={usage.loading} reload={usage.reload} />} />
    <p className="text-sm text-muted">Full session duration is counted when completion is recorded, so a session may have begun before the selected interval. Provider API calls and quota consumption are not tracked here.</p>
    <div className="flex flex-wrap gap-4">
      <label className="text-sm">Activity period
        <select aria-label="Activity period" value={period} onChange={(event) => setPeriod(event.target.value as Period)}
          className="mt-1 block h-10 rounded-lg border border-border bg-background px-3">
          <option value="1h">Last hour</option><option value="24h">Last 24 hours</option>
          <option value="7d">Last 7 days</option><option value="30d">Last 30 days</option>
        </select>
      </label>
      <label className="text-sm">Provider filter
        <select aria-label="Provider filter" value={provider} onChange={(event) => setProvider(event.target.value)}
          className="mt-1 block h-10 rounded-lg border border-border bg-background px-3">
          <option value="">All providers</option>{providers.map((entry) => <option key={entry} value={entry}>{entry}</option>)}
          {provider && !providers.includes(provider) && <option value={provider}>{provider}</option>}
        </select>
      </label>
    </div>
    <ErrorBanner error={usage.error} stale={!!usage.data} />
    {usage.data?.available && <p className="text-xs text-muted">{new Date(usage.data.since).toLocaleString()} – {new Date(usage.data.until).toLocaleString()}</p>}
    <Panel title="Recorded activity totals">
      {!usage.data ? (usage.loading ? <Loading /> : <EmptyState>Recorded activity could not be loaded.</EmptyState>)
        : !usage.data.available ? <EmptyState>Activity database unavailable. Recorded usage cannot be measured right now.</EmptyState>
        : shown.length === 0 ? <EmptyState>No CAAM activity recorded in this period{provider ? ` for ${provider}` : ""}.</EmptyState>
        : <div className="overflow-x-auto"><table className="w-full text-left text-sm">
          <caption className="sr-only">Recorded CAAM activity</caption>
          <thead className="text-xs uppercase text-muted"><tr>
            <th scope="col" className="px-6 py-3 font-medium">Profile</th><th scope="col" className="px-6 py-3 font-medium">Activations</th>
            <th scope="col" className="px-6 py-3 font-medium">Errors</th><th scope="col" className="px-6 py-3 font-medium">Completed session time</th>
            <th scope="col" className="px-6 py-3 font-medium">Last activity</th>
          </tr></thead>
          <tbody className="divide-y divide-border">{shown.map((entry) => <tr key={`${entry.tool}/${entry.profile}`}>
            <th scope="row" className="px-6 py-3 font-medium">{entry.tool}/{entry.profile}</th>
            <td className="px-6 py-3 tabular-nums">{entry.activations}</td><td className="px-6 py-3 tabular-nums">{entry.error_count}</td>
            <td className="px-6 py-3 tabular-nums">{activeTime(entry.active_seconds)}</td>
            <td className="px-6 py-3 text-muted">{timeAgo(entry.last_activity) || "Not recorded"}</td>
          </tr>)}</tbody>
        </table></div>}
    </Panel>
  </div>;
}

export default function UsagePage() {
  return <DashboardLayout><Usage /></DashboardLayout>;
}
