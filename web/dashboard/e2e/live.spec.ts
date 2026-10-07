import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";

// The dashboard against a real `caam serve`, run under a throwaway HOME that
// holds a made-up Claude login. Builds caam from this repository unless
// CAAM_BIN names a binary.

const repoRoot = path.resolve(__dirname, "../../..");

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
  let apiBase = "";
  let token = "";

  test.beforeAll(async () => {
    test.setTimeout(240_000);
    const home = mkdtempSync(path.join(tmpdir(), "caam-dashboard-e2e-"));
    const bin = process.env.CAAM_BIN ?? path.join(home, "caam");
    if (!process.env.CAAM_BIN) {
      execFileSync("go", ["build", "-o", bin, "./cmd/caam"], { cwd: repoRoot, stdio: "inherit" });
    }

    // A made-up Claude login for caam to find.
    mkdirSync(path.join(home, ".claude"), { recursive: true });
    writeFileSync(
      path.join(home, ".claude", ".credentials.json"),
      JSON.stringify({
        claudeAiOauth: {
          accessToken: "sk-ant-oat01-e2e-not-a-real-token",
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
      JSON.stringify({ oauthAccount: { emailAddress: "e2e@example.com", accountUuid: "e2e-account" } }),
    );

    const env: NodeJS.ProcessEnv = { ...process.env };
    for (const k of Object.keys(env)) {
      if (/^(CAAM_|CLAUDE_|CODEX_|GEMINI_|ANTHROPIC_|OPENAI_)/.test(k)) {
        delete env[k];
      }
    }
    Object.assign(env, {
      HOME: home,
      XDG_CONFIG_HOME: path.join(home, ".config"),
      XDG_DATA_HOME: path.join(home, ".local", "share"),
      XDG_STATE_HOME: path.join(home, ".local", "state"),
    });

    const port = await freePort();
    apiBase = `http://127.0.0.1:${port}`;
    token = execFileSync(bin, ["serve", "--port", String(port), "--show-token"], { env }).toString().trim();
    serve = spawn(bin, ["serve", "--port", String(port)], { env, stdio: "ignore" });
    await waitForHealth(`${apiBase}/health`);
  });

  test.afterAll(() => {
    serve?.kill();
  });

  test("connects through the dashboard link and saves the current login", async ({ page }) => {
    await page.goto(`/#${new URLSearchParams({ token, api: apiBase })}`);

    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
    // The token left the address bar and is kept by the browser.
    await expect(page).toHaveURL(/\/$/);
    // caam sees the made-up Claude login, not yet saved as a profile.
    await expect(page.getByText("logged in (unsaved login)")).toBeVisible();
    await expect(page.getByText("No coordinators.", { exact: false })).toBeVisible();

    await page.getByRole("link", { name: "Profiles" }).click();
    await expect(page.getByText("No saved profiles yet.", { exact: false })).toBeVisible();
    await page.getByLabel("Profile name").fill("e2e");
    await page.getByRole("button", { name: "Save" }).click();

    await expect(page.getByRole("cell", { name: "e2e", exact: true })).toBeVisible();
    await expect(page.getByRole("cell", { name: /e2e@example\.com/ })).toBeVisible();
  });

  test("asks for a new token after it is rejected", async ({ page }) => {
    await page.goto(`/#${new URLSearchParams({ token: "wrong", api: apiBase })}`);
    await expect(page.getByText("caam rejected the saved token. Paste the current one.")).toBeVisible();
  });
});
