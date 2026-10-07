import { expect, test } from "@playwright/test";

test.describe("App Boot", () => {
  test("loads the dashboard page", async ({ page }) => {
    await page.goto("/");

    // Check page title
    await expect(page).toHaveTitle(/CAAM Dashboard/);
  });

  test("asks to connect to caam without a token", async ({ page }) => {
    await page.goto("/");

    await expect(page.getByRole("heading", { name: "Connect to caam" })).toBeVisible();
    await expect(page.getByLabel("API token")).toBeVisible();
    await expect(page.getByRole("button", { name: "Connect" })).toBeDisabled();

    // Check for sidebar navigation
    for (const name of ["Dashboard", "Profiles", "Coordinators", "Activity", "Usage", "Settings"]) {
      await expect(page.getByRole("link", { name })).toBeVisible();
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
