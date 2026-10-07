"use client";

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import {
  ApiError,
  type Connection,
  connectionFromFragment,
  validateConnection,
} from "./api";

// The connection to `caam serve` lives in a small external store, shared by
// every route. Credentials stay in this tab's memory, never in browser storage.
interface ConnectionSnapshot {
  generation: number;
  conn: Connection | null;
  /** The optional CLI handoff has been read (false during the first render). */
  loaded: boolean;
  /** The API rejected the token; the user has to reconnect. */
  rejected: boolean;
  error?: string;
}

let snapshot: ConnectionSnapshot = { generation: 0, conn: null, loaded: false, rejected: false };
const listeners = new Set<() => void>();

function setSnapshot(next: Omit<ConnectionSnapshot, "generation">) {
  snapshot = { ...next, generation: snapshot.generation + 1 };
  for (const listener of listeners) {
    listener();
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

const serverSnapshot: ConnectionSnapshot = { generation: 0, conn: null, loaded: false, rejected: false };

function forgetLegacyConnection() {
  try {
    window.localStorage.removeItem("caam.connection");
  } catch {
    // Storage may be unavailable; it is never needed for a connection.
  }
}

/**
 * Loads the connection once in the browser: a token handed over in the URL
 * fragment is scrubbed before validation or any authenticated request starts.
 */
export function initConnection() {
  if (typeof window === "undefined") {
    return;
  }
  const hash = window.location.hash;
  const hasToken = new URLSearchParams(hash.replace(/^#/, "")).has("token");
  if (snapshot.loaded && !hasToken) return;
  if (hasToken) {
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
  }
  forgetLegacyConnection();
  try {
    setSnapshot({ conn: connectionFromFragment(hash), loaded: true, rejected: false });
  } catch (error) {
    setSnapshot({ conn: null, loaded: true, rejected: false, error: error instanceof Error ? error.message : "Invalid connection link." });
  }
}

export function connect(conn: Connection) {
  setSnapshot({ conn: validateConnection(conn), loaded: true, rejected: false });
}

export function disconnect() {
  setSnapshot({ conn: null, loaded: true, rejected: false });
}

export function isCurrentConnection(conn: Connection): boolean {
  return snapshot.conn === conn;
}

export function rejectConnection(conn: Connection) {
  if (isCurrentConnection(conn)) {
    setSnapshot({ conn: null, loaded: true, rejected: true });
  }
}

/** Resets the store (tests). */
export function resetConnectionForTests() {
  setSnapshot({ conn: null, loaded: false, rejected: false });
}

export function useConnection(): ConnectionSnapshot {
  const state = useSyncExternalStore(subscribe, () => snapshot, () => serverSnapshot);
  useEffect(() => {
    initConnection();
    const onHashChange = () => initConnection();
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);
  return state;
}

export interface ApiState<T> {
  data?: T;
  error?: ApiError;
  loading: boolean;
  checkedAt?: string;
  reload: () => void;
}

/**
 * Fetches fetcher's data with the current connection and refreshes it every
 * intervalMs. A rejected token marks the connection for reconnecting.
 * fetcher must be stable (a module-level function or a memoized period query).
 */
export function useApi<T>(
  fetcher: (conn: Connection, signal: AbortSignal) => Promise<T>,
  intervalMs = 15_000,
): ApiState<T> {
  const { conn } = useConnection();
  const [result, setResult] = useState<{
    data?: T; error?: ApiError; for?: Connection; fetcher?: typeof fetcher; tick?: number; checkedAt?: string;
  }>({});
  const [tick, setTick] = useState(0);
  const [polling, setPolling] = useState(false);
  const generation = useRef(0);

  useEffect(() => {
    if (!conn) {
      return;
    }
    let controller: AbortController | undefined;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    const run = () => {
      const requestGeneration = ++generation.current;
      controller?.abort();
      clearTimeout(timeout);
      controller = new AbortController();
      const requestController = controller;
      let timedOut = false;
      timeout = setTimeout(() => { timedOut = true; requestController.abort(); }, 10_000);
      fetcher(conn, requestController.signal).then(
        (data) => {
          if (requestGeneration === generation.current && isCurrentConnection(conn)) {
            setResult({ data, for: conn, fetcher, tick, checkedAt: new Date().toISOString() });
          }
        },
        (err: unknown) => {
          if (requestGeneration !== generation.current || !isCurrentConnection(conn)) {
            return;
          }
          const error = timedOut
            ? new ApiError(0, "The local API did not respond within 10 seconds. Check caam serve and refresh.")
            : err instanceof ApiError ? err : new ApiError(0, "The API returned an unexpected response.");
          if (error.unauthorized) {
            rejectConnection(conn);
            return;
          }
          setResult((prev) => ({
            data: prev.for === conn && prev.fetcher === fetcher ? prev.data : undefined,
            error, for: conn, fetcher, tick, checkedAt: new Date().toISOString(),
          }));
        },
      ).finally(() => {
        if (requestGeneration === generation.current) {
          clearTimeout(timeout);
          setPolling(false);
        }
      });
    };
    run();
    const id = setInterval(() => { setPolling(true); run(); }, intervalMs);
    return () => {
      generation.current += 1;
      controller?.abort();
      clearTimeout(timeout);
      clearInterval(id);
    };
  }, [conn, fetcher, intervalMs, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  const current = result.for === conn && result.fetcher === fetcher;
  return {
    data: current ? result.data : undefined,
    error: current ? result.error : undefined,
    loading: !!conn && (!current || result.tick !== tick || polling),
    checkedAt: current ? result.checkedAt : undefined,
    reload,
  };
}
