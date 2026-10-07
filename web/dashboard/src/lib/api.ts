// Client for the local caam API (`caam serve`). Types mirror internal/api.

export const DEFAULT_API_BASE = "http://127.0.0.1:7892";
const STORAGE_KEY = "caam.connection";

export interface Connection {
  baseUrl: string;
  token: string;
}

export interface Identity {
  email?: string;
  organization?: string;
  plan_type?: string;
  account_id?: string;
  expires_at?: string;
  provider: string;
}

export interface HealthStatus {
  status: "healthy" | "warning" | "critical" | "unknown" | string;
  expires_at?: string;
  error_count: number;
  cooldown_remaining?: string;
  renewable: boolean;
  recommendation?: string;
  refresh_due?: boolean | null;
  launch_usable?: boolean | null;
  login_required?: boolean | null;
}

export interface ToolStatus {
  tool: string;
  logged_in: boolean;
  active_profile?: string;
  health?: HealthStatus;
  identity?: Identity;
}

export interface StatusResponse {
  version: string;
  timestamp: string;
  tools: ToolStatus[];
}

export interface ProfileInfo {
  tool: string;
  name: string;
  active: boolean;
  system: boolean;
  health?: HealthStatus;
  identity?: Identity;
}

export interface ProfilesResponse {
  profiles: ProfileInfo[];
  count: number;
}

export interface UsageEntry {
  tool: string;
  profile: string;
  total_calls: number;
  error_count: number;
  last_used?: string;
}

export interface UsageResponse {
  tool?: string;
  period: string;
  usage: UsageEntry[];
}

export interface ActivityEvent {
  timestamp: string;
  type: "activate" | "login" | "refresh" | "error" | "switch" | "deactivate" | string;
  tool: string;
  profile: string;
  details?: Record<string, unknown>;
  duration_seconds?: number;
}

export interface CooldownInfo {
  tool: string;
  profile: string;
  hit_at: string;
  until: string;
  notes?: string;
}

export interface ActivityResponse {
  events: ActivityEvent[];
  cooldowns: CooldownInfo[];
}

export interface CoordinatorStatus {
  id: string;
  display_name?: string;
  endpoint: string;
  transport: string;
  status: string;
  backend?: string;
  last_seen?: string;
  last_checked?: string;
  error?: string;
}

export interface CoordinatorsResponse {
  coordinators: CoordinatorStatus[];
}

export interface ActivateResponse {
  success: boolean;
  tool: string;
  profile: string;
  message?: string;
  kept_live?: boolean;
  previous_profile?: string;
  auto_backup?: string;
  resnapshotted_profile?: string;
  warnings?: string[];
}

export interface BackupResponse {
  success: boolean;
  tool: string;
  profile: string;
  path?: string;
  message?: string;
}

/** An error response from the API (or no response at all, status 0). */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }

  /** The token is missing or wrong: the user has to reconnect. */
  get unauthorized(): boolean {
    return this.status === 401;
  }
}

/** Trims the base URL so paths can be appended to it. */
export function normalizeBaseUrl(url: string): string {
  return url.trim().replace(/\/+$/, "") || DEFAULT_API_BASE;
}

/**
 * Reads a connection handed over in the URL fragment, as `caam serve
 * --show-token` prints it: `#token=...` and optionally `&api=...`. The
 * fragment never reaches a server.
 */
export function connectionFromFragment(hash: string): Connection | null {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const token = params.get("token")?.trim();
  if (!token) {
    return null;
  }
  return { baseUrl: normalizeBaseUrl(params.get("api") ?? DEFAULT_API_BASE), token };
}

export function loadConnection(storage: Pick<Storage, "getItem"> | undefined): Connection | null {
  try {
    const raw = storage?.getItem(STORAGE_KEY);
    if (!raw) {
      return null;
    }
    const parsed = JSON.parse(raw) as Partial<Connection>;
    if (typeof parsed.token !== "string" || !parsed.token) {
      return null;
    }
    return { baseUrl: normalizeBaseUrl(parsed.baseUrl ?? DEFAULT_API_BASE), token: parsed.token };
  } catch {
    return null;
  }
}

export function saveConnection(
  storage: Pick<Storage, "setItem" | "removeItem"> | undefined,
  conn: Connection | null,
): void {
  try {
    if (conn) {
      storage?.setItem(STORAGE_KEY, JSON.stringify(conn));
    } else {
      storage?.removeItem(STORAGE_KEY);
    }
  } catch {
    // Storage can be unavailable (private windows); the session still works.
  }
}

async function request<T>(conn: Connection, path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(normalizeBaseUrl(conn.baseUrl) + path, {
      ...init,
      headers: {
        Authorization: `Bearer ${conn.token}`,
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...init?.headers,
      },
    });
  } catch {
    throw new ApiError(0, `cannot reach caam at ${conn.baseUrl}; is \`caam serve\` running?`);
  }
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`.trim();
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) {
        message = body.error;
      }
    } catch {
      // Not JSON; keep the status line.
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export const api = {
  status: (conn: Connection) => request<StatusResponse>(conn, "/api/v1/status"),
  profiles: (conn: Connection) => request<ProfilesResponse>(conn, "/api/v1/profiles"),
  usage: (conn: Connection) => request<UsageResponse>(conn, "/api/v1/usage"),
  activity: (conn: Connection) => request<ActivityResponse>(conn, "/api/v1/activity?limit=50"),
  coordinators: (conn: Connection) => request<CoordinatorsResponse>(conn, "/api/v1/coordinators"),
  activate: (conn: Connection, tool: string, profile: string) =>
    request<ActivateResponse>(conn, "/api/v1/actions/activate", {
      method: "POST",
      body: JSON.stringify({ tool, profile }),
    }),
  backup: (conn: Connection, tool: string, profile: string) =>
    request<BackupResponse>(conn, "/api/v1/actions/backup", {
      method: "POST",
      body: JSON.stringify({ tool, profile }),
    }),
};
