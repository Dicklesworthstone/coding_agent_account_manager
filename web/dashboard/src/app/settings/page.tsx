"use client";

import { DashboardLayout } from "@/components";
import { PageHeader, Panel } from "@/components/ui";
import { api } from "@/lib/api";
import { disconnect, useApi, useConnection } from "@/lib/connection";

function Settings() {
  const { conn } = useConnection();
  const status = useApi(api.status, 60_000);

  return (
    <div className="space-y-6">
      <PageHeader title="Settings" subtitle="How this dashboard reaches caam" />
      <Panel title="Connection">
        <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-3 px-6 py-4 text-sm">
          <dt className="text-muted">API address</dt>
          <dd className="font-mono">{conn?.baseUrl}</dd>
          <dt className="text-muted">caam version</dt>
          <dd>{status.data?.version ?? "—"}</dd>
          <dt className="text-muted">Token</dt>
          <dd className="text-muted">stored in this browser only</dd>
        </dl>
        <div className="border-t border-border px-6 py-4">
          <button
            type="button"
            onClick={disconnect}
            className="rounded-lg border border-border px-4 py-2 text-sm font-medium transition-colors hover:border-danger hover:text-danger"
          >
            Disconnect
          </button>
          <p className="mt-2 text-xs text-muted">
            Forgets the token. Reconnect with <code className="font-mono">caam serve --dashboard-url</code>{" "}
            or by pasting <code className="font-mono">caam serve --show-token</code>.
          </p>
        </div>
      </Panel>
    </div>
  );
}

export default function SettingsPage() {
  return (
    <DashboardLayout>
      <Settings />
    </DashboardLayout>
  );
}
