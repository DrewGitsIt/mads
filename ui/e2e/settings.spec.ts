import { test, expect } from "./fixtures";

test("settings journey: appearance persists, preflight reports live data service", async ({
  page,
  routerURL,
}) => {
  await page.goto(routerURL);
  await page.getByRole("button", { name: "Settings" }).click();

  const palette = page.getByRole("radiogroup", { name: "Palette" });
  const theme = page.getByRole("radiogroup", { name: "Theme" });

  await Promise.all([
    page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith("/config")),
    palette.getByRole("radio", { name: "Space" }).click(),
  ]);
  await Promise.all([
    page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith("/config")),
    theme.getByRole("radio", { name: "Light" }).click(),
  ]);

  const html = page.locator("html");
  await expect(html).toHaveAttribute("data-palette", "space");
  await expect(html).toHaveAttribute("data-theme", "light");

  // App.tsx applies the fetched config's theme/palette on boot (before any
  // navigation) — reload and check the root attributes directly, proving
  // the PUT /config round-trip persisted rather than just in-memory state.
  await page.reload();
  await expect(html).toHaveAttribute("data-palette", "space");
  await expect(html).toHaveAttribute("data-theme", "light");

  await page.getByRole("button", { name: "Settings" }).click();
  await page.getByRole("button", { name: "General" }).click();

  // The Router wires the real preflight.New checker (not StubChecker —
  // services/router/main.go), so GET /preflight genuinely checks the live
  // Data Service. Field name is "data_service" (underscore).
  const dataServiceRow = page.locator("li", { hasText: "data_service" });
  await expect(dataServiceRow).toBeVisible();
  await expect(dataServiceRow).toContainText("✓");
});
