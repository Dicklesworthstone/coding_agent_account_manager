"use client";

import { Save } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { DashboardLayout } from "@/components";
import { EmptyState, ErrorBanner, Loading, PageHeader, Panel, StatusChip } from "@/components/ui";
import { ApiError, api, type ProfileInfo } from "@/lib/api";
import { useApi, useConnection } from "@/lib/connection";

interface Notice {
  tone: "good" | "bad";
  text: string;
  details?: string[];
}

function matches(p: ProfileInfo, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) {
    return true;
  }
  return [p.tool, p.name, p.identity?.email, p.identity?.plan_type]
    .filter(Boolean)
    .some((field) => field!.toLowerCase().includes(needle));
}

function errorText(err: unknown): string {
  return err instanceof ApiError ? err.message : String(err);
}

function Profiles() {
  const { conn } = useConnection();
  const profiles = useApi(api.profiles);
  const status = useApi(api.status);
  const params = useSearchParams();
  const [query, setQuery] = useState(params.get("q") ?? "");
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [backupTool, setBackupTool] = useState("");
  const [backupName, setBackupName] = useState("");

  const all = (profiles.data?.profiles ?? []).filter((p) => !p.system);
  const shown = all.filter((p) => matches(p, query));
  const loggedInTools = (status.data?.tools ?? []).filter((t) => t.logged_in).map((t) => t.tool);
  const tool = backupTool || loggedInTools[0] || "";

  async function activate(p: ProfileInfo) {
    if (!conn) {
      return;
    }
    if (
      !window.confirm(
        `Switch ${p.tool} to profile "${p.name}"? Sessions already running may hit auth errors; switch before starting one.`,
      )
    ) {
      return;
    }
    setBusy(`${p.tool}/${p.name}`);
    setNotice(null);
    try {
      const res = await api.activate(conn, p.tool, p.name);
      setNotice({
        tone: res.success ? "good" : "bad",
        text: res.message || `${p.tool} now uses ${p.name}`,
        details: [
          ...(res.auto_backup ? [`The previous login was saved as ${res.auto_backup}.`] : []),
          ...(res.warnings ?? []),
        ],
      });
    } catch (err) {
      setNotice({ tone: "bad", text: `Could not activate ${p.name}: ${errorText(err)}` });
    } finally {
      setBusy(null);
      profiles.reload();
      status.reload();
    }
  }

  async function backup(e: React.FormEvent) {
    e.preventDefault();
    const name = backupName.trim();
    if (!conn || !tool || !name) {
      return;
    }
    setBusy("backup");
    setNotice(null);
    try {
      const res = await api.backup(conn, tool, name);
      setNotice({ tone: res.success ? "good" : "bad", text: res.message || `Saved the current ${tool} login as ${name}` });
      setBackupName("");
    } catch (err) {
      setNotice({ tone: "bad", text: `Could not save the ${tool} login: ${errorText(err)}` });
    } finally {
      setBusy(null);
      profiles.reload();
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader title="Profiles" subtitle="Saved logins you can switch between" />
      <ErrorBanner error={profiles.error} />
      {notice && (
        <div
          role="status"
          className={`rounded-lg px-4 py-3 text-sm ${notice.tone === "good" ? "bg-success/10 text-success" : "bg-danger/10 text-danger"}`}
        >
          <p>{notice.text}</p>
          {notice.details?.map((d) => (
            <p key={d} className="mt-1 opacity-80">
              {d}
            </p>
          ))}
        </div>
      )}

      <Panel
        title={profiles.data ? `${shown.length} of ${all.length} profiles` : "Profiles"}
        action={
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Filter by tool, name, or email"
            aria-label="Filter profiles"
            className="h-9 w-64 rounded-lg border border-border bg-background px-3 text-sm focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent"
          />
        }
      >
        {!profiles.data ? (
          <Loading />
        ) : shown.length === 0 ? (
          <EmptyState>
            {all.length === 0 ? (
              <>
                No saved profiles yet. Log in to a tool, then save the login below or with{" "}
                <code className="font-mono">caam backup &lt;tool&gt; &lt;name&gt;</code>.
              </>
            ) : (
              "No profiles match the filter."
            )}
          </EmptyState>
        ) : (
          <table className="w-full text-left text-sm">
            <thead className="text-xs uppercase text-muted">
              <tr>
                <th className="px-6 py-3 font-medium">Tool</th>
                <th className="px-6 py-3 font-medium">Profile</th>
                <th className="px-6 py-3 font-medium">Account</th>
                <th className="px-6 py-3 font-medium">Health</th>
                <th className="px-6 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {shown.map((p) => {
                const key = `${p.tool}/${p.name}`;
                return (
                  <tr key={key}>
                    <td className="px-6 py-3 capitalize">{p.tool}</td>
                    <td className="px-6 py-3 font-medium">{p.name}</td>
                    <td className="px-6 py-3 text-muted">
                      {[p.identity?.email, p.identity?.plan_type].filter(Boolean).join(" · ") || "—"}
                    </td>
                    <td className="px-6 py-3">
                      <StatusChip status={p.health?.status} />
                      {p.health?.recommendation && (
                        <p className="mt-1 text-xs text-muted">{p.health.recommendation}</p>
                      )}
                    </td>
                    <td className="px-6 py-3 text-right">
                      {p.active ? (
                        <span className="text-xs font-medium text-accent">active</span>
                      ) : (
                        <button
                          type="button"
                          onClick={() => activate(p)}
                          disabled={busy !== null}
                          aria-label={`Activate ${p.tool} profile ${p.name}`}
                          className="rounded-lg border border-border px-3 py-1 text-xs font-medium transition-colors hover:border-accent hover:text-accent disabled:opacity-50"
                        >
                          {busy === key ? "Activating…" : "Activate"}
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Panel>

      <Panel title="Save the current login">
        <form onSubmit={backup} className="flex flex-wrap items-end gap-4 px-6 py-4">
          <label className="text-sm font-medium">
            Tool
            <select
              value={tool}
              onChange={(e) => setBackupTool(e.target.value)}
              className="mt-1 block h-10 rounded-lg border border-border bg-background px-3 text-sm"
            >
              {loggedInTools.length === 0 && <option value="">no tool logged in</option>}
              {loggedInTools.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </label>
          <label className="text-sm font-medium">
            Profile name
            <input
              value={backupName}
              onChange={(e) => setBackupName(e.target.value)}
              placeholder="work"
              className="mt-1 block h-10 rounded-lg border border-border bg-background px-3 text-sm focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent"
            />
          </label>
          <button
            type="submit"
            disabled={busy !== null || !tool || !backupName.trim()}
            className="flex h-10 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-accent-foreground disabled:opacity-50"
          >
            <Save className="h-4 w-4" />
            {busy === "backup" ? "Saving…" : "Save"}
          </button>
        </form>
      </Panel>
    </div>
  );
}

export default function ProfilesPage() {
  return (
    <DashboardLayout>
      <Suspense fallback={<Loading />}>
        <Profiles />
      </Suspense>
    </DashboardLayout>
  );
}
