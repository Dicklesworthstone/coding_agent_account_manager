import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, disconnect, resetConnectionForTests } from "@/lib/connection";
import type { ProfileInfo } from "@/lib/api";
import ProfilesPage from "./page";

vi.mock("next/navigation", () => ({
  usePathname: () => "/profiles",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams("q=claude"),
}));
vi.mock("framer-motion", () => ({
  motion: { div: ({ children }: React.PropsWithChildren) => <div>{children}</div> },
  AnimatePresence: ({ children }: React.PropsWithChildren) => <>{children}</>,
}));

const conn = { baseUrl: "http://127.0.0.1:7892", token: "profile-fixture-token" };
function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status });
}
type Body = { tool: string; profile: string; overwrite?: boolean };
let saved: ProfileInfo[];
let posts: { path: string; body: Body; signal?: AbortSignal | null }[];
let postReply: (path: string, body: Body) => Response | Promise<Response>;
let profileFailure: boolean;
let profileReads: number;

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, "", "/profiles");
  resetConnectionForTests();
  saved = [
    { tool: "claude", name: "work", active: true, system: false, identity: { email: "a@example.test" } },
    { tool: "claude", name: "home", active: false, system: false, identity: { email: "b@example.test" }, health: { status: "warning", cooldown_remaining: "4m" } },
    { tool: "codex", name: "main", active: true, system: false },
    { tool: "claude", name: "_backup_1", active: false, system: true },
  ];
  posts = [];
  profileFailure = false;
  profileReads = 0;
  postReply = (path, body) => {
    if (path.endsWith("/activate")) {
      saved.forEach((profile) => { if (profile.tool === body.tool) profile.active = profile.name === body.profile; });
      return json({ success: true, tool: body.tool, profile: body.profile, auto_backup: "_auto_1", warnings: ["Previous live login preserved"] });
    }
    if (!saved.some((profile) => profile.tool === body.tool && profile.name === body.profile)) {
      saved.push({ tool: body.tool, name: body.profile, active: false, system: false });
    }
    return json({ success: true, tool: body.tool, profile: body.profile });
  };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input)).pathname;
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Body;
      posts.push({ path, body, signal: init.signal });
      return postReply(path, body);
    }
    if (path === "/api/v1/status") return json({ version: "1", timestamp: "2026-10-07T18:00:00Z",
      tools: ["claude", "codex", "cursor", "gemini", "grok", "opencode", "agy"].map((tool) => ({ tool, logged_in: tool === "claude" })) });
    profileReads += 1;
    return profileFailure ? json({ error: "vault unavailable" }, 503) : json({ count: saved.length, profiles: saved });
  }));
  connect(conn);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function loaded() {
  render(<ProfilesPage />);
  return screen.findByRole("table", { name: "Saved profiles" });
}

