import { expect, test, type Page } from "@playwright/test";

const token = "browser-fixture-token";
const now = "2026-10-07T18:00:00Z";

async function connect(page: Page, value = token) {
  await page.getByLabel("API token", { exact: true }).fill(value);
  await page.getByRole("button", { name: "Connect", exact: true }).click();
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

test.describe("App Boot", () => {
  test("loads the dashboard page", async ({ page }) => {
    await page.goto("/");
    await expect(page).toHaveTitle(/CAAM Dashboard/);
  });

  test("asks to connect to caam without a token", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Connect to caam" })).toBeVisible();
    await expect(page.getByLabel("API token")).toBeVisible();
    await expect(page.getByRole("button", { name: "Connect" })).toBeDisabled();
    for (const name of ["Dashboard", "Profiles", "Coordinators", "Activity", "Usage", "Settings"]) {
      await expect(page.getByRole("link", { name, exact: true })).toBeVisible();
    }
  });

  test("search jumps to the profiles page", async ({ page }) => {
    await page.goto("/");
    const searchInput = page.getByPlaceholder("Search profiles…");
    await searchInput.fill("work");
    await searchInput.press("Enter");
    await expect(page).toHaveURL(/\/profiles\?q=work$/);
  });
});

test("connects to real-shaped API data, confirms activation, and refetches", async ({ page }) => {
  let active = false;
  const mutations: unknown[] = [];
  const requests: { url: string; authorization?: string }[] = [];
  await page.route("http://127.0.0.1:7892/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = {
      "Access-Control-Allow-Origin": "http://localhost:3000",
      "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
      "Access-Control-Allow-Headers": "Authorization, Content-Type",
      "Content-Type": "application/json",
    };
    if (request.method() === "OPTIONS") { await route.fulfill({ status: 204, headers }); return; }
    requests.push({ url: request.url(), authorization: request.headers().authorization });
    let body: unknown;
    switch (url.pathname) {
      case "/api/v1/status": body = { version: "e2e", timestamp: now, tools: [{ tool: "grok", logged_in: true, active_profile: active ? "fresh" : "" }] }; break;
      case "/api/v1/profiles": body = { count: 1, profiles: [{ tool: "grok", name: "fresh", active, system: false, identity: { email: "fresh@example.test" }, health: { status: "healthy" } }] }; break;
      case "/api/v1/usage": body = { period: url.searchParams.get("period"), since: now, until: now, available: true, usage: [{ tool: "grok", profile: "fresh", activations: active ? 4 : 3, error_count: 0, active_seconds: 120 }] }; break;
      case "/api/v1/activity": body = { available: true, events: [{ timestamp: now, type: "activate", tool: "grok", profile: "fresh" }], cooldowns: [] }; break;
      case "/api/v1/coordinators": body = { coordinators: [{ id: "build", endpoint: "http://127.0.0.1:7890 via ssh build", transport: "ssh", status: "unreachable", error: "SSH connection unavailable", last_checked: now }] }; break;
      case "/api/v1/actions/activate":
        mutations.push(request.postDataJSON());
        active = true;
        body = { success: true, message: "activated grok/fresh", warnings: ["Previous live login was saved"] };
        break;
      default: await route.fulfill({ status: 404, headers, body: JSON.stringify({ error: "unexpected route" }) }); return;
    }
    await route.fulfill({ status: 200, headers, body: JSON.stringify(body) });
  });

  await page.goto("/");
  await expect(page).toHaveTitle(/CAAM Dashboard/);
  await expect(page.getByRole("table")).toHaveCount(0);
  await connect(page);
  await expect(page.getByText("SSH connection unavailable")).toBeVisible();
  await page.getByRole("link", { name: "Profiles", exact: true }).click();
  await expect(page.getByRole("cell", { name: "fresh@example.test", exact: true })).toBeVisible();
  const activate = page.getByRole("button", { name: "Activate grok profile fresh", exact: true });
  const cancelled = await answerConfirmation(page, () => activate.click(), false);
  expect(cancelled).toContain('Switch grok to profile "fresh"');
  expect(mutations).toHaveLength(0);
  const confirmed = await answerConfirmation(page, () => activate.click(), true);
  expect(confirmed).toContain('Switch grok to profile "fresh"');
  await expect(page.getByText("activated grok/fresh")).toBeVisible();
  await expect(page.getByText("Previous live login was saved")).toBeVisible();
  await expect(activate).toHaveCount(0);
  await expect(page.getByRole("row").filter({ has: page.getByRole("cell", { name: "fresh", exact: true }) })).toContainText("active");
  expect(mutations).toEqual([{ tool: "grok", profile: "fresh" }]);

  await page.getByRole("link", { name: "Usage", exact: true }).click();
  const activity = page.getByRole("table", { name: "Recorded CAAM activity" });
  await expect(activity).toContainText("fresh");
  await expect(activity.getByRole("cell", { name: "4", exact: true })).toBeVisible();
  await page.getByRole("combobox", { name: "Activity period", exact: true }).selectOption("7d");
  await expect.poll(() => requests.some((request) => request.url.endsWith("usage?period=7d"))).toBe(true);
  await page.getByRole("link", { name: "Activity", exact: true }).click();
  await expect(page.getByText("Activated grok fresh", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Profiles", exact: true }).click();
  await page.getByRole("searchbox", { name: "Filter profiles", exact: true }).fill("does-not-exist");
  await expect(page.getByText("No profiles match the filter.")).toBeVisible();
  expect(requests.every((request) => request.authorization === `Bearer ${token}` && !request.url.includes(token))).toBe(true);
  expect(await page.evaluate(() => Object.values(localStorage).concat(Object.values(sessionStorage)).some((value) => String(value).includes("browser-fixture-token")))).toBe(false);
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("button", { name: "Disconnect" }).click();
  await expect(page.getByLabel("API token", { exact: true })).toHaveValue("");
  await expect(page.getByRole("table")).toHaveCount(0);
  await page.getByRole("link", { name: "Profiles", exact: true }).click();
  await connect(page);
  await expect(page.getByRole("searchbox", { name: "Filter profiles", exact: true })).toHaveValue("");
  await expect(page.getByRole("cell", { name: "fresh@example.test", exact: true })).toBeVisible();
});

