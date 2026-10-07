// The dashboard only talks to the CAAM instance on the browser's own machine.
// Tokens belong to a connection in memory; callers must never persist them.
export const DEFAULT_API_BASE = "http://127.0.0.1:7892";

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
  provider?: string;
}

export interface HealthStatus {
  status: "healthy" | "warning" | "critical" | "unknown" | string;
  expires_at?: string;
  error_count?: number;
  cooldown_remaining?: string;
  renewable?: boolean;
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

export type Period = "1h" | "24h" | "7d" | "30d";

export interface UsageEntry {
  tool: string;
  profile: string;
  activations: number;
  error_count: number;
  active_seconds: number;
  last_activity?: string;
}

export interface UsageResponse {
  tool?: string;
  period: Period;
  since: string;
  until: string;
  available: boolean;
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
  available: boolean;
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

export interface ActionResponse {
  success: boolean;
  message?: string;
  warnings?: string[];
}

export interface ActivateResponse extends ActionResponse {
  tool?: string;
  profile?: string;
  kept_live?: boolean;
  previous_profile?: string;
  auto_backup?: string;
  resnapshotted_profile?: string;
}

export interface BackupResponse extends ActionResponse {
  tool?: string;
  profile?: string;
  path?: string;
}

export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "ApiError";
  }

  get unauthorized(): boolean {
    return this.status === 401 || this.status === 403;
  }
}

export function normalizeBaseUrl(value: string): string {
  let url: URL;
  try {
    url = new URL(value.trim() || DEFAULT_API_BASE);
  } catch {
    throw new Error("Enter a loopback API address, such as http://127.0.0.1:7892.");
  }
  if (
    !["http:", "https:"].includes(url.protocol) ||
    !["localhost", "127.0.0.1", "[::1]"].includes(url.hostname) ||
    url.username || url.password || url.search || url.hash || url.pathname !== "/"
  ) {
    throw new Error("The API address must be a loopback HTTP(S) origin: localhost, 127.0.0.1, or [::1], with no path, credentials, query, or fragment.");
  }
  return url.origin;
}

export function validateConnection(conn: Connection): Connection {
  const baseUrl = normalizeBaseUrl(conn.baseUrl);
  const token = conn.token.trim();
  if (!token || /[\r\n]/.test(token)) throw new Error("Paste the API token printed by caam serve --show-token.");
  return { baseUrl, token };
}

/** Reads the CLI's fragment handoff; the connection store scrubs it before use. */
export function connectionFromFragment(hash: string): Connection | null {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const token = params.get("token")?.trim();
  if (!token) return null;
  return validateConnection({ baseUrl: params.get("api") ?? DEFAULT_API_BASE, token });
}

