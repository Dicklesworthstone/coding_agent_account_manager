import { afterEach, describe, expect, it, vi } from "vitest";
import { describeEvent } from "./activity";
import {
  ApiError,
  api,
  connectionFromFragment,
  DEFAULT_API_BASE,
  normalizeBaseUrl,
} from "./api";

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

describe("loopback request boundary", () => {
  it.each([
    "https://example.com:7892", "http://localhost.evil.test:7892", "http://localhost:7892@evil.test",
    "http://127.0.0.1:7892/api/v1", "http://[::1]:7892/?token=secret", "file:///tmp/api",
    "http://user:token@127.0.0.1:7892", "http://127.0.0.1:7892#token", "http://127.0.0.2:7892",
  ])("never sends a credential to invalid origin %s", async (baseUrl) => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    await expect(api.status({ baseUrl, token: "fixture" })).rejects.toThrow(/loopback/);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("accepts IPv6 loopback and normalizes valid local origins", () => {
    expect(normalizeBaseUrl("http://[::1]:7892/")).toBe("http://[::1]:7892");
    expect(normalizeBaseUrl("https://localhost:7892/")).toBe("https://localhost:7892");
    expect(() => connectionFromFragment("#api=https://example.com&token=fixture")).toThrow(/loopback/);
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
    expect(init).toMatchObject({ credentials: "omit", redirect: "error", cache: "no-store", referrerPolicy: "no-referrer" });
    expect(url).not.toContain(conn.token);
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
    expect((err as ApiError).message).toBe("caam rejected the token. Paste the current one.");
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

  it.each([null, {}, { profiles: [{}] }, { profiles: "not-an-array" }])("rejects malformed profile data %j", async (body) => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body))));
    await expect(api.profiles(conn)).rejects.toThrow("invalid profile data");
  });

  it("rejects unreadable JSON and malformed action acknowledgements", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not json")));
    await expect(api.status(conn)).rejects.toThrow("unreadable response");
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ success: "true" }))));
    await expect(api.activate(conn, "grok", "fresh")).rejects.toThrow("invalid action result");
  });

  it("rejects a mismatched usage period instead of labeling it as current", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      available: true, period: "24h", since: "2026-10-07T10:00:00Z", until: "2026-10-07T11:00:00Z", usage: [],
    }))));
    await expect(api.usage(conn, "7d")).rejects.toThrow("different period");
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