test("rejects a token and keeps a failed snapshot replacement visibly unsuccessful", async ({ page }) => {
  const mutations: unknown[] = [];
  await page.route("http://127.0.0.1:7892/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = {
      "Access-Control-Allow-Origin": "http://localhost:3000",
      "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
      "Access-Control-Allow-Headers": "Authorization, Content-Type",
      "Content-Type": "application/json",
    };
    if (request.method() === "OPTIONS") { await route.fulfill({ status: 204, headers }); return; }
    if (request.headers().authorization !== `Bearer ${token}`) {
      await route.fulfill({ status: 401, headers, body: JSON.stringify({ error: "unauthorized" }) }); return;
    }
    let body: unknown;
    switch (url.pathname) {
      case "/api/v1/status": body = { version: "e2e", timestamp: now, tools: [{ tool: "grok", logged_in: true }] }; break;
      case "/api/v1/profiles": body = { count: 1, profiles: [{ tool: "grok", name: "existing", active: false, system: false }] }; break;
      case "/api/v1/usage": body = { period: url.searchParams.get("period"), since: now, until: now, available: false, usage: [] }; break;
      case "/api/v1/activity": body = { available: false, events: [], cooldowns: [] }; break;
      case "/api/v1/coordinators": body = { coordinators: [] }; break;
      case "/api/v1/actions/backup": mutations.push(request.postDataJSON()); body = { success: false, message: "No current live Grok login to back up" }; break;
      default: await route.fulfill({ status: 404, headers, body: "{}" }); return;
    }
    await route.fulfill({ status: 200, headers, body: JSON.stringify(body) });
  });
  await page.goto("/");
  await connect(page, "rejected-token");
  await expect(page.getByRole("alert").filter({ hasText: "caam rejected the token. Paste the current one." })).toBeVisible();
  await expect(page.getByLabel("API token", { exact: true })).toHaveValue("");
  await connect(page);
  await page.getByRole("link", { name: "Usage", exact: true }).click();
  await expect(page.getByText(/Activity database unavailable/)).toBeVisible();
  await page.getByRole("link", { name: "Activity", exact: true }).click();
  await expect(page.getByText("Activity database unavailable. Recent events and cooldowns cannot be measured right now.")).toBeVisible();
  await page.getByRole("link", { name: "Profiles", exact: true }).click();
  await page.getByRole("combobox", { name: "Tool", exact: true }).selectOption("grok");
  await page.getByLabel("Profile name", { exact: true }).fill("existing");
  const replace = page.getByRole("button", { name: "Replace snapshot", exact: true });
  await expect(replace).toBeDisabled();
  await page.getByRole("checkbox", { name: /Replace the existing snapshot grok\/existing/ }).check();
  const cancelled = await answerConfirmation(page, () => replace.click(), false);
  expect(cancelled).toContain("current live grok login");
  expect(cancelled).toContain("grok/existing");
  expect(cancelled).toContain("replaces the credentials already saved");
  expect(mutations).toHaveLength(0);
  const confirmed = await answerConfirmation(page, () => replace.click(), true);
  expect(confirmed).toContain("grok/existing");
  await expect(page.getByRole("alert").filter({ hasText: "No current live Grok login to back up" })).toBeVisible();
  expect(mutations).toEqual([{ tool: "grok", profile: "existing", overwrite: true }]);
  await expect(page.getByLabel("Profile name", { exact: true })).toHaveValue("existing");
  await expect(page.getByRole("cell", { name: "existing", exact: true })).toBeVisible();
  await expect(page.getByText("Saved the current grok login as existing", { exact: true })).toHaveCount(0);
});