async function request<T>(
  connection: Connection,
  path: string,
  signal: AbortSignal | undefined,
  parse: (value: unknown) => T,
  body?: object,
): Promise<T> {
  // Recheck at the boundary so no caller can accidentally send a token elsewhere.
  const conn = validateConnection(connection);
  if (!path.startsWith("/api/v1/") || path.includes("\\")) {
    throw new ApiError(0, "Invalid API route.");
  }
  let response: Response;
  try {
    response = await fetch(conn.baseUrl + path, {
    method: body === undefined ? "GET" : "POST",
    headers: {
      Authorization: `Bearer ${conn.token}`,
      Accept: "application/json",
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
    credentials: "omit",
    redirect: "error",
    cache: "no-store",
    referrerPolicy: "no-referrer",
    });
  } catch {
    throw new ApiError(0, "Cannot reach the local API; is `caam serve` running?");
  }
  if (response.status === 401 || response.status === 403) {
    throw new ApiError(response.status, "caam rejected the token. Paste the current one.");
  }
  let value: unknown;
  try {
    value = await response.json();
  } catch {
    throw new ApiError(response.status, "The API returned an unreadable response.");
  }
  if (!response.ok) {
    const message = record(value) && typeof value.error === "string"
      ? value.error
      : `The API request failed (${response.status}).`;
    throw new ApiError(response.status, message);
  }
  return parse(value);
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function optionalStrings(value: Record<string, unknown>, keys: string[]) {
  return keys.every((key) => value[key] === undefined || typeof value[key] === "string");
}

function accountDetails(value: Record<string, unknown>) {
  return (
    (value.health === undefined || value.health === null ||
      (record(value.health) && typeof value.health.status === "string" &&
        optionalStrings(value.health, ["expires_at", "cooldown_remaining", "recommendation"]))) &&
    (value.identity === undefined || value.identity === null ||
      (record(value.identity) && optionalStrings(value.identity, ["email", "organization", "account_id", "plan_type"])))
  );
}

function invalid(section: string): never {
  throw new ApiError(0, `The API returned invalid ${section} data.`);
}

export function parseStatus(value: unknown): StatusResponse {
  if (!record(value) || typeof value.version !== "string" || typeof value.timestamp !== "string" ||
    !Array.isArray(value.tools) || !value.tools.every((tool) => record(tool) &&
      typeof tool.tool === "string" && typeof tool.logged_in === "boolean" &&
      optionalStrings(tool, ["active_profile"]) && accountDetails(tool))) invalid("status");
  return value as unknown as StatusResponse;
}

export function parseProfiles(value: unknown): ProfilesResponse {
  if (!record(value) || !Array.isArray(value.profiles) || !value.profiles.every((profile) =>
    record(profile) && typeof profile.tool === "string" && typeof profile.name === "string" &&
    typeof profile.active === "boolean" && typeof profile.system === "boolean" && accountDetails(profile))) invalid("profile");
  return { profiles: value.profiles as unknown as ProfileInfo[], count: value.profiles.length };
}

export function parseUsage(value: unknown): UsageResponse {
  if (!record(value) || typeof value.available !== "boolean" ||
    !["1h", "24h", "7d", "30d"].includes(String(value.period)) ||
    typeof value.since !== "string" || typeof value.until !== "string" ||
    !Array.isArray(value.usage) || !value.usage.every((entry) => record(entry) &&
      typeof entry.tool === "string" && typeof entry.profile === "string" &&
      ["activations", "error_count", "active_seconds"].every((key) =>
        typeof entry[key] === "number" && Number.isFinite(entry[key]) && Number(entry[key]) >= 0) &&
      optionalStrings(entry, ["last_activity"]))) invalid("activity");
  return value as unknown as UsageResponse;
}

export function parseCoordinators(value: unknown): CoordinatorsResponse {
  if (!record(value) || !Array.isArray(value.coordinators) || !value.coordinators.every((coordinator) =>
    record(coordinator) && ["id", "endpoint", "transport", "status"].every((key) => typeof coordinator[key] === "string") &&
    optionalStrings(coordinator, ["display_name", "backend", "last_seen", "last_checked", "error"]))) invalid("coordinator");
  return value as unknown as CoordinatorsResponse;
}

export function parseAction(value: unknown): ActionResponse {
  if (!record(value) || typeof value.success !== "boolean" ||
    !optionalStrings(value, ["tool", "profile", "message", "path", "previous_profile", "auto_backup", "resnapshotted_profile"]) ||
    (value.warnings !== undefined && (!Array.isArray(value.warnings) || !value.warnings.every((warning) => typeof warning === "string")))) invalid("action result");
  return value as unknown as ActionResponse;
}
export function parseActivity(value: unknown): ActivityResponse {
  if (!record(value) || typeof value.available !== "boolean" || !Array.isArray(value.events) || !Array.isArray(value.cooldowns) ||
    !value.events.every((event) => record(event) &&
      ["timestamp", "type", "tool", "profile"].every((key) => typeof event[key] === "string") &&
      (event.details === undefined || record(event.details)) &&
      (event.duration_seconds === undefined || (typeof event.duration_seconds === "number" && Number.isFinite(event.duration_seconds)))) ||
    !value.cooldowns.every((cooldown) => record(cooldown) &&
      ["tool", "profile", "hit_at", "until"].every((key) => typeof cooldown[key] === "string") &&
      optionalStrings(cooldown, ["notes"]))) invalid("recent activity");
  return value as unknown as ActivityResponse;
}

export const api = {
  status: (conn: Connection, signal?: AbortSignal) => request(conn, "/api/v1/status", signal, parseStatus),
  profiles: (conn: Connection, signal?: AbortSignal) => request(conn, "/api/v1/profiles", signal, parseProfiles),
  usage: (conn: Connection, period: Period = "24h", signal?: AbortSignal) =>
    request(conn, `/api/v1/usage?period=${period}`, signal, (value) => {
      const result = parseUsage(value);
      if (result.period !== period) throw new ApiError(0, "The API returned activity for a different period.");
      return result;
    }),
  activity: (conn: Connection, signal?: AbortSignal) => request(conn, "/api/v1/activity?limit=50", signal, parseActivity),
  coordinators: (conn: Connection, signal?: AbortSignal) => request(conn, "/api/v1/coordinators", signal, parseCoordinators),
  activate: (conn: Connection, tool: string, profile: string, signal?: AbortSignal) =>
    request<ActivateResponse>(conn, "/api/v1/actions/activate", signal, parseAction, { tool, profile }),
  backup: (conn: Connection, tool: string, profile: string, overwrite = false, signal?: AbortSignal) =>
    request<BackupResponse>(conn, "/api/v1/actions/backup", signal, parseAction, {
      tool, profile, ...(overwrite ? { overwrite: true } : {}),
    }),
};
