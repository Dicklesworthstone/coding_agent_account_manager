"use client";

import { DashboardLayout } from "@/components";
import { EmptyState, ErrorBanner, Loading, PageHeader, Panel, timeAgo } from "@/components/ui";
import { api } from "@/lib/api";
import { useApi } from "@/lib/connection";

function Usage() {
  const usage = useApi(api.usage, 30_000);
  // The API counts errors per profile from its health records; it does not
  // count calls.
  const entries = [...(usage.data?.usage ?? [])].sort(
    (a, b) => b.error_count - a.error_count || a.profile.localeCompare(b.profile),
  );

  return (
    <div className="space-y-6">
      <PageHeader title="Usage" subtitle="Errors per profile from caam's health checks" />
      <ErrorBanner error={usage.error} />
      <Panel title={usage.data ? `Errors in the last ${usage.data.period}` : "Usage"}>
        {!usage.data ? (
          <Loading />
        ) : entries.length === 0 ? (
          <EmptyState>No health records yet.</EmptyState>
        ) : (
          <table className="w-full text-left text-sm">
            <thead className="text-xs uppercase text-muted">
              <tr>
                <th className="px-6 py-3 font-medium">Profile</th>
                <th className="px-6 py-3 text-right font-medium">Errors</th>
                <th className="px-6 py-3 font-medium">Last checked</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {entries.map((e) => (
                <tr key={`${e.tool}/${e.profile}`}>
                  <td className="px-6 py-3">
                    <span className="capitalize text-muted">{e.tool}</span>{" "}
                    <span className="font-medium">{e.profile}</span>
                  </td>
                  <td className={`px-6 py-3 text-right ${e.error_count ? "text-danger" : "text-muted"}`}>
                    {e.error_count}
                  </td>
                  <td className="px-6 py-3 text-muted">{timeAgo(e.last_used) || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}

export default function UsagePage() {
  return (
    <DashboardLayout>
      <Usage />
    </DashboardLayout>
  );
}
