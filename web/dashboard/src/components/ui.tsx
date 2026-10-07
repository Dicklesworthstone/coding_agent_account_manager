"use client";

import { AlertCircle, CheckCircle2, CircleDashed, XCircle } from "lucide-react";
import type { ApiError } from "@/lib/api";

export function Panel({
  title,
  action,
  children,
}: {
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="rounded-xl border border-border bg-surface">
      <div className="flex items-center justify-between border-b border-border px-6 py-4">
        <h2 className="font-semibold">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

export function PageHeader({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div>
      <h1 className="text-2xl font-semibold">{title}</h1>
      <p className="mt-1 text-muted">{subtitle}</p>
    </div>
  );
}

type Tone = "good" | "warn" | "bad" | "neutral";

const toneClasses: Record<Tone, string> = {
  good: "bg-success/10 text-success",
  warn: "bg-warning/10 text-warning",
  bad: "bg-danger/10 text-danger",
  neutral: "bg-surface-muted text-muted",
};

const toneIcons: Record<Tone, React.ReactNode> = {
  good: <CheckCircle2 className="h-3.5 w-3.5" />,
  warn: <AlertCircle className="h-3.5 w-3.5" />,
  bad: <XCircle className="h-3.5 w-3.5" />,
  neutral: <CircleDashed className="h-3.5 w-3.5" />,
};

/** How a health or coordinator status reads. */
export function statusTone(status: string | undefined): Tone {
  switch (status) {
    case "healthy":
    case "agent_running":
      return "good";
    case "warning":
    case "pending":
      return "warn";
    case "critical":
    case "unreachable":
    case "agent_not_running":
      return "bad";
    default:
      return "neutral";
  }
}

export function StatusChip({ status, label }: { status: string | undefined; label?: string }) {
  const tone = statusTone(status);
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${toneClasses[tone]}`}
    >
      {toneIcons[tone]}
      {label ?? (status ? status.replaceAll("_", " ") : "unknown")}
    </span>
  );
}

export function ErrorBanner({ error }: { error: ApiError | undefined }) {
  if (!error || error.unauthorized) {
    return null;
  }
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger/10 px-4 py-3 text-sm text-danger"
    >
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{error.message}</span>
    </div>
  );
}

export function EmptyState({ children }: { children: React.ReactNode }) {
  return <div className="px-6 py-10 text-center text-sm text-muted">{children}</div>;
}

export function Loading() {
  return <div className="px-6 py-10 text-center text-sm text-muted">Loading…</div>;
}

/** "3 min ago" for an RFC 3339 time; "" for none. */
export function timeAgo(iso: string | undefined, now = Date.now()): string {
  if (!iso) {
    return "";
  }
  const then = Date.parse(iso);
  if (Number.isNaN(then) || then <= 0) {
    return "";
  }
  const seconds = Math.round((now - then) / 1000);
  if (seconds < 60) {
    return seconds < 0 ? "just now" : `${seconds}s ago`;
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes} min ago`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 48) {
    return `${hours} h ago`;
  }
  return `${Math.round(hours / 24)} d ago`;
}
