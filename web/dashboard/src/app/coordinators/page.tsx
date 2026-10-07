"use client";

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

const statusHelp: Record<string, string> = {
  healthy: "The auth agent reaches it.",
  unreachable: "The auth agent cannot reach it; check the host and its caam-coordinator service.",
  pending: "The auth agent has not polled it yet.",
  unknown: "The running auth agent does not report it; restart the agent to pick up its config.",
  agent_running: "A single-coordinator agent is running.",
  agent_not_running: "The auth agent is not running: caam auth-agent service install",
};

function Coordinators() {
  const coordinators = useApi(api.coordinators, 5_000);
  const coords = coordinators.data?.coordinators ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Coordinators"
        subtitle="Remote hosts whose Claude Code sessions are logged back in automatically"
      />
      <ErrorBanner error={coordinators.error} />
      <Panel title={coordinators.data ? `${coords.length} coordinator(s)` : "Coordinators"}>
        {!coordinators.data ? (
          <Loading />
        ) : coords.length === 0 ? (
          <EmptyState>
            No coordinators are configured. Run{" "}
            <code className="font-mono">caam setup distributed</code> to deploy them, then{" "}
            <code className="font-mono">caam auth-agent service install</code>.
          </EmptyState>
        ) : (
          <table className="w-full text-left text-sm">
            <thead className="text-xs uppercase text-muted">
              <tr>
                <th className="px-6 py-3 font-medium">Host</th>
                <th className="px-6 py-3 font-medium">Status</th>
                <th className="px-6 py-3 font-medium">Transport</th>
                <th className="px-6 py-3 font-medium">Last seen</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {coords.map((c) => (
                <tr key={c.id}>
                  <td className="px-6 py-3">
                    <p className="font-medium">{c.display_name || c.id}</p>
                    <p className="font-mono text-xs text-muted">{c.endpoint}</p>
                  </td>
                  <td className="px-6 py-3">
                    <StatusChip status={c.status} />
                    <p className="mt-1 max-w-sm text-xs text-muted">
                      {c.error || statusHelp[c.status] || ""}
                    </p>
                  </td>
                  <td className="px-6 py-3 text-muted">
                    {[c.transport, c.backend].filter(Boolean).join(" · ")}
                  </td>
                  <td className="px-6 py-3 text-muted">{timeAgo(c.last_seen) || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}

export default function CoordinatorsPage() {
  return (
    <DashboardLayout>
      <Coordinators />
    </DashboardLayout>
  );
}
