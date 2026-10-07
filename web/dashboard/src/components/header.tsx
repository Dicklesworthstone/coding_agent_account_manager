"use client";

import { Search } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { useConnection } from "@/lib/connection";

export function Header() {
  const router = useRouter();
  const { conn } = useConnection();
  const [query, setQuery] = useState("");
  return (
    <header className="flex min-h-16 shrink-0 items-center justify-between gap-4 border-b border-border bg-surface px-4 py-3 sm:px-6">
      {/* Search jumps to the matching profiles */}
      <form
        role="search"
        className="flex flex-1 items-center gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          const q = query.trim();
          router.push(q ? `/profiles?q=${encodeURIComponent(q)}` : "/profiles");
        }}
      >
        <div className="relative max-w-md flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted" />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search profiles…"
            aria-label="Search profiles"
            className="h-10 w-full rounded-lg border border-border bg-background pl-10 pr-4 text-sm placeholder:text-muted focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </div>
      </form>

      {/* Connection */}
      <div className="flex min-w-0 items-center gap-2 text-xs text-muted">
        <span
          className={`h-2 w-2 rounded-full ${conn ? "bg-success" : "bg-border"}`}
          aria-hidden="true"
        />
        <span className="truncate">{conn ? conn.baseUrl.replace(/^https?:\/\//, "") : "not connected"}</span>
      </div>
    </header>
  );
}
