import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";

// The dashboard against a real `caam serve`, run under a throwaway HOME that
// holds a made-up Claude login. Builds caam from this repository unless
// CAAM_BIN names a binary.

const repoRoot = path.resolve(__dirname, "../../..");

function isolatedEnvironment(home: string): NodeJS.ProcessEnv {
  // Match internal/testutil/isolate.go: a private home, no ambient provider
  // credentials, no real agent CLIs or browser openers, and no OS keychain.
  // An allowlist also excludes newer provider/tool-home environment variables.
  const env: NodeJS.ProcessEnv = { NODE_ENV: "test" };
  for (const key of ["SystemRoot", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "LANG", "LC_ALL", "TZ", "GOMAXPROCS", "GOMEMLIMIT", "GOGC"]) {
    if (process.env[key] !== undefined) {
      env[key] = process.env[key];
    }
  }
  const stubDir = path.join(home, "stub-bin");
  const tempDir = path.join(home, "tmp");
  mkdirSync(stubDir, { mode: 0o700 });
  mkdirSync(tempDir, { mode: 0o700 });
  for (const name of ["claude", "codex", "gemini", "agy", "grok", "opencode", "cursor", "npx", "gcloud", "open", "xdg-open", "sensible-browser", "x-www-browser", "notify-send", "osascript"]) {
    const windows = process.platform === "win32";
    writeFileSync(
      path.join(stubDir, name + (windows ? ".cmd" : "")),
      windows ? "@exit /b 0\r\n" : "#!/bin/sh\nexit 0\n",
      { mode: 0o755 },
    );
  }
  return {
    ...env,
    HOME: home,
    USERPROFILE: home,
    APPDATA: path.join(home, "AppData", "Roaming"),
    LOCALAPPDATA: path.join(home, "AppData", "Local"),
    XDG_CONFIG_HOME: path.join(home, ".config"),
    XDG_DATA_HOME: path.join(home, ".local", "share"),
    XDG_STATE_HOME: path.join(home, ".local", "state"),
    XDG_CACHE_HOME: path.join(home, ".cache"),
    TMPDIR: tempDir,
    TMP: tempDir,
    TEMP: tempDir,
    PATH: stubDir,
    BROWSER: path.join(stubDir, process.platform === "win32" ? "open.cmd" : "open"),
    CAAM_KEYCHAIN: "0",
    CAAM_TEST_ISOLATED: "1",
  };
}

function writeClaudeLogin(home: string, accessToken: string, email: string) {
  mkdirSync(path.join(home, ".claude"), { recursive: true, mode: 0o700 });
  writeFileSync(
    path.join(home, ".claude", ".credentials.json"),
    JSON.stringify({
      claudeAiOauth: {
        accessToken,
        refreshToken: "sk-ant-ort01-e2e-not-a-real-token",
        expiresAt: Date.now() + 24 * 3600 * 1000,
        scopes: ["user:inference"],
        subscriptionType: "max",
      },
    }),
    { mode: 0o600 },
  );
  writeFileSync(
    path.join(home, ".claude.json"),
    JSON.stringify({ oauthAccount: { emailAddress: email, accountUuid: "e2e-" + email } }),
    { mode: 0o600 },
  );
}

async function answerConfirmation(page: Page, click: () => Promise<void>, accept: boolean) {
  const opened = page.waitForEvent("dialog");
  const clicked = click();
  const dialog = await opened;
  const kind = dialog.type();
  const message = dialog.message();
  if (accept) {
    await dialog.accept();
  } else {
    await dialog.dismiss();
  }
  await clicked;
  expect(kind).toBe("confirm");
  return message;
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address() as net.AddressInfo;
      srv.close(() => resolve(port));
    });
    srv.on("error", reject);
  });
}

async function waitForHealth(url: string) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      if ((await fetch(url)).ok) {
        return;
      }
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`caam serve did not come up at ${url}`);
}

