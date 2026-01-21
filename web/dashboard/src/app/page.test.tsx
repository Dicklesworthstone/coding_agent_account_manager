import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
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
    div: ({
      children,
      ...props
    }: React.PropsWithChildren<React.HTMLAttributes<HTMLDivElement>>) => (
      <div {...props}>{children}</div>
    ),
  },
  AnimatePresence: ({ children }: React.PropsWithChildren) => <>{children}</>,
}));

describe("DashboardPage", () => {
  it("renders the dashboard header", () => {
    render(<DashboardPage />);
    expect(screen.getByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
  });

  it("renders stat cards", () => {
    render(<DashboardPage />);
    expect(screen.getByText("Active Profiles")).toBeInTheDocument();
    expect(screen.getByText("API Calls Today")).toBeInTheDocument();
    expect(screen.getByText("Active Credentials")).toBeInTheDocument();
    expect(screen.getByText("Sync Status")).toBeInTheDocument();
  });

  it("renders recent activity section", () => {
    render(<DashboardPage />);
    expect(screen.getByText("Recent Activity")).toBeInTheDocument();
  });

  it("renders quick actions section", () => {
    render(<DashboardPage />);
    expect(screen.getByText("Quick Actions")).toBeInTheDocument();
    expect(screen.getByText("Switch Profile")).toBeInTheDocument();
    expect(screen.getByText("Refresh Token")).toBeInTheDocument();
    expect(screen.getByText("Sync Now")).toBeInTheDocument();
    expect(screen.getByText("View Logs")).toBeInTheDocument();
  });
});
