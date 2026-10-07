"use client";

import { Save } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef, useState } from "react";
import { DashboardLayout } from "@/components";
import { EmptyState, ErrorBanner, Loading, PageHeader, Panel, RefreshButton, StatusChip } from "@/components/ui";
import { ApiError, api, type ProfileInfo } from "@/lib/api";
import { isCurrentConnection, rejectConnection, useApi, useConnection } from "@/lib/connection";

interface Notice {
  tone: "good" | "bad";
  text: string;
  details?: string[];
}

function matches(profile: ProfileInfo, query: string): boolean {
  const needle = query.trim().toLowerCase();
  return [profile.tool, profile.name, profile.identity?.email, profile.identity?.organization, profile.identity?.plan_type]
    .filter(Boolean).some((field) => field!.toLowerCase().includes(needle));
}

function Profiles() {
  const { conn } = useConnection();
  const profiles = useApi(api.profiles);
  const status = useApi(api.status);
  const params = useSearchParams();
  const fromSearch = params.get("q") ?? "";
  const [filter, setFilter] = useState({ fromSearch, text: fromSearch });
  const query = filter.fromSearch === fromSearch ? filter.text : fromSearch;
  const [provider, setProvider] = useState("");
  const [showSystem, setShowSystem] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [backupTool, setBackupTool] = useState("");
  const [backupName, setBackupName] = useState("");
  const [replaceBackup, setReplaceBackup] = useState(false);
  const actionController = useRef<AbortController | null>(null);
  const actionGeneration = useRef(0);

  useEffect(() => () => {
    actionGeneration.current += 1;
    actionController.current?.abort();
  }, []);

  const saved = profiles.data?.profiles ?? [];
  const all = saved.filter((profile) => showSystem || !profile.system);
  const shown = all.filter((profile) => (!provider || profile.tool === provider) && matches(profile, query));
  const providers = Array.from(new Set([...(status.data?.tools.map((entry) => entry.tool) ?? []), ...saved.map((profile) => profile.tool)])).sort();
  const loggedInTools = (status.data?.tools ?? []).filter((entry) => entry.logged_in).map((entry) => entry.tool);
  const tool = loggedInTools.includes(backupTool) ? backupTool : loggedInTools[0] || "";
  const existing = saved.find((profile) => profile.tool === tool && profile.name === backupName.trim());
  const actionsAvailable = !!conn && !!profiles.data && !profiles.error && !busy;
  const backupAvailable = actionsAvailable && !!status.data && !status.error && !!tool && !!backupName.trim() &&
    !existing?.system && (!existing || replaceBackup);

  async function perform(kind: "activate" | "backup", targetTool: string, name: string, overwrite = false) {
    if (!conn || actionController.current) return;
    const generation = ++actionGeneration.current;
    const controller = new AbortController();
    actionController.current = controller;
    const timeout = setTimeout(() => controller.abort(), 10_000);
    setBusy(kind === "backup" ? "backup" : `${targetTool}/${name}`);
    setNotice(null);
    try {
      const result = kind === "activate"
        ? await api.activate(conn, targetTool, name, controller.signal)
        : await api.backup(conn, targetTool, name, overwrite, controller.signal);
      if (generation !== actionGeneration.current || !isCurrentConnection(conn)) return;
      if (controller.signal.aborted) throw new Error("Acknowledgement arrived after the request timed out.");
      if (!result.success) {
        setNotice({ tone: "bad", text: result.message || "CAAM did not complete the operation. No success was reported." });
        return;
      }
      const previous = "auto_backup" in result && typeof result.auto_backup === "string"
        ? [`The previous login was saved as ${result.auto_backup}.`] : [];
      setNotice({
        tone: "good",
        text: result.message || (kind === "activate" ? `${targetTool} now uses ${name}` : `Saved the current ${targetTool} login as ${name}`),
        details: [...previous, ...(result.warnings ?? [])],
      });
      if (kind === "backup") { setBackupName(""); setReplaceBackup(false); }
      profiles.reload();
      status.reload();
    } catch (error) {
      if (generation !== actionGeneration.current || !isCurrentConnection(conn)) return;
      if (error instanceof ApiError && error.unauthorized) {
        rejectConnection(conn);
      } else {
        setNotice({ tone: "bad", text: error instanceof ApiError && error.status >= 400
          ? error.message
          : "No confirmed result was received. The operation may have completed; refresh to check before trying again." });
      }
    } finally {
      clearTimeout(timeout);
      if (generation === actionGeneration.current) {
        actionController.current = null;
        setBusy(null);
      }
    }
  }

  function activate(profile: ProfileInfo) {
    if (!actionsAvailable || profile.active || profile.system) return;
    if (!window.confirm(`Switch ${profile.tool} to profile "${profile.name}" on this machine? CAAM will preserve the previous live credentials according to your safety settings.`)) return;
    void perform("activate", profile.tool, profile.name);
  }

  function backup(event: React.FormEvent) {
    event.preventDefault();
    if (!backupAvailable) return;
    const name = backupName.trim();
    const overwrite = !!existing && replaceBackup;
    const replacement = overwrite ? " This replaces the credentials already saved under that name." : "";
    if (!window.confirm(`Copy the current live ${tool} login into the saved snapshot ${tool}/${name}?${replacement}`)) return;
    void perform("backup", tool, name, overwrite);
  }

  return <div className="space-y-6">
    <PageHeader title="Profiles" subtitle="Saved logins you can switch between"
      action={<RefreshButton loading={profiles.loading || status.loading}
        reload={() => { profiles.reload(); status.reload(); }} />} />
    <ErrorBanner error={profiles.error} stale={!!profiles.data} />
    <ErrorBanner error={status.error} stale={!!status.data} />
    {notice && <div role={notice.tone === "good" ? "status" : "alert"}
      className={`rounded-lg px-4 py-3 text-sm ${notice.tone === "good" ? "bg-success/10 text-success" : "bg-danger/10 text-danger"}`}>
      <p>{notice.text}</p>
      {notice.details?.map((detail, index) => <p key={index} className="mt-1 opacity-80">{detail}</p>)}
    </div>}
    <div className="flex flex-wrap items-end gap-4">
      <label className="min-w-48 flex-1 text-sm">Filter profiles
        <input type="search" value={query} onChange={(event) => setFilter({ fromSearch, text: event.target.value })}
          placeholder="Name, provider, email, or organization" aria-label="Filter profiles"
          className="mt-1 h-10 w-full rounded-lg border border-border bg-background px-3 text-sm" />
      </label>
      <label className="text-sm">Provider filter
        <select aria-label="Provider filter" value={provider} onChange={(event) => setProvider(event.target.value)}
          className="mt-1 block h-10 rounded-lg border border-border bg-background px-3">
          <option value="">All providers</option>{providers.map((entry) => <option key={entry} value={entry}>{entry}</option>)}
        </select>
      </label>
      <label className="flex items-center gap-2 py-2 text-sm"><input type="checkbox" checked={showSystem}
        onChange={(event) => setShowSystem(event.target.checked)} />Show recovery snapshots</label>
    </div>
    <Panel title={profiles.data ? `${shown.length} of ${all.length} profiles` : "Profiles"}>
      {!profiles.data ? (profiles.loading ? <Loading /> : <EmptyState>Saved profiles are unavailable. Refresh to try again.</EmptyState>)
        : shown.length === 0 ? <EmptyState>{all.length === 0
          ? <>No saved profiles yet. Log in to a tool, then save the login below or with <code className="font-mono">caam backup &lt;tool&gt; &lt;name&gt;</code>.</>
          : "No profiles match the filter."}</EmptyState>
        : <div className="overflow-x-auto"><table className="w-full text-left text-sm">
          <caption className="sr-only">Saved profiles</caption>
          <thead className="text-xs uppercase text-muted"><tr>
            <th scope="col" className="px-6 py-3 font-medium">Tool</th><th scope="col" className="px-6 py-3 font-medium">Profile</th>
            <th scope="col" className="px-6 py-3 font-medium">Account</th><th scope="col" className="px-6 py-3 font-medium">Health</th>
            <th scope="col" className="px-6 py-3">Action</th>
          </tr></thead>
          <tbody className="divide-y divide-border">{shown.map((profile) => {
            const key = `${profile.tool}/${profile.name}`;
            return <tr key={key}>
              <td className="px-6 py-3 capitalize">{profile.tool}</td>
              <td className="px-6 py-3 font-medium">{profile.name}{profile.system && <p className="text-xs text-muted">Recovery snapshot</p>}</td>
              <td className="px-6 py-3 text-muted">{[profile.identity?.email || profile.identity?.account_id, profile.identity?.organization, profile.identity?.plan_type].filter(Boolean).join(" · ") || "Identity unavailable"}</td>
              <td className="px-6 py-3"><StatusChip status={profile.health?.status} />
                {profile.health?.cooldown_remaining && <p className="mt-1 text-xs text-warning">Cooldown: {profile.health.cooldown_remaining}</p>}
                {profile.health?.expires_at && <p className="mt-1 text-xs text-muted">Expires {new Date(profile.health.expires_at).toLocaleString()}</p>}
                {profile.health?.recommendation && <p className="mt-1 text-xs text-muted">{profile.health.recommendation}</p>}
              </td>
              <td className="px-6 py-3 text-right">{profile.active ? <span className="text-xs font-medium text-accent">active</span>
                : <button type="button" onClick={() => activate(profile)} disabled={!actionsAvailable || profile.system}
                  aria-label={`Activate ${profile.tool} profile ${profile.name}`}
                  className="rounded-lg border border-border px-3 py-1 text-xs font-medium hover:border-accent hover:text-accent disabled:opacity-50">
                  {profile.system ? "Recovery only" : busy === key ? "Activating…" : "Activate"}
                </button>}
              </td>
            </tr>;
          })}</tbody>
        </table></div>}
    </Panel>
    <Panel title="Save the current login">
      <p className="px-6 pt-4 text-sm text-muted">Copy the selected tool&apos;s current live login into a named saved snapshot. Use a new name unless you intend to replace its saved credentials.</p>
      <form onSubmit={backup} className="space-y-4 px-6 py-4">
        <div className="flex flex-wrap items-end gap-4">
          <label className="text-sm font-medium">Tool
            <select value={tool} onChange={(event) => { setBackupTool(event.target.value); setReplaceBackup(false); }}
              disabled={!actionsAvailable || !!status.error} className="mt-1 block h-10 rounded-lg border border-border bg-background px-3 text-sm">
              {loggedInTools.length === 0 && <option value="">no tool logged in</option>}
              {loggedInTools.map((entry) => <option key={entry} value={entry}>{entry}</option>)}
            </select>
          </label>
          <label className="text-sm font-medium">Profile name
            <input value={backupName} maxLength={128} disabled={!actionsAvailable} required
              onChange={(event) => { setBackupName(event.target.value); setReplaceBackup(false); }}
              placeholder="work" className="mt-1 block h-10 rounded-lg border border-border bg-background px-3 text-sm" />
          </label>
          <button type="submit" disabled={!backupAvailable}
            className="flex h-10 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-accent-foreground disabled:opacity-50">
            <Save className="h-4 w-4" aria-hidden="true" />{busy === "backup" ? "Saving…" : existing ? "Replace snapshot" : "Save"}
          </button>
        </div>
        {existing?.system ? <p className="text-sm text-warning">Choose a new name. Recovery snapshots cannot be replaced here.</p>
          : existing && <label className="flex items-start gap-2 text-sm text-warning">
            <input type="checkbox" className="mt-1" checked={replaceBackup} onChange={(event) => setReplaceBackup(event.target.checked)} />
            Replace the existing snapshot {tool}/{backupName.trim()} with the current live login.
          </label>}
      </form>
    </Panel>
  </div>;
}

export default function ProfilesPage() {
  return <DashboardLayout><Suspense fallback={<Loading />}><Profiles /></Suspense></DashboardLayout>;
}