test.describe("with a running caam serve", () => {
  test.describe.configure({ mode: "serial" });

  let serve: ChildProcess | undefined;
  let home = "";
  let apiBase = "";
  let token = "";
  let dashboardLink = "";

  test.beforeAll(async () => {
    test.setTimeout(240_000);
    home = mkdtempSync(path.join(tmpdir(), "caam-dashboard-e2e-"));
    const bin = process.env.CAAM_BIN ? path.resolve(process.env.CAAM_BIN) : path.join(home, "caam");
    if (!process.env.CAAM_BIN) {
      execFileSync("go", ["build", "-o", bin, "./cmd/caam"], { cwd: repoRoot, stdio: "inherit" });
    }

    writeClaudeLogin(home, "sk-ant-oat01-e2e-not-a-real-token", "e2e@example.com");
    const env = isolatedEnvironment(home);

    const port = await freePort();
    apiBase = `http://127.0.0.1:${port}`;
    token = execFileSync(bin, ["serve", "--port", String(port), "--show-token"], { env }).toString().trim();
    dashboardLink = execFileSync(bin, ["serve", "--port", String(port), "--dashboard-url", "http://localhost:3000"], { env }).toString().trim();
    serve = spawn(bin, ["serve", "--port", String(port)], { env, stdio: "ignore" });
    await waitForHealth(`${apiBase}/health`);
    const response = await fetch(`${apiBase}/api/v1/status`, { headers: { Authorization: `Bearer ${token}` } });
    expect(response.ok).toBe(true);
    const status = await response.json() as { tools: { tool: string; logged_in: boolean }[] };
    expect(status.tools.filter((tool) => tool.logged_in).map((tool) => tool.tool)).toEqual(["claude"]);
  });

  test.afterAll(() => {
    serve?.kill();
  });

  test("connects through the dashboard link and saves and replaces the current login", async ({ page }) => {
    const mutations: { tool: string; profile: string; overwrite?: boolean }[] = [];
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url() === `${apiBase}/api/v1/actions/backup`) {
        mutations.push(request.postDataJSON());
      }
    });
    await page.goto(dashboardLink);

    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
    // The CLI handoff is scrubbed, and the token lives only in this tab's memory.
    await expect(page).toHaveURL(/\/$/);
    expect(await page.evaluate((value) => Object.values(localStorage).concat(Object.values(sessionStorage)).some((entry) => String(entry).includes(value)), token)).toBe(false);
    // caam sees the made-up Claude login, not yet saved as a profile.
    await expect(page.getByText("logged in (unsaved login)")).toBeVisible();
    await expect(page.getByText("No coordinators.", { exact: false })).toBeVisible();

    await page.getByRole("link", { name: "Profiles", exact: true }).click();
    await expect(page.getByText("No saved profiles yet.", { exact: false })).toBeVisible();
    await page.getByLabel("Profile name").fill("e2e");
    const save = page.getByRole("button", { name: "Save", exact: true });
    const cancelled = await answerConfirmation(page, () => save.click(), false);
    expect(cancelled).toContain("current live claude login");
    expect(cancelled).toContain("claude/e2e");
    expect(mutations).toHaveLength(0);
    const savedCredentials = path.join(home, ".local", "share", "caam", "vault", "claude", "e2e", ".credentials.json");
    expect(existsSync(savedCredentials)).toBe(false);
    await answerConfirmation(page, () => save.click(), true);

    await expect(page.getByRole("cell", { name: "e2e", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: /e2e@example\.com/ })).toBeVisible();
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({ tool: "claude", profile: "e2e" });
    expect(mutations[0].overwrite ?? false).toBe(false);
    const original = readFileSync(savedCredentials);
    expect(JSON.parse(original.toString()).claudeAiOauth.accessToken).toBe("sk-ant-oat01-e2e-not-a-real-token");

    writeClaudeLogin(home, "sk-ant-oat01-e2e-replacement-not-real", "replacement@example.com");
    await page.getByLabel("Profile name").fill("e2e");
    const replace = page.getByRole("button", { name: "Replace snapshot", exact: true });
    await expect(replace).toBeDisabled();
    await page.getByRole("checkbox", { name: /Replace the existing snapshot claude\/e2e/ }).check();
    const cancelledReplacement = await answerConfirmation(page, () => replace.click(), false);
    expect(cancelledReplacement).toContain("replaces the credentials already saved");
    expect(cancelledReplacement).toContain("claude/e2e");
    expect(mutations).toHaveLength(1);
    expect(readFileSync(savedCredentials)).toEqual(original);
    await answerConfirmation(page, () => replace.click(), true);
    await expect(page.getByRole("cell", { name: /replacement@example\.com/ })).toBeVisible();
    expect(mutations).toHaveLength(2);
    expect(mutations[1]).toEqual({ tool: "claude", profile: "e2e", overwrite: true });
    expect(JSON.parse(readFileSync(savedCredentials, "utf8")).claudeAiOauth.accessToken).toBe("sk-ant-oat01-e2e-replacement-not-real");

    await page.reload();
    await expect(page.getByRole("heading", { name: "Connect to caam" })).toBeVisible();
    await expect(page.getByLabel("API token")).toHaveValue("");
    await expect(page.getByRole("table")).toHaveCount(0);
  });

  test("asks for a new token after it is rejected", async ({ page }) => {
    await page.goto(`/#${new URLSearchParams({ token: "wrong", api: apiBase })}`);
    await expect(page.getByText("caam rejected the token. Paste the current one.")).toBeVisible();
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByLabel("API token")).toHaveValue("");
  });
});
