import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, disconnect, resetConnectionForTests, useConnection } from "@/lib/connection";
import type { ActivityResponse, ProfileInfo } from "@/lib/api";
import DashboardPage from "./page";
import SettingsPage from "./settings/page";
import UsagePage from "./usage/page";
import ActivityPage from "./activity/page";

vi.mock("next/navigation", () => ({
  usePathname: () => "/",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock("framer-motion", () => ({
  motion: { div: ({ children }: React.PropsWithChildren) => <div>{children}</div> },
  AnimatePresence: ({ children }: React.PropsWithChildren) => <>{children}</>,
}));

const token = "local-test-token-do-not-persist";
const conn = { baseUrl: "http://127.0.0.1:7892", token };
const now = "2026-10-07T18:00:00Z";

function ConnectionObserver() {
  useConnection();
  return null;
}

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function fixtures() {
  return {
    status: { version: "1.2.3", timestamp: now, tools: ["claude", "codex", "cursor", "gemini", "grok", "opencode", "agy"].map((tool) => ({
      tool, logged_in: tool === "claude" || tool === "grok",
      active_profile: tool === "claude" ? "work" : "",
      identity: tool === "claude" ? { email: "work@example.test" } : undefined,
    })) },
    profiles: { count: 3, profiles: [
      { tool: "claude", name: "work", active: true, system: false, health: { status: "healthy" } },
      { tool: "grok", name: "fresh", active: false, system: false, health: { status: "warning", cooldown_remaining: "4m" } },
      { tool: "claude", name: "_backup_1", active: false, system: true },
    ] as ProfileInfo[] },
    usage: { period: "24h", since: "2026-10-06T18:00:00Z", until: now, available: true, usage: [
      { tool: "grok", profile: "fresh", activations: 3, error_count: 1, active_seconds: 5400, last_activity: now },
    ] },
    activity: { available: true, events: [{ timestamp: now, type: "switch", tool: "grok", profile: "fresh", details: { from: "old", reason: "rate_limit" } }],
      cooldowns: [{ tool: "claude", profile: "work", hit_at: now, until: "2026-10-07T19:00:00Z" }] } as ActivityResponse,
    coordinators: { coordinators: [
      { id: "build", display_name: "Build host", endpoint: "http://127.0.0.1:7890 via ssh build", transport: "ssh", status: "healthy" },
      { id: "gpu", endpoint: "http://127.0.0.1:7890 via ssh gpu", transport: "ssh", status: "unreachable", error: "Coordinator connection unavailable" },
    ] },
  };
}
type Intercept = (url: URL, init: RequestInit) => Promise<Response> | Response | undefined;
function mockAPI(data = fixtures(), intercept?: Intercept) {
  const fetcher = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input));
    const override = intercept?.(url, init);
    if (override !== undefined) return override;
    switch (url.pathname) {
      case "/api/v1/status": return json(data.status);
      case "/api/v1/profiles": return json(data.profiles);
      case "/api/v1/usage": return json({ ...data.usage, period: url.searchParams.get("period") });
      case "/api/v1/activity": return json(data.activity);
      case "/api/v1/coordinators": return json(data.coordinators);
      default: return json({ error: "unexpected route" }, 404);
    }
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
function submitConnection(value = token) {
  fireEvent.change(screen.getByLabelText("API token"), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: "Connect" }));
}
function pendingResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((complete) => { resolve = complete; });
  return { promise, resolve };
}
beforeEach(() => {
  window.history.replaceState(null, "", "/");
  window.localStorage.clear();
  resetConnectionForTests();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Dashboard connection and overview", () => {
  it("starts disconnected, preserves routes, and makes no invented requests", async () => {
    const fetcher = mockAPI();
    render(<DashboardPage />);
    expect(await screen.findByRole("heading", { name: "Connect to caam" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
    expect(screen.queryByText("API Calls Today")).not.toBeInTheDocument();
    expect(screen.queryByText("Admin")).not.toBeInTheDocument();
    for (const [name, href] of [["Dashboard", "/"], ["Profiles", "/profiles"], ["Coordinators", "/coordinators"], ["Activity", "/activity"], ["Usage", "/usage"], ["Settings", "/settings"]]) {
      expect(screen.getByRole("link", { name })).toHaveAttribute("href", href);
    }
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("connects to actual data without persisting credentials", async () => {
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const fetcher = mockAPI();
    render(<DashboardPage />);
    submitConnection();
    expect(await screen.findByText("work · work@example.test")).toBeInTheDocument();
    expect(screen.getByText("warning or critical health")).toBeInTheDocument();
    expect(screen.getByText("Build host")).toBeInTheDocument();
    expect(screen.getByText("Coordinator connection unavailable")).toBeInTheDocument();
    expect(screen.getByText("2/7")).toBeInTheDocument();
    expect(screen.getByText("1/2")).toBeInTheDocument();
    expect(screen.getByText("agy")).toBeInTheDocument();
    expect(screen.getByText("Saved profiles").parentElement).toHaveTextContent("2");
    expect(storage).not.toHaveBeenCalled();
    for (const [url, init] of fetcher.mock.calls) {
      expect(String(url)).not.toContain(token);
      expect(init).toMatchObject({ credentials: "omit", redirect: "error", cache: "no-store", referrerPolicy: "no-referrer", headers: { Authorization: `Bearer ${token}` } });
    }
  });

  it("consumes and scrubs the CLI fragment before any request, without storage", async () => {
    const storage = vi.spyOn(Storage.prototype, "setItem");
    window.history.replaceState(null, "", "/#api=http%3A%2F%2F127.0.0.1%3A7892&token=fragment-fixture");
    mockAPI(undefined, () => { expect(window.location.hash).toBe(""); return undefined; });
    render(<DashboardPage />);
    expect(await screen.findByText("work · work@example.test")).toBeInTheDocument();
    expect(storage).not.toHaveBeenCalled();
    cleanup();
    resetConnectionForTests();
    render(<DashboardPage />);
    expect(await screen.findByRole("heading", { name: "Connect to caam" })).toBeInTheDocument();
  });

  it("scrubs a rejected remote fragment and never sends its token", async () => {
    const fetcher = mockAPI();
    window.history.replaceState(null, "", "/#api=https%3A%2F%2Fevil.example&token=fixture");
    render(<DashboardPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("loopback");
    expect(window.location.hash).toBe("");
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("consumes later same-document handoffs and scrubs rejected destinations", async () => {
    const fetcher = mockAPI(undefined, () => { expect(window.location.hash).toBe(""); return undefined; });
    connect(conn);
    const view = render(<><DashboardPage /><ConnectionObserver /></>);
    await screen.findByText("work · work@example.test");
    view.rerender(<><DashboardPage /></>);
    await act(async () => {
      window.history.replaceState(null, "", "/#token=new-fragment-fixture");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    await screen.findByText("work · work@example.test");
    expect(fetcher.mock.calls.some(([, init]) => new Headers(init?.headers).get("Authorization") === "Bearer new-fragment-fixture")).toBe(true);
    const beforeInvalid = fetcher.mock.calls.length;
    await act(async () => {
      window.history.replaceState(null, "", "/#api=https%3A%2F%2Fevil.example&token=remote-fixture");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(await screen.findByRole("alert")).toHaveTextContent("loopback");
    expect(window.location.hash).toBe("");
    expect(fetcher).toHaveBeenCalledTimes(beforeInvalid);
  });

  it("forgets legacy persisted credentials instead of loading them", async () => {
    window.localStorage.setItem("caam.connection", JSON.stringify(conn));
    const fetcher = mockAPI();
    render(<DashboardPage />);
    expect(await screen.findByRole("heading", { name: "Connect to caam" })).toBeInTheDocument();
    expect(window.localStorage.getItem("caam.connection")).toBeNull();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("rejects a remote pasted origin before sending a credential", async () => {
    const fetcher = mockAPI();
    render(<DashboardPage />);
    fireEvent.change(await screen.findByLabelText("API address"), { target: { value: "https://evil.example" } });
    submitConnection();
    expect(screen.getByRole("alert")).toHaveTextContent("loopback");
    expect(fetcher).not.toHaveBeenCalled();
  });

  it.each([401, 403])("clears the session and private data after HTTP %s", async (status) => {
    mockAPI(undefined, () => json({ error: "invalid token" }, status));
    connect(conn);
    render(<DashboardPage />);
    expect(await screen.findByText("caam rejected the token. Paste the current one.")).toBeInTheDocument();
    expect(screen.getByLabelText("API token")).toHaveValue("");
    expect(screen.queryByText("work · work@example.test")).not.toBeInTheDocument();
  });

  it("shows independent data and actionable errors for partial failures", async () => {
    mockAPI(undefined, (url) => url.pathname.endsWith("/profiles") ? json({ error: "vault unavailable" }, 503) : undefined);
    connect(conn);
    render(<DashboardPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("vault unavailable");
    expect(await screen.findByText("Build host")).toBeInTheDocument();
    expect(screen.getByText("Saved profiles").parentElement).toHaveTextContent("—");
  });

  it("keeps failed reads explicitly stale after a successful response", async () => {
    let failed = false;
    mockAPI(undefined, (url) => failed && url.pathname.endsWith("/coordinators") ? json({ error: "agent unavailable" }, 503) : undefined);
    connect(conn);
    render(<DashboardPage />);
    await screen.findByText("Build host");
    failed = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Showing the last successful result");
    expect(screen.getByText("Build host")).toBeInTheDocument();
  });

  it("disconnects from Settings and clears the header search before reconnecting", async () => {
    mockAPI();
    connect(conn);
    render(<SettingsPage />);
    await screen.findByText("held only in this tab's memory; cleared on disconnect or reload");
    fireEvent.change(screen.getByLabelText("Search profiles"), { target: { value: "private search" } });
    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    expect(await screen.findByLabelText("API token")).toHaveValue("");
    expect(screen.getByLabelText("Search profiles")).toHaveValue("");
    submitConnection();
    expect(await screen.findByRole("heading", { name: "Settings" })).toBeInTheDocument();
  });

  it("ignores a disconnected session even when reconnected with the same endpoint and token", async () => {
    const old = pendingResponse();
    const data = fixtures();
    let first = true;
    const fetcher = mockAPI(data, (url) => first && url.pathname.endsWith("/status") ? old.promise : undefined);
    connect(conn);
    render(<DashboardPage />);
    const oldSignal = fetcher.mock.calls.find(([url]) => String(url).endsWith("/status"))?.[1]?.signal;
    first = false;
    data.status.tools[0].active_profile = "new-session";
    await act(async () => { disconnect(); connect(conn); });
    expect(await screen.findByText("new-session · work@example.test")).toBeInTheDocument();
    expect(oldSignal?.aborted).toBe(true);
    await act(async () => { old.resolve(json(fixtures().status)); });
    expect(screen.queryByText("work · work@example.test")).not.toBeInTheDocument();
    expect(screen.getByText("new-session · work@example.test")).toBeInTheDocument();
  });

  it("polls for outside changes and stops after disconnect", async () => {
    vi.useFakeTimers();
    const data = fixtures();
    const fetcher = mockAPI(data);
    connect(conn);
    render(<DashboardPage />);
    await act(async () => {});
    data.status.tools[0].active_profile = "external-change";
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(screen.getByText("external-change · work@example.test")).toBeInTheDocument();
    act(() => disconnect());
    const stoppedAt = fetcher.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(fetcher).toHaveBeenCalledTimes(stoppedAt);
  });
});

describe("Recorded usage and recent activity", () => {
  it("shows measured aggregates and distinguishes an unavailable DB from empty history", async () => {
    const data = fixtures();
    mockAPI(data);
    connect(conn);
    render(<UsagePage />);
    expect(await screen.findByRole("table", { name: "Recorded CAAM activity" })).toBeInTheDocument();
    expect(screen.getByText("1h 30m")).toBeInTheDocument();
    data.usage.available = false;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText(/Activity database unavailable/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    data.usage.available = true;
    data.usage.usage = [];
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("No CAAM activity recorded in this period.")).toBeInTheDocument();
  });

  it("aborts and ignores a late previous-period result", async () => {
    const old = pendingResponse();
    const data = fixtures();
    const fetcher = mockAPI(data, (url) => {
      if (!url.pathname.endsWith("/usage")) return undefined;
      if (url.searchParams.get("period") === "24h") return old.promise;
      return json({ ...data.usage, period: "7d", usage: [{ ...data.usage.usage[0], profile: "current-period" }] });
    });
    connect(conn);
    render(<UsagePage />);
    fireEvent.change(await screen.findByRole("combobox", { name: "Activity period" }), { target: { value: "7d" } });
    expect(await screen.findByText("grok/current-period")).toBeInTheDocument();
    const signal = fetcher.mock.calls.find(([url]) => String(url).endsWith("usage?period=24h"))?.[1]?.signal;
    expect(signal?.aborted).toBe(true);
    await act(async () => { old.resolve(json({ ...data.usage, usage: [{ ...data.usage.usage[0], profile: "stale-period" }] })); });
    expect(screen.queryByText("grok/stale-period")).not.toBeInTheDocument();
  });

  it("preserves recent switches and cooldowns with a distinct unavailable state", async () => {
    const data = fixtures();
    mockAPI(data);
    connect(conn);
    render(<ActivityPage />);
    expect(await screen.findByText("Switched grok from old to fresh (rate limit)")).toBeInTheDocument();
    expect(screen.getByText("Cooling down")).toBeInTheDocument();
    data.activity.available = false;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText(/Activity database unavailable/)).toBeInTheDocument();
    expect(screen.queryByText("Cooling down")).not.toBeInTheDocument();
    data.activity.available = true;
    data.activity.events = [];
    data.activity.cooldowns = [];
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("Nothing recorded yet.")).toBeInTheDocument();
  });

  it("filters recorded usage by the selected provider", async () => {
    mockAPI();
    connect(conn);
    render(<UsagePage />);
    const table = await screen.findByRole("table", { name: "Recorded CAAM activity" });
    fireEvent.change(screen.getByRole("combobox", { name: "Provider filter" }), { target: { value: "grok" } });
    expect(within(table).getByText("grok/fresh")).toBeInTheDocument();
  });
});
