import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, resetConnectionForTests } from "@/lib/connection";
import ProfilesPage from "./page";

vi.mock("next/navigation", () => ({
  usePathname: () => "/profiles",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  useSearchParams: () => new URLSearchParams("q=claude"),
}));

vi.mock("framer-motion", () => ({
  motion: {
    div: ({ children }: React.PropsWithChildren) => <div>{children}</div>,
  },
  AnimatePresence: ({ children }: React.PropsWithChildren) => <>{children}</>,
}));

const profiles = {
  count: 3,
  profiles: [
    { tool: "claude", name: "work", active: true, system: false, identity: { email: "a@example.com", provider: "claude" } },
    { tool: "claude", name: "home", active: false, system: false, identity: { email: "b@example.com", provider: "claude" } },
    { tool: "codex", name: "main", active: true, system: false },
  ],
};

let posts: { path: string; body: unknown }[] = [];

beforeEach(() => {
  window.localStorage.clear();
  resetConnectionForTests();
  posts = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const path = new URL(url).pathname;
      if (init?.method === "POST") {
        const body = JSON.parse(init.body as string) as { tool: string; profile: string };
        posts.push({ path, body });
        if (path.endsWith("/activate")) {
          return new Response(
            JSON.stringify({ success: true, tool: body.tool, profile: body.profile, auto_backup: "_auto_1" }),
          );
        }
        return new Response(JSON.stringify({ success: true, tool: body.tool, profile: body.profile }));
      }
      if (path === "/api/v1/status") {
        return new Response(
          JSON.stringify({ version: "1", timestamp: "", tools: [{ tool: "claude", logged_in: true }] }),
        );
      }
      return new Response(JSON.stringify(profiles));
    }),
  );
  connect({ baseUrl: "http://127.0.0.1:7892", token: "tok" });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ProfilesPage", () => {
  it("filters by the search from the header", async () => {
    render(<ProfilesPage />);
    expect(await screen.findByText("2 of 3 profiles")).toBeInTheDocument();
    expect(screen.queryByText("main")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Filter profiles"), { target: { value: "b@example" } });
    expect(screen.getByText("1 of 3 profiles")).toBeInTheDocument();
  });

  it("activates a profile only after confirmation", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(<ProfilesPage />);
    const button = await screen.findByRole("button", { name: "Activate claude profile home" });

    fireEvent.click(button);
    expect(posts).toHaveLength(0);

    fireEvent.click(button);
    await waitFor(() => expect(posts).toEqual([{ path: "/api/v1/actions/activate", body: { tool: "claude", profile: "home" } }]));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(await screen.findByText("The previous login was saved as _auto_1.")).toBeInTheDocument();
  });

  it("saves the current login under a new name", async () => {
    render(<ProfilesPage />);
    fireEvent.change(await screen.findByLabelText("Profile name"), { target: { value: "spare" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(posts).toEqual([{ path: "/api/v1/actions/backup", body: { tool: "claude", profile: "spare" } }]));
  });
});
