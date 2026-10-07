"use client";

import { PlugZap } from "lucide-react";
import { useState } from "react";
import { DEFAULT_API_BASE, normalizeBaseUrl } from "@/lib/api";
import { connect } from "@/lib/connection";

/** Asks for the `caam serve` address and API token. */
export function ConnectForm({ rejected }: { rejected: boolean }) {
  const [baseUrl, setBaseUrl] = useState(DEFAULT_API_BASE);
  const [token, setToken] = useState("");

  return (
    <div className="mx-auto mt-16 max-w-lg rounded-xl border border-border bg-surface p-8">
      <div className="flex items-center gap-3">
        <div className="rounded-lg bg-accent/10 p-2 text-accent">
          <PlugZap className="h-6 w-6" />
        </div>
        <h1 className="text-xl font-semibold">Connect to caam</h1>
      </div>
      <p className="mt-4 text-sm text-muted">
        The dashboard reads and acts through the local caam API. Start it and print its token
        with:
      </p>
      <pre className="mt-2 rounded-lg bg-surface-muted px-3 py-2 font-mono text-sm">
        caam serve --show-token
      </pre>
      <p className="mt-2 text-sm text-muted">
        Or open the link <code className="font-mono">caam serve --dashboard-url {"<this address>"}</code>{" "}
        prints, which connects without pasting anything.
      </p>
      {rejected && (
        <p role="alert" className="mt-4 rounded-lg bg-danger/10 px-3 py-2 text-sm text-danger">
          caam rejected the saved token. Paste the current one.
        </p>
      )}
      <form
        className="mt-6 space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (token.trim()) {
            connect({ baseUrl: normalizeBaseUrl(baseUrl), token: token.trim() });
          }
        }}
      >
        <label className="block text-sm font-medium">
          API address
          <input
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            className="mt-1 h-10 w-full rounded-lg border border-border bg-background px-3 text-sm focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </label>
        <label className="block text-sm font-medium">
          API token
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
            className="mt-1 h-10 w-full rounded-lg border border-border bg-background px-3 font-mono text-sm focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent"
          />
        </label>
        <button
          type="submit"
          disabled={!token.trim()}
          className="h-10 w-full rounded-lg bg-accent text-sm font-medium text-accent-foreground transition-opacity disabled:opacity-50"
        >
          Connect
        </button>
      </form>
    </div>
  );
}
