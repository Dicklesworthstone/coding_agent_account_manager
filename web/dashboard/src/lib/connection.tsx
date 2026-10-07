"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import {
  ApiError,
  type Connection,
  connectionFromFragment,
  loadConnection,
  saveConnection,
} from "./api";

// The connection to `caam serve` lives in a small external store, shared by
// every component and kept in localStorage.
interface ConnectionSnapshot {
  conn: Connection | null;
  /** The stored connection has been read (false during the first render). */
  loaded: boolean;
  /** The API rejected the token; the user has to reconnect. */
  rejected: boolean;
}

let snapshot: ConnectionSnapshot = { conn: null, loaded: false, rejected: false };
const listeners = new Set<() => void>();

function setSnapshot(next: ConnectionSnapshot) {
  snapshot = next;
  for (const listener of listeners) {
    listener();
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

const serverSnapshot: ConnectionSnapshot = { conn: null, loaded: false, rejected: false };

function storage(): Storage | undefined {
  try {
    return typeof window === "undefined" ? undefined : window.localStorage;
  } catch {
    return undefined;
  }
}

/**
 * Loads the connection once in the browser: a token handed over in the URL
 * fragment (then removed from the address bar) wins over the stored one.
 */
export function initConnection() {
  if (snapshot.loaded || typeof window === "undefined") {
    return;
  }
  const fromFragment = connectionFromFragment(window.location.hash);
  if (fromFragment) {
    saveConnection(storage(), fromFragment);
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
  }
  setSnapshot({ conn: fromFragment ?? loadConnection(storage()), loaded: true, rejected: false });
}

export function connect(conn: Connection) {
  saveConnection(storage(), conn);
  setSnapshot({ conn, loaded: true, rejected: false });
}

export function disconnect() {
  saveConnection(storage(), null);
  setSnapshot({ conn: null, loaded: true, rejected: false });
}

function markRejected() {
  if (!snapshot.rejected) {
    setSnapshot({ ...snapshot, rejected: true });
  }
}

/** Resets the store (tests). */
export function resetConnectionForTests() {
  setSnapshot({ conn: null, loaded: false, rejected: false });
}

export function useConnection(): ConnectionSnapshot {
  const state = useSyncExternalStore(subscribe, () => snapshot, () => serverSnapshot);
  useEffect(initConnection, []);
  return state;
}

export interface ApiState<T> {
  data?: T;
  error?: ApiError;
  loading: boolean;
  reload: () => void;
}

/**
 * Fetches fetcher's data with the current connection and refreshes it every
 * intervalMs. A rejected token marks the connection for reconnecting.
 * fetcher must be stable (a module-level function).
 */
export function useApi<T>(
  fetcher: (conn: Connection) => Promise<T>,
  intervalMs = 10_000,
): ApiState<T> {
  const { conn } = useConnection();
  const [result, setResult] = useState<{ data?: T; error?: ApiError; for?: Connection }>({});
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!conn) {
      return;
    }
    let cancelled = false;
    const run = () => {
      fetcher(conn).then(
        (data) => {
          if (!cancelled) {
            setResult({ data, for: conn });
          }
        },
        (err: unknown) => {
          if (cancelled) {
            return;
          }
          const error = err instanceof ApiError ? err : new ApiError(0, String(err));
          if (error.unauthorized) {
            markRejected();
          }
          setResult((prev) => ({ data: prev.for === conn ? prev.data : undefined, error, for: conn }));
        },
      );
    };
    run();
    const id = setInterval(run, intervalMs);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [conn, fetcher, intervalMs, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  const current = result.for === conn;
  return {
    data: current ? result.data : undefined,
    error: current ? result.error : undefined,
    loading: !!conn && !current,
    reload,
  };
}
