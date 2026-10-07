import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, resetConnectionForTests } from "@/lib/connection";
import DashboardPage from "./page";

// Mock next/navigation
vi.mock("next/navigation", () => ({
  usePathname: () => "/",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
  }),
}));

// Mock framer-motion to avoid animation issues in tests
vi.mock("framer-motion", () => ({
  motion: {
    div: ({ children }: React.PropsWithChildren) => <div>{children}</div>,
  },
  AnimatePresence: ({ children }: React.PropsWithChildren) => <>{children}</>,
}));

const responses: Record<string, unknown> = {
  "/api/v1/status": {
    version: "1.2.3",
    timestamp: "2026-10-07T10:00:00Z",
    tools: [
      {
        tool: "claude",
        logged_in: true,
        active_profile: "work",
        identity: { email: "alice@example.com", provider: "claude" },
        health: { status: "healthy", error_count: 0, renewable: true },
      },
      { tool: "codex", logged_in: false },
    ],
  },
  "/api/v1/profiles": {
    count: 3,
    profiles: [
      { tool: "claude", name: "work", active: true, system: false, health: { status: "healthy", error_count: 0, renewable: true } },
      { tool: "claude", name: "home", active: false, system: false, health: { status: "critical", error_count: 2, renewable: false } },
      { tool: "claude", name: "_backup_1", active: false, system: true },
    ],
  },
  "/api/v1/coordinators": {
    coordinators: [
      { id: "csd", display_name: "build box", endpoint: "http://127.0.0.1:7890", transport: "ssh", status: "healthy" },
      { id: "gpu", endpoint: "http://127.0.0.1:7890", transport: "ssh", status: "unreachable", error: "ssh: connect timed out" },
    ],
  },
};

function stubApi(status = 200) {
  const fetchMock = vi.fn(async (url: string) => {
    const path = new URL(url).pathname;
    if (status !== 200) {
      return new Response(JSON.stringify({ error: "invalid token" }), { status });
    }
    return new Response(JSON.stringify(responses[path] ?? {}), { status: 200 });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  window.localStorage.clear();
  resetConnectionForTests();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DashboardPage", () => {
  it("asks to connect when no token is stored", async () => {
    render(<DashboardPage />);
    expect(await screen.findByRole("heading", { name: "Connect to caam" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  });

  it("connects with a pasted token and shows live data", async () => {
    const fetchMock = stubApi();
    render(<DashboardPage />);

    fireEvent.change(await screen.findByLabelText("API token"), { target: { value: "  tok  " } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
    expect(JSON.parse(window.localStorage.getItem("caam.connection") ?? "{}")).toMatchObject({ token: "tok" });

    // 1/2 tools logged in and 1/2 coordinators healthy; 2 saved profiles
    // (system snapshots excluded), 1 of them unhealthy.
    expect(await screen.findAllByText("1/2")).toHaveLength(2);
    expect(await screen.findByText("warning or critical health")).toBeInTheDocument();
    expect(screen.getByText("work · alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("build box")).toBeInTheDocument();
    expect(screen.getByText("ssh: connect timed out")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();

    const auth = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1].headers as Record<string, string>;
    expect(auth.Authorization).toBe("Bearer tok");
  });

  it("asks for a new token when caam rejects the stored one", async () => {
    stubApi(401);
    connect({ baseUrl: "http://127.0.0.1:7892", token: "stale" });
    render(<DashboardPage />);

    expect(await screen.findByText("caam rejected the saved token. Paste the current one.")).toBeInTheDocument();
  });

  it("shows when caam cannot be reached", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    connect({ baseUrl: "http://127.0.0.1:7892", token: "tok" });
    render(<DashboardPage />);

    await waitFor(() => expect(screen.getAllByRole("alert")[0]).toHaveTextContent("is `caam serve` running?"));
  });
});