describe("ProfilesPage", () => {
  it("preserves header search and adds provider/recovery filters with actual health", async () => {
    const table = await loaded();
    expect(screen.getByText("2 of 3 profiles")).toBeInTheDocument();
    expect(within(table).queryByText("main")).not.toBeInTheDocument();
    expect(screen.getByText("Cooldown: 4m")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Filter profiles"), { target: { value: "b@example" } });
    expect(screen.getByText("1 of 3 profiles")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Filter profiles"), { target: { value: "" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Provider filter" }), { target: { value: "codex" } });
    expect(within(table).getByText("main")).toBeInTheDocument();
    expect(within(table).queryByText("home")).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: "Provider filter" }), { target: { value: "" } });
    expect(screen.getByRole("option", { name: "agy" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("checkbox", { name: "Show recovery snapshots" }));
    expect(screen.getByRole("button", { name: "Activate claude profile _backup_1" })).toBeDisabled();
  });

  it("activates only after confirmation, displays warnings, and refetches active state", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const table = await loaded();
    const button = within(table).getByRole("button", { name: "Activate claude profile home" });
    const beforeReads = profileReads;
    fireEvent.click(button);
    expect(posts).toHaveLength(0);
    fireEvent.click(button);
    expect(await screen.findByText("The previous login was saved as _auto_1.")).toBeInTheDocument();
    expect(screen.getByText("Previous live login preserved")).toBeInTheDocument();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(posts.map(({ path, body }) => ({ path, body }))).toEqual([{ path: "/api/v1/actions/activate", body: { tool: "claude", profile: "home" } }]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Activate claude profile home" })).not.toBeInTheDocument());
    expect(profileReads).toBeGreaterThan(beforeReads);
  });

  it("keeps HTTP 200 success:false visibly unsuccessful without a fabricated success fallback", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    postReply = () => json({ success: false });
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Activate claude profile home" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("No success was reported");
    expect(screen.queryByText("claude now uses home")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Activate claude profile home" })).toBeEnabled();
  });

  it("backs up the current live login only after exact new-name confirmation", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    await loaded();
    fireEvent.change(screen.getByLabelText("Profile name"), { target: { value: "spare" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(posts).toHaveLength(0);
    expect(confirm).toHaveBeenLastCalledWith("Copy the current live claude login into the saved snapshot claude/spare?");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved the current claude login as spare")).toBeInTheDocument();
    expect(posts[0].body).toEqual({ tool: "claude", profile: "spare" });
    expect(screen.getByLabelText("Profile name")).toHaveValue("");
    expect(await within(screen.getByRole("table", { name: "Saved profiles" })).findByText("spare")).toBeInTheDocument();
  });

  it("requires replacement opt-in plus a specific confirmation and preserves a failed name", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    postReply = () => json({ success: false, message: "No current live login to back up" });
    await loaded();
    fireEvent.change(screen.getByLabelText("Profile name"), { target: { value: "home" } });
    expect(screen.getByRole("button", { name: "Replace snapshot" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "Replace the existing snapshot claude/home with the current live login." }));
    fireEvent.click(screen.getByRole("button", { name: "Replace snapshot" }));
    expect(confirm).toHaveBeenCalledWith("Copy the current live claude login into the saved snapshot claude/home? This replaces the credentials already saved under that name.");
    expect(await screen.findByRole("alert")).toHaveTextContent("No current live login to back up");
    expect(posts[0].body).toEqual({ tool: "claude", profile: "home", overwrite: true });
    expect(screen.getByLabelText("Profile name")).toHaveValue("home");
  });

  it("never permits replacement of a known recovery snapshot", async () => {
    const confirm = vi.spyOn(window, "confirm");
    await loaded();
    fireEvent.change(screen.getByLabelText("Profile name"), { target: { value: "_backup_1" } });
    expect(screen.getByText("Choose a new name. Recovery snapshots cannot be replaced here.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace snapshot" })).toBeDisabled();
    expect(confirm).not.toHaveBeenCalled();
    expect(posts).toHaveLength(0);
  });

  it("disables mutations when the profile snapshot cannot be refreshed", async () => {
    await loaded();
    profileFailure = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Showing the last successful result");
    expect(screen.getByRole("button", { name: "Activate claude profile home" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Profile name"), { target: { value: "new-name" } });
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("does not replay an unacknowledged action", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    postReply = () => { throw new TypeError("connection lost"); };
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Activate claude profile home" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The operation may have completed");
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled());
    expect(posts).toHaveLength(1);
  });

  it("aborts an old action and ignores its acknowledgement after an identical reconnection", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let resolve!: (response: Response) => void;
    postReply = () => new Promise<Response>((complete) => { resolve = complete; });
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Activate claude profile home" }));
    await act(async () => { disconnect(); connect(conn); });
    await screen.findByRole("table", { name: "Saved profiles" });
    expect(posts[0].signal?.aborted).toBe(true);
    expect(screen.getByRole("button", { name: "Activate claude profile home" })).toBeEnabled();
    await act(async () => { resolve(json({ success: true, message: "stale success" })); });
    expect(screen.queryByText("stale success")).not.toBeInTheDocument();
  });
});
