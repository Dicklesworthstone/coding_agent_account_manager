import { afterEach, describe, expect, it, vi } from "vitest";
import { describeEvent } from "./activity";
import {
  ApiError,
  api,
  connectionFromFragment,
  DEFAULT_API_BASE,
  loadConnection,
  saveConnection,
} from "./api";

function memoryStorage() {
  const data = new Map<string, string>();
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("connectionFromFragment", () => {
  it("reads the token and API address caam serve --dashboard-url prints", () => {
    expect(connectionFromFragment("#api=http%3A%2F%2F127.0.0.1%3A9000%2F&token=tok%2B%2F%3D")).toEqual({
      baseUrl: "http://127.0.0.1:9000",
      token: "tok+/=",
    });
  });

  it("defaults the API address", () => {
    expect(connectionFromFragment("#token=abc")).toEqual({ baseUrl: DEFAULT_API_BASE, token: "abc" });
  });

  it("ignores fragments without a token", () => {
    expect(connectionFromFragment("")).toBeNull();
    expect(connectionFromFragment("#section")).toBeNull();
    expect(connectionFromFragment("#token=")).toBeNull();
  });
});

describe("stored connection", () => {
  it("round-trips and can be forgotten", () => {
    const storage = memoryStorage();
    expect(loadConnection(storage)).toBeNull();
    saveConnection(storage, { baseUrl: "http://127.0.0.1:7892", token: "t" });
    expect(loadConnection(storage)).toEqual({ baseUrl: "http://127.0.0.1:7892", token: "t" });
    saveConnection(storage, null);
    expect(loadConnection(storage)).toBeNull();
  });

  it("ignores corrupt entries", () => {
    const storage = memoryStorage();
    storage.setItem("caam.connection", "{not json");
    expect(loadConnection(storage)).toBeNull();
    storage.setItem("caam.connection", JSON.stringify({ baseUrl: "x" }));
    expect(loadConnection(storage)).toBeNull();
  });
});

describe("api requests", () => {
  const conn = { baseUrl: "http://127.0.0.1:7892/", token: "secret" };

  it("sends the bearer token", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ coordinators: [] }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.coordinators(conn)).resolves.toEqual({ coordinators: [] });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("http://127.0.0.1:7892/api/v1/coordinators");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer secret");
  });

  it("posts actions as JSON", async () => {
    const fetchMock = vi.fn(
      async () => new Response(JSON.stringify({ success: true, tool: "claude", profile: "work" })),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.activate(conn, "claude", "work");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("http://127.0.0.1:7892/api/v1/actions/activate");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ tool: "claude", profile: "work" });
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it("surfaces the API's error message and a rejected token", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ error: "invalid token" }), { status: 401 })),
    );
    const err = await api.status(conn).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).message).toBe("invalid token");
    expect((err as ApiError).unauthorized).toBe(true);
  });

  it("explains an unreachable server", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    const err = (await api.status(conn).catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(0);
    expect(err.message).toContain("caam serve");
  });
});

describe("describeEvent", () => {
  it("describes switches with their reason", () => {
    expect(
      describeEvent({
        timestamp: "2026-10-07T10:00:00Z",
        type: "switch",
        tool: "claude",
        profile: "home",
        details: { from: "work", reason: "rate_limit" },
      }),
    ).toBe("Switched claude from work to home (rate limit)");
  });

  it("describes errors", () => {
    expect(
      describeEvent({
        timestamp: "2026-10-07T10:00:00Z",
        type: "error",
        tool: "codex",
        profile: "a",
        details: { error: "refresh failed" },
      }),
    ).toBe("Error on codex a: refresh failed");
  });
});
